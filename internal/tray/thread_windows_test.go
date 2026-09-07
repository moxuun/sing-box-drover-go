//go:build windows

package tray

import (
	"runtime"
	"testing"

	winapi "golang.org/x/sys/windows"
)

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
