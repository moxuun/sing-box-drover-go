//go:build darwin

package windows

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type AutostartState int

const (
	AutostartUnknown AutostartState = iota
	AutostartDisabled
	AutostartEnabled
)

const (
	launchAgentLabel = "com.moxuun.sing-box-drover"
	launchctlTimeout = 10 * time.Second
)

func launchAgentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist"), nil
}

func launchctlDomain() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

func runLaunchctl(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), launchctlTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/launchctl", args...)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return output, ctx.Err()
	}
	if err != nil {
		return output, fmt.Errorf("launchctl %s: %w (%s)", strings.Join(args, " "), err, bytes.TrimSpace(output))
	}
	return output, nil
}

func bootoutLaunchAgent() {
	_, _ = runLaunchctl("bootout", launchctlDomain()+"/"+launchAgentLabel)
}

func launchAgentXML(executable string) ([]byte, error) {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	b.WriteString("<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n")
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	writePlistString(&b, "Label", launchAgentLabel)
	b.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	if err := writePlistValue(&b, executable, 4); err != nil {
		return nil, err
	}
	b.WriteString("  </array>\n")
	b.WriteString("  <key>LimitLoadToSessionType</key>\n  <string>Aqua</string>\n")
	b.WriteString("  <key>RunAtLoad</key>\n  <true/>\n")
	b.WriteString("  <key>KeepAlive</key>\n  <false/>\n")
	b.WriteString("  <key>ProcessType</key>\n  <string>Interactive</string>\n")
	b.WriteString("</dict>\n</plist>\n")
	return []byte(b.String()), nil
}

func writePlistString(b *strings.Builder, key, value string) {
	fmt.Fprintf(b, "  <key>")
	_ = xml.EscapeText(b, []byte(key))
	b.WriteString("</key>\n")
	if err := writePlistValue(b, value, 2); err != nil {
		return
	}
}

func writePlistValue(b *strings.Builder, value string, indent int) error {
	b.WriteString(strings.Repeat(" ", indent))
	b.WriteString("<string>")
	if err := xml.EscapeText(b, []byte(value)); err != nil {
		return err
	}
	b.WriteString("</string>\n")
	return nil
}

func QueryAutostart() (AutostartState, error) {
	path, err := launchAgentPath()
	if err != nil {
		return AutostartUnknown, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return AutostartDisabled, nil
	}
	if err != nil {
		return AutostartUnknown, fmt.Errorf("read launch agent: %w", err)
	}
	if !bytes.Contains(data, []byte("<string>"+launchAgentLabel+"</string>")) {
		return AutostartUnknown, errors.New("launch agent has an unexpected label")
	}
	return AutostartEnabled, nil
}

func SetAutostart(enabled bool, owner string) error {
	if os.Geteuid() == 0 {
		return errors.New("configure login startup before launching the controller as root")
	}
	path, err := launchAgentPath()
	if err != nil {
		return err
	}
	if !enabled {
		bootoutLaunchAgent()
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove launch agent: %w", err)
		}
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if strings.TrimSpace(executable) == "" {
		return errors.New("controller executable path is empty")
	}
	data, err := launchAgentXML(executable)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create LaunchAgents directory: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".sing-box-drover-*.plist")
	if err != nil {
		return fmt.Errorf("create launch agent: %w", err)
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write launch agent: %w", err)
	}
	if err := temp.Chmod(0o644); err != nil {
		_ = temp.Close()
		return fmt.Errorf("set launch agent permissions: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync launch agent: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close launch agent: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("install launch agent: %w", err)
	}
	state, err := QueryAutostart()
	if err != nil {
		return fmt.Errorf("verify launch agent: %w", err)
	}
	if state != AutostartEnabled {
		return errors.New("verify launch agent: launch agent is not enabled")
	}
	return nil
}
