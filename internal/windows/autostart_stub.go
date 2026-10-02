//go:build !windows && !darwin

package windows

type AutostartState int

const (
	AutostartUnknown AutostartState = iota
	AutostartDisabled
	AutostartEnabled
)

func QueryAutostart() (AutostartState, error)       { return AutostartUnknown, ErrUnsupported }
func SetAutostart(enabled bool, owner string) error { return ErrUnsupported }
