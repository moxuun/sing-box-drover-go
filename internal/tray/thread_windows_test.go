//go:build windows

package tray

import (
	"runtime"
	"testing"

	winapi "golang.org/x/sys/windows"
)

func TestShouldRestoreTrayIconAfterTaskbarRecreationOrResume(t *testing.T) {
	const taskbarCreated = 0xc123
	for _, test := range []struct {
		name    string
		message uint32
		wParam  uintptr
		want    bool
	}{
		{name: "taskbar recreated", message: taskbarCreated, want: true},
		{name: "automatic resume", message: wmPowerBroadcast, wParam: pbtAPMResumeAutomatic, want: true},
		{name: "other power event", message: wmPowerBroadcast, wParam: 0x000a, want: false},
		{name: "unrelated message", message: wmNull, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldRestoreTrayIcon(test.message, test.wParam, taskbarCreated); got != test.want {
				t.Fatalf("shouldRestoreTrayIcon() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRunOnTrayThreadKeepsWindowThread(t *testing.T) {
	var before, after uint32
	if err := runOnTrayThread(func() error {
		before = winapi.GetCurrentThreadId()
		runtime.Gosched()
		after = winapi.GetCurrentThreadId()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if before == 0 || before != after {
		t.Fatalf("tray callback thread changed: before=%d after=%d", before, after)
	}
}

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
