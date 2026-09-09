package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeJSONKeepsCommentLikeString(t *testing.T) {
	input := `{"url":"https://example.test/a//b", // comment
      "items":[1,2,],}`
	got := NormalizeJSON(input)
	if !strings.Contains(got, `https://example.test/a//b`) || strings.Contains(got, "comment") {
		t.Fatalf("normalization changed string/comment handling: %q", got)
	}
	if _, err := ReadSingBoxConfig(input); err != nil {
		t.Fatal(err)
	}
}

func TestReadSingBoxConfigAddsRuntimeAPIAndFiltersTun(t *testing.T) {
	input := `{
	  // source comments remain outside generated runtime text
	  "inbounds": [
	    {"type":"mixed", "listen":"127.0.0.1", "listen_port":1080},
	    {"type":"tun", "tag":"tun-in"},
	  ],
	  "outbounds": [{"type":"selector", "tag":"proxy", "outbounds":["机场A","香港01"], "default":"香港01"}],
	}`
	cfg, err := ReadSingBoxConfig(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckSingBoxConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.HasTunInbound || cfg.ProxyPort != 1080 || cfg.ClashAPI.ExternalController == "" || cfg.ClashAPI.Secret == "" {
		t.Fatalf("unexpected config: %#v", cfg)
	}
	if strings.Contains(cfg.JSONWithoutTUN, `"type":"tun"`) || !strings.Contains(cfg.JSONWithTUN, `"type":"tun"`) {
		t.Fatalf("TUN filtering failed: with=%s without=%s", cfg.JSONWithTUN, cfg.JSONWithoutTUN)
	}
	if cfg.Selectors[0].DefaultIndex != 1 || cfg.Selectors[0].Outbounds[0] != "机场A" {
		t.Fatalf("selector order/default lost: %#v", cfg.Selectors)
	}
}

func TestReadSingBoxConfigProviderOnlySelectorAddsRuntimeAPI(t *testing.T) {
	input := `{
	  "inbounds": [{"type":"mixed", "listen":"127.0.0.1", "listen_port":1080}],
	  "outbounds": [{"type":"selector", "tag":"proxy", "use_all_providers":true}]
	}`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := ReadConfigSource(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ReadSingBoxConfig(source.JSONText)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.HasSelector || len(cfg.Selectors) != 0 {
		t.Fatalf("provider-only selector was not identified independently of static options: %#v", cfg)
	}
	if cfg.ClashAPI.ExternalController != "127.0.0.1:9090" || cfg.ClashAPI.Secret == "" {
		t.Fatalf("default Clash API was not added: %#v", cfg.ClashAPI)
	}
	for name, runtimeJSON := range map[string]string{"with TUN": cfg.JSONWithTUN, "without TUN": cfg.JSONWithoutTUN} {
		if !strings.Contains(runtimeJSON, `"clash_api"`) || !strings.Contains(runtimeJSON, `"external_controller":"127.0.0.1:9090"`) {
			t.Fatalf("%s runtime JSON lacks injected Clash API: %s", name, runtimeJSON)
		}
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != input {
		t.Fatalf("provider-only API injection rewrote the source file: %q", string(stored))
	}
}

func TestReadConfigSourceStripsBOM(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(jsonPath, append([]byte{0xef, 0xbb, 0xbf}, []byte(`{"inbounds":[{"type":"mixed","listen":"127.0.0.1","listen_port":1}]}`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	jsonSource, err := ReadConfigSource(jsonPath)
	if err != nil || strings.HasPrefix(jsonSource.JSONText, "\ufeff") {
		t.Fatalf("BOM source mismatch: %#v %v", jsonSource, err)
	}
	if _, err := ReadSingBoxConfig("\ufeff" + jsonSource.JSONText); err != nil {
		t.Fatalf("BOM JSON should be accepted: %v", err)
	}
}
