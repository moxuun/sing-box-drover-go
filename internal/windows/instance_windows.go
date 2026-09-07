//go:build windows

package windows

import (
	"fmt"

	winapi "golang.org/x/sys/windows"
)

type SingleInstance struct{ handle winapi.Handle }

func AcquireSingleInstance(name string) (*SingleInstance, bool, error) {
	namePtr, err := winapi.UTF16PtrFromString(name)
	if err != nil {
		return nil, false, err
	}
	// Own a newly-created mutex. A restart/elevation child can then wait for
	// the previous controller to close its handle before taking over.
	h, err := winapi.CreateMutex(nil, true, namePtr)
	if h == 0 {
		return nil, false, fmt.Errorf("CreateMutex: %w", err)
	}
	if err != nil && err != winapi.ERROR_ALREADY_EXISTS {
		_ = winapi.CloseHandle(h)
		return nil, false, fmt.Errorf("CreateMutex: %w", err)
	}
	return &SingleInstance{handle: h}, err != winapi.ERROR_ALREADY_EXISTS, nil
}

func (i *SingleInstance) Close() {
	if i != nil && i.handle != 0 {
		_ = winapi.CloseHandle(i.handle)
		i.handle = 0
	}
}
