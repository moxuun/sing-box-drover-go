//go:build !linux

package windows

func CheckTunPrivileges(corePath string) error {
	if !IsProcessElevated() {
		return ErrElevationRequired
	}
	return nil
}
