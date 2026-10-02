//go:build windows

package windows

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestEnableSystemProxyRejectsBlankHost(t *testing.T) {
	if _, err := EnableSystemProxy(" \t", 10808); err == nil {
		t.Fatal("blank system proxy host was accepted")
	}
}

func TestRestoreTargetTurnsOffItsOwnLeftoverProxy(t *testing.T) {
	leftover := proxySettings{
		flags:  proxyTypeProxy,
		server: "http://127.0.0.1:10808",
		bypass: "<local>;127.*",
	}
	target := restoreTarget(leftover, "127.0.0.1")
	if target.flags&proxyTypeProxy != 0 {
		t.Fatalf("leftover proxy was kept enabled: %+v", target)
	}
	if target.flags&proxyTypeDirect == 0 {
		t.Fatalf("restore target is neither direct nor proxy: %+v", target)
	}
}

func TestRestoreTargetKeepsUserProxy(t *testing.T) {
	cases := []proxySettings{
		{flags: proxyTypeDirect | proxyTypeProxy, server: "http://proxy.example:8080", bypass: "<local>"},
		{flags: proxyTypeDirect | proxyTypeProxy, server: "http=127.0.0.1:10808;https=proxy.example:8080", bypass: "<local>"},
		{flags: proxyTypeDirect, server: "http://127.0.0.1:10808", bypass: "<local>"},
		{flags: proxyTypeDirect | proxyTypeProxy, server: "", bypass: ""},
	}
	for _, original := range cases {
		if target := restoreTarget(original, "127.0.0.1"); target != original {
			t.Fatalf("user proxy settings were changed: %+v -> %+v", original, target)
		}
	}
}

func TestProxyHostsMatchAcceptsWinINetForms(t *testing.T) {
	cases := []struct {
		server string
		host   string
		want   bool
	}{
		{"http://127.0.0.1:10808", "127.0.0.1", true},
		{"127.0.0.1:10808", "127.0.0.1", true},
		{"http=127.0.0.1:10808;https=127.0.0.1:10808", "127.0.0.1", true},
		{"http://LOCALHOST:10808", "localhost", true},
		{"http=127.0.0.1:10808;https=proxy.example:8080", "127.0.0.1", false},
		{"http://proxy.example:8080", "127.0.0.1", false},
		{"http://127.0.0.1:10809", "127.0.0.1", true},
		{"", "127.0.0.1", false},
	}
	for _, tc := range cases {
		if got := proxyHostsMatch(tc.server, tc.host); got != tc.want {
			t.Fatalf("proxyHostsMatch(%q, %q) = %v, want %v", tc.server, tc.host, got, tc.want)
		}
	}
}

func TestRestoreAfterProxyVerificationFailureReportsRollbackError(t *testing.T) {
	verifyErr := errors.New("query failed")
	rollbackErr := errors.New("rollback failed")
	err := restoreAfterProxyVerificationFailure(proxySettings{}, verifyErr, func(proxySettings) error {
		return rollbackErr
	})
	if !errors.Is(err, verifyErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("rollback error = %v, want both verification and rollback errors", err)
	}
	if !strings.Contains(err.Error(), "restore original system proxy") {
		t.Fatalf("rollback error lacks recovery context: %v", err)
	}
}

func TestRestoreAfterProxyVerificationFailurePreservesVerificationError(t *testing.T) {
	verifyErr := errors.New("query failed")
	err := restoreAfterProxyVerificationFailure(proxySettings{}, verifyErr, func(proxySettings) error {
		return nil
	})
	if !errors.Is(err, verifyErr) || strings.Contains(err.Error(), "restore original system proxy") {
		t.Fatalf("successful rollback error = %v, want verification error only", err)
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
