//go:build windows

package tray

import (
	"errors"
	"unsafe"
)

const (
	trayIconSize        = 32
	trayIconViewBox     = 16.0
	trayIconSupersample = 4
)

type iconPoint struct {
	x float64
	y float64
}

type iconInfo struct {
	fIcon    uint32
	xHotspot uint32
	yHotspot uint32
	hbmMask  uintptr
	hbmColor uintptr
}

var (
	createIconIndirect = user32.NewProc("CreateIconIndirect")
	createBitmap       = gdi32.NewProc("CreateBitmap")
	destroyIcon        = user32.NewProc("DestroyIcon")
)

// The supplied SVG is one non-zero-winding path: its outer silhouette is
// filled and these four reverse-winding faces cut the seams back out. Keeping
// the source SVG beside these coordinates makes the Windows HICON independent
// of WebView, GDI+, and an SVG runtime while retaining the supplied shape.
var boxSeamOuter = []iconPoint{
	{7.443, 0.184}, {8.557, 0.184}, {15.686, 3.036}, {16, 3.5},
	{16, 12.162}, {15.371, 13.09}, {8.186, 15.964}, {7.814, 15.964},
	{0.63, 13.09}, {0, 12.162}, {0, 3.5}, {0.314, 3.036},
}

var boxSeamFaces = [][]iconPoint{
	{{8.186, 1.113}, {7.814, 1.113}, {1.846, 3.5}, {4.25, 4.461}, {10.404, 2}},
	{{11.75, 2.539}, {5.596, 5}, {8, 5.961}, {14.154, 3.5}},
	{{15, 4.239}, {8.5, 6.839}, {8.5, 14.761}, {15, 12.161}, {15, 4.24}},
	{{7.5, 14.762}, {7.5, 6.838}, {1, 4.239}, {1, 12.162}},
}

func createTrayIcon(kind trayIconKind) (uintptr, error) {
	pixels := make([]uint32, trayIconSize*trayIconSize)
	baseR, baseG, baseB := trayFillColor(kind)
	for y := 0; y < trayIconSize; y++ {
		for x := 0; x < trayIconSize; x++ {
			baseSamples := 0
			for sy := 0; sy < trayIconSupersample; sy++ {
				for sx := 0; sx < trayIconSupersample; sx++ {
					point := iconPoint{
						x: (float64(x) + (float64(sx)+0.5)/trayIconSupersample) * trayIconViewBox / trayIconSize,
						y: (float64(y) + (float64(sy)+0.5)/trayIconSupersample) * trayIconViewBox / trayIconSize,
					}
					if boxSeamContains(point) {
						baseSamples++
					}
				}
			}
			baseAlpha := byte(baseSamples * 255 / (trayIconSupersample * trayIconSupersample))
			pixels[y*trayIconSize+x] = uint32(baseAlpha)<<24 | uint32(baseR)<<16 | uint32(baseG)<<8 | uint32(baseB)
		}
	}

	var bits uintptr
	info := bitmapInfo{
		header: bitmapInfoHeader{
			size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
			width:       trayIconSize,
			height:      -trayIconSize,
			planes:      1,
			bitCount:    32,
			compression: biRGB,
		},
	}
	hbmColor, _, _ := createDIBSection.Call(
		0,
		uintptr(unsafe.Pointer(&info)),
		dibRGBColors,
		uintptr(unsafe.Pointer(&bits)),
		0,
		0,
	)
	if hbmColor == 0 || bits == 0 {
		return 0, errors.New("CreateDIBSection failed for tray icon")
	}
	if result, _, _ := setDIBits.Call(
		0,
		hbmColor,
		0,
		trayIconSize,
		uintptr(unsafe.Pointer(&pixels[0])),
		uintptr(unsafe.Pointer(&info)),
		dibRGBColors,
	); result == 0 {
		deleteObject.Call(hbmColor)
		return 0, errors.New("SetDIBits failed for tray icon")
	}

	maskStride := (trayIconSize + 7) / 8
	maskBits := make([]byte, maskStride*trayIconSize)
	for y := 0; y < trayIconSize; y++ {
		for x := 0; x < trayIconSize; x++ {
			if pixels[y*trayIconSize+x]>>24 == 0 {
				maskBits[y*maskStride+x/8] |= 0x80 >> (x & 7)
			}
		}
	}
	hbmMask, _, _ := createBitmap.Call(
		trayIconSize,
		trayIconSize,
		1,
		1,
		uintptr(unsafe.Pointer(&maskBits[0])),
	)
	if hbmMask == 0 {
		deleteObject.Call(hbmColor)
		return 0, errors.New("CreateBitmap failed for tray icon mask")
	}

	iconData := iconInfo{fIcon: 1, hbmMask: hbmMask, hbmColor: hbmColor}
	hIcon, _, _ := createIconIndirect.Call(uintptr(unsafe.Pointer(&iconData)))
	deleteObject.Call(hbmMask)
	deleteObject.Call(hbmColor)
	if hIcon == 0 {
		return 0, errors.New("CreateIconIndirect failed for tray icon")
	}
	return hIcon, nil
}

func boxSeamContains(point iconPoint) bool {
	if !pointInPolygon(point, boxSeamOuter) {
		return false
	}
	for _, face := range boxSeamFaces {
		if pointInPolygon(point, face) {
			return false
		}
	}
	return true
}

func pointInPolygon(point iconPoint, polygon []iconPoint) bool {
	inside := false
	for i, j := 0, len(polygon)-1; i < len(polygon); j, i = i, i+1 {
		a, b := polygon[i], polygon[j]
		if (a.y > point.y) == (b.y > point.y) {
			continue
		}
		cross := (b.x-a.x)*(point.y-a.y)/(b.y-a.y) + a.x
		if point.x < cross {
			inside = !inside
		}
	}
	return inside
}

func trayBaseColor() (r, g, b byte) {
	background := menuColor()
	r, g, b = byte(background>>16), byte(background>>8), byte(background)
	if 299*int(r)+587*int(g)+114*int(b) < 128000 {
		return 236, 236, 236
	}
	return 32, 32, 32
}

func trayFillColor(kind trayIconKind) (r, g, b byte) {
	switch kind {
	case trayIconGreen:
		return 34, 197, 94
	case trayIconRed:
		return 239, 68, 68
	default:
		return trayBaseColor()
	}
}

func destroyTrayIcon(icon uintptr) {
	if icon != 0 {
		destroyIcon.Call(icon)
	}
}
