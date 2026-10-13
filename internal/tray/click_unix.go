//go:build !windows

package tray

func shiftPressed() bool { return false }

func (t *Tray) showMenu() error { return nil }
