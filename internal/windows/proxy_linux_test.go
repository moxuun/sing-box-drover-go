//go:build linux

package windows

import (
	"errors"
	"fmt"
	"testing"
)

func TestLinuxProxyWriteFailureRestoresOriginalSettings(t *testing.T) {
	values := map[string]string{}
	originals := map[string]string{}
	failure := errors.New("settings backend failed")
	writes := 0
	run := func(args ...string) (string, error) {
		key := args[1] + "." + args[2]
		if _, ok := values[key]; !ok {
			values[key] = "original-" + key
			originals[key] = values[key]
		}
		switch args[0] {
		case "writable":
			return "true", nil
		case "get":
			return values[key], nil
		case "set":
			writes++
			values[key] = args[3]
			if writes == 4 {
				return "", failure
			}
			return "", nil
		default:
			return "", fmt.Errorf("unexpected gsettings operation: %v", args)
		}
	}
	if _, err := enableLinuxProxy(run, "127.0.0.1", 2080); !errors.Is(err, failure) {
		t.Fatalf("enableLinuxProxy() = %v, want %v", err, failure)
	}
	for key, original := range originals {
		if values[key] != original {
			t.Fatalf("failed write left %s = %q, want original %q", key, values[key], original)
		}
	}
}

func TestLinuxProxyRejectsAcknowledgedWriteWithoutPersistence(t *testing.T) {
	writes := 0
	run := func(args ...string) (string, error) {
		switch args[0] {
		case "writable":
			return "true", nil
		case "get":
			return "original", nil
		case "set":
			writes++
			return "", nil
		default:
			return "", fmt.Errorf("unexpected operation: %v", args)
		}
	}
	if _, err := enableLinuxProxy(run, "127.0.0.1", 2080); err == nil {
		t.Fatal("unpersisted proxy write reported success")
	}
	if writes != 2 {
		t.Fatalf("writes=%d, want one attempted write and one rollback", writes)
	}
}

func TestLinuxProxyRestoreRejectsAcknowledgedWriteWithoutPersistence(t *testing.T) {
	session := ProxySession{settings: []linuxProxySetting{{
		schema: "org.gnome.system.proxy", key: "mode", original: "'none'", applied: "'manual'",
	}}}
	restored, err := restoreLinuxProxy(func(...string) (string, error) {
		return "'manual'", nil
	}, session)
	if err == nil || restored {
		t.Fatalf("unpersisted restore reported success: (%v, %v)", restored, err)
	}
}

func TestLinuxProxyRestoreRetriesAfterPartialFailure(t *testing.T) {
	session := ProxySession{settings: []linuxProxySetting{
		{schema: "org.gnome.system.proxy.http", key: "host", original: "'old'", applied: "'127.0.0.1'"},
		{schema: "org.gnome.system.proxy", key: "mode", original: "'none'", applied: "'manual'"},
	}}
	values := map[string]string{"host": "'127.0.0.1'", "mode": "'manual'"}
	failure := errors.New("restore failed")
	fail := true
	modeWrites := 0
	run := func(args ...string) (string, error) {
		key := args[2]
		if args[0] == "get" {
			return values[key], nil
		}
		if key == "host" && fail {
			return "", failure
		}
		if key == "mode" {
			modeWrites++
		}
		values[key] = args[3]
		return "", nil
	}
	if restored, err := restoreLinuxProxy(run, session); restored || !errors.Is(err, failure) {
		t.Fatalf("first restoration = (%v, %v), want failure", restored, err)
	}
	if values["mode"] != "'none'" || values["host"] != "'127.0.0.1'" {
		t.Fatalf("unexpected partial restoration: %v", values)
	}
	fail = false
	if restored, err := restoreLinuxProxy(run, session); !restored || err != nil {
		t.Fatalf("retry restoration = (%v, %v), want success", restored, err)
	}
	if values["host"] != "'old'" || modeWrites != 1 {
		t.Fatalf("retry did not restore the remaining setting: values=%v, modeWrites=%d", values, modeWrites)
	}
}

func TestLinuxProxyRestorePreservesExternalChange(t *testing.T) {
	writes := 0
	run := func(args ...string) (string, error) {
		if args[0] == "set" {
			writes++
		}
		return "'auto'", nil
	}
	session := ProxySession{settings: []linuxProxySetting{{
		schema: "org.gnome.system.proxy", key: "mode", original: "'none'", applied: "'manual'",
	}}}
	restored, err := restoreLinuxProxy(run, session)
	if err != nil || restored || writes != 0 {
		t.Fatalf("restoreLinuxProxy() = (%v, %v), writes=%d", restored, err, writes)
	}
}

func TestLinuxProxyRejectsInvalidEndpointBeforeAccessingSettings(t *testing.T) {
	for _, endpoint := range []struct {
		host string
		port int
	}{{"", 2080}, {"bad\x00host", 2080}, {"127.0.0.1", 0}, {"127.0.0.1", 65536}} {
		_, err := enableLinuxProxy(func(...string) (string, error) {
			t.Fatal("invalid endpoint accessed desktop settings")
			return "", nil
		}, endpoint.host, endpoint.port)
		if err == nil {
			t.Fatalf("invalid endpoint accepted: %#v", endpoint)
		}
	}
}
