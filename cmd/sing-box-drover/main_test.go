package main

import (
	"errors"
	"testing"

	"sing-box-drover/internal/app"
)

func TestCleanupFailureCannotExitAsAHandledHandoff(t *testing.T) {
	for _, outcome := range []error{app.ErrHandoffReported, app.ErrHandoffAborted, app.ErrElevationHandoff} {
		t.Run(outcome.Error(), func(t *testing.T) {
			if !handledError(outcome) {
				t.Fatalf("successful handled outcome became fatal: %v", outcome)
			}
			err := errors.Join(outcome, app.ErrCleanupFailed, errors.New("core shutdown failed"))
			if handledError(err) {
				t.Fatalf("cleanup failure would return success instead of exit code 1: %v", err)
			}
		})
	}
}
