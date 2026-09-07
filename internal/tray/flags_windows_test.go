//go:build windows

package tray

import (
	"testing"
	"unsafe"

	winapi "golang.org/x/sys/windows"
)

func TestFlagAtlasContainsGeneratedAssets(t *testing.T) {
	loadFlagAtlas()
	if len(flagAtlasRecords) < 200 {
		t.Fatalf("flag atlas contains %d records, want the complete asset set", len(flagAtlasRecords))
	}
	if len(flagPixels("LA")) != flagWidth*flagHeight*4 {
		t.Fatal("flag atlas is missing the Laos asset")
	}
	if flagPixels("ZZ") != nil {
		t.Fatal("flag atlas unexpectedly contains the unknown ZZ code")
	}
}

func TestCreateFlagBitmap(t *testing.T) {
	bitmap := createFlagBitmap("US")
	if bitmap == 0 {
		t.Fatal("createFlagBitmap returned a null HBITMAP")
	}
	deleteObject.Call(bitmap)
}

func TestSetMenuItemBitmap(t *testing.T) {
	menu, _, _ := createPopupMenu.Call()
	if menu == 0 {
		t.Fatal("CreatePopupMenu returned a null HMENU")
	}
	defer destroyMenu.Call(menu)

	text, err := winapi.UTF16PtrFromString("test")
	if err != nil {
		t.Fatal(err)
	}
	if ok, _, _ := appendMenu.Call(menu, mfString, 1, uintptr(unsafe.Pointer(text))); ok == 0 {
		t.Fatal("AppendMenuW failed")
	}
	bitmap := createFlagBitmap("US")
	if bitmap == 0 {
		t.Fatal("createFlagBitmap returned a null HBITMAP")
	}
	defer deleteObject.Call(bitmap)
	if !setMenuItemBitmap(menu, 1, bitmap) {
		t.Fatal("SetMenuItemInfoW failed to attach the bitmap")
	}
}

func TestSelectorDisplayInfoRemovesExistingFlag(t *testing.T) {
	text, code := selectorDisplayInfo("mysub/🇺🇸 serv xtls-reality")
	if text != "mysub/ serv xtls-reality" || code != "US" {
		t.Fatalf("selectorDisplayInfo returned (%q, %q)", text, code)
	}
}
