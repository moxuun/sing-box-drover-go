//go:build !windows

package windows

type ProxySession struct{}

func EnableSystemProxy(host string, port int) (ProxySession, error) {
	return ProxySession{}, ErrUnsupported
}

func RestoreSystemProxy(ProxySession) (bool, error) { return false, ErrUnsupported }
