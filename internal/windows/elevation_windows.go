//go:build windows

package windows

import (
	"fmt"
	"os"
	"syscall"
	"time"
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
	seeMaskNoCloseProc = 0x00000040
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

// LaunchedProcess is a replacement started by LaunchSelfHandoff. The parent has
// to keep the handle to know whether the replacement is gone before it takes a
// rolled-back controller back.
type LaunchedProcess struct{ handle winapi.Handle }

// PID is the process id of the replacement.
func (p *LaunchedProcess) PID() int {
	if p == nil || p.handle == 0 {
		return 0
	}
	pid, err := winapi.GetProcessId(p.handle)
	if err != nil {
		return 0
	}
	return int(pid)
}

// Terminated reports whether the replacement has exited, waiting at most
// timeout. It never reports true for a process that is still running, so a
// caller that gets false must not assume the process is gone.
func (p *LaunchedProcess) Terminated(timeout time.Duration) bool {
	if p == nil || p.handle == 0 {
		return false
	}
	if timeout < 0 {
		timeout = 0
	}
	state, err := winapi.WaitForSingleObject(p.handle, uint32(timeout/time.Millisecond))
	return err == nil && state == winapi.WAIT_OBJECT_0
}

// Close releases the process handle.
func (p *LaunchedProcess) Close() {
	if p == nil || p.handle == 0 {
		return
	}
	_ = winapi.CloseHandle(p.handle)
	p.handle = 0
}

// LaunchSelfHandoff starts a replacement for a controller handoff and keeps a
// handle to it, so the caller can tell whether it is still running. It is the
// same launch as LaunchSelf with the process handle retained; a launch that
// cannot be tracked is reported as an error instead of returning a handle that
// cannot be waited for.
func LaunchSelfHandoff(params string, elevate bool) (*LaunchedProcess, error) {
	verb := "open"
	if elevate {
		verb = "runas"
	}
	file, err := os.Executable()
	if err != nil {
		return nil, err
	}
	verbPtr, _ := syscall.UTF16PtrFromString(verb)
	filePtr, err := syscall.UTF16PtrFromString(file)
	if err != nil {
		return nil, err
	}
	paramPtr, err := syscall.UTF16PtrFromString(params)
	if err != nil {
		return nil, err
	}
	dirPtr, err := syscall.UTF16PtrFromString(executableDir(file))
	if err != nil {
		return nil, err
	}
	info := shellExecuteInfo{cbSize: uint32(unsafe.Sizeof(shellExecuteInfo{})), fMask: seeMaskNoAsync | seeMaskNoCloseProc, lpVerb: verbPtr, lpFile: filePtr, lpParameters: paramPtr, lpDirectory: dirPtr, nShow: swShownNormal}
	if r1, _, callErr := shellExecuteEx.Call(uintptr(unsafe.Pointer(&info))); r1 == 0 {
		if callErr != nil && callErr != syscall.Errno(0) {
			return nil, fmt.Errorf("ShellExecuteEx: %w", callErr)
		}
		return nil, fmt.Errorf("ShellExecuteEx failed")
	}
	if info.hProcess == 0 {
		return nil, ErrUntrackedProcess
	}
	return &LaunchedProcess{handle: winapi.Handle(info.hProcess)}, nil
}
