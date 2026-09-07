//go:build windows

package config

import (
	winapi "golang.org/x/sys/windows"
)

func replaceFile(from, to string) error {
	fromPtr, err := winapi.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	toPtr, err := winapi.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return winapi.MoveFileEx(fromPtr, toPtr, winapi.MOVEFILE_REPLACE_EXISTING|winapi.MOVEFILE_WRITE_THROUGH)
}
