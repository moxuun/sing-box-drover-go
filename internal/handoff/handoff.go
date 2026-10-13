// Package handoff implements the controller handoff between a running instance
// and the replacement it starts.
//
// The parent keeps its tray, its core and the single-instance lock until the
// replacement has validated everything it needs. Only then does the parent give
// up the lock and let the replacement take over. A failure after that point is
// rolled back: the parent takes ownership back and restarts its core, but only
// after the replacement is confirmed gone, so the two controllers can never own
// the system proxy at the same time.
package handoff

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// Message kinds. The parent and the child alternate strictly: the child reports
// readiness, the parent proceeds, the child reports the takeover result.
const (
	// KindAuth: the opening frame of the child, carrying the shared token. It is
	// sent by the transport's Connect, not by the protocol below.
	KindAuth = "auth"
	// KindReady: the child validated its own options, configuration and core and
	// has not taken anything over yet.
	KindReady = "ready"
	// KindProceed: the parent gave up ownership; the child may acquire it.
	KindProceed = "proceed"
	// KindTakeover: the child owns the controller and its user interface is up.
	KindTakeover = "takeover"
	// KindFailed: the child could not take over; the parent rolls back.
	KindFailed = "failed"
	// KindAbort: the parent gave up; the child must release what it took.
	KindAbort = "abort"
)

// Default phase budgets. Readiness covers the child's configuration check, which
// has its own 30s limit; the transfer covers the parent's complete stop budget;
// the startup covers the child's core start and its tray.
const (
	DefaultReadinessTimeout = 60 * time.Second
	DefaultTransferTimeout  = 45 * time.Second
	DefaultStartupTimeout   = 30 * time.Second
	DefaultTerminationWait  = 15 * time.Second
)

// ErrAborted reports that the instance being replaced took the controller back
// instead of handing it over. Nothing was handed over.
var ErrAborted = errors.New("the previous instance aborted the handoff")

// ErrReported reports a handoff failure that was already reported to the instance
// being replaced. That instance owns the user-facing message, so the replacement
// has to exit without reporting the same failure a second time.
var ErrReported = errors.New("the failure was reported to the previous instance")

// Message is one frame of the protocol.
type Message struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason,omitempty"`
	// Token authenticates the child on its opening frame.
	Token string `json:"token,omitempty"`
}

// Conn is the authenticated connection of one handoff.
type Conn interface {
	Send(Message) error
	// Receive waits for the next message. The peer closing the connection is
	// reported as an error, which is what ends a phase without an answer.
	Receive(ctx context.Context) (Message, error)
	Close() error
}

// Listener accepts the single connection of one handoff. Implementations have to
// authenticate the connecting process: the address and the token are handed to
// the child, and Accept verifies that the peer is the process the parent started.
type Listener interface {
	// Address is passed to the replacement on its command line.
	Address() string
	// Token authenticates both ends of the connection.
	Token() string
	// Accept waits for the replacement and verifies its identity. expectedPID is
	// the process the parent started.
	Accept(ctx context.Context, expectedPID int) (Conn, error)
	Close() error
}

// Transport creates the parent side of one handoff.
type Transport interface {
	Listen() (Listener, error)
}

// LaunchedProcess is the replacement the parent started.
type LaunchedProcess interface {
	PID() int
	// Terminated reports whether the process has exited, waiting at most timeout.
	// It must not report true for a process that is still running.
	Terminated(timeout time.Duration) bool
	Close()
}

// Launch starts the replacement with the handoff address and token.
type Launch func(params string, elevate bool) (LaunchedProcess, error)

// HandoffFlags lists the parameters a replacement receives for a handoff. The
// values are a pipe path, a hexadecimal token and a process id, so they never
// need quoting.
const (
	FlagPipe  = "handoff-pipe"
	FlagToken = "handoff-token"
	FlagPeer  = "handoff-peer"
)

func handoffFlags(flags, address, token string) string {
	return fmt.Sprintf("%s -%s %s -%s %s -%s %d", flags, FlagPipe, address, FlagToken, token, FlagPeer, os.Getpid())
}

// ParentOptions carries one handoff from the parent's side.
type ParentOptions struct {
	Transport Transport
	// Flags are the replacement's own command line, without the handoff
	// parameters, which RunParent appends.
	Flags string
	// Elevate asks for an elevated replacement, which is what makes the launch
	// cross the UAC boundary.
	Elevate bool
	// Launch starts the replacement. It receives the complete command line.
	Launch func(params string, elevate bool) (LaunchedProcess, error)

	// Transfer runs after the replacement reported readiness and before the parent
	// gives up ownership: it saves the rollback state, hands the system proxy back,
	// stops the parent's core and waits for the pending callbacks of that core.
	Transfer func(ctx context.Context) error
	// Release gives up instance ownership so the replacement can acquire it.
	Release func()
	// Reacquire takes instance ownership back during a rollback.
	Reacquire func() error
	// Restore brings the parent's runtime back during a rollback. It runs only
	// after the replacement is confirmed gone.
	Restore func(ctx context.Context) error

	ReadinessTimeout time.Duration
	TransferTimeout  time.Duration
	StartupTimeout   time.Duration
	TerminationWait  time.Duration

	// Log records progress. It may be nil.
	Log func(string)
}

func (o ParentOptions) timeout(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

func (o ParentOptions) log(message string) {
	if o.Log != nil {
		o.Log(message)
	}
}

// RunParent performs one handoff.
//
// A nil result means the replacement owns the controller: the parent must not
// touch the core, the system proxy or the instance lock again. A non-nil result
// means the parent still owns them, after a rollback when the failure happened
// after the transfer.
func RunParent(ctx context.Context, options ParentOptions) error {
	if options.Transport == nil {
		return errors.New("handoff transport is not configured")
	}
	if options.Launch == nil {
		return errors.New("handoff launch is not configured")
	}
	listener, err := options.Transport.Listen()
	if err != nil {
		return fmt.Errorf("prepare handoff: %w", err)
	}
	defer listener.Close()
	child, err := options.Launch(handoffFlags(options.Flags, listener.Address(), listener.Token()), options.Elevate)
	if err != nil {
		return err
	}
	defer child.Close()

	// Phase 1: the replacement starts and validates its own options, configuration
	// and core. Nothing has changed for the parent yet, so a failure here needs no
	// rollback.
	readyCtx, cancelReady := context.WithTimeout(ctx, options.timeout(options.ReadinessTimeout, DefaultReadinessTimeout))
	conn, err := listener.Accept(readyCtx, child.PID())
	if err != nil {
		cancelReady()
		return fmt.Errorf("replacement did not connect: %w", err)
	}
	defer conn.Close()
	// Accept only proves that the replacement started and authenticated. It has to
	// report readiness before the parent gives anything up.
	message, err := conn.Receive(readyCtx)
	cancelReady()
	if err != nil {
		return fmt.Errorf("replacement did not become ready: %w", err)
	}
	if message.Kind != KindReady {
		if message.Kind == KindFailed {
			// Nothing was transferred yet, so there is nothing to roll back; the
			// replacement's reason is the whole story.
			return fmt.Errorf("replacement is not ready: %s", message.Reason)
		}
		return fmt.Errorf("replacement sent an unexpected %q message instead of readiness", message.Kind)
	}
	options.log("replacement is ready; transferring the controller")

	// Phase 2: the parent gives up what it owns. This is the point of no return:
	// every failure below has to be rolled back.
	transferCtx, cancelTransfer := context.WithTimeout(ctx, options.timeout(options.TransferTimeout, DefaultTransferTimeout))
	err = options.Transfer(transferCtx)
	cancelTransfer()
	if err != nil {
		return fmt.Errorf("stop the current controller: %w", err)
	}
	if options.Release != nil {
		options.Release()
	}
	if err := conn.Send(Message{Kind: KindProceed}); err != nil {
		return options.rollback(ctx, conn, child, fmt.Errorf("tell the replacement to take over: %w", err))
	}

	// Phase 3: the replacement owns the lock, starts its core and reports back once
	// its user interface is up.
	startupCtx, cancelStartup := context.WithTimeout(ctx, options.timeout(options.StartupTimeout, DefaultStartupTimeout))
	message, err = conn.Receive(startupCtx)
	cancelStartup()
	if err != nil {
		return options.rollback(ctx, conn, child, fmt.Errorf("replacement did not confirm the takeover: %w", err))
	}
	switch message.Kind {
	case KindTakeover:
		options.log("replacement owns the controller")
		return nil
	case KindFailed:
		return options.rollback(ctx, conn, child, fmt.Errorf("replacement failed: %s", message.Reason))
	default:
		return options.rollback(ctx, conn, child, fmt.Errorf("replacement sent an unexpected %q message", message.Kind))
	}
}

// rollback restores the parent after a failure that happened once the parent had
// stopped its core.
//
// The parent only takes ownership back after the replacement is confirmed gone:
// restarting the old core while the replacement might still own the lock would
// give two controllers the same system proxy. When the termination cannot be
// confirmed the parent stays alive and reports that instead.
func (o ParentOptions) rollback(ctx context.Context, conn Conn, child LaunchedProcess, cause error) error {
	_ = conn.Send(Message{Kind: KindAbort, Reason: cause.Error()})
	_ = conn.Close()
	if !child.Terminated(o.timeout(o.TerminationWait, DefaultTerminationWait)) {
		return fmt.Errorf("%w; the replacement is still running, so this controller stays up without a core", cause)
	}
	o.log("replacement is gone; taking the controller back")
	if o.Reacquire != nil {
		if err := o.Reacquire(); err != nil {
			return errors.Join(cause, fmt.Errorf("take instance ownership back: %w", err))
		}
	}
	if o.Restore != nil {
		if err := o.Restore(ctx); err != nil {
			return errors.Join(cause, fmt.Errorf("restore the previous controller: %w", err))
		}
	}
	return cause
}

// ChildOptions carries one handoff from the replacement's side.
type ChildOptions struct {
	Conn Conn

	// Validate checks the child's own options, configuration and core. The result
	// has to be retained by the caller: configuration must not be re-read after
	// readiness was acknowledged.
	Validate func(ctx context.Context) error
	// Acquire takes instance ownership after the parent released it.
	Acquire func(ctx context.Context) error
	// Start starts the validated core.
	Start func(ctx context.Context) error

	TransferTimeout time.Duration
	StartupTimeout  time.Duration

	Log func(string)
}

func (o ChildOptions) timeout(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

func (o ChildOptions) log(message string) {
	if o.Log != nil {
		o.Log(message)
	}
}

// Session is the replacement's side of a handoff that already owns the
// controller. It has to be confirmed once the replacement's user interface is up,
// or aborted.
//
// Releasing what a failed handoff took is the caller's job: it owns the core and
// the instance, and RunChild only reports the failure.
type Session struct {
	conn    Conn
	aborted chan struct{}
	log     func(string)
}

// RunChild performs the replacement's side of one handoff.
//
// The returned session is open: the replacement owns the lock and its core, and
// the parent is waiting for the confirmation. A non-nil error means the
// replacement must not continue as a controller.
func RunChild(ctx context.Context, options ChildOptions) (*Session, error) {
	if options.Conn == nil {
		return nil, errors.New("handoff connection is missing")
	}
	if options.Validate == nil || options.Acquire == nil || options.Start == nil {
		return nil, errors.New("handoff validation, acquire and start are all required")
	}
	// Validate before acquiring anything: a replacement that cannot run must not
	// disturb the controller it replaces.
	if err := options.Validate(ctx); err != nil {
		// The parent is waiting for readiness and shows the failure, so it has to
		// learn why instead of waiting for its readiness budget to expire.
		_ = options.Conn.Send(Message{Kind: KindFailed, Reason: err.Error()})
		return nil, fmt.Errorf("%w: %w", ErrReported, err)
	}
	if err := options.Conn.Send(Message{Kind: KindReady}); err != nil {
		return nil, fmt.Errorf("report handoff readiness: %w", err)
	}
	transferCtx, cancelTransfer := context.WithTimeout(ctx, options.timeout(options.TransferTimeout, DefaultTransferTimeout))
	message, err := options.Conn.Receive(transferCtx)
	cancelTransfer()
	if err != nil {
		return nil, fmt.Errorf("wait for the previous instance to hand over: %w", err)
	}
	switch message.Kind {
	case KindProceed:
	case KindAbort:
		return nil, fmt.Errorf("the previous instance aborted the handoff: %s", message.Reason)
	default:
		return nil, fmt.Errorf("the previous instance sent an unexpected %q message", message.Kind)
	}

	session := &Session{conn: options.Conn, aborted: make(chan struct{}), log: options.log}
	// An abort can arrive at any time before this process confirms the takeover:
	// while it acquires the instance, while it starts its core, while its tray
	// comes up. Watching for it here keeps the previous instance's rollback
	// bounded: it waits for this process to be gone, so an abort has to stop this
	// process's core instead of letting it keep running.
	go session.watchAbort()

	startupCtx, cancelStartup := session.startupContext(ctx)
	defer cancelStartup()
	if err := options.Acquire(startupCtx); err != nil {
		session.fail(startupCtx, err)
		return nil, fmt.Errorf("%w: %w", ErrReported, err)
	}
	if err := options.Start(startupCtx); err != nil {
		session.fail(startupCtx, err)
		return nil, fmt.Errorf("%w: %w", ErrReported, err)
	}
	if session.Aborted() {
		session.fail(nil, ErrAborted)
		return nil, ErrAborted
	}
	return session, nil
}

// startupContext is the context the acquisition and the core start run under. It
// is cancelled when the parent aborts the handoff, so a replacement that is still
// starting stops instead of keeping a controller nobody handed over.
func (s *Session) startupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	startupCtx, cancel := context.WithCancel(ctx)
	go func() {
		select {
		case <-s.aborted:
			cancel()
		case <-startupCtx.Done():
		}
	}()
	return startupCtx, cancel
}

// Confirm reports that the replacement owns the controller and its user interface
// is up. The parent stops waiting for a takeover result. ErrAborted means the
// parent gave up: the replacement has to stop its core and exit.
func (s *Session) Confirm() error {
	if s == nil {
		return nil
	}
	if s.Aborted() {
		return ErrAborted
	}
	if err := s.conn.Send(Message{Kind: KindTakeover}); err != nil {
		s.Close()
		return fmt.Errorf("confirm handoff takeover: %w", err)
	}
	s.Close()
	return nil
}

// Abort reports that the replacement could not take over. The caller releases
// what it took.
func (s *Session) Abort(reason string) {
	if s == nil {
		return
	}
	s.fail(nil, errors.New(reason))
}

func (s *Session) fail(ctx context.Context, cause error) {
	if ctx == nil || ctx.Err() == nil {
		_ = s.conn.Send(Message{Kind: KindFailed, Reason: cause.Error()})
	}
	s.Close()
}

func (s *Session) watchAbort() {
	for {
		message, err := s.conn.Receive(context.Background())
		if err != nil {
			// The connection is gone: either the parent gave up on the handoff or the
			// confirmation closed it. Nothing left to watch.
			return
		}
		if message.Kind == KindAbort {
			if s.log != nil {
				s.log("previous instance aborted the handoff: " + message.Reason)
			}
			s.abortOnce()
			return
		}
	}
}

func (s *Session) abortOnce() {
	select {
	case <-s.aborted:
	default:
		close(s.aborted)
	}
}

// Aborted reports whether the parent gave up on this handoff.
func (s *Session) Aborted() bool {
	if s == nil {
		return false
	}
	select {
	case <-s.aborted:
		return true
	default:
		return false
	}
}

// Close releases the handoff connection.
func (s *Session) Close() {
	if s == nil {
		return
	}
	_ = s.conn.Close()
}
