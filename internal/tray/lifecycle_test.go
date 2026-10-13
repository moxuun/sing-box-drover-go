package tray

import (
	"errors"
	"testing"
	"time"

	"github.com/egoist/mygo"

	"sing-box-drover/internal/app"
)

func TestResumeRecoveryGateSerializesConcurrentEvents(t *testing.T) {
	tray := &Tray{}
	if !tray.beginResumeRecovery() {
		t.Fatal("first resume recovery was rejected")
	}
	if tray.beginResumeRecovery() {
		t.Fatal("concurrent resume recovery was admitted")
	}
	tray.finishResumeRecovery()
	if !tray.beginResumeRecovery() {
		t.Fatal("resume recovery did not reopen after completion")
	}
	tray.finishResumeRecovery()
}

func TestNativeTerminationCleansUpBeforeBlockedGUIWorkReturns(t *testing.T) {
	tray := &Tray{
		controller: &app.App{}, actions: make(chan func(), 1), done: make(chan struct{}),
		workerDone: make(chan struct{}), stopped: make(chan struct{}),
	}
	started := make(chan struct{})
	release := make(chan struct{})
	tray.actions <- func() { close(started); <-release }
	go tray.work()
	<-started
	finished := make(chan struct{})
	go func() { tray.nativeQuit(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("native termination waited for work requiring the GUI callback to return")
	}
	select {
	case <-tray.stopped:
	default:
		t.Fatal("native termination returned before controller cleanup")
	}
	if tray.enqueue(func() { t.Error("work ran after native termination") }) {
		t.Fatal("native termination accepted new work")
	}
	if err := tray.controller.SwitchSelector(nil, "unused", "unused"); err == nil || err.Error() != "controller is closed" {
		close(release)
		t.Fatalf("native termination did not close the controller: %v", err)
	}
	tray.nativeQuit() // repeated native termination is safe
	close(release)
	<-tray.workerDone
}

func TestControllerCleanupRetriesAfterFailure(t *testing.T) {
	tray := &Tray{done: make(chan struct{}), stopped: make(chan struct{})}
	failure := errors.New("core did not stop")
	calls := 0
	closeController := func() error {
		calls++
		if calls == 1 {
			return failure
		}
		return nil
	}
	if err := tray.closeController(closeController); !errors.Is(err, failure) {
		t.Fatalf("first cleanup = %v, want %v", err, failure)
	}
	select {
	case <-tray.stopped:
		t.Fatal("failed cleanup allowed the native application to quit")
	default:
	}
	close(tray.done)
	other := &mygo.MenuItem{ID: "tun", Type: mygo.MenuItemCheckbox, Click: func(*mygo.MenuItem, *mygo.Window) {
		t.Error("a non-Quit action ran during shutdown")
	}}
	tray.dispatchMenuItem(other)
	if other.IsChecked() {
		t.Fatal("shutdown dispatch changed a rejected checkbox")
	}
	tray.dispatchMenuItem(&mygo.MenuItem{ID: "quit", Click: func(*mygo.MenuItem, *mygo.Window) {
		if err := tray.closeController(closeController); err != nil {
			t.Fatalf("retry cleanup = %v", err)
		}
	}})
	select {
	case <-tray.stopped:
	default:
		t.Fatal("successful retry did not complete shutdown")
	}
	if err := tray.closeController(closeController); err != nil || calls != 2 {
		t.Fatalf("completed cleanup ran again: calls=%d error=%v", calls, err)
	}
}

func TestActionsRunSeriallyAndRejectWorkAfterShutdown(t *testing.T) {
	tray := &Tray{actions: make(chan func(), 3), done: make(chan struct{}), workerDone: make(chan struct{})}
	started := make(chan struct{})
	release := make(chan struct{})
	result := make(chan int, 2)
	if !tray.enqueue(func() {
		close(started)
		<-release
		result <- 1
	}) || !tray.enqueue(func() { result <- 2 }) {
		t.Fatal("failed to queue controller actions")
	}
	go tray.work()
	<-started
	select {
	case <-result:
		t.Fatal("a later action ran while the first was still active")
	default:
	}
	close(release)
	for _, want := range []int{1, 2} {
		select {
		case got := <-result:
			if got != want {
				t.Fatalf("controller actions ran out of order: got %d, want %d", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("controller action did not complete")
		}
	}
	close(tray.done)
	select {
	case <-tray.workerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("controller worker did not stop")
	}
	if tray.enqueue(func() { t.Error("action ran after shutdown") }) {
		t.Fatal("controller worker accepted work after shutdown")
	}
}
