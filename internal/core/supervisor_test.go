package core

import (
	"context"
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
	if err := supervisor.Start(`{"inbounds":[]}`); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for {
		if supervisor.State() == StateFailed {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("supervisor did not report failure; state=%v output=%q", supervisor.State(), supervisor.LastOutput())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if output := supervisor.LastOutput(); !strings.Contains(output, "FATAL bad configuration") {
		t.Fatalf("missing crash output: %q", output)
	}
}
