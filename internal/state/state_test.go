package state

import (
	"encoding/json"
	"os"
	"testing"
)

func TestSyncSelectorsScopesAndReloads(t *testing.T) {
	path := t.TempDir() + "/state.json"
	f := Load(path)
	if err := f.SyncSelectors(map[string]string{"proxy": "香港01", "other": "东京01"}, []string{"proxy", "other"}); err != nil {
		t.Fatal(err)
	}
	if err := f.SyncSelectors(map[string]string{"proxy": "香港02"}, []string{"proxy", "other"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.GetSelector("other"); ok {
		t.Fatal("scoped selector was not removed")
	}
	loaded := Load(path)
	if got, ok := loaded.GetSelector("proxy"); !ok || got != "香港02" {
		t.Fatalf("reload mismatch: %q %v", got, ok)
	}
}

func TestSyncSelectorsPreservesUnknownStateFields(t *testing.T) {
	path := t.TempDir() + "/state.json"
	if err := os.WriteFile(path, []byte(`{"version":2,"selectors":{"proxy":"old"},"custom":{"keep":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f := Load(path)
	if err := f.SyncSelectors(map[string]string{"proxy": "new"}, []string{"proxy"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	var version int
	var custom map[string]bool
	if json.Unmarshal(root["version"], &version) != nil || json.Unmarshal(root["custom"], &custom) != nil || version != 2 || !custom["keep"] {
		t.Fatalf("unknown state fields were lost: %s", data)
	}
}
