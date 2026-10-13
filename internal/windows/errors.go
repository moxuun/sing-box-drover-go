package windows

import "errors"

var (
	ErrUnsupported       = errors.New("platform integration is unavailable on this OS")
	ErrElevationRequired = errors.New("administrator elevation is required")
	// ErrUntrackedProcess reports a launched replacement whose process handle is
	// unavailable, so the caller cannot tell when it exits. A handoff must not
	// start without that, because a rollback would have to restart the old core
	// while the replacement might still be running.
	ErrUntrackedProcess = errors.New("the launched process cannot be tracked")
)
