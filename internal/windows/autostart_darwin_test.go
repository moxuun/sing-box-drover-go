//go:build darwin

package windows

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchAgentXMLPreservesPath(t *testing.T) {
	data, err := launchAgentXML("/Applications/Sing & Box.app/Contents/MacOS/sing-box-drover")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "<string>"+launchAgentLabel+"</string>") {
		t.Fatalf("launch agent label missing: %s", text)
	}
	if !strings.Contains(text, "Sing &amp; Box.app") {
		t.Fatalf("executable path was not XML escaped: %s", text)
	}
	if !strings.Contains(text, "<key>RunAtLoad</key>") {
		t.Fatalf("RunAtLoad missing: %s", text)
	}
}

func TestSetAndQueryAutostart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if state, err := QueryAutostart(); err != nil || state != AutostartDisabled {
		t.Fatalf("initial state = %v, err=%v", state, err)
	}
	if err := SetAutostart(true, ""); err != nil {
		t.Fatal(err)
	}
	if state, err := QueryAutostart(); err != nil || state != AutostartEnabled {
		t.Fatalf("enabled state = %v, err=%v", state, err)
	}
	path := filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("launch agent was not installed: %v", err)
	}
	if err := SetAutostart(false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("launch agent still exists: %v", err)
	}
}
