package handoff

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// memoryConn is one end of an in-memory handoff connection. It behaves like the
// pipe transport: a frame is delivered to the peer, and either end closing the
// connection ends it for both ends.
type memoryConn struct {
	in     chan Message
	out    chan Message
	closed chan struct{}
	// closeOnce is shared by both ends: either end closing the connection ends it
	// for both, and closing twice is not a second close.
	closeOnce *sync.Once
	mu        sync.Mutex
	sent      []Message
}

func newMemoryPair() (*memoryConn, *memoryConn) {
	toParent := make(chan Message, 16)
	toChild := make(chan Message, 16)
	closed := make(chan struct{})
	closeOnce := &sync.Once{}
	return &memoryConn{in: toParent, out: toChild, closed: closed, closeOnce: closeOnce},
		&memoryConn{in: toChild, out: toParent, closed: closed, closeOnce: closeOnce}
}

func (c *memoryConn) Send(message Message) error {
	c.mu.Lock()
	c.sent = append(c.sent, message)
	c.mu.Unlock()
	select {
	case <-c.closed:
		return errors.New("handoff connection is closed")
	default:
	}
	select {
	case c.out <- message:
		return nil
	case <-c.closed:
		return errors.New("handoff connection is closed")
	}
}

func (c *memoryConn) Receive(ctx context.Context) (Message, error) {
	// A frame that already arrived wins over a closed connection.
	select {
	case message := <-c.in:
		return message, nil
	default:
	}
	select {
	case message := <-c.in:
		return message, nil
	case <-c.closed:
		return Message{}, errors.New("handoff connection is closed")
	case <-ctx.Done():
		return Message{}, ctx.Err()
	}
}

func (c *memoryConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (c *memoryConn) kinds() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	kinds := make([]string, 0, len(c.sent))
	for _, message := range c.sent {
		kinds = append(kinds, message.Kind)
	}
	return kinds
}

// recorder collects the order in which both sides of a handoff touch the
// controller.
type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) add(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *recorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

func (r *recorder) step(event string) func(context.Context) error {
	return func(context.Context) error {
		r.add(event)
		return nil
	}
}

// stubProcess is a launched replacement that never really runs.
type stubProcess struct {
	terminated bool
	closed     bool
}

func (p *stubProcess) PID() int { return 4242 }
func (p *stubProcess) Terminated(time.Duration) bool {
	return p.terminated
}
func (p *stubProcess) Close() { p.closed = true }

// stubTransport hands the parent one prepared connection.
type stubTransport struct {
	conn Conn
	err  error
}

func (t stubTransport) Listen() (Listener, error) {
	if t.err != nil {
		return nil, t.err
	}
	return stubListener{conn: t.conn}, nil
}

type stubListener struct{ conn Conn }

func (l stubListener) Address() string { return "stub" }
func (l stubListener) Token() string   { return "stub-token" }
func (l stubListener) Accept(context.Context, int) (Conn, error) {
	return l.conn, nil
}
func (l stubListener) Close() error { return nil }

// parentOptions is a handoff whose every step is recorded and whose replacement
// is a stub.
func parentOptions(t *testing.T, conn Conn, events *recorder, process *stubProcess, launchErr error) ParentOptions {
	t.Helper()
	return ParentOptions{
		Transport: stubTransport{conn: conn},
		Flags:     "-restart",
		Launch: func(string, bool) (LaunchedProcess, error) {
			if launchErr != nil {
				return nil, launchErr
			}
			return process, nil
		},
		Transfer:         events.step("parent transfer"),
		Release:          func() { events.add("parent release") },
		Reacquire:        func() error { events.add("parent reacquire"); return nil },
		Restore:          events.step("parent restore"),
		ReadinessTimeout: 50 * time.Millisecond,
		TransferTimeout:  500 * time.Millisecond,
		StartupTimeout:   50 * time.Millisecond,
		TerminationWait:  50 * time.Millisecond,
	}
}

// childOptions is a replacement whose every step is recorded.
func childOptions(t *testing.T, conn Conn, events *recorder, validateErr, acquireErr, startErr error) ChildOptions {
	t.Helper()
	step := func(event string, failure error) func(context.Context) error {
		return func(context.Context) error {
			events.add(event)
			return failure
		}
	}
	return ChildOptions{
		Conn:            conn,
		Validate:        step("child validate", validateErr),
		Acquire:         step("child acquire", acquireErr),
		Start:           step("child start", startErr),
		TransferTimeout: 500 * time.Millisecond,
	}
}

func assertEvents(t *testing.T, events *recorder, want ...string) {
	t.Helper()
	got := events.list()
	if len(got) != len(want) {
		t.Fatalf("handoff steps = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("handoff steps = %v, want %v", got, want)
		}
	}
}

func receiveKind(t *testing.T, conn Conn, want string) Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	message, err := conn.Receive(ctx)
	if err != nil {
		t.Fatalf("receive %s: %v", want, err)
	}
	if message.Kind != want {
		t.Fatalf("received %q, want %q", message.Kind, want)
	}
	return message
}

func TestRunParentTransfersTheControllerOnceTheReplacementIsReady(t *testing.T) {
	parent, child := newMemoryPair()
	defer parent.Close()
	defer child.Close()

	events := &recorder{}
	process := &stubProcess{terminated: true}
	parentDone := make(chan error, 1)
	go func() { parentDone <- RunParent(context.Background(), parentOptions(t, parent, events, process, nil)) }()

	// The replacement has to report readiness before the parent gives anything up.
	if err := child.Send(Message{Kind: KindReady}); err != nil {
		t.Fatalf("send ready: %v", err)
	}
	receiveKind(t, child, KindProceed)
	if err := child.Send(Message{Kind: KindTakeover}); err != nil {
		t.Fatalf("send takeover: %v", err)
	}
	if err := <-parentDone; err != nil {
		t.Fatalf("RunParent() error = %v", err)
	}
	assertEvents(t, events, "parent transfer", "parent release")
	if !process.closed {
		t.Fatal("RunParent() did not release the launched replacement")
	}
}

func TestRunParentTakesNothingOverWhenTheReplacementReportsWhyItIsNotReady(t *testing.T) {
	parent, child := newMemoryPair()
	defer parent.Close()
	defer child.Close()

	events := &recorder{}
	process := &stubProcess{terminated: true}
	parentDone := make(chan error, 1)
	go func() { parentDone <- RunParent(context.Background(), parentOptions(t, parent, events, process, nil)) }()

	// The replacement connects, reports that it cannot run and exits. The parent
	// must show its reason instead of a generic readiness failure, and keep its
	// core, its proxy and its instance.
	if err := child.Send(Message{Kind: KindFailed, Reason: "the configuration is invalid"}); err != nil {
		t.Fatalf("send failed: %v", err)
	}
	err := <-parentDone
	if err == nil {
		t.Fatal("RunParent() succeeded without a ready replacement")
	}
	if !strings.Contains(err.Error(), "the configuration is invalid") {
		t.Fatalf("RunParent() error = %v, want the replacement's reason", err)
	}
	assertEvents(t, events)
	if !process.closed {
		t.Fatal("RunParent() did not release the replacement that reported a failure")
	}
}

func TestRunParentTakesNothingOverWhenTheReplacementNeverBecomesReady(t *testing.T) {
	parent, child := newMemoryPair()
	defer parent.Close()
	defer child.Close()

	events := &recorder{}
	process := &stubProcess{terminated: true}
	// The replacement connects and then hangs: nothing is handed over, so the
	// parent must keep its core, its proxy and its instance.
	err := RunParent(context.Background(), parentOptions(t, parent, events, process, nil))
	if err == nil {
		t.Fatal("RunParent() succeeded without a ready replacement")
	}
	if !strings.Contains(err.Error(), "did not become ready") {
		t.Fatalf("RunParent() error = %v, want a readiness failure", err)
	}
	assertEvents(t, events)
	if !process.closed {
		t.Fatal("RunParent() did not release the replacement that never became ready")
	}
}

func TestRunParentGivesNothingUpWhenTheReplacementCannotBeLaunched(t *testing.T) {
	parent, _ := newMemoryPair()
	defer parent.Close()

	launchErr := errors.New("elevation was refused")
	events := &recorder{}
	err := RunParent(context.Background(), parentOptions(t, parent, events, nil, launchErr))
	if !errors.Is(err, launchErr) {
		t.Fatalf("RunParent() error = %v, want %v", err, launchErr)
	}
	assertEvents(t, events)
}

func TestRunParentRollsBackWhenTheReplacementFailsAfterTheTransfer(t *testing.T) {
	parent, child := newMemoryPair()
	defer parent.Close()
	defer child.Close()

	events := &recorder{}
	process := &stubProcess{terminated: true}
	parentDone := make(chan error, 1)
	go func() { parentDone <- RunParent(context.Background(), parentOptions(t, parent, events, process, nil)) }()

	if err := child.Send(Message{Kind: KindReady}); err != nil {
		t.Fatalf("send ready: %v", err)
	}
	receiveKind(t, child, KindProceed)
	if err := child.Send(Message{Kind: KindFailed, Reason: "the core did not start"}); err != nil {
		t.Fatalf("send failed: %v", err)
	}
	err := <-parentDone
	if err == nil || !strings.Contains(err.Error(), "the core did not start") {
		t.Fatalf("RunParent() error = %v, want the replacement failure", err)
	}
	// The parent takes the controller back only after the replacement is gone.
	assertEvents(t, events, "parent transfer", "parent release", "parent reacquire", "parent restore")
}

func TestRunParentKeepsTheControllerWhenTheReplacementSurvivesTheRollback(t *testing.T) {
	parent, child := newMemoryPair()
	defer parent.Close()
	defer child.Close()

	events := &recorder{}
	// A replacement that is still running may still own the instance and the
	// system proxy, so the parent must not restart its own core.
	process := &stubProcess{terminated: false}
	parentDone := make(chan error, 1)
	go func() { parentDone <- RunParent(context.Background(), parentOptions(t, parent, events, process, nil)) }()

	if err := child.Send(Message{Kind: KindReady}); err != nil {
		t.Fatalf("send ready: %v", err)
	}
	receiveKind(t, child, KindProceed)
	if err := child.Send(Message{Kind: KindFailed, Reason: "the tray did not start"}); err != nil {
		t.Fatalf("send failed: %v", err)
	}
	err := <-parentDone
	if err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("RunParent() error = %v, want a still running replacement", err)
	}
	assertEvents(t, events, "parent transfer", "parent release")
	if !strings.Contains(strings.Join(parent.kinds(), ","), KindAbort) {
		t.Fatalf("parent sent %v, want an abort", parent.kinds())
	}
}

func TestRunParentAbortsAReplacementThatDoesNotConfirm(t *testing.T) {
	parent, child := newMemoryPair()
	defer parent.Close()
	defer child.Close()

	events := &recorder{}
	process := &stubProcess{terminated: true}
	parentDone := make(chan error, 1)
	go func() { parentDone <- RunParent(context.Background(), parentOptions(t, parent, events, process, nil)) }()

	if err := child.Send(Message{Kind: KindReady}); err != nil {
		t.Fatalf("send ready: %v", err)
	}
	receiveKind(t, child, KindProceed)
	// The replacement never confirms: the parent has to take the controller back.
	message := receiveKind(t, child, KindAbort)
	if message.Reason == "" {
		t.Fatal("abort carried no reason")
	}
	if err := <-parentDone; err == nil {
		t.Fatal("RunParent() succeeded without a takeover confirmation")
	}
	assertEvents(t, events, "parent transfer", "parent release", "parent reacquire", "parent restore")
}

func TestRunParentRollsBackOnAnUnexpectedMessage(t *testing.T) {
	parent, child := newMemoryPair()
	defer parent.Close()
	defer child.Close()

	events := &recorder{}
	process := &stubProcess{terminated: true}
	parentDone := make(chan error, 1)
	go func() { parentDone <- RunParent(context.Background(), parentOptions(t, parent, events, process, nil)) }()

	if err := child.Send(Message{Kind: KindReady}); err != nil {
		t.Fatalf("send ready: %v", err)
	}
	receiveKind(t, child, KindProceed)
	if err := child.Send(Message{Kind: KindReady}); err != nil {
		t.Fatalf("send ready: %v", err)
	}
	err := <-parentDone
	if err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("RunParent() error = %v, want an unexpected message failure", err)
	}
	assertEvents(t, events, "parent transfer", "parent release", "parent reacquire", "parent restore")
}

func TestRunParentRollsBackWhenTheReplacementCannotBeToldToTakeOver(t *testing.T) {
	parent, child := newMemoryPair()

	events := &recorder{}
	process := &stubProcess{terminated: true}
	parentDone := make(chan error, 1)
	go func() { parentDone <- RunParent(context.Background(), parentOptions(t, parent, events, process, nil)) }()

	if err := child.Send(Message{Kind: KindReady}); err != nil {
		t.Fatalf("send ready: %v", err)
	}
	receiveKind(t, child, KindProceed)
	// The replacement is gone before it can be told to take over.
	if err := child.Close(); err != nil {
		t.Fatalf("close the replacement connection: %v", err)
	}
	if err := <-parentDone; err == nil {
		t.Fatal("RunParent() succeeded although the replacement disappeared")
	}
	assertEvents(t, events, "parent transfer", "parent release", "parent reacquire", "parent restore")
}

func TestRunParentBringsTheCoreBackWhenTheTransferFails(t *testing.T) {
	parent, child := newMemoryPair()
	defer parent.Close()
	defer child.Close()

	events := &recorder{}
	process := &stubProcess{terminated: true}
	options := parentOptions(t, parent, events, process, nil)
	// The transfer stops the core before it can fail, so the core still has to come
	// back. Nothing was released, so ownership is not taken back either: the
	// replacement never received the proceed that would let it take it.
	options.Transfer = func(context.Context) error {
		events.add("parent transfer")
		return errors.New("stop the current controller")
	}
	parentDone := make(chan error, 1)
	go func() { parentDone <- RunParent(context.Background(), options) }()

	if err := child.Send(Message{Kind: KindReady}); err != nil {
		t.Fatalf("send ready: %v", err)
	}
	if err := <-parentDone; err == nil {
		t.Fatal("RunParent() succeeded although the transfer failed")
	}
	assertEvents(t, events, "parent transfer", "parent restore")
}

func TestRunParentRequiresATransportAndALaunch(t *testing.T) {
	if err := RunParent(context.Background(), ParentOptions{}); err == nil {
		t.Fatal("RunParent() without a transport succeeded")
	}
	if err := RunParent(context.Background(), ParentOptions{Transport: stubTransport{}}); err == nil {
		t.Fatal("RunParent() without a launch succeeded")
	}
}

func TestRunChildTakesNothingOverWhenItCannotRun(t *testing.T) {
	parent, child := newMemoryPair()
	defer parent.Close()
	defer child.Close()

	events := &recorder{}
	validateErr := errors.New("the configuration is invalid")
	session, err := RunChild(context.Background(), childOptions(t, child, events, validateErr, nil, nil))
	if !errors.Is(err, validateErr) {
		t.Fatalf("RunChild() error = %v, want %v", err, validateErr)
	}
	if session != nil {
		t.Fatal("RunChild() returned a session for a replacement that cannot run")
	}
	// The parent must be told why the replacement cannot run, and must not be told
	// to give anything up.
	if kinds := child.kinds(); len(kinds) != 1 || kinds[0] != KindFailed {
		t.Fatalf("child sent %v, want one %q message", kinds, KindFailed)
	}
	if kinds := parent.kinds(); len(kinds) != 0 {
		t.Fatalf("parent sent %v, want no message", kinds)
	}
	if !errors.Is(err, ErrReported) {
		t.Fatalf("RunChild() error = %v, want a failure reported to the previous instance", err)
	}
	assertEvents(t, events, "child validate")
}

func TestRunChildWaitsForTheParentBeforeItTakesTheController(t *testing.T) {
	parent, child := newMemoryPair()
	defer parent.Close()
	defer child.Close()

	events := &recorder{}
	childDone := make(chan error, 1)
	go func() {
		session, err := RunChild(context.Background(), childOptions(t, child, events, nil, nil, nil))
		if err == nil {
			err = session.Confirm()
		}
		childDone <- err
	}()

	receiveKind(t, parent, KindReady)
	assertEvents(t, events, "child validate")
	if err := parent.Send(Message{Kind: KindProceed}); err != nil {
		t.Fatalf("send proceed: %v", err)
	}
	receiveKind(t, parent, KindTakeover)
	if err := <-childDone; err != nil {
		t.Fatalf("replacement error = %v", err)
	}
	assertEvents(t, events, "child validate", "child acquire", "child start")
}

func TestRunChildRefusesAnAbortInsteadOfTheHandover(t *testing.T) {
	parent, child := newMemoryPair()
	defer parent.Close()
	defer child.Close()

	events := &recorder{}
	childDone := make(chan error, 1)
	go func() {
		_, err := RunChild(context.Background(), childOptions(t, child, events, nil, nil, nil))
		childDone <- err
	}()

	receiveKind(t, parent, KindReady)
	if err := parent.Send(Message{Kind: KindAbort, Reason: "the parent gave up"}); err != nil {
		t.Fatalf("send abort: %v", err)
	}
	err := <-childDone
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("RunChild() error = %v, want an abort", err)
	}
	assertEvents(t, events, "child validate")
}

func TestRunChildReportsAFailureToTakeTheControllerOver(t *testing.T) {
	parent, child := newMemoryPair()
	defer parent.Close()
	defer child.Close()

	events := &recorder{}
	startErr := errors.New("the core did not start")
	childDone := make(chan error, 1)
	go func() {
		_, err := RunChild(context.Background(), childOptions(t, child, events, nil, nil, startErr))
		childDone <- err
	}()

	receiveKind(t, parent, KindReady)
	if err := parent.Send(Message{Kind: KindProceed}); err != nil {
		t.Fatalf("send proceed: %v", err)
	}
	message := receiveKind(t, parent, KindFailed)
	if !strings.Contains(message.Reason, startErr.Error()) {
		t.Fatalf("failure reason = %q, want %v", message.Reason, startErr)
	}
	if err := <-childDone; !errors.Is(err, startErr) {
		t.Fatalf("RunChild() error = %v, want %v", err, startErr)
	}
	assertEvents(t, events, "child validate", "child acquire", "child start")
}

func TestRunChildCancelsAcquisitionAndStartup(t *testing.T) {
	for _, stage := range []string{"acquire", "start"} {
		for _, cause := range []string{"abort", "disconnect", "timeout"} {
			t.Run(stage+"/"+cause, func(t *testing.T) {
				parent, child := newMemoryPair()
				defer parent.Close()
				defer child.Close()
				started := make(chan struct{})
				options := childOptions(t, child, &recorder{}, nil, nil, nil)
				waitForCancellation := func(ctx context.Context) error {
					close(started)
					<-ctx.Done()
					return ctx.Err()
				}
				if stage == "acquire" {
					options.Acquire = waitForCancellation
				} else {
					options.Start = waitForCancellation
				}
				if cause == "timeout" {
					options.StartupTimeout = 50 * time.Millisecond
				}
				childDone := make(chan error, 1)
				go func() {
					_, err := RunChild(context.Background(), options)
					childDone <- err
				}()
				receiveKind(t, parent, KindReady)
				if err := parent.Send(Message{Kind: KindProceed}); err != nil {
					t.Fatal(err)
				}
				select {
				case <-started:
				case <-time.After(2 * time.Second):
					t.Fatal("startup stage did not begin")
				}
				switch cause {
				case "abort":
					if err := parent.Send(Message{Kind: KindAbort, Reason: "parent gave up"}); err != nil {
						t.Fatal(err)
					}
				case "disconnect":
					parent.Close()
				}
				select {
				case err := <-childDone:
					want := ErrAborted
					if cause == "timeout" {
						want = ErrReported
					}
					if !errors.Is(err, want) {
						t.Fatalf("cancelled handoff = %v, want %v", err, want)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("cancelled acquisition/startup did not return")
				}
			})
		}
	}
}

func TestRunChildReportsItsStartupDeadlineToTheParent(t *testing.T) {
	parent, child := newMemoryPair()
	defer parent.Close()
	defer child.Close()
	options := childOptions(t, child, &recorder{}, nil, nil, nil)
	options.StartupTimeout = 20 * time.Millisecond
	options.Start = func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	result := make(chan error, 1)
	go func() {
		_, err := RunChild(context.Background(), options)
		result <- err
	}()
	receiveKind(t, parent, KindReady)
	if err := parent.Send(Message{Kind: KindProceed}); err != nil {
		t.Fatal(err)
	}
	failure := receiveKind(t, parent, KindFailed)
	if !strings.Contains(failure.Reason, context.DeadlineExceeded.Error()) {
		t.Fatalf("reported startup deadline = %q", failure.Reason)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrReported) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("startup deadline = %v, want reported deadline", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out startup did not return")
	}
}

func TestSessionConfirmReportsAnAbortThatArrivesBeforeTheTrayIsUp(t *testing.T) {
	testSessionAbortedBeforeTray(t, false)
}

func TestSessionConfirmRejectsDisconnectBeforeTheTrayIsUp(t *testing.T) {
	testSessionAbortedBeforeTray(t, true)
}

func testSessionAbortedBeforeTray(t *testing.T, disconnect bool) {
	t.Helper()
	parent, child := newMemoryPair()
	defer parent.Close()
	defer child.Close()

	events := &recorder{}
	childDone := make(chan *Session, 1)
	childErr := make(chan error, 1)
	go func() {
		session, err := RunChild(context.Background(), childOptions(t, child, events, nil, nil, nil))
		if err != nil {
			childErr <- err
			return
		}
		childDone <- session
	}()

	receiveKind(t, parent, KindReady)
	if err := parent.Send(Message{Kind: KindProceed}); err != nil {
		t.Fatalf("send proceed: %v", err)
	}
	var session *Session
	select {
	case session = <-childDone:
	case err := <-childErr:
		t.Fatalf("RunChild() error = %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("RunChild() did not return")
	}
	if disconnect {
		parent.Close()
	} else if err := parent.Send(Message{Kind: KindAbort, Reason: "the parent gave up"}); err != nil {
		t.Fatalf("send abort: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !session.Aborted() {
		if time.Now().After(deadline) {
			t.Fatal("the replacement did not notice the abort")
		}
		time.Sleep(time.Millisecond)
	}
	if err := session.Confirm(); !errors.Is(err, ErrAborted) {
		t.Fatalf("Confirm() error = %v, want %v", err, ErrAborted)
	}
}

func TestSessionAbortTellsTheParentTheTakeoverFailed(t *testing.T) {
	parent, child := newMemoryPair()
	defer parent.Close()
	defer child.Close()

	events := &recorder{}
	childDone := make(chan *Session, 1)
	childErr := make(chan error, 1)
	go func() {
		session, err := RunChild(context.Background(), childOptions(t, child, events, nil, nil, nil))
		if err != nil {
			childErr <- err
			return
		}
		childDone <- session
	}()

	receiveKind(t, parent, KindReady)
	if err := parent.Send(Message{Kind: KindProceed}); err != nil {
		t.Fatalf("send proceed: %v", err)
	}
	var session *Session
	select {
	case session = <-childDone:
	case err := <-childErr:
		t.Fatalf("RunChild() error = %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("RunChild() did not return")
	}

	session.Abort("the tray did not start")
	message := receiveKind(t, parent, KindFailed)
	if message.Reason != "the tray did not start" {
		t.Fatalf("failure reason = %q", message.Reason)
	}
}

func TestHandoffFlagsCarryTheAddressTokenAndPeer(t *testing.T) {
	flags := handoffFlags("-restart -tun", `\\.\pipe\sing-box-drover-handoff-x`, "00ff")
	for _, want := range []string{
		"-restart -tun",
		"-" + FlagPipe + ` \\.\pipe\sing-box-drover-handoff-x`,
		"-" + FlagToken + " 00ff",
		"-" + FlagPeer + " " + strconv.Itoa(os.Getpid()),
	} {
		if !strings.Contains(flags, want) {
			t.Fatalf("handoff flags %q do not contain %q", flags, want)
		}
	}
	if strings.ContainsAny(flags, `"'`) {
		t.Fatalf("handoff flags %q need quoting", flags)
	}
}

func TestRunChildRequiresItsSteps(t *testing.T) {
	parent, child := newMemoryPair()
	defer parent.Close()
	defer child.Close()

	if _, err := RunChild(context.Background(), ChildOptions{}); err == nil {
		t.Fatal("RunChild() without a connection succeeded")
	}
	if _, err := RunChild(context.Background(), ChildOptions{Conn: child}); err == nil {
		t.Fatal("RunChild() without its steps succeeded")
	}
}

// TestHandoffTransfersTheController checks the two sides against each other: the
// parent has to stop its core before the replacement takes the instance over, and
// it must not touch its own runtime again once the replacement confirmed.
func TestHandoffTransfersTheController(t *testing.T) {
	parent, child := newMemoryPair()
	defer parent.Close()
	defer child.Close()

	events := &recorder{}
	process := &stubProcess{terminated: true}
	parentDone := make(chan error, 1)
	childDone := make(chan error, 1)
	go func() { parentDone <- RunParent(context.Background(), parentOptions(t, parent, events, process, nil)) }()
	go func() {
		session, err := RunChild(context.Background(), childOptions(t, child, events, nil, nil, nil))
		if err == nil {
			err = session.Confirm()
		}
		childDone <- err
	}()

	if err := <-parentDone; err != nil {
		t.Fatalf("RunParent() error = %v", err)
	}
	if err := <-childDone; err != nil {
		t.Fatalf("replacement error = %v", err)
	}
	assertEvents(t, events,
		"child validate",
		"parent transfer",
		"parent release",
		"child acquire",
		"child start",
	)
}

func TestRunParentReportsTheReasonOfAFailedReplacement(t *testing.T) {
	parent, child := newMemoryPair()
	defer parent.Close()
	defer child.Close()

	events := &recorder{}
	process := &stubProcess{terminated: true}
	parentDone := make(chan error, 1)
	go func() { parentDone <- RunParent(context.Background(), parentOptions(t, parent, events, process, nil)) }()

	if err := child.Send(Message{Kind: KindReady}); err != nil {
		t.Fatalf("send ready: %v", err)
	}
	receiveKind(t, child, KindProceed)
	if err := child.Send(Message{Kind: KindFailed, Reason: "the port is already in use"}); err != nil {
		t.Fatalf("send failed: %v", err)
	}
	err := <-parentDone
	if err == nil || !strings.Contains(err.Error(), "the port is already in use") {
		t.Fatalf("RunParent() error = %v, want the replacement's reason", err)
	}
}

func TestSessionCloseIsIdempotent(t *testing.T) {
	parent, child := newMemoryPair()
	defer parent.Close()
	defer child.Close()

	session := &Session{conn: child, aborted: make(chan struct{}), log: func(string) {}}
	session.Close()
	session.Close()
	if session.Aborted() {
		t.Fatal("a closed session reports an abort")
	}
	var missing *Session
	if err := missing.Confirm(); err != nil {
		t.Fatalf("Confirm() on a missing session = %v", err)
	}
	missing.Close()
	missing.Abort("nothing to abort")
	if missing.Aborted() {
		t.Fatal("a missing session reports an abort")
	}
}
