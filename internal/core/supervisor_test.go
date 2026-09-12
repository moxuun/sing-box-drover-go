package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSupervisorStopsWithoutUnexpectedFailure(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("requires a POSIX shell for the portable lifecycle fixture")
	}
	script := filepath.Join(t.TempDir(), "fake-sing-box.sh")
	contents := "#!/bin/sh\ntrap 'exit 0' INT TERM\ncat >/dev/null\nwhile :; do sleep 1; done\n"
	if err := os.WriteFile(script, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	supervisor := NewSupervisor(script, nil)
	defer supervisor.Close()
	if err := supervisor.Start(`{"inbounds":[]}`); err != nil {
		t.Fatal(err)
	}
	if supervisor.State() != StateRunning {
		t.Fatalf("state = %v, want running", supervisor.State())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := supervisor.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if state := supervisor.State(); state != StateStopped {
		t.Fatalf("state = %v, want stopped", state)
	}
}

func TestWaitForStopCompletionDefersCleanupUntilDoneOrForce(t *testing.T) {
	t.Run("child done", func(t *testing.T) {
		done := make(chan struct{})
		close(done)
		calledForce := false
		cleaned := false
		waitForStopCompletion(func() { cleaned = true }, done, context.Background(), time.Hour, func() { calledForce = true })
		if calledForce {
			t.Fatal("force path ran after child completion")
		}
		if !cleaned {
			t.Fatal("cleanup did not run after child completion")
		}
	})

	t.Run("force path", func(t *testing.T) {
		done := make(chan struct{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		events := make([]string, 0, 2)
		waitForStopCompletion(func() { events = append(events, "cleanup") }, done, ctx, time.Hour, func() {
			events = append(events, "force")
			close(done)
		})
		if strings.Join(events, ",") != "force,cleanup" {
			t.Fatalf("cleanup ordering = %v, want force,cleanup", events)
		}
	})
}

func TestStopWaitDurationSkipsGracePeriodAfterSignalFailure(t *testing.T) {
	if got := stopWaitDuration(context.Background(), nil); got != 10*time.Second {
		t.Fatalf("normal stop wait = %v, want 10s", got)
	}
	if got := stopWaitDuration(context.Background(), errors.New("signal unavailable")); got != 0 {
		t.Fatalf("failed-signal stop wait = %v, want 0", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	if got := stopWaitDuration(ctx, errors.New("signal unavailable")); got != 0 {
		t.Fatalf("failed-signal stop with deadline wait = %v, want 0", got)
	}
}

func TestBoundedBufferKeepsTail(t *testing.T) {
	var buffer boundedBuffer
	input := make([]byte, MaxCapturedOutput+17)
	for i := range input {
		input[i] = byte(i)
	}
	if _, err := buffer.Write(input); err != nil {
		t.Fatal(err)
	}
	got := []byte(buffer.String())
	if len(got) != MaxCapturedOutput || got[0] != input[17] || got[len(got)-1] != input[len(input)-1] {
		t.Fatalf("bounded output has wrong tail: len=%d", len(got))
	}
}

func TestSupervisorCheckRejectsMissingExecutable(t *testing.T) {
	supervisor := NewSupervisor("", nil)
	if err := supervisor.Check(`{}`); err == nil {
		t.Fatal("empty executable path unexpectedly passed configuration check")
	}
}

func TestSupervisorCheckWithRealSingBox(t *testing.T) {
	exePath := os.Getenv("SING_BOX_DROVER_TEST_CORE")
	if exePath == "" {
		t.Skip("set SING_BOX_DROVER_TEST_CORE to run the real sing-box configuration check")
	}
	supervisor := NewSupervisor(exePath, nil)
	if err := supervisor.Check(`{}`); err != nil {
		t.Fatalf("minimal configuration check failed: %v", err)
	}
	err := supervisor.Check(`{"inbounds":[{"type":"definitely-invalid","tag":"bad"}]}`)
	if err == nil {
		t.Fatal("semantic configuration error unexpectedly passed sing-box check")
	}
	if !strings.Contains(err.Error(), "sing-box configuration check failed") {
		t.Fatalf("configuration check error lacks context: %v", err)
	}
}

func TestSupervisorReportsUnexpectedExitWithBoundedDiagnostics(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("requires a POSIX shell for the portable lifecycle fixture")
	}
	script := filepath.Join(t.TempDir(), "crash-sing-box.sh")
	contents := "#!/bin/sh\necho 'FATAL bad configuration' >&2\nexit 7\n"
	if err := os.WriteFile(script, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	events := make(chan Event, 8)
	supervisor := NewSupervisor(script, nil)
	supervisor.SetHandler(func(event Event) { events <- event })
	defer supervisor.Close()
	err := supervisor.Start(`{"inbounds":[]}`)
	if err == nil || !strings.Contains(err.Error(), "FATAL bad configuration") {
		t.Fatalf("startup error = %v, want captured fatal output", err)
	}
	if supervisor.State() != StateFailed {
		t.Fatalf("supervisor state = %v, want failed", supervisor.State())
	}
	if output := supervisor.LastOutput(); !strings.Contains(output, "FATAL bad configuration") {
		t.Fatalf("missing crash output: %q", output)
	}
}
