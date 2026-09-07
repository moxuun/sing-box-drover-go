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
	miimString   = 0x00000040
	miimBitmap   = 0x00000080
	dibRGBColors = 0
	biRGB        = 0
	colorMenu    = 4
	flagWidth    = 18
	flagHeight   = 12
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

//go:embed assets/flags.dat
var flagAtlasData []byte

var (
	gdi32            = winapi.NewLazySystemDLL("gdi32.dll")
	createDIBSection = gdi32.NewProc("CreateDIBSection")
	setDIBits        = gdi32.NewProc("SetDIBits")
	deleteObject     = gdi32.NewProc("DeleteObject")
	setMenuItemInfo  = user32.NewProc("SetMenuItemInfoW")
	getSysColor      = user32.NewProc("GetSysColor")

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

// appendSelectorItem keeps the original selector value for command routing,
// while replacing the first country marker in the visible menu text. A native
// bitmap from the bundled asset atlas keeps flags visible even when the
// Windows menu font renders regional-indicator emoji as plain letters.
func (t *Tray) appendSelectorItem(menu uintptr, menuFlags uint32, id uint32, value string, cache map[string]uintptr) {
	text, code := selectorDisplayInfo(value)
	if text == "" {
		text = value
	}
	ptr, err := winapi.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	if ok, _, _ := appendMenu.Call(menu, uintptr(menuFlags), uintptr(id), uintptr(unsafe.Pointer(ptr))); ok == 0 {
		return
	}
	if code == "" {
		return
	}
	bmp, exists := cache[code]
	if !exists {
		bmp = createFlagBitmap(code)
		cache[code] = bmp
		if bmp != 0 {
			t.menuBitmaps = append(t.menuBitmaps, bmp)
		}
	}
	if bmp != 0 && setMenuItemBitmap(menu, uintptr(id), bmp) {
		return
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

func menuColor() uint32 {
	value, _, _ := getSysColor.Call(colorMenu)
	color := uint32(value)
	r := byte(color & 0xff)
	g := byte((color >> 8) & 0xff)
	b := byte((color >> 16) & 0xff)
	return rgb(r, g, b)
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
