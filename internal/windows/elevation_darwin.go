//go:build darwin

package windows

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

const administratorScript = `on run argv
do shell script (item 1 of argv) with administrator privileges
end run`

func IsProcessElevated() bool { return os.Geteuid() == 0 }

func LaunchSelf(params string, elevate bool) error {
	file, err := os.Executable()
	if err != nil {
		return err
	}
	args, err := splitCommandLine(params)
	if err != nil {
		return err
	}
	if !elevate || os.Geteuid() == 0 {
		cmd := exec.Command(file, args...)
		cmd.Dir = filepath.Dir(file)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("launch controller: %w", err)
		}
		return nil
	}
	command := "nohup " + quoteShell(file)
	for _, arg := range args {
		command += " " + quoteShell(arg)
	}
	command += " </dev/null >/dev/null 2>&1 &"
	if lockPath, err := instanceLockPath(); err == nil {
		command = instanceLockEnvironment + "=" + quoteShell(lockPath) + " " + command
	}
	cmd := exec.Command("/usr/bin/osascript", "-e", administratorScript, "--", command)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("launch elevated controller: %w", err)
	}
	return nil
}

func splitCommandLine(value string) ([]string, error) {
	var args []string
	var current strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if current.Len() > 0 {
			args = append(args, current.String())
			current.Reset()
		}
	}
	for _, r := range value {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
		case ' ', '\t', '\n', '\r':
			flush()
		default:
			current.WriteRune(r)
		}
	}
	if escaped {
		current.WriteRune('\\')
	}
	if quote != 0 {
		return nil, errors.New("unterminated quote in launch arguments")
	}
	flush()
	return args, nil
}

func quoteShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
