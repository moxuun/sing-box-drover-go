//go:build darwin

package windows

import (
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type AutostartState int

const (
	AutostartUnknown AutostartState = iota
	AutostartDisabled
	AutostartEnabled
)

const launchAgentLabel = "com.moxuun.sing-box-drover"

func launchAgentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist"), nil
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

// launchAgentElement is one child of the plist's dict: a <key>, a <string>, or
// an <array> of strings. Others keeps children that are not <string> elements so
// a value of the wrong type is rejected instead of being skipped.
type launchAgentElement struct {
	XMLName xml.Name
	Value   string               `xml:",chardata"`
	Strings []string             `xml:"string"`
	Others  []launchAgentElement `xml:",any"`
}

// launchAgentPlist is the shape this app writes: <plist><dict>…</dict></plist>.
type launchAgentPlist struct {
	XMLName xml.Name `xml:"plist"`
	Dict    struct {
		Elements []launchAgentElement `xml:",any"`
	} `xml:"dict"`
}

// launchAgentIsEnabled reports whether the plist actually starts this controller
// at login. A truncated or unrelated plist can still contain the label string, so
// the whole structure has to hold: children are alternating key/value pairs, the
// known keys carry the expected value type, Label names this app, the first
// ProgramArguments entry names a program, and the job is enabled at login.
// Duplicate keys follow plist semantics: the last value wins.
func launchAgentIsEnabled(data []byte) bool {
	var plist launchAgentPlist
	if err := xml.Unmarshal(data, &plist); err != nil {
		return false
	}
	elements := plist.Dict.Elements
	if len(elements)%2 != 0 {
		// A trailing key without a value: the children are not a sequence of
		// key/value pairs this check can read.
		return false
	}
	label := ""
	var programs []string
	runAtLoad := false
	disabled := false
	for i := 0; i < len(elements); i += 2 {
		key, value := elements[i], elements[i+1]
		if key.XMLName.Local != "key" || value.XMLName.Local == "key" {
			return false
		}
		switch strings.TrimSpace(key.Value) {
		case "Label":
			if value.XMLName.Local != "string" {
				return false
			}
			label = strings.TrimSpace(value.Value)
		case "ProgramArguments":
			if value.XMLName.Local != "array" || len(value.Others) > 0 {
				return false
			}
			programs = value.Strings
		case "RunAtLoad":
			if !isPlistBool(value) {
				return false
			}
			runAtLoad = value.XMLName.Local == "true"
		case "Disabled":
			if !isPlistBool(value) {
				return false
			}
			disabled = value.XMLName.Local == "true"
		}
	}
	if label != launchAgentLabel || !runAtLoad || disabled {
		return false
	}
	// The first argument is the program launchd starts; a definition without it
	// does not start anything.
	return len(programs) > 0 && strings.TrimSpace(programs[0]) != ""
}

func isPlistBool(element launchAgentElement) bool {
	return element.XMLName.Local == "true" || element.XMLName.Local == "false"
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
	if !launchAgentIsEnabled(data) {
		return AutostartUnknown, errors.New("launch agent does not start sing-box-drover at login")
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
		// Only the plist is removed. The job launchd already loaded is this very
		// process, so booting it out would terminate the running tray and its core
		// instead of just cancelling the next login start.
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
