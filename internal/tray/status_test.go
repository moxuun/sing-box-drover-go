package tray

import (
	"testing"

	"sing-box-drover/internal/core"
)

func TestTrayRuntimeStatus(t *testing.T) {
	tests := []struct {
		name        string
		status      trayRuntimeStatus
		wantIcon    trayIconKind
		wantTooltip string
	}{
		{name: "stopped", status: trayRuntimeStatus{coreState: core.StateStopped}, wantIcon: trayIconPlain, wantTooltip: "Not running"},
		{name: "proxy", status: trayRuntimeStatus{coreState: core.StateRunning, systemProxy: true}, wantIcon: trayIconGreen, wantTooltip: "System Proxy"},
		{name: "tun", status: trayRuntimeStatus{coreState: core.StateRunning, tun: true}, wantIcon: trayIconGreen, wantTooltip: "TUN"},
		{name: "proxy and tun", status: trayRuntimeStatus{coreState: core.StateRunning, systemProxy: true, tun: true}, wantIcon: trayIconGreen, wantTooltip: "System Proxy + TUN"},
		{name: "ordinary proxy", status: trayRuntimeStatus{coreState: core.StateRunning}, wantIcon: trayIconPlain, wantTooltip: "Proxy running"},
		{name: "core failure", status: trayRuntimeStatus{coreState: core.StateFailed}, wantIcon: trayIconRed, wantTooltip: "Error"},
		{name: "operation failure", status: trayRuntimeStatus{coreState: core.StateRunning, fault: true}, wantIcon: trayIconRed, wantTooltip: "Error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.status.iconKind(); got != test.wantIcon {
				t.Fatalf("iconKind() = %d, want %d", got, test.wantIcon)
			}
			if got := test.status.tooltip(); got != test.wantTooltip {
				t.Fatalf("tooltip() = %q, want %q", got, test.wantTooltip)
			}
		})
	}
}
