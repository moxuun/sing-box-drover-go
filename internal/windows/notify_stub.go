//go:build !windows && !darwin

package windows

func ShowError(title, message string) {}
