//go:build !windows

package handoff

import (
	"context"
	"testing"
)

// TestDefaultTransportIsUnsupportedOnThisPlatform pins the contract the
// controller relies on: a platform without an authenticated handoff channel
// reports it instead of transferring ownership blindly, so the controller keeps
// its one-shot launch path.
func TestDefaultTransportIsUnsupportedOnThisPlatform(t *testing.T) {
	transport, err := DefaultTransport()
	if err == nil {
		t.Fatal("DefaultTransport() reported a transport on a platform without one")
	}
	if transport != nil {
		t.Fatalf("DefaultTransport() = %v, want no transport", transport)
	}
	if _, err := Connect(context.Background(), `\\.\pipe\sing-box-drover-handoff-x`, "token", 1); err == nil {
		t.Fatal("Connect() succeeded on a platform without a handoff transport")
	}
}
