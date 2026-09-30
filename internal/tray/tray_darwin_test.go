//go:build darwin

package tray

import (
	"testing"

	"sing-box-drover/internal/clash"
)

func TestSelectorFingerprintTracksStructureNotRuntimeSelection(t *testing.T) {
	selectors := []clash.Selector{{
		Name:        "proxy",
		All:         []string{"a", "b"},
		Now:         "a",
		ResolvedNow: "resolved-a",
	}}
	initial := selectorFingerprint(selectors, "auto")

	selectors[0].Now = "b"
	selectors[0].ResolvedNow = "resolved-b"
	if got := selectorFingerprint(selectors, "auto"); got != initial {
		t.Fatalf("runtime selection changed structural fingerprint: %q -> %q", initial, got)
	}

	selectors[0].All = append(selectors[0].All, "c")
	if got := selectorFingerprint(selectors, "auto"); got == initial {
		t.Fatal("outbound list change did not update structural fingerprint")
	}
}
