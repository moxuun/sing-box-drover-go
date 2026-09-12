//go:build windows

package tray

import (
	_ "embed"
	"encoding/binary"
	"sync"
	"unsafe"

	winapi "golang.org/x/sys/windows"
)

const (
	miimState     = 0x00000001
	miimString    = 0x00000040
	miimBitmap    = 0x00000080
	mimStyle      = 0x00000010
	dibRGBColors  = 0
	biRGB         = 0
	colorMenu     = 4
	colorMenuText = 7
	dfcMenu       = 2
	dfcsMenuCheck = 0x0001
	mnsCheckOrBmp = 0x04000000
	smCxMenuCheck = 71
	smCyMenuCheck = 72
	flagWidth     = 18
	flagHeight    = 12
	bitmapGap     = 3
)

type menuItemInfo struct {
	cbSize        uint32
	fMask         uint32
	fType         uint32
	fState        uint32
	wID           uint32
	hSubMenu      uintptr
	hbmpChecked   uintptr
	hbmpUnchecked uintptr
	dwItemData    uintptr
	dwTypeData    *uint16
	cch           uint32
	hbmpItem      uintptr
}

type menuInfo struct {
	cbSize          uint32
	fMask           uint32
	dwStyle         uint32
	cyMax           uint32
	hbrBack         uintptr
	dwContextHelpID uint32
	dwMenuData      uintptr
}

type bitmapInfoHeader struct {
	size          uint32
	width         int32
	height        int32
	planes        uint16
	bitCount      uint16
	compression   uint32
	sizeImage     uint32
	xPelsPerMeter int32
	yPelsPerMeter int32
	clrUsed       uint32
	clrImportant  uint32
}

type bitmapInfo struct {
	header bitmapInfoHeader
	colors [1]uint32
}

type flagAtlasRecord struct {
	code   string
	pixels []byte // RGBA, flagWidth*flagHeight pixels.
}

type selectorBitmapKey struct {
	code    string
	checked bool
}

//go:embed assets/flags.dat
var flagAtlasData []byte

var (
	gdi32              = winapi.NewLazySystemDLL("gdi32.dll")
	createDIBSection   = gdi32.NewProc("CreateDIBSection")
	setDIBits          = gdi32.NewProc("SetDIBits")
	getDIBits          = gdi32.NewProc("GetDIBits")
	createCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	selectObject       = gdi32.NewProc("SelectObject")
	deleteDC           = gdi32.NewProc("DeleteDC")
	deleteObject       = gdi32.NewProc("DeleteObject")
	setMenuItemInfo    = user32.NewProc("SetMenuItemInfoW")
	drawFrameControl   = user32.NewProc("DrawFrameControl")
	getMenuInfo        = user32.NewProc("GetMenuInfo")
	setMenuInfo        = user32.NewProc("SetMenuInfo")
	getSysColor        = user32.NewProc("GetSysColor")
	getSystemMetrics   = user32.NewProc("GetSystemMetrics")

	flagAtlasOnce    sync.Once
	flagAtlasRecords []flagAtlasRecord
)

func loadFlagAtlas() {
	flagAtlasOnce.Do(func() {
		const headerSize = 10
		const recordSize = 2 + flagWidth*flagHeight*4
		data := flagAtlasData
		if len(data) < headerSize || string(data[:4]) != "SBD1" || data[4] != 1 || data[5] != flagWidth || data[6] != flagHeight {
			return
		}
		count := int(binary.LittleEndian.Uint16(data[8:10]))
		if count == 0 || headerSize+count*recordSize > len(data) {
			return
		}
		flagAtlasRecords = make([]flagAtlasRecord, 0, count)
		for i := 0; i < count; i++ {
			offset := headerSize + i*recordSize
			code := string(data[offset : offset+2])
			pixels := data[offset+2 : offset+recordSize]
			flagAtlasRecords = append(flagAtlasRecords, flagAtlasRecord{code: code, pixels: pixels})
		}
	})
}

func flagPixels(code string) []byte {
	code = canonicalFlagCode(code)
	if code == "" {
		return nil
	}
	loadFlagAtlas()
	for _, record := range flagAtlasRecords {
		if record.code == code {
			return record.pixels
		}
	}
	return nil
}

// useSharedCheckAndBitmapColumn makes the selector bitmap occupy the same
// leading column as the native check mark. Flagged selector items are marked
// unchecked in the menu state and carry a composite bitmap containing the
// check glyph plus the flag, while ordinary items continue to use MF_CHECKED.
func useSharedCheckAndBitmapColumn(menu uintptr) bool {
	info := menuInfo{
		cbSize:  uint32(unsafe.Sizeof(menuInfo{})),
		fMask:   mimStyle,
		dwStyle: mnsCheckOrBmp,
	}
	ok, _, _ := setMenuInfo.Call(menu, uintptr(unsafe.Pointer(&info)))
	return ok != 0
}

// appendSelectorItem replaces the first country marker in the visible menu
// text; the caller keeps the original selector value for command routing. A native
// bitmap from the bundled asset atlas keeps flags visible even when the
// Windows menu font renders regional-indicator emoji as plain letters. Items
// with a country marker use one composite bitmap: the system check glyph is
// drawn in the first gutter and the flag starts after a fixed gap. This keeps
// the familiar checkmark while keeping it separate from the flag within the
// shared bitmap column.
func (t *Tray) appendSelectorItem(menu uintptr, menuFlags uint32, id uint32, value string, cache map[selectorBitmapKey]uintptr) {
	text, code := selectorDisplayInfo(value)
	if text == "" {
		text = value
	}
	ptr, err := winapi.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	checked := menuFlags&mfChecked != 0
	itemFlags := menuFlags
	if code != "" {
		// MNS_CHECKORBMP-style menus draw either the native check or hbmpItem.
		// The composite bitmap contains the check for flagged selector items.
		itemFlags &^= mfChecked
	}
	if ok, _, _ := appendMenu.Call(menu, uintptr(itemFlags), uintptr(id), uintptr(unsafe.Pointer(ptr))); ok == 0 {
		return
	}
	if code == "" {
		return
	}
	key := selectorBitmapKey{code: code, checked: checked}
	bmp, exists := cache[key]
	if !exists {
		bmp = createSelectorBitmap(code, checked)
		cache[key] = bmp
		if bmp != 0 {
			t.menuBitmaps = append(t.menuBitmaps, bmp)
		}
	}
	if bmp != 0 && setMenuItemBitmap(menu, uintptr(id), bmp) {
		return
	}
	if checked {
		// If the composite bitmap cannot be attached, restore the native check
		// before falling back to the original text.
		setMenuItemState(menu, uintptr(id), mfChecked)
	}
	// Keep a visible fallback if creating or attaching the bitmap failed.
	if text != value {
		fallback, fallbackErr := winapi.UTF16PtrFromString(value)
		if fallbackErr == nil {
			info := menuItemInfo{
				cbSize:     uint32(unsafe.Sizeof(menuItemInfo{})),
				fMask:      miimString,
				dwTypeData: fallback,
			}
			setMenuItemInfo.Call(menu, uintptr(id), 0, uintptr(unsafe.Pointer(&info)))
		}
	}
}

func setMenuItemBitmap(menu, id, bitmap uintptr) bool {
	info := menuItemInfo{
		cbSize:   uint32(unsafe.Sizeof(menuItemInfo{})),
		fMask:    miimBitmap,
		hbmpItem: bitmap,
	}
	ok, _, _ := setMenuItemInfo.Call(menu, id, 0, uintptr(unsafe.Pointer(&info)))
	return ok != 0
}

func setMenuItemState(menu, id uintptr, state uint32) bool {
	info := menuItemInfo{
		cbSize: uint32(unsafe.Sizeof(menuItemInfo{})),
		fMask:  miimState,
		fState: state,
	}
	ok, _, _ := setMenuItemInfo.Call(menu, id, 0, uintptr(unsafe.Pointer(&info)))
	return ok != 0
}

func (t *Tray) releaseMenuBitmaps() {
	for _, bitmap := range t.menuBitmaps {
		if bitmap != 0 {
			deleteObject.Call(bitmap)
		}
	}
	t.menuBitmaps = nil
}

func createFlagBitmap(code string) uintptr {
	source := flagPixels(code)
	if len(source) != flagWidth*flagHeight*4 {
		return 0
	}

	var bits uintptr
	info := bitmapInfo{
		header: bitmapInfoHeader{
			size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
			width:       flagWidth,
			height:      -flagHeight, // top-down DIB
			planes:      1,
			bitCount:    32,
			compression: biRGB,
		},
	}
	hbitmap, _, _ := createDIBSection.Call(
		0,
		uintptr(unsafe.Pointer(&info)),
		dibRGBColors,
		uintptr(unsafe.Pointer(&bits)),
		0,
		0,
	)
	if hbitmap == 0 || bits == 0 {
		return 0
	}

	pixels := make([]uint32, flagWidth*flagHeight)
	background := menuColor()
	for i := range pixels {
		offset := i * 4
		pixels[i] = blendFlagPixel(source[offset], source[offset+1], source[offset+2], source[offset+3], background)
	}
	if result, _, _ := setDIBits.Call(
		0,
		hbitmap,
		0,
		flagHeight,
		uintptr(unsafe.Pointer(&pixels[0])),
		uintptr(unsafe.Pointer(&info)),
		dibRGBColors,
	); result == 0 {
		deleteObject.Call(hbitmap)
		return 0
	}
	return hbitmap
}

func createSelectorBitmap(code string, checked bool) uintptr {
	source := flagPixels(code)
	if len(source) != flagWidth*flagHeight*4 {
		return 0
	}
	checkWidth, checkHeight := menuCheckDimensions()
	width := checkWidth + bitmapGap + flagWidth
	height := checkHeight
	if height < flagHeight {
		height = flagHeight
	}

	var bits uintptr
	info := bitmapInfo{
		header: bitmapInfoHeader{
			size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
			width:       int32(width),
			height:      -int32(height), // top-down DIB
			planes:      1,
			bitCount:    32,
			compression: biRGB,
		},
	}
	hbitmap, _, _ := createDIBSection.Call(
		0,
		uintptr(unsafe.Pointer(&info)),
		dibRGBColors,
		uintptr(unsafe.Pointer(&bits)),
		0,
		0,
	)
	if hbitmap == 0 || bits == 0 {
		return 0
	}

	background := menuColor()
	pixels := make([]uint32, width*height)
	for i := range pixels {
		pixels[i] = background
	}
	flagOffset := checkWidth + bitmapGap
	flagTop := (height - flagHeight) / 2
	for y := 0; y < flagHeight; y++ {
		for x := 0; x < flagWidth; x++ {
			sourceOffset := (y*flagWidth + x) * 4
			pixels[(flagTop+y)*width+flagOffset+x] = blendFlagPixel(
				source[sourceOffset],
				source[sourceOffset+1],
				source[sourceOffset+2],
				source[sourceOffset+3],
				background,
			)
		}
	}
	if result, _, _ := setDIBits.Call(
		0,
		hbitmap,
		0,
		uintptr(height),
		uintptr(unsafe.Pointer(&pixels[0])),
		uintptr(unsafe.Pointer(&info)),
		dibRGBColors,
	); result == 0 {
		deleteObject.Call(hbitmap)
		return 0
	}
	if checked && !drawNativeCheck(hbitmap, checkWidth, checkHeight) {
		deleteObject.Call(hbitmap)
		return 0
	}
	if checked {
		// DrawFrameControl returns a black-on-white menu mask. Convert the
		// mask's white background back to the menu color before the bitmap is
		// attached, otherwise it would show as a white rectangle on the menu.
		normalizeNativeCheck(hbitmap, &info, width, height, checkWidth, checkHeight, background, menuTextColor())
	}
	return hbitmap
}

func menuCheckDimensions() (width, height int) {
	widthValue, _, _ := getSystemMetrics.Call(smCxMenuCheck)
	heightValue, _, _ := getSystemMetrics.Call(smCyMenuCheck)
	width = int(widthValue)
	height = int(heightValue)
	if width <= 0 {
		width = flagWidth
	}
	if height <= 0 {
		height = flagHeight
	}
	return width, height
}

type menuRect struct {
	left, top, right, bottom int32
}

func drawNativeCheck(hbitmap uintptr, width, height int) bool {
	dc, _, _ := createCompatibleDC.Call(0)
	if dc == 0 {
		return false
	}
	defer deleteDC.Call(dc)
	previous, _, _ := selectObject.Call(dc, hbitmap)
	if previous == 0 {
		return false
	}
	defer selectObject.Call(dc, previous)
	rect := menuRect{right: int32(width), bottom: int32(height)}
	ok, _, _ := drawFrameControl.Call(
		dc,
		uintptr(unsafe.Pointer(&rect)),
		dfcMenu,
		dfcsMenuCheck,
	)
	return ok != 0
}

func normalizeNativeCheck(hbitmap uintptr, info *bitmapInfo, width, height, checkWidth, checkHeight int, background, foreground uint32) bool {
	dc, _, _ := createCompatibleDC.Call(0)
	if dc == 0 {
		return false
	}
	defer deleteDC.Call(dc)
	pixels := make([]uint32, width*height)
	if result, _, _ := getDIBits.Call(
		dc,
		hbitmap,
		0,
		uintptr(height),
		uintptr(unsafe.Pointer(&pixels[0])),
		uintptr(unsafe.Pointer(info)),
		dibRGBColors,
	); result == 0 {
		return false
	}
	for y := 0; y < checkHeight && y < height; y++ {
		for x := 0; x < checkWidth && x < width; x++ {
			index := y*width + x
			// GetDIBits may return a zero alpha byte for the mask even though
			// the RGB channels contain the documented black-on-white pixels.
			switch pixels[index] & 0x00ffffff {
			case 0x00ffffff:
				pixels[index] = background
			case 0:
				pixels[index] = foreground
			}
		}
	}
	result, _, _ := setDIBits.Call(
		dc,
		hbitmap,
		0,
		uintptr(height),
		uintptr(unsafe.Pointer(&pixels[0])),
		uintptr(unsafe.Pointer(info)),
		dibRGBColors,
	)
	return result != 0
}

func menuColor() uint32 {
	value, _, _ := getSysColor.Call(colorMenu)
	color := uint32(value)
	r := byte(color & 0xff)
	g := byte((color >> 8) & 0xff)
	b := byte((color >> 16) & 0xff)
	return rgb(r, g, b)
}

func menuTextColor() uint32 {
	value, _, _ := getSysColor.Call(colorMenuText)
	color := uint32(value)
	return rgb(byte(color&0xff), byte((color>>8)&0xff), byte((color>>16)&0xff))
}

func blendFlagPixel(r, g, b, alpha byte, background uint32) uint32 {
	if alpha == 0 {
		return background
	}
	if alpha == 255 {
		return rgb(r, g, b)
	}
	backgroundR := byte(background >> 16)
	backgroundG := byte(background >> 8)
	backgroundB := byte(background)
	return rgb(
		blendChannel(r, backgroundR, alpha),
		blendChannel(g, backgroundG, alpha),
		blendChannel(b, backgroundB, alpha),
	)
}

func blendChannel(source, background, alpha byte) byte {
	return byte((uint16(source)*uint16(alpha) + uint16(background)*uint16(255-alpha) + 127) / 255)
}

func rgb(r, g, b byte) uint32 {
	return 0xff000000 | uint32(r)<<16 | uint32(g)<<8 | uint32(b)
}
