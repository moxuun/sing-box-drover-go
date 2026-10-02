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
	if _, err := EnableSystemProxy("127.0.0.1", 10808); err != nil {
		t.Fatalf("enable proxy: %v", err)
	}
	applied, err := queryProxySettings()
	if err != nil {
		t.Fatalf("query applied proxy settings: %v", err)
	}
	t.Logf("applied: flags=%#x server=%q bypass=%q pac=%q", applied.flags, applied.server, applied.bypass, applied.autoConfigURL)

	cleared, err := RestoreSystemProxy(ProxySession{})
	if err != nil {
		t.Fatalf("clear proxy: %v", err)
	}
	if !cleared {
		t.Fatal("clearing the system proxy reported no change")
	}
	current, err := queryProxySettings()
	if err != nil {
		t.Fatalf("query cleared proxy settings: %v", err)
	}
	t.Logf("cleared: flags=%#x server=%q bypass=%q pac=%q", current.flags, current.server, current.bypass, current.autoConfigURL)
	// The official client switches the manual proxy and any explicit PAC URL
	// off and leaves the strings in the registry alone.
	if want := uint32(proxyTypeDirect | proxyTypeAutoDetect); current.flags != want {
		t.Fatalf("proxy flags = %#x, want %#x", current.flags, want)
	}
	if current.server != applied.server || current.bypass != applied.bypass || current.autoConfigURL != applied.autoConfigURL {
		t.Fatalf("clearing changed the proxy strings: %+v, want %+v", current, applied)
	}
}
