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
system-proxy-auto = yes
selector-menu-layout = nested
selector-persist = 0
log-file = logs/drover.log
`
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	o, err := LoadOptions(path)
	if err != nil {
		t.Fatal(err)
	}
	if o.SBDir != filepath.Join(dir, "cores") || o.SBConfigFile != filepath.Join(dir, "config.json") || !o.SystemProxyAuto || o.TunStartMode != "on" || o.SelectorMenuLayout != "nested" || o.SelectorPersist || o.LogFile != filepath.Join(dir, "logs", "drover.log") {
		t.Fatalf("options mismatch: %#v", o)
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
