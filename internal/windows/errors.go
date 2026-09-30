package windows

import "errors"

var (
	ErrUnsupported       = errors.New("platform integration is unavailable on this OS")
	ErrElevationRequired = errors.New("administrator elevation is required")
)
