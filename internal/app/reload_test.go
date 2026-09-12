package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sing-box-drover/internal/clash"
	"sing-box-drover/internal/config"
	"sing-box-drover/internal/core"
)

const reloadConfigJSON = `{
  "experimental": {
    "clash_api": {
      "external_controller": "127.0.0.1:9090",
      "secret": "secret"
    }
  },
  "inbounds": [
    {"type": "mixed", "listen": "127.0.0.1", "listen_port": 1080},
    {"type": "tun", "tag": "tun-in"}
  ],
  "outbounds": [
    {
      "type": "selector",
      "tag": "proxy",
      "outbounds": ["node1", "node2"],
      "default": "node1",
      "providers": ["my-provider"],
      "future_option": {"enabled": true}
    }
  ],
  "future_section": {"keep": [1, 2, 3]}
}`

func writeReloadSource(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadConfigCandidateReloadsJSONAndPreservesRuntimeFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeReloadSource(t, path, reloadConfigJSON)
	a := &App{
		source: config.ConfigSource{FilePath: path},
	}

	candidate, err := a.readConfigCandidate(false, false)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.config.ProxyPort != 1080 || candidate.tun {
		t.Fatalf("unexpected JSON candidate: %#v", candidate)
	}
	if len(candidate.selectors) != 1 || candidate.selectors[0].Now != "node1" {
		t.Fatalf("selector default was not preserved: %#v", candidate.selectors)
	}
	var runtime map[string]any
	if err := json.Unmarshal([]byte(candidate.config.JSONWithTUN), &runtime); err != nil {
		t.Fatal(err)
	}
	outbounds, ok := runtime["outbounds"].([]any)
	if !ok || len(outbounds) != 1 {
		t.Fatalf("outbounds were not preserved: %#v", runtime["outbounds"])
	}
	selector, ok := outbounds[0].(map[string]any)
	if !ok || selector["future_option"] == nil || selector["providers"] == nil || runtime["future_section"] == nil {
		t.Fatalf("provider/unknown fields were lost: %#v", runtime)
	}
	if strings.Contains(candidate.config.JSONWithoutTUN, `"type":"tun"`) {
		t.Fatalf("TUN inbound was not filtered from the runtime copy: %s", candidate.config.JSONWithoutTUN)
	}
}

func TestRestartWithConfigKeepsOldStateWhenPreflightFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeReloadSource(t, path, `{"inbounds":[`) // invalid JSON must fail before a supervisor is touched.
	oldConfig := config.SingBoxConfig{
		ProxyHost:      "127.0.0.1",
		ProxyPort:      1080,
		JSONWithoutTUN: `{"old":true}`,
	}
	oldSelectors := []clash.Selector{{Name: "proxy", All: []string{"old"}, Now: "old"}}
	a := &App{
		source:    config.ConfigSource{FilePath: path, JSONText: `{"old":true}`},
		config:    oldConfig,
		selectors: oldSelectors,
	}

	if err := a.restartWithConfig(false, false); err == nil {
		t.Fatal("invalid configuration unexpectedly passed preflight")
	}
	if a.source.JSONText != `{"old":true}` || a.config.JSONWithoutTUN != oldConfig.JSONWithoutTUN {
		t.Fatalf("old config state changed after preflight failure: source=%#v config=%#v", a.source, a.config)
	}
	if got := a.Selectors(); len(got) != 1 || got[0].Now != "old" {
		t.Fatalf("old selector state changed after preflight failure: %#v", got)
	}
}

func TestRestartWithConfigKeepsRunningStateWhenSingBoxCheckFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeReloadSource(t, path, reloadConfigJSON)
	oldConfig := config.SingBoxConfig{
		ProxyHost:      "127.0.0.1",
		ProxyPort:      1080,
		JSONWithoutTUN: `{"old":true}`,
	}
	oldSelectors := []clash.Selector{{Name: "proxy", All: []string{"old"}, Now: "old"}}
	checkErr := errors.New("semantic configuration error")
	a := &App{
		source:    config.ConfigSource{FilePath: path, JSONText: `{"old":true}`},
		config:    oldConfig,
		selectors: oldSelectors,
		configChecker: func(runtimeJSON string) error {
			if !strings.Contains(runtimeJSON, `"future_section"`) {
				t.Fatalf("checker did not receive the complete runtime JSON: %s", runtimeJSON)
			}
			return checkErr
		},
	}

	err := a.restartWithConfig(false, false)
	if !errors.Is(err, checkErr) {
		t.Fatalf("restartWithConfig() error = %v, want %v", err, checkErr)
	}
	if a.source.JSONText != `{"old":true}` || a.config.JSONWithoutTUN != oldConfig.JSONWithoutTUN {
		t.Fatalf("old config state changed after sing-box check failure: source=%#v config=%#v", a.source, a.config)
	}
	if got := a.Selectors(); len(got) != 1 || got[0].Now != "old" {
		t.Fatalf("old selector state changed after sing-box check failure: %#v", got)
	}
}

func TestRealConfigCandidatesPassSingBoxCheck(t *testing.T) {
	configPath := os.Getenv("SING_BOX_DROVER_TEST_CONFIG")
	corePath := os.Getenv("SING_BOX_DROVER_TEST_CORE")
	if configPath == "" || corePath == "" {
		t.Skip("set SING_BOX_DROVER_TEST_CONFIG and SING_BOX_DROVER_TEST_CORE to check real runtime variants")
	}
	supervisor := core.NewSupervisor(corePath, nil)
	a := &App{
		source:        config.ConfigSource{FilePath: configPath},
		configChecker: supervisor.Check,
	}
	withoutTun, err := a.checkConfigCandidate(false, false)
	if err != nil {
		t.Fatalf("non-TUN runtime candidate: %v", err)
	}
	if withoutTun.tun {
		t.Fatal("non-TUN candidate unexpectedly retained TUN")
	}
	withTun, err := a.checkConfigCandidate(true, false)
	if err != nil {
		t.Fatalf("TUN runtime candidate: %v", err)
	}
	if withTun.config.HasTunInbound && !withTun.tun {
		t.Fatal("TUN candidate did not retain the configured TUN inbound")
	}
}

func TestReplacementActionsDoNotLaunchWhenSingBoxCheckFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeReloadSource(t, path, reloadConfigJSON)
	checkErr := errors.New("semantic configuration error")
	for _, test := range []struct {
		name string
		run  func(*App) error
	}{
		{name: "restart", run: func(a *App) error { return a.Restart() }},
		{name: "elevated TUN handoff", run: func(a *App) error { return a.LaunchElevated(true) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			launched := false
			a := &App{
				source: config.ConfigSource{FilePath: path},
				configChecker: func(string) error {
					return checkErr
				},
				selfLauncher: func(string, bool) error {
					launched = true
					return nil
				},
			}
			err := test.run(a)
			if !errors.Is(err, checkErr) {
				t.Fatalf("replacement action error = %v, want %v", err, checkErr)
			}
			if launched {
				t.Fatal("replacement process launched after configuration check failed")
			}
		})
	}
}
