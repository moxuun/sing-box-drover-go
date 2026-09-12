//go:build windows

package windows

import (
	"os"
	"testing"
)

func TestEnableSystemProxyRejectsBlankHost(t *testing.T) {
	if _, err := EnableSystemProxy(" \t", 10808); err == nil {
		t.Fatal("blank system proxy host was accepted")
	}
}

func TestProxySettingsEqualIncludesRestorableFields(t *testing.T) {
	base := proxySettings{
		flags:         proxyTypeDirect | proxyTypeProxy,
		server:        "http://127.0.0.1:10808",
		bypass:        "<local>",
		autoConfigURL: "http://example.invalid/proxy.pac",
	}
	if !proxySettingsEqual(base, base) {
		t.Fatal("identical proxy settings were not equal")
	}
	cases := []proxySettings{
		{flags: proxyTypeDirect, server: base.server, bypass: base.bypass, autoConfigURL: base.autoConfigURL},
		{flags: base.flags, server: "http://127.0.0.1:10809", bypass: base.bypass, autoConfigURL: base.autoConfigURL},
		{flags: base.flags, server: base.server, bypass: "localhost", autoConfigURL: base.autoConfigURL},
		{flags: base.flags, server: base.server, bypass: base.bypass},
	}
	for _, changed := range cases {
		if proxySettingsEqual(base, changed) {
			t.Fatalf("different proxy settings were equal: %+v", changed)
		}
	}
}

func TestSystemProxyRoundTrip(t *testing.T) {
	if os.Getenv("SING_BOX_DROVER_PROXY_INTEGRATION") != "1" {
		t.Skip("set SING_BOX_DROVER_PROXY_INTEGRATION=1 to temporarily change the current user's proxy")
	}
	original, err := queryProxySettings()
	if err != nil {
		t.Fatalf("query original proxy settings: %v", err)
	}
	session, err := EnableSystemProxy("127.0.0.1", 10808)
	if err != nil {
		t.Fatalf("enable proxy: %v", err)
	}
	restored := false
	defer func() {
		if !restored {
			if err := setProxySettings(original); err != nil {
				t.Errorf("emergency proxy restore: %v", err)
			}
		}
	}()

	current, err := queryProxySettings()
	if err != nil {
		t.Fatalf("query applied proxy settings: %v", err)
	}
	if !proxySettingsEqual(current, session.applied) {
		t.Fatal("queried proxy settings differ from the recorded applied state")
	}
	didRestore, err := RestoreSystemProxy(session)
	if err != nil {
		t.Fatalf("restore proxy: %v", err)
	}
	if !didRestore {
		t.Fatal("proxy restore unexpectedly relinquished ownership")
	}
	restored = true
	current, err = queryProxySettings()
	if err != nil {
		t.Fatalf("query restored proxy settings: %v", err)
	}
	if !proxySettingsEqual(current, original) {
		t.Fatal("proxy settings did not return to their original state")
	}
}
