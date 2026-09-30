//go:build windows

package windows

import "path/filepath"

func CoreExecutableName() string { return "sing-box.exe" }

func AutostartRequiresElevation() bool { return true }

func SystemProxyRequiresElevation() bool { return false }

func RuntimeDir(executable string) string { return filepath.Dir(executable) }
