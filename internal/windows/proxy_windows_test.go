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

func TestSystemProxyRoundTrip(t *testing.T) {
	if os.Getenv("SING_BOX_DROVER_PROXY_INTEGRATION") != "1" {
		t.Skip("set SING_BOX_DROVER_PROXY_INTEGRATION=1 to temporarily change the current user's proxy")
	}
	original, err := queryProxySettings()
	if err != nil {
		t.Fatalf("query original proxy settings: %v", err)
	}
	// Whatever the assertions below prove, the user's own settings go back.
	defer func() {
		if err := setProxySettings(original); err != nil {
			t.Errorf("emergency proxy restore: %v", err)
		}
	}()
	session, err := EnableSystemProxy("127.0.0.1", 10808)
	if err != nil {
		t.Fatalf("enable proxy: %v", err)
	}
	applied, err := queryProxySettings()
	if err != nil {
		t.Fatalf("query applied proxy settings: %v", err)
	}
	t.Logf("applied: flags=%#x server=%q bypass=%q pac=%q", applied.flags, applied.server, applied.bypass, applied.autoConfigURL)
	// Taking the proxy over must not touch the user's exception list, and must
	// not enable automatic configuration discovery behind their back.
	if applied.bypass != original.bypass {
		t.Fatalf("enable changed the bypass list: %q, want %q", applied.bypass, original.bypass)
	}
	if want := uint32(proxyTypeProxy); applied.flags != want {
		t.Fatalf("enabled proxy flags = %#x, want %#x", applied.flags, want)
	}
	if applied.autoConfigURL != "" {
		t.Fatalf("enable left a PAC URL in place: %q", applied.autoConfigURL)
	}

	cleared, err := RestoreSystemProxy(session)
	if err != nil {
		t.Fatalf("restore proxy: %v", err)
	}
	if !cleared {
		t.Fatal("restoring the system proxy reported no change")
	}
	current, err := queryProxySettings()
	if err != nil {
		t.Fatalf("query restored proxy settings: %v", err)
	}
	t.Logf("restored: flags=%#x server=%q bypass=%q pac=%q", current.flags, current.server, current.bypass, current.autoConfigURL)
	// Releasing the proxy puts the captured configuration back. Leaving
	// DIRECT | AUTO_DETECT behind would turn on WPAD on a machine that was on
	// plain DIRECT before, and would lose a bypass list the user had set up.
	if current != original {
		t.Fatalf("restore produced %+v, want the captured settings %+v", current, original)
	}
}

func TestRestoreSystemProxyWithoutCapturedSettingsClearsProxy(t *testing.T) {
	if os.Getenv("SING_BOX_DROVER_PROXY_INTEGRATION") != "1" {
		t.Skip("set SING_BOX_DROVER_PROXY_INTEGRATION=1 to temporarily change the current user's proxy")
	}
	// A session handed over from a build that captured nothing still has to
	// release the proxy rather than leave it pointing at a dead port.
	if _, err := RestoreSystemProxy(ProxySession{}); err != nil {
		t.Fatalf("restore empty session: %v", err)
	}
}
