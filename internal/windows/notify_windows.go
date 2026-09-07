//go:build windows

package windows

import (
	"unsafe"

	winapi "golang.org/x/sys/windows"
)

var (
	notifyUser32     = winapi.NewLazySystemDLL("user32.dll")
	notifyMessageBox = notifyUser32.NewProc("MessageBoxW")
)

const (
	messageBoxOK        = 0x00000000
	messageBoxIconError = 0x00000010
)

// ShowError gives GUI builds a visible diagnostic when startup fails before
// the tray window exists. Console builds still print the same error in main.
func ShowError(title, message string) {
	titlePtr, err := winapi.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	messagePtr, err := winapi.UTF16PtrFromString(message)
	if err != nil {
		return
	}
	_, _, _ = notifyMessageBox.Call(0, uintptr(unsafe.Pointer(messagePtr)), uintptr(unsafe.Pointer(titlePtr)), messageBoxOK|messageBoxIconError)
}
