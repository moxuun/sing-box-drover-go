//go:build !windows && !darwin

package windows

func IsProcessElevated() bool                      { return false }
func LaunchSelf(params string, elevate bool) error { return ErrUnsupported }
