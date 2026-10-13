package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"sing-box-drover/internal/logging"
)

const MaxCapturedOutput = 8 << 10

const startupGracePeriod = 750 * time.Millisecond
const configCheckTimeout = 30 * time.Second

// StopGracePeriod bounds how long a stop waits for sing-box to exit on its own
// before the process is killed.
const StopGracePeriod = 10 * time.Second

// StopCleanupWait bounds the wait for the exit goroutine to release its process
// and job handles after the process is gone.
const StopCleanupWait = time.Second

// stopTotalTimeout bounds the complete stopping operation, including the wait
// for the publication slot. It applies even when the caller passes a context
// without a deadline (Close, Start): an unbounded wait would let a core that
// cannot be confirmed stopped look like a clean shutdown.
const stopTotalTimeout = StopGracePeriod + StopCleanupWait

// NotifyDrainTimeout bounds the wait for queued state notifications: shutdown
// waits for the terminal state before returning, and a start waits for the
// previous generation's callbacks before launching a replacement. It is part of
// the budget a replacement process has to outlast.
const NotifyDrainTimeout = StopGracePeriod

type State int

const (
	StateStopped State = iota
	StateStarting
	StateRunning
	StateStopping
	StateFailed
)

type EventKind int

const (
	EventState EventKind = iota
	EventError
)

type Event struct {
	Kind    EventKind
	State   State
	Message string
}

type boundedBuffer struct {
	mu   sync.Mutex
	data []byte
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(p) >= MaxCapturedOutput {
		b.data = append(b.data[:0], p[len(p)-MaxCapturedOutput:]...)
		return len(p), nil
	}
	if overflow := len(b.data) + len(p) - MaxCapturedOutput; overflow > 0 {
		copy(b.data, b.data[overflow:])
		b.data = b.data[:len(b.data)-overflow]
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(append([]byte(nil), b.data...))
}

type Supervisor struct {
	mu sync.Mutex
	// publishSem serializes storing a state and queueing its event so a
	// transition can never be reported after a later one that contradicts it.
	// It is a one-slot semaphore rather than a mutex so a stop can give up
	// waiting for a slow publisher when its context ends.
	publishSem chan struct{}
	// notifyMu guards the delivery queue. Events are queued while publishSem is
	// held, so the delivery order matches the order states were stored, while the
	// application handler runs on the delivery goroutine instead of on the caller:
	// a slow handler (restoring the system proxy takes seconds) must not delay
	// signalling the core, confirming its exit, or starting a replacement.
	notifyMu sync.Mutex
	// notifyQueue holds events waiting for delivery, notifyPending counts them
	// plus the event currently being delivered, and notifyIdle is closed once both
	// reach zero. A nil notifyIdle means the queue is already idle, so a waiter
	// never blocks on a stale channel.
	notifyQueue   []Event
	notifyPending int
	notifyIdle    chan struct{}
	// notifyWake carries one pending wake-up and notifyStop retires the delivery
	// goroutine once nothing can still be delivered.
	notifyWake chan struct{}
	notifyStop chan struct{}
	notifyOnce sync.Once
	exePath    string
	logger     *logging.Logger
	cmd        *exec.Cmd
	process    *os.Process
	job        processJob
	state      State
	output     *boundedBuffer
	// done is closed once this generation's process is gone, its OS handles are
	// released and its terminal state is queued - but before that state is
	// delivered. Waiting on it confirms the stop and keeps a follow-up generation's
	// notifications ordered behind it, without waiting for the application handler.
	done          chan struct{}
	gen           uint64
	stopRequested bool
	handler       func(Event)
}

func NewSupervisor(exePath string, logger *logging.Logger) *Supervisor {
	supervisor := &Supervisor{
		exePath:    exePath,
		logger:     logger,
		state:      StateStopped,
		publishSem: make(chan struct{}, 1),
		notifyWake: make(chan struct{}, 1),
		notifyStop: make(chan struct{}),
	}
	go supervisor.deliverNotifications()
	return supervisor
}

func (s *Supervisor) SetHandler(handler func(Event)) {
	s.mu.Lock()
	s.handler = handler
	s.mu.Unlock()
}

func (s *Supervisor) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *Supervisor) Running() bool {
	state := s.State()
	return state == StateStarting || state == StateRunning
}

func (s *Supervisor) LastOutput() string {
	s.mu.Lock()
	output := s.output
	s.mu.Unlock()
	if output == nil {
		return ""
	}
	return output.String()
}

// notify invokes the application handler. It runs on the delivery goroutine.
func (s *Supervisor) notify(event Event) {
	s.mu.Lock()
	handler := s.handler
	s.mu.Unlock()
	if handler != nil {
		handler(event)
	}
}

// Check asks the configured sing-box executable to validate the exact runtime
// JSON without disturbing the currently managed process.
func (s *Supervisor) Check(configJSON string) error {
	return s.CheckContext(context.Background(), configJSON)
}

// CheckContext is Check with the caller's context. The context bounds the check
// subprocess, so a cancelled resume does not leave a `sing-box check` running
// behind the recovery that started it.
func (s *Supervisor) CheckContext(ctx context.Context, configJSON string) error {
	return CheckExecutable(ctx, s.exePath, configJSON)
}

// CheckExecutable validates configJSON with the given sing-box executable. A
// replacement process reads its own sb-dir, so the preflight for it has to use
// the executable that replacement will run rather than the managed one.
func CheckExecutable(ctx context.Context, exePath, configJSON string) error {
	if strings.TrimSpace(exePath) == "" {
		return errors.New("sing-box executable path is empty")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, configCheckTimeout)
	defer cancel()
	output := &boundedBuffer{}
	cmd := exec.CommandContext(ctx, exePath, "--disable-color", "check", "-c", "stdin")
	cmd.Dir = executableDir(exePath)
	cmd.Stdin = strings.NewReader(configJSON)
	cmd.Stdout = io.MultiWriter(output)
	cmd.Stderr = io.MultiWriter(output)
	configureCommand(cmd)
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		message := "sing-box configuration check failed: " + err.Error()
		if detail := strings.TrimSpace(output.String()); detail != "" {
			message += "\n" + criticalOutput(detail)
		}
		return errors.New(message)
	}
	return nil
}

func (s *Supervisor) setState(state State, message string) {
	s.publishState(func() bool { return true }, state, message)
}

// publishState applies mutate, stores the resulting state and queues the
// matching event as one unit. mutate runs under the supervisor lock and reports
// whether the transition happened; when it does not, nothing is stored or
// queued.
//
// Serializing publication is what keeps a startup promotion from notifying
// "running" after the exit goroutine already notified "stopped", which would
// leave the tray describing a core that is gone.
func (s *Supervisor) publishState(mutate func() bool, state State, message string) bool {
	s.publishSem <- struct{}{}
	defer s.releasePublish()
	s.mu.Lock()
	changed := mutate()
	if changed {
		s.state = state
	}
	s.mu.Unlock()
	if !changed {
		return false
	}
	s.publishLocked(state, message)
	return true
}

// publishLocked queues a state that is already stored. Callers must hold the
// publication slot and must not hold the supervisor lock.
func (s *Supervisor) publishLocked(state State, message string) {
	s.enqueue(Event{Kind: EventState, State: state, Message: message})
}

// enqueue hands an event to the delivery goroutine. Callers hold the publication
// slot, which is what keeps delivery ordered. The call never blocks: storing a
// state and reporting it must not wait for a slow application handler.
func (s *Supervisor) enqueue(event Event) {
	s.notifyMu.Lock()
	if s.notifyPending == 0 {
		// Nothing is queued and nothing is being delivered: open a fresh idle channel
		// so waiters block until this event has been delivered. The queue being empty
		// is not enough here: the event being delivered has already left it.
		s.notifyIdle = make(chan struct{})
	}
	s.notifyQueue = append(s.notifyQueue, event)
	s.notifyPending++
	s.notifyMu.Unlock()
	select {
	case s.notifyWake <- struct{}{}:
	default:
		// A wake-up is already pending, so the delivery goroutine will re-check the
		// queue. The channel is buffered: dropping the wake-up here is safe, losing
		// it with an unbuffered channel would not be.
	}
}

// deliverNotifications delivers queued state events in order on its own
// goroutine. Keeping the handler off the publishing path is what bounds Stop and
// Start: the caller stores and queues a state and returns, while the handler runs
// here.
func (s *Supervisor) deliverNotifications() {
	for {
		select {
		case <-s.notifyWake:
		case <-s.notifyStop:
			return
		}
		for {
			s.notifyMu.Lock()
			if len(s.notifyQueue) == 0 {
				s.notifyMu.Unlock()
				break
			}
			event := s.notifyQueue[0]
			s.notifyQueue[0] = Event{}
			s.notifyQueue = s.notifyQueue[1:]
			s.notifyMu.Unlock()
			// Log on the delivery goroutine as well: a file write on the publishing
			// path could delay a stop just like a slow handler.
			if s.logger != nil {
				s.logger.Log("Core", strings.TrimSpace(event.Message))
			}
			s.notify(event)
			s.notifyMu.Lock()
			s.notifyPending--
			if s.notifyPending == 0 {
				// Only now is the queue idle: an event queued while this one was being
				// delivered keeps the channel open, so waiters cover it too.
				close(s.notifyIdle)
				s.notifyIdle = nil
			}
			s.notifyMu.Unlock()
		}
	}
}

// waitForNotifications waits until every queued state has been delivered. The
// application restores the system proxy from a terminal state, so shutdown must
// not drop that delivery; the wait is bounded because a handler that never
// returns must not hang shutdown either.
func (s *Supervisor) waitForNotifications(ctx context.Context) error {
	s.notifyMu.Lock()
	idle := s.notifyIdle
	s.notifyMu.Unlock()
	if idle == nil {
		return nil
	}
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("deliver core state notifications: %w", ctx.Err())
	}
}

func (s *Supervisor) Start(configJSON string) error {
	return s.StartContext(context.Background(), configJSON)
}

// StartContext cancels acquisition barriers and the startup grace period,
// without binding a successfully started core to the startup context.
func (s *Supervisor) StartContext(ctx context.Context, configJSON string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.Stop(ctx); err != nil {
		return err
	}
	// Barrier: the previous generation's callbacks have to finish before a new core
	// starts. A queued terminal callback disables the system proxy, and this core's
	// proxy is enabled right after startup, so delivering that callback later would
	// turn off the proxy the replacement just enabled. A blocked callback fails the
	// start with a clear error instead.
	barrier, cancelBarrier := context.WithTimeout(ctx, NotifyDrainTimeout)
	barrierErr := s.waitForNotifications(barrier)
	cancelBarrier()
	if barrierErr != nil {
		return barrierErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(s.exePath) == "" {
		return errors.New("sing-box executable path is empty")
	}
	s.setState(StateStarting, "starting sing-box")

	output := &boundedBuffer{}
	cmd := exec.Command(s.exePath, "--disable-color", "run", "-c", "stdin")
	cmd.Dir = executableDir(s.exePath)
	cmd.Stdin = strings.NewReader(configJSON)
	cmd.Stdout = io.MultiWriter(output)
	cmd.Stderr = io.MultiWriter(output)
	configureCommand(cmd)
	if err := cmd.Start(); err != nil {
		s.setState(StateFailed, "sing-box start failed: "+err.Error())
		return err
	}
	job, err := attachProcessJob(cmd.Process)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		s.setState(StateFailed, "cannot attach sing-box to a job: "+err.Error())
		return err
	}

	s.mu.Lock()
	s.stopRequested = false
	s.cmd = cmd
	s.process = cmd.Process
	s.job = job
	s.output = output
	s.done = make(chan struct{})
	s.gen++
	gen := s.gen
	done := s.done
	s.mu.Unlock()
	startupExit := make(chan error, 1)
	go s.wait(gen, cmd, done, startupExit)
	timer := time.NewTimer(startupGracePeriod)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return s.cancelStart(ctx.Err(), cmd.Process)
	case err := <-startupExit:
		message := "sing-box stopped during startup"
		if err != nil {
			message = "sing-box failed during startup: " + err.Error()
		}
		if output := strings.TrimSpace(s.LastOutput()); output != "" {
			message += "\n" + criticalOutput(output)
		}
		return errors.New(message)
	case <-timer.C:
		if err := ctx.Err(); err != nil {
			return s.cancelStart(err, cmd.Process)
		}
		if !s.promoteToRunning(gen) {
			// The process exited while the timer was pending. Its exit goroutine
			// already published the final state and the detailed message.
			return errors.New("sing-box stopped during startup")
		}
		return nil
	}
}

// cancelStart kills only the process launched by this startup and confirms exit.
func (s *Supervisor) cancelStart(cause error, process *os.Process) error {
	s.mu.Lock()
	if s.process == process {
		s.stopRequested = true
	}
	s.mu.Unlock()
	s.forceKill(process)
	ctx, cancel := context.WithTimeout(context.Background(), StopCleanupWait)
	defer cancel()
	return errors.Join(cause, s.Stop(ctx))
}

// promoteToRunning reports whether gen could be promoted to StateRunning.
//
// The exit goroutine clears the process fields before it publishes the terminal
// state, so a process that already exited is always refused here instead of
// being reported as running afterwards.
func (s *Supervisor) promoteToRunning(gen uint64) bool {
	return s.publishState(func() bool {
		return gen == s.gen && s.process != nil && s.state == StateStarting
	}, StateRunning, "sing-box running")
}

func (s *Supervisor) wait(gen uint64, cmd *exec.Cmd, done chan struct{}, startupExit chan<- error) {
	err := cmd.Wait()
	s.mu.Lock()
	if gen != s.gen {
		s.mu.Unlock()
		return
	}
	job := s.job
	stopRequested := s.stopRequested
	s.job = nil
	s.cmd = nil
	s.process = nil
	s.mu.Unlock()
	closeProcessJob(job)
	if err != nil && !stopRequested {
		message := "sing-box process exited unexpectedly: " + err.Error()
		if output := s.LastOutput(); strings.TrimSpace(output) != "" {
			message += "\n" + criticalOutput(output)
		}
		s.setState(StateFailed, message)
	} else {
		s.setState(StateStopped, "sing-box stopped")
	}
	close(done)
	if startupExit != nil {
		startupExit <- err
	}
}

func criticalOutput(output string) string {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "FATAL") || strings.HasPrefix(line, "PANIC") || strings.HasPrefix(line, "panic:") || strings.HasPrefix(line, "fatal error:") {
			if len(line) > 2000 {
				line = line[:2000]
			}
			return line
		}
	}
	return strings.TrimSpace(output)
}

// waitForStopCompletion signals the process and waits for the exit goroutine to
// finish this generation: the process is gone, its handles are released and its
// terminal state is queued. It reports whether that completion was observed:
// false means the process could not be confirmed gone, and the caller must not
// treat the stop as complete.
func waitForStopCompletion(cleanup func(), done <-chan struct{}, ctx context.Context, wait time.Duration, force func()) bool {
	if cleanup != nil {
		defer cleanup()
	}
	timer := time.NewTimer(wait)
	stopTimer := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
	stopped := false
	select {
	case <-done:
		stopped = true
		stopTimer()
	case <-ctx.Done():
		stopTimer()
		if force != nil {
			force()
		}
	case <-timer.C:
		if force != nil {
			force()
		}
	}

	// Wait for the goroutine to release the job/process handles. A short
	// bounded wait keeps shutdown responsive even if an OS API misbehaves; it is
	// also the last chance to observe a forced kill completing. It must not run
	// past the caller's deadline, or a stop would report success after its own
	// timeout.
	cleanupWait := StopCleanupWait
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < cleanupWait {
			cleanupWait = remaining
		}
	}
	if cleanupWait < 0 {
		cleanupWait = 0
	}
	select {
	case <-done:
		stopped = true
	case <-time.After(cleanupWait):
		// The wait goroutine still owns the process handle. Leave it to finish
		// rather than touching a handle that could be reused by a new process.
	}
	return stopped
}

func stopWaitDuration(ctx context.Context, signalErr error) time.Duration {
	wait := StopGracePeriod
	if signalErr != nil {
		wait = 0
	}
	if deadline, ok := ctx.Deadline(); ok {
		deadlineWait := time.Until(deadline)
		if deadlineWait < wait {
			wait = deadlineWait
			if wait < 0 {
				wait = 0
			}
		}
	}
	return wait
}

// acquirePublish takes the publication slot, giving up when ctx is done. The
// plain send used by publishState would wait forever behind a slow publisher, and
// a stop has to stay cancellable and bounded even then.
func (s *Supervisor) acquirePublish(ctx context.Context) error {
	select {
	case s.publishSem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Supervisor) releasePublish() { <-s.publishSem }

func (s *Supervisor) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// Bound the complete stopping operation, including the wait for the
	// publication slot, and report a core that cannot be confirmed stopped as a
	// failure instead of a clean shutdown.
	ctx, cancel := context.WithTimeout(ctx, stopTotalTimeout)
	defer cancel()
	if err := s.acquirePublish(ctx); err != nil {
		return fmt.Errorf("stop sing-box: %w", err)
	}
	s.mu.Lock()
	process := s.process
	done := s.done
	if process == nil {
		s.mu.Unlock()
		// Release the publication lock before waiting: the exit goroutine
		// queues the final state during that wait.
		s.releasePublish()
		// The wait goroutine clears process just before queueing done. Wait for that
		// queueing so a concurrent Start cannot report the new core before the
		// previous process's final state notification.
		if done != nil {
			select {
			case <-done:
			case <-ctx.Done():
				return fmt.Errorf("stop sing-box: %w", ctx.Err())
			}
		}
		return nil
	}
	s.state = StateStopping
	s.stopRequested = true
	s.mu.Unlock()
	// Signal the process before the stopping state is queued: queueing runs the
	// application handler, and a slow handler must not delay the stop signal. The
	// publication slot is still held, so the terminal state cannot be queued ahead
	// of this one.
	cleanup, signalErr := requestGracefulStop(process)
	s.publishLocked(StateStopping, "stopping sing-box")
	s.releasePublish()

	wait := stopWaitDuration(ctx, signalErr)
	stopped := waitForStopCompletion(cleanup, done, ctx, wait, func() { s.forceKill(process) })
	// Log after the wait: a synchronous file write inside the stop budget could
	// itself delay the shutdown that budget exists to bound.
	if signalErr != nil && s.logger != nil {
		s.logger.Log("Core", "graceful stop signal failed: "+signalErr.Error())
	}
	if !stopped {
		return errors.New("stop sing-box: the process did not exit within the shutdown deadline")
	}
	return nil
}

// Quiesce stops the core and waits for the state notifications it queued.
//
// A caller that is about to hand its controller to another process has to wait
// for those: a queued terminal state runs the application's handler, and a handler
// that reacts to a stopped core would act on the controller the replacement just
// took over.
func (s *Supervisor) Quiesce(ctx context.Context) error {
	if err := s.Stop(ctx); err != nil {
		return err
	}
	return s.waitForNotifications(ctx)
}

func (s *Supervisor) forceKill(process *os.Process) {
	if process == nil {
		return
	}
	// Closing a Windows Job Object terminates the whole tree, including any
	// descendants that may still hold the stdout/stderr pipe open. Clear the
	// field under the same lock used by wait so ownership is unambiguous.
	s.mu.Lock()
	var job processJob
	if s.process == process {
		job = s.job
		s.job = nil
	}
	s.mu.Unlock()
	closeProcessJob(job)
	_ = process.Kill()
}

func (s *Supervisor) Close() error {
	stopErr := s.Stop(context.Background())
	// Deliver the remaining states before shutdown returns: the application
	// restores the system proxy from a terminal state, so dropping that delivery
	// would leave the proxy pointing at a core that is gone.
	ctx, cancel := context.WithTimeout(context.Background(), NotifyDrainTimeout)
	defer cancel()
	drainErr := s.waitForNotifications(ctx)
	if stopErr == nil && drainErr == nil {
		// Retire the delivery goroutine only when nothing can still arrive: a later
		// terminal event would otherwise never reach the application.
		s.notifyOnce.Do(func() { close(s.notifyStop) })
	}
	return errors.Join(stopErr, drainErr)
}
