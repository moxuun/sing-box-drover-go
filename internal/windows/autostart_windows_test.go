//go:build windows

package windows

import (
	"encoding/binary"
	"os/exec"
	"slices"
	"testing"
	"time"
	"unicode/utf16"
)

func TestRunTaskSchedulerQueryIsBounded(t *testing.T) {
	done := make(chan struct{})
	go func() {
		_, _ = runTaskScheduler("/Query", "/TN", taskName, "/XML")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(taskSchedulerTimeout + time.Second):
		t.Fatal("Task Scheduler query exceeded its timeout")
	}
}

func TestAutostartCreateArgsUseLimitedRunLevel(t *testing.T) {
	args := autostartCreateArgs(`C:\Users\Example User\sing-box-drover-go.exe`, `DOMAIN\user`)
	want := []string{"/Create", "/TN", taskName, "/SC", "ONLOGON", "/RU", `DOMAIN\user`, "/IT", "/RL", "LIMITED", "/TR", `"C:\Users\Example User\sing-box-drover-go.exe"`, "/F"}
	if len(args) != len(want) {
		t.Fatalf("schtasks argument count = %d, want %d: %#v", len(args), len(want), args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("schtasks argument %d = %q, want %q; args=%#v", i, args[i], want[i], args)
		}
	}
}

func TestTaskEnabledFromXML(t *testing.T) {
	for _, test := range []struct {
		name string
		xml  string
		want bool
	}{
		{name: "enabled", xml: `<Task><Settings><Enabled>true</Enabled></Settings></Task>`, want: true},
		{name: "disabled", xml: `<Task><Settings><Enabled>false</Enabled></Settings></Task>`, want: false},
		{name: "default enabled", xml: `<Task><Settings /></Task>`, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := taskEnabledFromXML([]byte(test.xml))
			if err != nil || got != test.want {
				t.Fatalf("taskEnabledFromXML() = %v, %v; want %v", got, err, test.want)
			}
		})
	}
}

func TestTaskEnabledFromXMLRejectsMalformedData(t *testing.T) {
	if _, err := taskEnabledFromXML([]byte(`<Task>`)); err == nil {
		t.Fatal("malformed task XML unexpectedly parsed")
	}
}

func TestTaskEnabledFromUTF16XML(t *testing.T) {
	text := `<?xml version="1.0" encoding="UTF-16"?><Task><Settings><Enabled>false</Enabled></Settings></Task>`
	units := utf16.Encode([]rune(text))
	data := make([]byte, 2+len(units)*2)
	data[0], data[1] = 0xff, 0xfe
	for i, unit := range units {
		binary.LittleEndian.PutUint16(data[2+i*2:], unit)
	}
	got, err := taskEnabledFromXML(data)
	if err != nil {
		t.Fatalf("taskEnabledFromXML() UTF-16 error = %v", err)
	}
	if got {
		t.Fatal("UTF-16 disabled task was reported as enabled")
	}
}

func TestTaskEnabledFromUTF8BytesWithUTF16Declaration(t *testing.T) {
	data := []byte(`<?xml version="1.0" encoding="UTF-16"?><Task><Settings><Enabled>true</Enabled></Settings></Task>`)
	got, err := taskEnabledFromXML(data)
	if err != nil {
		t.Fatalf("taskEnabledFromXML() mismatched declaration error = %v", err)
	}
	if !got {
		t.Fatal("enabled task with a UTF-16 declaration was reported as disabled")
	}
}

func TestSetAutostartDisableIsIdempotentWhenTaskIsMissing(t *testing.T) {
	// The scheduler call is replaced: QueryAutostart reports an existing but
	// disabled task as AutostartDisabled as well, so driving the real command
	// with the fixed task name would delete a definition the user really has.
	var calls [][]string
	original := runTaskScheduler
	runTaskScheduler = func(args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return []byte("ERROR: The system cannot find the file specified."), &exec.ExitError{}
	}
	t.Cleanup(func() { runTaskScheduler = original })
	if err := SetAutostart(false, ""); err != nil {
		t.Fatalf("SetAutostart(false) with no task: %v", err)
	}
	// A no-op implementation must not pass this test: the delete has to reach the
	// scheduler for the task the rest of the package manages.
	want := []string{"/Delete", "/TN", taskName, "/F"}
	if len(calls) != 1 || !slices.Equal(calls[0], want) {
		t.Fatalf("scheduler calls = %v, want one %v", calls, want)
	}
}
