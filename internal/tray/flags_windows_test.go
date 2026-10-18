//go:build windows

package tray

import (
	"strings"
	"testing"
	"unsafe"

	"github.com/egoist/mygo"
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
		flagOffset := checkWidth + bitmapGap
		flagTop := (height - flagHeight) / 2
		flagPixels := 0
		for y := 0; y < flagHeight; y++ {
			for x := 0; x < flagWidth; x++ {
				if pixels[(flagTop+y)*width+flagOffset+x] != background {
					flagPixels++
				}
			}
		}
		if flagPixels == 0 {
			deleteObject.Call(bitmap)
			t.Fatalf("selector bitmap (checked=%t) has no flag pixels", checked)
		}
		deleteObject.Call(bitmap)
	}
}

func TestNormalizeNativeCheckMakesMaskOpaque(t *testing.T) {
	background := rgb(12, 34, 56)
	foreground := rgb(200, 180, 160)
	pixels := []uint32{0x00ffffff, 0, 0x00123456, background}
	if !normalizeNativeCheck(pixels, 2, 2, 2, 2, background, foreground) {
		t.Fatal("normalizeNativeCheck returned false")
	}
	if pixels[0] != background || pixels[1] != foreground {
		t.Fatalf("normalized mask colors = %#x, %#x", pixels[0], pixels[1])
	}
	if pixels[2] != 0xff123456 || pixels[3] != background {
		t.Fatalf("normalized mask alpha/colors = %#x, %#x", pixels[2], pixels[3])
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

func TestNativePopupRendersFlagsAndChecks(t *testing.T) {
	items := []*mygo.MenuItem{
		{ID: "selector:0:0", Label: "🇺🇸 node", Type: mygo.MenuItemCheckbox},
		{ID: "selector:0:1", Label: "🇺🇸 node", Type: mygo.MenuItemCheckbox, Checked: true},
	}
	popup, err := newNativePopupMenu(mygo.NewMenu(items))
	if err != nil {
		t.Fatal(err)
	}
	defer popup.close()
	getMenuItemInfo := user32.NewProc("GetMenuItemInfoW")
	for i, item := range items {
		var text [256]uint16
		info := menuItemInfo{
			cbSize:     uint32(unsafe.Sizeof(menuItemInfo{})),
			fMask:      miimString | miimBitmap | miimState,
			dwTypeData: &text[0],
			cch:        uint32(len(text) - 1),
		}
		if ok, _, _ := getMenuItemInfo.Call(popup.menu, uintptr(i), 1, uintptr(unsafe.Pointer(&info))); ok == 0 {
			t.Fatal("GetMenuItemInfoW failed")
		}
		if got := winapi.UTF16ToString(text[:]); got != " node" {
			t.Fatalf("native label = %q, want flag marker replaced by bitmap", got)
		}
		if info.hbmpItem == 0 || info.fState&mfChecked != 0 {
			t.Fatal("native menu must use a composite flag/check bitmap")
		}
		if item.Label != "🇺🇸 node" || item.IsChecked() != (i == 1) {
			t.Fatal("native rendering changed the shared menu item")
		}
	}
}

func TestNativePopupRejectsInvalidTextAndReleasesResources(t *testing.T) {
	getGuiResources := user32.NewProc("GetGuiResources")
	for _, invalid := range []*mygo.MenuItem{
		{ID: "selector:0:1", Label: "bad\x00node"},
		{Label: "bad\x00group", Submenu: []*mygo.MenuItem{{Label: "node"}}},
		{Label: "valid", Submenu: []*mygo.MenuItem{{Label: "bad\x00child"}}},
		{Label: string([]byte{0xff})},
	} {
		menu := mygo.NewMenu([]*mygo.MenuItem{
			{ID: "selector:0:0", Label: "🇺🇸 node", Type: mygo.MenuItemCheckbox},
			invalid,
		})
		var before [2]uintptr
		for kind := range before {
			before[kind], _, _ = getGuiResources.Call(^uintptr(0), uintptr(kind))
		}
		popup, err := newNativePopupMenu(menu)
		if popup != nil {
			popup.close()
			t.Fatal("invalid menu unexpectedly produced a native popup")
		}
		if err == nil || !strings.Contains(err.Error(), "invalid native menu item text") {
			t.Fatalf("newNativePopupMenu() error = %v", err)
		}
		for kind, count := range before {
			after, _, _ := getGuiResources.Call(^uintptr(0), uintptr(kind))
			if after != count {
				t.Fatalf("invalid menu leaked native resources (kind=%d): before=%d after=%d", kind, count, after)
			}
		}
	}
}

func TestWithoutSelectorItemsKeepsRecoveryItems(t *testing.T) {
	// Native menu command IDs are uint16, so a provider that expands past 0xffff
	// nodes cannot be shown. The popup must still open with the items that carry
	// no native ID, otherwise the only way out of a GUI build (Quit) disappears
	// exactly when the user needs it.
	items := []*mygo.MenuItem{
		{ID: "proxy", Label: "System Proxy", Type: mygo.MenuItemCheckbox},
		mygo.Separator(),
		{ID: "selector:0:0", Label: "node-a", Type: mygo.MenuItemCheckbox},
		{ID: "selector:0:1", Label: "node-b", Type: mygo.MenuItemCheckbox},
		{Label: "flat group", Submenu: []*mygo.MenuItem{{ID: "selector:1:0", Label: "node-c"}}},
		{Label: "plain group", Submenu: []*mygo.MenuItem{{ID: "nested", Label: "keep me"}}},
		mygo.Separator(),
		{ID: "autostart", Label: "Start with Windows", Type: mygo.MenuItemCheckbox},
		{ID: "restart", Label: "Restart core"},
		{ID: "homepage", Label: "Homepage"},
		{ID: "quit", Label: "Quit"},
	}
	kept := withoutSelectorItems(items)
	// The plain group carries no ID of its own, so match the entries by label.
	want := []string{"System Proxy", "", "plain group", "", "Start with Windows", "Restart core", "Homepage", "Quit"}
	if len(kept) != len(want) {
		t.Fatalf("withoutSelectorItems kept %d items, want %d", len(kept), len(want))
	}
	for i, item := range kept {
		if item.Label != want[i] {
			t.Fatalf("item %d = %q, want %q", i, item.Label, want[i])
		}
	}
	if containsSelectorItem(kept) {
		t.Fatal("containsSelectorItem reported a selector in the filtered list")
	}
	// Quit must survive: it is the only exit in a console-less build.
	if last := kept[len(kept)-1]; last.ID != "quit" {
		t.Fatalf("fallback dropped the Quit item: %q", last.Label)
	}
	// A group whose children are all plain items stays intact.
	for _, item := range kept {
		if item.Label == "plain group" {
			if len(item.Submenu) != 1 || item.Submenu[0].ID != "nested" {
				t.Fatalf("plain group was damaged: %+v", item.Submenu)
			}
			return
		}
	}
	t.Fatal("plain group was dropped by the fallback")
}

func TestTooManyMenuItemsSentinelIsRecognisable(t *testing.T) {
	// newNativePopupMenu recovers only from this sentinel; a plain errors.New in
	// build would silently stop the fallback from ever running.
	if !strings.Contains(errTooManyMenuItems.Error(), "too many native menu items") {
		t.Fatalf("sentinel error = %q", errTooManyMenuItems)
	}
}
