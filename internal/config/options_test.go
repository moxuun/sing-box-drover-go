package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOptionsResolvesRelativeFilesAndBooleans(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sing-box-drover.ini")
	text := `[sing-box-drover]
sb-dir = cores
sb-config-file = config.json
tun-start-mode = on
system-proxy-auto = on
selector-menu-layout = nested
log-file = logs/drover.log
homepage-url = http://127.0.0.1:9090/ui/
`
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	o, err := LoadOptions(path)
	if err != nil {
		t.Fatal(err)
	}
	if o.SBDir != filepath.Join(dir, "cores") || o.SBConfigFile != filepath.Join(dir, "config.json") || o.HomepageURL != "http://127.0.0.1:9090/ui/" || !o.SystemProxyAuto || o.TunStartMode != "on" || o.SelectorMenuLayout != "nested" || o.LogFile != filepath.Join(dir, "logs", "drover.log") {
		t.Fatalf("options mismatch: %#v", o)
	}
}

func TestLoadOptionsAcceptsLegacyBooleanAliases(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sing-box-drover.ini")
	text := `[sing-box-drover]
tun-start-mode = 1
system-proxy-auto = 0
`
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	o, err := LoadOptions(path)
	if err != nil {
		t.Fatal(err)
	}
	if o.TunStartMode != "on" || o.SystemProxyAuto {
		t.Fatalf("legacy boolean aliases mismatch: %#v", o)
	}
}

func TestLoadOptionsRejectsInvalidBooleanValues(t *testing.T) {
	dir := t.TempDir()
	values := []struct {
		name  string
		entry string
	}{
		{name: "tun mode", entry: "tun-start-mode = maybe"},
		{name: "system proxy", entry: "system-proxy-auto = maybe"},
	}

	for _, value := range values {
		t.Run(value.name, func(t *testing.T) {
			path := filepath.Join(dir, value.name+".ini")
			if err := os.WriteFile(path, []byte("[sing-box-drover]\n"+value.entry+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadOptions(path); err == nil {
				t.Fatalf("invalid value was accepted: %s", value.entry)
			}
		})
	}
}

func TestLoadOptionsRejectsNonWebHomepageURL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sing-box-drover.ini")
	if err := os.WriteFile(path, []byte("[sing-box-drover]\nhomepage-url = file:///tmp/panel\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOptions(path); err == nil {
		t.Fatal("invalid homepage URL was accepted")
	}
}

func TestLoadOptionsAnchorsMissingConfigToBaseDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sing-box-drover.ini")
	if err := os.WriteFile(path, []byte("[sing-box-drover]\nsb-config-file = missing.json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	o, err := LoadOptions(path)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "missing.json")
	if o.SBConfigFile != want {
		t.Fatalf("missing config path = %q, want %q", o.SBConfigFile, want)
	}
}
