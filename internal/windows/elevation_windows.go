//go:build windows

package windows

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"

	winapi "golang.org/x/sys/windows"
)

var (
	advapi              = winapi.NewLazySystemDLL("advapi32.dll")
	shell32             = winapi.NewLazySystemDLL("shell32.dll")
	openProcessToken    = advapi.NewProc("OpenProcessToken")
	getTokenInformation = advapi.NewProc("GetTokenInformation")
	shellExecuteEx      = shell32.NewProc("ShellExecuteExW")
)

const (
	tokenQuery         = 0x0008
	tokenElevationInfo = 20
	seeMaskNoAsync     = 0x00000100
	swShownNormal      = 1
)

type tokenElevation struct{ TokenIsElevated uint32 }
type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hWnd         uintptr
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     uintptr
	lpIDList     uintptr
	lpClass      *uint16
	hKeyClass    uintptr
	dwHotKey     uint32
	hIcon        uintptr
	hProcess     uintptr
}

func IsProcessElevated() bool {
	var token winapi.Handle
	process, _ := winapi.GetCurrentProcess()
	if r1, _, _ := openProcessToken.Call(uintptr(process), tokenQuery, uintptr(unsafe.Pointer(&token))); r1 == 0 {
		return false
	}
	defer winapi.CloseHandle(token)
	var elevation tokenElevation
	var returned uint32
	r1, _, _ := getTokenInformation.Call(uintptr(token), tokenElevationInfo, uintptr(unsafe.Pointer(&elevation)), uintptr(unsafe.Sizeof(elevation)), uintptr(unsafe.Pointer(&returned)))
	return r1 != 0 && elevation.TokenIsElevated != 0
}

func LaunchSelf(params string, elevate bool) error {
	verb := "open"
	if elevate {
		verb = "runas"
	}
	file, err := os.Executable()
	if err != nil {
		return err
	}
	verbPtr, _ := syscall.UTF16PtrFromString(verb)
	filePtr, err := syscall.UTF16PtrFromString(file)
	if err != nil {
		return err
	}
	paramPtr, err := syscall.UTF16PtrFromString(params)
	if err != nil {
		return err
	}
	dirPtr, err := syscall.UTF16PtrFromString(executableDir(file))
	if err != nil {
		return err
	}
	info := shellExecuteInfo{cbSize: uint32(unsafe.Sizeof(shellExecuteInfo{})), fMask: seeMaskNoAsync, lpVerb: verbPtr, lpFile: filePtr, lpParameters: paramPtr, lpDirectory: dirPtr, nShow: swShownNormal}
	if r1, _, callErr := shellExecuteEx.Call(uintptr(unsafe.Pointer(&info))); r1 == 0 {
		if callErr != nil && callErr != syscall.Errno(0) {
			return fmt.Errorf("ShellExecuteEx: %w", callErr)
		}
		return fmt.Errorf("ShellExecuteEx failed")
	}
	if info.hProcess != 0 {
		_ = winapi.CloseHandle(winapi.Handle(info.hProcess))
	}
	return nil
}

func executableDir(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '\\' || path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}
