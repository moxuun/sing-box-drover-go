//go:build windows

package tray

import (
	"errors"
	"strings"
	"unsafe"

	"github.com/egoist/mygo"
	winapi "golang.org/x/sys/windows"
)

const (
	mfString    = 0x0000
	mfGrayed    = 0x0001
	mfPopup     = 0x0010
	mfChecked   = 0x0008
	mfSeparator = 0x0800

	tpmRightButton = 0x0002
	tpmNoNotify    = 0x0080
	tpmReturnCmd   = 0x0100
	wsPopup        = 0x80000000
	wsExToolWindow = 0x00000080
)

var (
	user32              = winapi.NewLazySystemDLL("user32.dll")
	createPopupMenu     = user32.NewProc("CreatePopupMenu")
	appendMenu          = user32.NewProc("AppendMenuW")
	destroyMenu         = user32.NewProc("DestroyMenu")
	createWindowEx      = user32.NewProc("CreateWindowExW")
	destroyWindow       = user32.NewProc("DestroyWindow")
	setForegroundWindow = user32.NewProc("SetForegroundWindow")
	getCursorPos        = user32.NewProc("GetCursorPos")
	trackPopupMenuEx    = user32.NewProc("TrackPopupMenuEx")
	postMessage         = user32.NewProc("PostMessageW")
)

// MyGo v0.4.8 has no menu bitmap API. This popup uses the shared menu's items
// and callbacks on MyGo's main thread, leaving the tray and event loop to MyGo.
type nativePopupMenu struct {
	menu uintptr
	// items and checked stay parallel: checked records the checkmark drawn for
	// each item, which a refresh can change while the popup is open.
	items   []*mygo.MenuItem
	checked []bool
	bitmaps map[selectorBitmapKey]uintptr
}

func newNativePopupMenu(menu *mygo.Menu) (*nativePopupMenu, error) {
	if menu == nil {
		return nil, errors.New("tray menu is unavailable")
	}
	p := &nativePopupMenu{bitmaps: make(map[selectorBitmapKey]uintptr)}
	var err error
	p.menu, err = p.build(menu.Items())
	if err != nil {
		p.close()
		return nil, err
	}
	return p, nil
}

func (p *nativePopupMenu) build(items []*mygo.MenuItem) (menu uintptr, err error) {
	menu, _, _ = createPopupMenu.Call()
	if menu == 0 {
		return 0, errors.New("CreatePopupMenu failed")
	}
	defer func() {
		if err != nil {
			destroyMenu.Call(menu)
			menu = 0
		}
	}()
	if !useSharedCheckAndBitmapColumn(menu) {
		return menu, errors.New("SetMenuInfo failed for bitmap column")
	}
	for _, item := range items {
		if !item.IsVisible() {
			continue
		}
		if item.Type == mygo.MenuItemSeparator {
			if ok, _, _ := appendMenu.Call(menu, mfSeparator, 0, 0); ok == 0 {
				return menu, errors.New("AppendMenuW failed for separator")
			}
			continue
		}
		if !validMenuText(item.Label) {
			return menu, errors.New("invalid native menu item text")
		}
		label, labelErr := winapi.UTF16PtrFromString(item.Label)
		if labelErr != nil {
			return menu, labelErr
		}
		flags := uint32(mfString)
		if !item.IsEnabled() {
			flags |= mfGrayed
		}
		if item.Submenu != nil {
			sub, buildErr := p.build(item.Submenu)
			if buildErr != nil {
				return menu, buildErr
			}
			if ok, _, _ := appendMenu.Call(menu, uintptr(flags|mfPopup), sub, uintptr(unsafe.Pointer(label))); ok == 0 {
				destroyMenu.Call(sub)
				return menu, errors.New("AppendMenuW failed for submenu")
			}
			continue
		}
		if len(p.items) == 0xffff {
			return menu, errors.New("too many native menu items")
		}
		// Keep the displayed items alive even if a refresh replaces t.menu
		// while TrackPopupMenuEx dispatches queued MyGo callbacks.
		p.items = append(p.items, item)
		p.checked = append(p.checked, item.IsChecked())
		id := uint32(len(p.items))
		if item.IsChecked() {
			flags |= mfChecked
		}
		if strings.HasPrefix(item.ID, "selector:") {
			if appendErr := p.appendSelectorItem(menu, flags, id, item.Label); appendErr != nil {
				return menu, appendErr
			}
		} else if ok, _, _ := appendMenu.Call(menu, uintptr(flags), uintptr(id), uintptr(unsafe.Pointer(label))); ok == 0 {
			return menu, errors.New("AppendMenuW failed for menu item")
		}
	}
	return menu, nil
}

func (p *nativePopupMenu) close() {
	if p.menu != 0 {
		destroyMenu.Call(p.menu)
		p.menu = 0
	}
	for _, bitmap := range p.bitmaps {
		if bitmap != 0 {
			deleteObject.Call(bitmap)
		}
	}
	p.bitmaps = nil
}

func (p *nativePopupMenu) track() (*mygo.MenuItem, bool, error) {
	class, err := winapi.UTF16PtrFromString("STATIC")
	if err != nil {
		return nil, false, err
	}
	// A temporary invisible owner lets the existing MyGo message loop process
	// this popup; the built-in class requires no extra callback or registration.
	owner, _, _ := createWindowEx.Call(wsExToolWindow, uintptr(unsafe.Pointer(class)), 0, wsPopup, 0, 0, 0, 0, 0, 0, 0, 0)
	if owner == 0 {
		return nil, false, errors.New("CreateWindowExW failed for popup owner")
	}
	defer destroyWindow.Call(owner)
	var point struct{ x, y int32 }
	if ok, _, _ := getCursorPos.Call(uintptr(unsafe.Pointer(&point))); ok == 0 {
		return nil, false, errors.New("GetCursorPos failed")
	}
	setForegroundWindow.Call(owner)
	command, _, _ := trackPopupMenuEx.Call(p.menu, tpmRightButton|tpmNoNotify|tpmReturnCmd, uintptr(point.x), uintptr(point.y), owner, 0)
	postMessage.Call(owner, 0, 0, 0) // WM_NULL completes notification-area menu dismissal.
	if command == 0 || command > uintptr(len(p.items)) {
		return nil, false, nil
	}
	index := command - 1
	return p.items[index], p.checked[index], nil
}

func (t *Tray) showMenu() error {
	popup, err := newNativePopupMenu(t.menu)
	if err != nil {
		return err
	}
	item, displayed, err := popup.track()
	popup.close()
	if err != nil || item == nil {
		return err
	}
	// The popup drew this checkmark when it was built; a refresh can rewrite the
	// shared item while the popup is open. Restore what the user saw so the
	// dispatch toggles into the target they chose.
	if item.Type == mygo.MenuItemCheckbox {
		item.SetChecked(displayed)
	}
	t.dispatchMenuItem(item)
	return nil
}
