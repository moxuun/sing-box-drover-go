package tray

import "sing-box-drover/internal/core"

type trayIconKind uint8

const (
	trayIconPlain trayIconKind = iota
	trayIconGreen
	trayIconRed
)

type trayRuntimeStatus struct {
	coreState   core.State
	systemProxy bool
	tun         bool
	fault       bool
}

func (s trayRuntimeStatus) iconKind() trayIconKind {
	if s.fault || s.coreState == core.StateFailed {
		return trayIconRed
	}
	if s.coreState == core.StateStopped {
		return trayIconPlain
	}
	if s.systemProxy || s.tun {
		return trayIconGreen
	}
	return trayIconPlain
}

func (s trayRuntimeStatus) tooltip() string {
	if s.fault || s.coreState == core.StateFailed {
		return "Error"
	}
	if s.coreState == core.StateStopped {
		return "Not running"
	}
	if s.systemProxy && s.tun {
		return "System Proxy + TUN"
	}
	if s.systemProxy {
		return "System Proxy"
	}
	if s.tun {
		return "TUN"
	}
	if s.coreState == core.StateRunning {
		return "Proxy running"
	}
	return "Not running"
}
