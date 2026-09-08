//go:build windows

package tray

import "testing"

func TestCreateTrayIconVariants(t *testing.T) {
	for _, kind := range []trayIconKind{trayIconPlain, trayIconGreen, trayIconRed} {
		icon, err := createTrayIcon(kind)
		if err != nil {
			t.Fatalf("createTrayIcon(%d): %v", kind, err)
		}
		if icon == 0 {
			t.Fatalf("createTrayIcon(%d) returned a null HICON", kind)
		}
		destroyTrayIcon(icon)
	}
}

func TestTrayFillColors(t *testing.T) {
	for _, test := range []struct {
		kind  trayIconKind
		wantR byte
		wantG byte
		wantB byte
	}{
		{kind: trayIconGreen, wantR: 34, wantG: 197, wantB: 94},
		{kind: trayIconRed, wantR: 239, wantG: 68, wantB: 68},
	} {
		r, g, b := trayFillColor(test.kind)
		if r != test.wantR || g != test.wantG || b != test.wantB {
			t.Fatalf("trayFillColor(%d) = (%d, %d, %d), want (%d, %d, %d)", test.kind, r, g, b, test.wantR, test.wantG, test.wantB)
		}
	}
}
