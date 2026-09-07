package windows

import "errors"

var (
	ErrUnsupported       = errors.New("Windows integration is unavailable on this platform")
	ErrElevationRequired = errors.New("administrator elevation is required")
)
