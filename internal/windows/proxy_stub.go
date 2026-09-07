//go:build !windows

package windows

func EnableSystemProxy(host string, port int) error { return ErrUnsupported }
func DisableSystemProxy() error                     { return ErrUnsupported }
