//go:build !windows

package handoff

import (
	"context"
	"errors"
)

// errUnsupported is returned where no authenticated handoff channel exists. The
// caller stays on its previous one-shot launch path instead of transferring
// ownership without a way to confirm the replacement.
var errUnsupported = errors.New("controller handoff is not supported on this platform")

// DefaultTransport reports that this platform has no handoff transport.
func DefaultTransport() (Transport, error) { return nil, errUnsupported }

// Connect reports that this platform has no handoff transport.
func Connect(ctx context.Context, address, token string, serverPID int) (Conn, error) {
	return nil, errUnsupported
}
