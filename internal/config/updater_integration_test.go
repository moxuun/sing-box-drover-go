package config

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBPFUpdaterValidatesAndAtomicallyReplacesProfile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{\n  \"inbounds\": [{\"type\": \"mixed\", \"listen\": \"127.0.0.1\", \"listen_port\": 1080}], // comment\n}\n"))
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "profile.bpf")
	initial := CreateRemoteBPF(`{"inbounds":[{"type":"mixed","listen":"127.0.0.1","listen_port":1080}]}`, "demo", server.URL, true, 15, 0)
	data, err := EncodeBPF(initial)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	u := &BPFUpdater{Path: path, Profile: initial, HTTPClient: server.Client()}
	if err := u.update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if u.Profile.LastUpdated <= 0 || !strings.Contains(u.Profile.ConfigJSON, "comment") {
		t.Fatalf("updated profile metadata/content missing: %#v", u.Profile)
	}
	loaded, err := ReadConfigSource(path)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.IsBPF() || loaded.BPFProfile.ConfigJSON != u.Profile.ConfigJSON {
		t.Fatalf("replacement mismatch: %#v", loaded)
	}
}
