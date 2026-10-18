package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"sing-box-drover/internal/core"
	"sing-box-drover/internal/handoff"
	platform "sing-box-drover/internal/windows"
)

// fakeConn is one end of a handoff connection a test drives by hand.
type fakeConn struct {
	mu       sync.Mutex
	toChild  chan handoff.Message
	toParent chan handoff.Message
	closed   chan struct{}
	once     sync.Once
}

func newFakeConn() *fakeConn {
	return &fakeConn{
		toChild:  make(chan handoff.Message, 8),
		toParent: make(chan handoff.Message, 8),
		closed:   make(chan struct{}),
	}
}

func (c *fakeConn) Send(message handoff.Message) error {
	select {
	case c.toChild <- message:
		return nil
	case <-c.closed:
		return errors.New("handoff connection is closed")
	}
}

func (c *fakeConn) Receive(ctx context.Context) (handoff.Message, error) {
	select {
	case message := <-c.toParent:
		return message, nil
	case <-c.closed:
		return handoff.Message{}, errors.New("handoff connection is closed")
	case <-ctx.Done():
		return handoff.Message{}, ctx.Err()
	}
}

func (c *fakeConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

// childSends delivers a message from the replacement to the controller.
func (c *fakeConn) childSends(t *testing.T, kind string) {
	t.Helper()
	select {
	case c.toParent <- handoff.Message{Kind: kind}:
	case <-time.After(handoffTestTimeout):
		t.Fatalf("the controller never read the %q message", kind)
	}
}

// childReceives waits for a message the controller sends to the replacement.
func (c *fakeConn) childReceives(t *testing.T) handoff.Message {
	t.Helper()
	select {
	case message := <-c.toChild:
		return message
	case <-time.After(handoffTestTimeout):
		t.Fatal("the controller sent no message to the replacement")
		return handoff.Message{}
	}
}

type fakeListener struct{ conn handoff.Conn }

func (l fakeListener) Address() string { return "test" }
func (l fakeListener) Token() string   { return "test-token" }
func (l fakeListener) Accept(context.Context, int) (handoff.Conn, error) {
	return l.conn, nil
}
func (l fakeListener) Close() error { return nil }

type fakeTransport struct{ conn handoff.Conn }

func (t fakeTransport) Listen() (handoff.Listener, error) {
	return fakeListener{conn: t.conn}, nil
}

type fakeProcess struct {
	pid        int
	terminated bool
	closed     bool
}

func (p *fakeProcess) PID() int { return p.pid }
func (p *fakeProcess) Terminated(time.Duration) bool {
	return p.terminated
}
func (p *fakeProcess) Close() { p.closed = true }

const handoffTestTimeout = 5 * time.Second

// handoffTestController is a controller whose core, instance and proxy are all
// observed by the test instead of being real.
type handoffTestController struct {
	*App
	conn     *fakeConn
	process  *fakeProcess
	launches int
	cores    int
	proxies  int
}

func newHandoffTestController(t *testing.T) *handoffTestController {
	t.Helper()
	controller := &handoffTestController{
		App: &App{
			supervisor: core.NewSupervisor("", nil),
			events:     make(chan Event, 1),
		},
		conn:    newFakeConn(),
		process: &fakeProcess{pid: 4242},
	}
	// The controller starts out owning the instance lock: a handoff that keeps it,
	// gives it up or takes it back is only observable from a state that has it.
	// A fake lock also makes Windows tests independent of the real controller.
	controller.instanceAcquirer = func() (*platform.SingleInstance, bool, error) {
		return &platform.SingleInstance{}, true, nil
	}
	instance, first, err := controller.instanceAcquirer()
	if err != nil || !first {
		t.Fatalf("acquire the test instance lock: acquired=%v err=%v", first, err)
	}
	controller.App.instance = instance
	t.Cleanup(func() {
		_ = controller.conn.Close()
		if err := controller.App.Close(); err != nil {
			t.Errorf("close the handoff test controller: %v", err)
		}
	})
	controller.handoffTransportFn = func() (handoff.Transport, error) {
		return fakeTransport{conn: controller.conn}, nil
	}
	controller.handoffLauncher = func(string, bool) (handoff.LaunchedProcess, error) {
		controller.launches++
		return controller.process, nil
	}
	controller.selfLauncher = func(string, bool) error {
		t.Error("a controller with a handoff transport used the one-shot launcher")
		return nil
	}
	controller.coreStarter = func(context.Context, string) error {
		controller.cores++
		return nil
	}
	// A restarted core reports itself as running, which is what the supervisor
	// does once sing-box survived its startup grace period.
	controller.coreState = func() core.State { return core.StateRunning }
	controller.systemProxyEnabler = func(string, int) (platform.ProxySession, error) {
		controller.proxies++
		return platform.ProxySession{}, nil
	}
	controller.systemProxyRestorer = func(platform.ProxySession) (bool, error) {
		return true, nil
	}
	return controller
}

// run starts a handoff launch in the background and returns its result channel.
func (c *handoffTestController) run(t *testing.T) <-chan error {
	t.Helper()
	result := make(chan error, 1)
	go func() { result <- c.launchReplacement("-restart", false, false) }()
	return result
}

func TestLaunchReplacementTransfersTheControllerToAConfirmedReplacement(t *testing.T) {
	controller := newHandoffTestController(t)
	result := controller.run(t)

	// The replacement is launched, but the controller keeps its core until the
	// replacement reports that it is ready.
	select {
	case message := <-controller.conn.toChild:
		t.Fatalf("controller sent %q before the replacement was ready", message.Kind)
	case <-time.After(20 * time.Millisecond):
	}
	if controller.cores != 0 {
		t.Fatalf("the core was restarted before the replacement was ready")
	}

	controller.conn.childSends(t, handoff.KindReady)
	if message := controller.conn.childReceives(t); message.Kind != handoff.KindProceed {
		t.Fatalf("controller sent %q, want %q", message.Kind, handoff.KindProceed)
	}
	controller.conn.childSends(t, handoff.KindTakeover)

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("launchReplacement() error = %v", err)
		}
	case <-time.After(handoffTestTimeout):
		t.Fatal("launchReplacement() did not return")
	}
	if controller.launches != 1 {
		t.Fatalf("replacement launches = %d, want 1", controller.launches)
	}
	// The replacement owns the core and the proxy now: this controller must not
	// take either of them back.
	if controller.cores != 0 {
		t.Fatalf("the controller restarted its core after a successful handoff")
	}
	if controller.proxies != 0 {
		t.Fatalf("the controller restored the system proxy after a successful handoff")
	}
	if controller.App.instance != nil {
		t.Fatal("the controller kept instance ownership after a successful handoff")
	}
	if !controller.App.handedOver {
		t.Fatal("the controller did not record that it handed the instance over")
	}
}

func TestLaunchReplacementRestartsTheCoreWhenTheReplacementFails(t *testing.T) {
	controller := newHandoffTestController(t)
	controller.App.proxyActive = true
	controller.App.proxyOwned = true
	result := controller.run(t)

	controller.conn.childSends(t, handoff.KindReady)
	if message := controller.conn.childReceives(t); message.Kind != handoff.KindProceed {
		t.Fatalf("controller sent %q, want %q", message.Kind, handoff.KindProceed)
	}
	// The replacement took the controller over, failed to start its core and is
	// gone, so this controller has to take everything back.
	controller.process.terminated = true
	controller.conn.childSends(t, handoff.KindFailed)

	select {
	case err := <-result:
		if err == nil {
			t.Fatal("launchReplacement() succeeded although the replacement failed")
		}
		if !strings.Contains(err.Error(), "replacement") {
			t.Fatalf("launchReplacement() error = %v", err)
		}
	case <-time.After(handoffTestTimeout):
		t.Fatal("launchReplacement() did not return")
	}
	// The rollback has to leave this controller running the core and the proxy.
	if controller.cores != 1 {
		t.Fatalf("core restarts = %d, want 1", controller.cores)
	}
	if controller.proxies != 1 {
		t.Fatalf("system proxy restores = %d, want 1", controller.proxies)
	}
	if controller.App.instance == nil {
		t.Fatal("the controller did not take instance ownership back")
	}
	if controller.App.handedOver {
		t.Fatal("the controller kept the handover recorded after taking ownership back")
	}
}

func TestLaunchReplacementKeepsRunningWithoutACoreWhenTheReplacementSurvives(t *testing.T) {
	controller := newHandoffTestController(t)
	result := controller.run(t)

	controller.conn.childSends(t, handoff.KindReady)
	if message := controller.conn.childReceives(t); message.Kind != handoff.KindProceed {
		t.Fatalf("controller sent %q, want %q", message.Kind, handoff.KindProceed)
	}
	// The replacement failed, but it is still running: starting the core here
	// would put two cores on the machine, so this controller stays up without one.
	controller.conn.childSends(t, handoff.KindFailed)

	select {
	case err := <-result:
		if err == nil {
			t.Fatal("launchReplacement() succeeded although the replacement failed")
		}
	case <-time.After(handoffTestTimeout):
		t.Fatal("launchReplacement() did not return")
	}
	if controller.cores != 0 {
		t.Fatalf("core restarts = %d, want 0 while the replacement runs", controller.cores)
	}
	if controller.App.instance != nil {
		t.Fatal("the controller kept the instance lock it handed to the replacement")
	}
	if !controller.App.handedOver {
		t.Fatal("the controller did not record that it handed the instance over")
	}
	// Ownership only comes back by taking the lock again, so every start path has
	// to refuse instead of competing with the replacement's core.
	if err := controller.App.startCore(); !errors.Is(err, ErrHandoffOwnershipLost) {
		t.Fatalf("startCore() after the handover = %v, want %v", err, ErrHandoffOwnershipLost)
	}
	if controller.cores != 0 {
		t.Fatalf("core restarts = %d, want 0 after the refused start", controller.cores)
	}
}

func TestLaunchReplacementKeepsTheCoreWhenTheReplacementIsNotReady(t *testing.T) {
	controller := newHandoffTestController(t)
	controller.conn.childSends(t, handoff.KindFailed)
	result := controller.run(t)

	select {
	case err := <-result:
		if err == nil {
			t.Fatal("launchReplacement() succeeded although the replacement failed")
		}
	case <-time.After(handoffTestTimeout):
		t.Fatal("launchReplacement() did not return")
	}
	// Nothing was handed over, so the core has to keep running untouched and the
	// instance lock has to stay here.
	if controller.cores != 0 {
		t.Fatalf("core restarts = %d, want 0", controller.cores)
	}
	if controller.App.instance == nil {
		t.Fatal("the controller gave up instance ownership before the replacement was ready")
	}
	if controller.App.handedOver {
		t.Fatal("the controller recorded a handover although nothing was handed over")
	}
}

func TestLaunchReplacementFallsBackToTheOneShotLaunchWithoutATransport(t *testing.T) {
	controller := newHandoffTestController(t)
	controller.handoffTransportFn = nil
	launched := 0
	controller.selfLauncher = func(flags string, elevated bool) error {
		launched++
		if !strings.Contains(flags, "-restart") {
			t.Fatalf("one-shot launch flags = %q, want the restart flag", flags)
		}
		return nil
	}

	if err := controller.launchReplacement("-restart", false, false); err != nil {
		t.Fatalf("launchReplacement() error = %v", err)
	}
	if launched != 1 {
		t.Fatalf("one-shot launches = %d, want 1", launched)
	}
	if controller.launches != 0 {
		t.Fatal("a controller without a transport used the handoff launcher")
	}
}
