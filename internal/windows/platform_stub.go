//go:build !windows && !darwin

package windows

import "path/filepath"

func CoreExecutableName() string { return "sing-box" }

func AutostartRequiresElevation() bool { return false }

func SystemProxyRequiresElevation() bool { return false }

func RuntimeDir(executable string) string { return filepath.Dir(executable) }
