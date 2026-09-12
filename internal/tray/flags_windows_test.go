//go:build windows

package tray

import (
	"strings"
	"testing"
	"unsafe"

	winapi "golang.org/x/sys/windows"
	"sing-box-drover/internal/app"
	"sing-box-drover/internal/clash"
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

func TestCreateSelectorBitmap(t *testing.T) {
	checkWidth, checkHeight := menuCheckDimensions()
	width := checkWidth + bitmapGap + flagWidth
	height := checkHeight
	if height < flagHeight {
		height = flagHeight
	}
	info := bitmapInfo{
		header: bitmapInfoHeader{
			size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
			width:       int32(width),
			height:      -int32(height),
			planes:      1,
			bitCount:    32,
			compression: biRGB,
		},
	}
	for _, checked := range []bool{false, true} {
		bitmap := createSelectorBitmap("US", checked)
		if bitmap == 0 {
			t.Fatalf("createSelectorBitmap(checked=%t) returned a null HBITMAP", checked)
		}
		pixels := make([]uint32, width*height)
		dc, _, _ := createCompatibleDC.Call(0)
		if dc == 0 {
			deleteObject.Call(bitmap)
			t.Fatal("CreateCompatibleDC failed for selector bitmap")
		}
		if result, _, _ := getDIBits.Call(
			dc,
			bitmap,
			0,
			uintptr(height),
			uintptr(unsafe.Pointer(&pixels[0])),
			uintptr(unsafe.Pointer(&info)),
			dibRGBColors,
		); result == 0 {
			deleteDC.Call(dc)
			deleteObject.Call(bitmap)
			t.Fatal("GetDIBits failed for selector bitmap")
		}
		deleteDC.Call(dc)
		background := menuColor()
		checkPixels := 0
		for y := 0; y < checkHeight; y++ {
			for x := 0; x < checkWidth; x++ {
				if pixels[y*width+x] != background {
					checkPixels++
				}
			}
		}
		if checked && checkPixels == 0 {
			deleteObject.Call(bitmap)
			t.Fatal("checked selector bitmap has no check pixels")
		}
		if !checked && checkPixels != 0 {
			deleteObject.Call(bitmap)
			t.Fatalf("unchecked selector bitmap has %d check-column pixels", checkPixels)
		}
		deleteObject.Call(bitmap)
	}
}

func TestUseSharedCheckAndBitmapColumn(t *testing.T) {
	menu, _, _ := createPopupMenu.Call()
	if menu == 0 {
		t.Fatal("CreatePopupMenu returned a null HMENU")
	}
	defer destroyMenu.Call(menu)

	if !useSharedCheckAndBitmapColumn(menu) {
		t.Fatal("useSharedCheckAndBitmapColumn failed")
	}
	info := menuInfo{
		cbSize: uint32(unsafe.Sizeof(menuInfo{})),
		fMask:  mimStyle,
	}
	if ok, _, _ := getMenuInfo.Call(menu, uintptr(unsafe.Pointer(&info))); ok == 0 {
		t.Fatal("GetMenuInfo failed after configuring popup")
	}
	if info.dwStyle&mnsCheckOrBmp == 0 {
		t.Fatalf("MNS_CHECKORBMP is not enabled: %#x", info.dwStyle)
	}
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

func TestSelectorOptionTextDisplaysResolvedRuntimeGroup(t *testing.T) {
	selector := clash.Selector{Name: "proxy", Now: "🎈 自动选择", ResolvedNow: "node-b"}
	if got := selectorOptionText(selector, selector.Now); got != "🎈 自动选择（当前：node-b）" {
		t.Fatalf("selectorOptionText() = %q", got)
	}
	if got := selectorOptionText(selector, "node-a"); got != "node-a" {
		t.Fatalf("selectorOptionText() changed an unselected option: %q", got)
	}
}

func TestBuildMenuRejectsInvalidSelectorTextAndReleasesBitmaps(t *testing.T) {
	tray := &Tray{controller: &app.App{}}
	menu, err := tray.buildMenu([]clash.Selector{
		{Name: "valid", All: []string{"US node"}, Now: "US node"},
		{Name: "invalid", All: []string{"bad\x00node"}, Now: "bad\x00node"},
	})
	if menu != 0 {
		destroyMenu.Call(menu)
		t.Fatal("buildMenu returned a menu after rejecting selector text")
	}
	if err == nil || !strings.Contains(err.Error(), "selector item") {
		t.Fatalf("buildMenu() error = %v, want selector item encoding error", err)
	}
	if len(tray.menuBitmaps) != 0 {
		t.Fatalf("failed menu retained %d bitmap handles", len(tray.menuBitmaps))
	}
}

func TestBuildMenuRejectsInvalidSelectorGroupText(t *testing.T) {
	tray := &Tray{controller: &app.App{}}
	menu, err := tray.buildMenu([]clash.Selector{
		{Name: "bad\x00group", All: []string{"node"}, Now: "node"},
	})
	if menu != 0 {
		destroyMenu.Call(menu)
		t.Fatal("buildMenu returned a menu after rejecting selector group text")
	}
	if err == nil || !strings.Contains(err.Error(), "selector group") {
		t.Fatalf("buildMenu() error = %v, want selector group encoding error", err)
	}
	if len(tray.menuBitmaps) != 0 {
		t.Fatalf("failed menu retained %d bitmap handles", len(tray.menuBitmaps))
	}
}

func TestOpenURLRejectsUnencodableText(t *testing.T) {
	if err := openURL("https://example.test/\x00"); err == nil {
		t.Fatal("openURL accepted a URL containing NUL")
	}
}
