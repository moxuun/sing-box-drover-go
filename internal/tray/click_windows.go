//go:build windows

package tray

import "golang.org/x/sys/windows"

var getKeyState = windows.NewLazySystemDLL("user32.dll").NewProc("GetKeyState")

func shiftPressed() bool {
	state, _, _ := getKeyState.Call(0x10)
	return state&0x8000 != 0
}
