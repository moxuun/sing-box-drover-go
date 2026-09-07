//go:build windows

package windows

import winapi "golang.org/x/sys/windows"

var (
	dpiUser32                     = winapi.NewLazySystemDLL("user32.dll")
	setProcessDPIAwarenessContext = dpiUser32.NewProc("SetProcessDpiAwarenessContext")
	setProcessDPIAware            = dpiUser32.NewProc("SetProcessDPIAware")
)

// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 is the most precise process
// context available on supported Windows versions. Keep the fallback for
// older systems where SetProcessDpiAwarenessContext is unavailable.
var dpiAwarenessContextPerMonitorAwareV2 = ^uintptr(3)

func EnableDPIAwareness() {
	if setProcessDPIAwarenessContext.Find() == nil {
		if result, _, _ := setProcessDPIAwarenessContext.Call(dpiAwarenessContextPerMonitorAwareV2); result != 0 {
			return
		}
	}
	_, _, _ = setProcessDPIAware.Call()
}
