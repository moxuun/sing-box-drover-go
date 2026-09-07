package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sing-box-drover/internal/clash"
	"sing-box-drover/internal/config"
	"sing-box-drover/internal/state"
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
	stateFile := state.Load(filepath.Join(t.TempDir(), "state.json"))
	if err := stateFile.SyncSelectors(map[string]string{"proxy": "node2"}, []string{"proxy"}); err != nil {
		t.Fatal(err)
	}
	a := &App{
		source:  config.ConfigSource{FilePath: path},
		options: config.Options{SelectorPersist: true},
		state:   stateFile,
	}

	candidate, err := a.readConfigCandidate(false, false)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.source.IsBPF() || candidate.config.ProxyPort != 1080 || candidate.tun {
		t.Fatalf("unexpected JSON candidate: %#v", candidate)
	}
	if len(candidate.selectors) != 1 || candidate.selectors[0].Now != "node2" {
		t.Fatalf("selector persistence was not applied: %#v", candidate.selectors)
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

func TestReadConfigCandidateReloadsBPFProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.bpf")
	profile := config.CreateRemoteBPF(reloadConfigJSON, "profile", "https://example.test/config.json", true, 15, 42)
	data, err := config.EncodeBPF(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	a := &App{source: config.ConfigSource{FilePath: path}}

	candidate, err := a.readConfigCandidate(false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !candidate.source.IsBPF() || candidate.source.BPFProfile != profile {
		t.Fatalf("BPF profile was not reloaded intact: %#v", candidate.source)
	}
	if !strings.Contains(candidate.config.JSONWithTUN, `"future_section"`) || !strings.Contains(candidate.config.JSONWithTUN, `"providers"`) {
		t.Fatalf("BPF config fields were lost: %s", candidate.config.JSONWithTUN)
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
		source:    config.ConfigSource{FilePath: path, Format: config.ConfigSourceJSON, JSONText: `{"old":true}`},
		config:    oldConfig,
		selectors: oldSelectors,
		restored:  true,
	}

	if err := a.restartWithConfig(false, false); err == nil {
		t.Fatal("invalid configuration unexpectedly passed preflight")
	}
	if a.source.JSONText != `{"old":true}` || a.config.JSONWithoutTUN != oldConfig.JSONWithoutTUN || !a.restored {
		t.Fatalf("old config state changed after preflight failure: source=%#v config=%#v restored=%v", a.source, a.config, a.restored)
	}
	if got := a.Selectors(); len(got) != 1 || got[0].Now != "old" {
		t.Fatalf("old selector state changed after preflight failure: %#v", got)
	}
}
