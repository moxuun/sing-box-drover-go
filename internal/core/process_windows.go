//go:build windows

package core

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"

	winapi "golang.org/x/sys/windows"
)

type processJob interface{}

type windowsJob struct{ handle winapi.Handle }

var (
	kernel32                 = winapi.NewLazySystemDLL("kernel32.dll")
	attachConsole            = kernel32.NewProc("AttachConsole")
	freeConsole              = kernel32.NewProc("FreeConsole")
	generateConsoleCtrlEvent = kernel32.NewProc("GenerateConsoleCtrlEvent")
	setConsoleCtrlHandler    = kernel32.NewProc("SetConsoleCtrlHandler")
)

func executableDir(path string) string { return filepath.Dir(path) }

func configureCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: winapi.CREATE_NEW_CONSOLE | winapi.CREATE_NEW_PROCESS_GROUP}
}

func attachProcessJob(process *os.Process) (processJob, error) {
	if process == nil {
		return 0, errors.New("process is nil")
	}
	job, err := winapi.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := winapi.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = winapi.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := winapi.SetInformationJobObject(job, winapi.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		winapi.CloseHandle(job)
		return 0, err
	}
	processHandle, err := winapi.OpenProcess(winapi.PROCESS_SET_QUOTA|winapi.PROCESS_TERMINATE|winapi.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(process.Pid))
	if err != nil {
		winapi.CloseHandle(job)
		return 0, err
	}
	assignErr := winapi.AssignProcessToJobObject(job, processHandle)
	winapi.CloseHandle(processHandle)
	if assignErr != nil {
		winapi.CloseHandle(job)
		return 0, assignErr
	}
	return windowsJob{handle: job}, nil
}

func closeProcessJob(job processJob) {
	if j, ok := job.(windowsJob); ok && j.handle != 0 {
		_ = winapi.CloseHandle(j.handle)
	}
}

func requestGracefulStop(process *os.Process) (func(), error) {
	if process == nil {
		return nil, errors.New("process is nil")
	}
	_, _, _ = freeConsole.Call()
	if ok, _, err := attachConsole.Call(uintptr(process.Pid)); ok == 0 {
		return nil, err
	}
	if ok, _, err := setConsoleCtrlHandler.Call(0, 1); ok == 0 {
		_, _, _ = freeConsole.Call()
		return nil, err
	}
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			_, _, _ = setConsoleCtrlHandler.Call(0, 0)
			_, _, _ = freeConsole.Call()
		})
	}
	ok, _, err := generateConsoleCtrlEvent.Call(winapi.CTRL_C_EVENT, 0)
	if ok == 0 {
		cleanup()
		return nil, err
	}
	return cleanup, nil
}
