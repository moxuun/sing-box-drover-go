//go:build windows

package windows

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
)

type AutostartState int

const (
	AutostartUnknown AutostartState = iota
	AutostartDisabled
	AutostartEnabled
)

const taskName = "sing-box-drover"

const taskSchedulerTimeout = 10 * time.Second

func taskMissing(output []byte) bool {
	message := strings.ToLower(string(output))
	for _, phrase := range []string{"does not exist", "cannot find", "not found", "system cannot find the path", "不存在", "找不到"} {
		if strings.Contains(message, phrase) {
			return true
		}
	}
	return false
}

func runTaskScheduler(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), taskSchedulerTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "schtasks.exe", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	return out, err
}

func autostartCreateArgs(file, username string) []string {
	// schtasks expects a single /TR string. Double quotes preserve paths with
	// spaces while allowing the task action to remain the executable itself.
	tr := `"` + strings.ReplaceAll(file, `"`, `\"`) + `"`
	return []string{"/Create", "/TN", taskName, "/SC", "ONLOGON", "/RU", username, "/IT", "/RL", "LIMITED", "/TR", tr, "/F"}
}

func decodeTaskXML(data []byte) ([]byte, bool, error) {
	const (
		littleEndian = iota
		bigEndian
	)
	order := -1
	offset := 0
	switch {
	case len(data) >= 2 && data[0] == 0xff && data[1] == 0xfe:
		order, offset = littleEndian, 2
	case len(data) >= 2 && data[0] == 0xfe && data[1] == 0xff:
		order, offset = bigEndian, 2
	case len(data) >= 2 && data[0] == '<' && data[1] == 0:
		order = littleEndian
	case len(data) >= 2 && data[0] == 0 && data[1] == '<':
		order = bigEndian
	default:
		return data, false, nil
	}
	payload := data[offset:]
	if len(payload)%2 != 0 {
		return nil, false, fmt.Errorf("invalid UTF-16 task definition length")
	}
	units := make([]uint16, len(payload)/2)
	for i := range units {
		if order == littleEndian {
			units[i] = binary.LittleEndian.Uint16(payload[i*2:])
		} else {
			units[i] = binary.BigEndian.Uint16(payload[i*2:])
		}
	}
	return []byte(string(utf16.Decode(units))), true, nil
}

func taskEnabledFromXML(data []byte) (bool, error) {
	var definition struct {
		Settings struct {
			Enabled *bool `xml:"Enabled"`
		} `xml:"Settings"`
	}
	decoded, utf16XML, err := decodeTaskXML(data)
	if err != nil {
		return false, err
	}
	decoder := xml.NewDecoder(bytes.NewReader(decoded))
	if utf16XML {
		decoder.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) {
			return input, nil
		}
	}
	if err := decoder.Decode(&definition); err != nil {
		return false, err
	}
	if definition.Settings.Enabled == nil {
		return true, nil
	}
	return *definition.Settings.Enabled, nil
}

func QueryAutostart() (AutostartState, error) {
	out, err := runTaskScheduler("/Query", "/TN", taskName, "/XML")
	if err == nil {
		enabled, parseErr := taskEnabledFromXML(out)
		if parseErr != nil {
			return AutostartUnknown, fmt.Errorf("parse task definition: %w", parseErr)
		}
		if enabled {
			return AutostartEnabled, nil
		}
		return AutostartDisabled, nil
	}
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() != 0 {
		if taskMissing(out) {
			return AutostartDisabled, nil
		}
		return AutostartUnknown, fmt.Errorf("query task: %w (%s)", err, bytes.TrimSpace(out))
	}
	return AutostartUnknown, err
}

func SetAutostart(enabled bool) error {
	if !enabled {
		out, err := runTaskScheduler("/Delete", "/TN", taskName, "/F")
		if err != nil {
			// Deleting an absent task is idempotent for this UI action.
			if !taskMissing(out) {
				return fmt.Errorf("remove task: %w", err)
			}
		}
		return nil
	}
	file, err := os.Executable()
	if err != nil {
		return err
	}
	currentUser, err := user.Current()
	if err != nil {
		return fmt.Errorf("resolve current user: %w", err)
	}
	if strings.TrimSpace(currentUser.Username) == "" {
		return fmt.Errorf("resolve current user: empty username")
	}
	if out, err := runTaskScheduler(autostartCreateArgs(file, currentUser.Username)...); err != nil {
		return fmt.Errorf("register task: %w (%s)", err, bytes.TrimSpace(out))
	}
	if state, err := QueryAutostart(); err != nil {
		return fmt.Errorf("verify task: %w", err)
	} else if state != AutostartEnabled {
		return fmt.Errorf("verify task: task is not enabled")
	}
	return nil
}
