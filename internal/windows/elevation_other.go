//go:build !windows

package windows

import "time"

// LaunchedProcess is the replacement of a controller handoff. Only Windows can
// track the replacement it launched, so on the other platforms the type only
// exists to keep callers compiling.
type LaunchedProcess struct{}

// PID is unknown, because this platform never launches a trackable replacement.
func (p *LaunchedProcess) PID() int { return 0 }

// Terminated reports the replacement as gone: no process was ever launched.
func (p *LaunchedProcess) Terminated(timeout time.Duration) bool { return true }

// Close releases nothing.
func (p *LaunchedProcess) Close() {}

// LaunchSelfHandoff reports that this platform cannot launch a replacement it
// can wait for, which a handoff requires.
func LaunchSelfHandoff(params string, elevate bool) (*LaunchedProcess, error) {
	return nil, ErrUnsupported
}
