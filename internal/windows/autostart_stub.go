//go:build !windows

package windows

type AutostartState int

const (
	AutostartUnknown AutostartState = iota
	AutostartDisabled
	AutostartEnabled
)

func QueryAutostart() (AutostartState, error) { return AutostartUnknown, ErrUnsupported }
func SetAutostart(enabled bool) error         { return ErrUnsupported }
