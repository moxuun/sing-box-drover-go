package tray

import (
	"bytes"
	"image/png"
	"testing"
)

func TestTrayIconVariants(t *testing.T) {
	for _, icon := range [][]byte{plainStatusIcon, greenStatusIcon, redStatusIcon} {
		image, err := png.Decode(bytes.NewReader(icon))
		if err != nil {
			t.Fatalf("invalid embedded tray PNG: %v", err)
		}
		if bounds := image.Bounds(); bounds.Dx() == 0 || bounds.Dx() != bounds.Dy() {
			t.Fatalf("invalid tray icon dimensions: %v", bounds)
		}
	}
	if bytes.Equal(plainStatusIcon, greenStatusIcon) || bytes.Equal(greenStatusIcon, redStatusIcon) {
		t.Fatal("tray status icons must remain visually distinct")
	}
}
