//go:build darwin

package tray

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#include "wake_darwin.h"
*/
import "C"

import "sync"

var (
	wakeOnce     sync.Once
	wakeCallback func()
)

//export droverWakeCallback
func droverWakeCallback() {
	if callback := wakeCallback; callback != nil {
		go callback()
	}
}

func watchSystemWake(callback func()) {
	wakeOnce.Do(func() {
		wakeCallback = callback
		C.droverInstallWakeObserver()
	})
}
