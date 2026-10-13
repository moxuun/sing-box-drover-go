package main

import (
	"errors"
	"fmt"
	"os"

	"sing-box-drover/internal/app"
	"sing-box-drover/internal/tray"
	platform "sing-box-drover/internal/windows"
)

func main() {
	os.Exit(run())
}

// run reports the process outcome as an exit code. The callers are scripts, the
// Task Scheduler and launchd, and they need to tell a failure from a success, so a
// failed start must not look like a clean run.
//
// The handoff errors keep exit code 0 because they are not failures: another
// instance is running, or this one did its one-shot job and handed over.
func run() int {
	platform.EnableDPIAwareness()
	controller, err := app.New(os.Args[1:])
	if err != nil {
		if handledError(err) {
			return 0
		}
		return reportError(err)
	}
	if err := tray.Run(controller); err != nil {
		// Cleanup that failed is a failure of its own: the core may still be running,
		// so it is reported even when the handoff outcome itself is not a failure and
		// must not be shown twice.
		if closeErr := controller.Close(); closeErr != nil {
			return reportError(errors.Join(err, closeErr))
		}
		// An aborted handoff or one the previous instance was told about is not a
		// failure of this process: that instance owns the outcome.
		if handledError(err) {
			return 0
		}
		return reportError(err)
	}
	return 0
}

// handledError marks process outcomes that already have an owner. Cleanup
// failures remain fatal even when they accompany one of those outcomes.
func handledError(err error) bool {
	if errors.Is(err, app.ErrCleanupFailed) {
		return false
	}
	return errors.Is(err, app.ErrElevationHandoff) || errors.Is(err, app.ErrAlreadyRunning) || errors.Is(err, app.ErrAutostartHandled) || errors.Is(err, app.ErrHandoffAborted) || errors.Is(err, app.ErrHandoffReported)
}

// reportError surfaces a fatal error to the user and returns the failure code.
func reportError(err error) int {
	fmt.Fprintln(os.Stderr, "sing-box-drover:", err)
	platform.ShowError("sing-box-drover", err.Error())
	return 1
}
