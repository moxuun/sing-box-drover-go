//go:build windows

package tray

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"sing-box-drover/internal/app"
	"sing-box-drover/internal/clash"
	"sing-box-drover/internal/core"
	platform "sing-box-drover/internal/windows"

	winapi "golang.org/x/sys/windows"
)

const (
	wmTrayCallback    = 0x8001
	wmClose           = 0x0010
	wmDestroy         = 0x0002
	wmNull            = 0x0000
	wmQueryEndSession = 0x0011
	wmEndSession      = 0x0016
	wmPowerBroadcast  = 0x0218

	pbtAPMResumeAutomatic = 0x0012

	csHRedraw      = 0x0002
	csVRedraw      = 0x0001
	wsExToolWindow = 0x00000080
	wsExNoActivate = 0x08000000

	nimAdd        = 0
	nimModify     = 1
	nimDelete     = 2
	nimSetVersion = 4
	nifMessage    = 1
	nifIcon       = 2
	nifTip        = 4
	nifInfo       = 16
	nifShowTip    = 0x80

	nimVersion4 = 4

	mfString    = 0x00000000
	mfSeparator = 0x00000800
	mfChecked   = 0x00000008
	mfDisabled  = 0x00000002
	mfGrayed    = 0x00000001
	mfPopup     = 0x00000010

	tpmReturnCmd = 0x0100
	tpmNonotify  = 0x0080

	vkShift         = 0x10
	cmdSystemProxy  = 100
	cmdTun          = 101
	cmdRestart      = 102
	cmdAutostart    = 103
	cmdHomepage     = 104
	cmdQuit         = 105
	cmdSelectorBase = 1000
)

type point struct{ X, Y int32 }

type msg struct {
	HWnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
	Private uint32
}

type wndClassEx struct {
	CbSize     uint32
	Style      uint32
	WndProc    uintptr
	CbClsExtra int32
	CbWndExtra int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSmall  uintptr
}

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	Flags            uint32
	CallbackMessage  uint32
	Icon             uintptr
	Tip              [128]uint16
	State            uint32
	StateMask        uint32
	Info             [256]uint16
	TimeoutOrVersion uint32
	InfoTitle        [64]uint16
	InfoFlags        uint32
	Guid             guid
	BalloonIcon      uintptr
}

var (
	user32              = winapi.NewLazySystemDLL("user32.dll")
	shell32             = winapi.NewLazySystemDLL("shell32.dll")
	kernel32            = winapi.NewLazySystemDLL("kernel32.dll")
	registerClassEx     = user32.NewProc("RegisterClassExW")
	unregisterClass     = user32.NewProc("UnregisterClassW")
	createWindowEx      = user32.NewProc("CreateWindowExW")
	destroyWindow       = user32.NewProc("DestroyWindow")
	defWindowProc       = user32.NewProc("DefWindowProcW")
	getMessage          = user32.NewProc("GetMessageW")
	translateMessage    = user32.NewProc("TranslateMessage")
	dispatchMessage     = user32.NewProc("DispatchMessageW")
	postMessage         = user32.NewProc("PostMessageW")
	postQuitMessage     = user32.NewProc("PostQuitMessage")
	setForegroundWindow = user32.NewProc("SetForegroundWindow")
	getCursorPos        = user32.NewProc("GetCursorPos")
	getKeyState         = user32.NewProc("GetKeyState")
	createPopupMenu     = user32.NewProc("CreatePopupMenu")
	appendMenu          = user32.NewProc("AppendMenuW")
	destroyMenu         = user32.NewProc("DestroyMenu")
	trackPopupMenu      = user32.NewProc("TrackPopupMenu")
	registerWindowMsg   = user32.NewProc("RegisterWindowMessageW")
	getModuleHandle     = kernel32.NewProc("GetModuleHandleW")
	shellNotifyIcon     = shell32.NewProc("Shell_NotifyIconW")
	shellExecute        = shell32.NewProc("ShellExecuteW")
)

var (
	traysMu sync.Mutex
	trays   = map[uintptr]*Tray{}
)

type selectorAction struct{ selector, value string }

type Tray struct {
	controller       *app.App
	hWnd             uintptr
	className        *uint16
	icon             uintptr
	selectors        map[uint32]selectorAction
	menuBitmaps      []uintptr
	notifyVersion4   bool
	taskbarCreated   uint32
	eventGate        trayEventGate
	autostartEnabled bool
	statusMu         sync.Mutex
	fault            bool
	iconMu           sync.Mutex
	resumeMu         sync.Mutex
	resumeRunning    bool
	done             chan struct{}
	closeOnce        sync.Once
	closeErr         error
}

func Run(controller *app.App) error {
	if controller == nil {
		return errors.New("controller is nil")
	}
	return runOnTrayThread(func() error { return run(controller) })
}

func runOnTrayThread(run func() error) error {
	// Win32 binds each HWND and its message queue to the creating OS thread.
	// Keep that thread for the whole window/message-loop lifetime; otherwise
	// the goroutine may migrate after CreateWindowEx and pump a different queue.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	return run()
}

func run(controller *app.App) (runErr error) {
	tray, err := newTray(controller)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, tray.close()) }()
	if controller.Options().SystemProxyAuto || controller.Flags().Proxy {
		if err := controller.EnableSystemProxy(); err != nil {
			tray.setFault(true)
			tray.balloon("System proxy could not be enabled: "+err.Error(), "Error", true)
		} else {
			_ = tray.refreshIcon()
		}
	}
	go tray.watchEvents()
	if state, err := controller.QueryAutostart(); err == nil {
		tray.autostartEnabled = state == platform.AutostartEnabled
	}
	for {
		var m msg
		ret, _, callErr := getMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(ret) == -1 {
			return fmt.Errorf("GetMessage: %w", callErr)
		}
		if ret == 0 {
			break
		}
		translateMessage.Call(uintptr(unsafe.Pointer(&m)))
		dispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
	return nil
}

func newTray(controller *app.App) (*Tray, error) {
	className, err := winapi.UTF16PtrFromString("sing-box-drover-tray-window")
	if err != nil {
		return nil, err
	}
	taskbarCreatedName, err := winapi.UTF16PtrFromString("TaskbarCreated")
	if err != nil {
		return nil, err
	}
	taskbarCreated, _, _ := registerWindowMsg.Call(uintptr(unsafe.Pointer(taskbarCreatedName)))
	if taskbarCreated == 0 {
		return nil, errors.New("RegisterWindowMessage(TaskbarCreated) failed")
	}
	instance, _, instanceErr := getModuleHandle.Call(0)
	if instance == 0 {
		return nil, instanceErr
	}
	t := &Tray{
		controller:     controller,
		className:      className,
		selectors:      map[uint32]selectorAction{},
		taskbarCreated: uint32(taskbarCreated),
		done:           make(chan struct{}),
	}
	callback := winapi.NewCallback(t.windowProc)
	class := wndClassEx{CbSize: uint32(unsafe.Sizeof(wndClassEx{})), Style: csHRedraw | csVRedraw, WndProc: callback, Instance: instance}
	class.ClassName = className
	if atom, _, registerErr := registerClassEx.Call(uintptr(unsafe.Pointer(&class))); atom == 0 && registerErr != winapi.ERROR_CLASS_ALREADY_EXISTS {
		return nil, fmt.Errorf("RegisterClassEx: %w", registerErr)
	}
	hWnd, _, createErr := createWindowEx.Call(wsExToolWindow|wsExNoActivate, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(className)), 0, 0, 0, 0, 0, 0, 0, instance, 0)
	if hWnd == 0 {
		return nil, fmt.Errorf("CreateWindowEx: %w", createErr)
	}
	t.hWnd = hWnd
	traysMu.Lock()
	trays[hWnd] = t
	traysMu.Unlock()
	t.icon, err = createTrayIcon(t.runtimeStatus().iconKind())
	if err != nil {
		return nil, errors.Join(err, t.close())
	}
	if err := t.installIcon(false); err != nil {
		return nil, errors.Join(err, t.close())
	}
	return t, nil
}

func (t *Tray) close() error {
	t.closeOnce.Do(func() {
		close(t.done)
		t.iconMu.Lock()
		if t.hWnd != 0 {
			data := t.dataLocked(nifMessage)
			_, _, _ = shellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&data)))
			traysMu.Lock()
			delete(trays, t.hWnd)
			traysMu.Unlock()
			_, _, _ = destroyWindow.Call(t.hWnd)
			t.hWnd = 0
		}
		destroyTrayIcon(t.icon)
		t.icon = 0
		t.iconMu.Unlock()
		if t.className != nil {
			instance, _, _ := getModuleHandle.Call(0)
			_, _, _ = unregisterClass.Call(uintptr(unsafe.Pointer(t.className)), instance)
		}
		t.releaseMenuBitmaps()
		if t.controller != nil {
			t.closeErr = t.controller.Close()
		}
	})
	return t.closeErr
}

func (t *Tray) windowProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	if shouldRestoreTrayIcon(message, wParam, t.taskbarCreated) {
		_ = t.installIcon(true)
		if message == wmPowerBroadcast {
			go t.recoverAfterResume()
			return 1
		}
		return 0
	}
	switch message {
	case wmTrayCallback:
		event := decodeTrayEvent(lParam, wParam, t.notifyVersion4, trayIconID)
		if t.eventGate.accept(event, time.Now()) {
			switch event.Kind {
			case trayEventActivate:
				t.handleTrayClick()
			case trayEventMenu:
				if event.HasPoint {
					anchor := point{X: event.X, Y: event.Y}
					t.showMenuAt(&anchor)
				} else {
					t.showMenu()
				}
			}
		}
	case wmQueryEndSession:
		_ = t.controller.Close()
		return 1
	case wmEndSession:
		if wParam != 0 {
			_ = t.controller.Close()
		}
	case wmClose:
		postQuitMessage.Call(0)
	case wmDestroy:
		postQuitMessage.Call(0)
	default:
		result, _, _ := defWindowProc.Call(hwnd, uintptr(message), wParam, lParam)
		return result
	}
	return 0
}

func shouldRestoreTrayIcon(message uint32, wParam uintptr, taskbarCreated uint32) bool {
	return taskbarCreated != 0 && message == taskbarCreated ||
		message == wmPowerBroadcast && uint32(wParam) == pbtAPMResumeAutomatic
}

func (t *Tray) recoverAfterResume() {
	if !t.beginResumeRecovery() {
		return
	}
	defer t.finishResumeRecovery()
	ctx, cancel := context.WithTimeout(context.Background(), app.ResumeRecoveryTimeout)
	err := t.controller.RecoverAfterResume(ctx)
	cancel()
	if err != nil {
		t.setFault(true)
		t.balloon("Resume recovery failed: "+err.Error(), "Error", true)
	}
}

func (t *Tray) beginResumeRecovery() bool {
	t.resumeMu.Lock()
	defer t.resumeMu.Unlock()
	if t.resumeRunning {
		return false
	}
	t.resumeRunning = true
	return true
}

func (t *Tray) finishResumeRecovery() {
	t.resumeMu.Lock()
	t.resumeRunning = false
	t.resumeMu.Unlock()
}

func (t *Tray) handleTrayClick() {
	shift, _, _ := getKeyState.Call(vkShift)
	if shift&0x8000 != 0 {
		if t.controller.HasTunInbound() {
			t.toggleTun(!t.controller.TunActive())
		}
		return
	}
	if !t.controller.TunActive() {
		t.toggleProxy(!t.controller.SystemProxyActive())
	}
}

func (t *Tray) toggleProxy(enabled bool) {
	var err error
	if enabled {
		err = t.controller.EnableSystemProxy()
	} else {
		err = t.controller.DisableSystemProxy()
	}
	if err != nil {
		t.setFault(true)
		t.balloon(err.Error(), "Error", true)
		return
	}
	t.setFault(false)
}

func (t *Tray) toggleTun(enabled bool) {
	if err := t.controller.ToggleTun(enabled); err != nil {
		if errors.Is(err, platform.ErrElevationRequired) {
			if launchErr := t.controller.LaunchElevated(enabled); launchErr == nil {
				_ = t.controller.Close()
				postQuitMessage.Call(0)
				return
			} else {
				t.setFault(true)
				t.balloon("Elevation was not accepted: "+launchErr.Error(), "Error", true)
				return
			}
		}
		t.setFault(true)
		t.balloon(err.Error(), "Error", true)
		return
	}
	t.setFault(false)
}

func (t *Tray) showMenu() {
	t.showMenuAt(nil)
}

func (t *Tray) showMenuAt(anchor *point) {
	selectors, err := t.controller.RefreshSelectors(context.Background())
	if err != nil && len(selectors) == 0 && strings.Contains(strings.ToLower(err.Error()), "not configured") == false {
		t.setFault(true)
		t.balloon("Selector refresh failed: "+err.Error(), "Error", true)
	}
	menu, err := t.buildMenu(selectors)
	if err != nil {
		t.setFault(true)
		t.balloon(err.Error(), "Error", true)
		return
	}
	defer func() {
		destroyMenu.Call(menu)
		t.releaseMenuBitmaps()
	}()
	var cursor point
	if anchor != nil {
		cursor = *anchor
	} else {
		getCursorPos.Call(uintptr(unsafe.Pointer(&cursor)))
	}
	setForegroundWindow.Call(t.hWnd)
	command, _, _ := trackPopupMenu.Call(menu, tpmReturnCmd|tpmNonotify, uintptr(cursor.X), uintptr(cursor.Y), 0, t.hWnd, 0)
	if command != 0 {
		t.handleCommand(uint32(command))
	}
	postMessage.Call(t.hWnd, wmNull, 0, 0)
}

func (t *Tray) buildMenu(selectors []clash.Selector) (uintptr, error) {
	menu, _, err := createPopupMenu.Call()
	if menu == 0 {
		return 0, fmt.Errorf("CreatePopupMenu: %w", err)
	}
	buildComplete := false
	defer func() {
		if !buildComplete {
			t.releaseMenuBitmaps()
		}
	}()
	if !useSharedCheckAndBitmapColumn(menu) {
		destroyMenu.Call(menu)
		return 0, errors.New("SetMenuInfo: unable to configure check mark and bitmap column")
	}
	t.selectors = map[uint32]selectorAction{}
	appendText := func(flags uint32, id uint32, text string) error {
		ptr, err := winapi.UTF16PtrFromString(text)
		if err != nil {
			return err
		}
		if ok, _, callErr := appendMenu.Call(menu, uintptr(flags), uintptr(id), uintptr(unsafe.Pointer(ptr))); ok == 0 {
			return callErr
		}
		return nil
	}
	proxyFlags := uint32(mfString)
	if t.controller.SystemProxyActive() {
		proxyFlags |= mfChecked
	}
	if err := appendText(proxyFlags, cmdSystemProxy, "System Proxy"); err != nil {
		destroyMenu.Call(menu)
		return 0, err
	}
	if t.controller.HasTunInbound() {
		tunFlags := uint32(mfString)
		if t.controller.TunActive() {
			tunFlags |= mfChecked
		}
		if err := appendText(tunFlags, cmdTun, "TUN"); err != nil {
			destroyMenu.Call(menu)
			return 0, err
		}
	}
	if len(selectors) > 0 {
		appendMenu.Call(menu, mfSeparator, 0, 0)
		nested := clash.UseNested(t.controller.Options().SelectorMenuLayout, selectors)
		id := uint32(cmdSelectorBase)
		bitmapCache := map[selectorBitmapKey]uintptr{}
		for _, selector := range selectors {
			if nested {
				submenu, _, _ := createPopupMenu.Call()
				if submenu == 0 {
					continue
				}
				if !useSharedCheckAndBitmapColumn(submenu) {
					destroyMenu.Call(submenu)
					destroyMenu.Call(menu)
					return 0, errors.New("SetMenuInfo: unable to configure submenu check mark and bitmap column")
				}
				for _, value := range selector.All {
					flags := uint32(mfString)
					if value == selector.Now {
						flags |= mfChecked
					}
					if err := t.appendSelectorItem(submenu, flags, id, selectorOptionText(selector, value), bitmapCache); err != nil {
						destroyMenu.Call(submenu)
						destroyMenu.Call(menu)
						return 0, err
					}
					t.selectors[id] = selectorAction{selector: selector.Name, value: value}
					id++
				}
				ptr, _ := winapi.UTF16PtrFromString(selector.Name)
				appendMenu.Call(menu, mfPopup, submenu, uintptr(unsafe.Pointer(ptr)))
			} else {
				ptr, _ := winapi.UTF16PtrFromString(selector.Name)
				appendMenu.Call(menu, mfString|mfDisabled|mfGrayed, 0, uintptr(unsafe.Pointer(ptr)))
				for _, value := range selector.All {
					flags := uint32(mfString)
					if value == selector.Now {
						flags |= mfChecked
					}
					if err := t.appendSelectorItem(menu, flags, id, selectorOptionText(selector, value), bitmapCache); err != nil {
						destroyMenu.Call(menu)
						return 0, err
					}
					t.selectors[id] = selectorAction{selector: selector.Name, value: value}
					id++
				}
				appendMenu.Call(menu, mfSeparator, 0, 0)
			}
		}
	}
	appendMenu.Call(menu, mfSeparator, 0, 0)
	if state, err := t.controller.QueryAutostart(); err == nil {
		t.autostartEnabled = state == platform.AutostartEnabled
	}
	autoFlags := uint32(mfString)
	if t.autostartEnabled {
		autoFlags |= mfChecked
	}
	appendText(autoFlags, cmdAutostart, "Start with Windows")
	appendText(mfString, cmdRestart, "Restart core")
	appendText(mfString, cmdHomepage, "Homepage")
	appendMenu.Call(menu, mfSeparator, 0, 0)
	appendText(mfString, cmdQuit, "Quit")
	buildComplete = true
	return menu, nil
}

func selectorOptionText(selector clash.Selector, value string) string {
	if value == selector.Now && selector.ResolvedNow != "" && selector.ResolvedNow != value {
		return value + "（当前：" + selector.ResolvedNow + "）"
	}
	return value
}

func (t *Tray) handleCommand(command uint32) {
	switch command {
	case cmdSystemProxy:
		t.toggleProxy(!t.controller.SystemProxyActive())
	case cmdTun:
		t.toggleTun(!t.controller.TunActive())
	case cmdRestart:
		if err := t.controller.Restart(); err != nil {
			t.setFault(true)
			t.balloon(err.Error(), "Error", true)
		} else {
			_ = t.controller.Close()
			postQuitMessage.Call(0)
		}
	case cmdAutostart:
		if err := t.controller.SetAutostart(!t.autostartEnabled); err != nil {
			if errors.Is(err, platform.ErrElevationRequired) {
				if launchErr := t.controller.LaunchAutostartElevated(!t.autostartEnabled); launchErr == nil {
					_ = t.controller.Close()
					postQuitMessage.Call(0)
					return
				} else {
					t.setFault(true)
					t.balloon("Elevation was not accepted: "+launchErr.Error(), "Error", true)
					return
				}
			}
			t.setFault(true)
			t.balloon(err.Error(), "Error", true)
		} else {
			t.autostartEnabled = !t.autostartEnabled
			t.setFault(false)
		}
	case cmdHomepage:
		openURL(t.controller.Options().HomepageURL)
	case cmdQuit:
		postQuitMessage.Call(0)
	default:
		if action, ok := t.selectors[command]; ok {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			err := t.controller.SwitchSelector(ctx, action.selector, action.value)
			cancel()
			if err != nil {
				t.setFault(true)
				t.balloon(err.Error(), "Error", true)
			} else {
				t.setFault(false)
			}
		}
	}
}

func (t *Tray) runtimeStatus() trayRuntimeStatus {
	t.statusMu.Lock()
	fault := t.fault
	t.statusMu.Unlock()
	return trayRuntimeStatus{
		coreState:   t.controller.CoreState(),
		systemProxy: t.controller.SystemProxyActive(),
		tun:         t.controller.TunActive(),
		fault:       fault,
	}
}

func (t *Tray) setFault(fault bool) {
	t.statusMu.Lock()
	t.fault = fault
	t.statusMu.Unlock()
	_ = t.refreshIcon()
}

func (t *Tray) refreshIcon() error {
	t.iconMu.Lock()
	defer t.iconMu.Unlock()
	select {
	case <-t.done:
		return nil
	default:
	}
	icon, err := createTrayIcon(t.runtimeStatus().iconKind())
	if err != nil {
		return err
	}
	old := t.icon
	t.icon = icon
	if t.hWnd != 0 {
		if err := t.notifyLocked(nimModify); err != nil {
			t.icon = old
			destroyTrayIcon(icon)
			return err
		}
	}
	destroyTrayIcon(old)
	return nil
}

func (t *Tray) data(flags uint32) notifyIconData {
	t.iconMu.Lock()
	defer t.iconMu.Unlock()
	return t.dataLocked(flags)
}

func (t *Tray) dataLocked(flags uint32) notifyIconData {
	data := notifyIconData{CbSize: uint32(unsafe.Sizeof(notifyIconData{})), HWnd: t.hWnd, UID: 1, Flags: flags, CallbackMessage: wmTrayCallback, Icon: t.icon}
	tip, _ := winapi.UTF16FromString(t.runtimeStatus().tooltip())
	copy(data.Tip[:], tip)
	return data
}

func (t *Tray) installIcon(replace bool) error {
	t.iconMu.Lock()
	defer t.iconMu.Unlock()
	select {
	case <-t.done:
		return nil
	default:
	}
	if t.hWnd == 0 {
		return nil
	}
	if replace {
		deleteData := t.dataLocked(nifMessage)
		_, _, _ = shellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&deleteData)))
	}
	if err := t.notifyLocked(nimAdd); err != nil {
		return err
	}
	versionData := t.dataLocked(nifMessage | nifShowTip)
	versionData.TimeoutOrVersion = nimVersion4
	result, _, _ := shellNotifyIcon.Call(nimSetVersion, uintptr(unsafe.Pointer(&versionData)))
	t.notifyVersion4 = result != 0
	return nil
}

func (t *Tray) notifyLocked(operation uint32) error {
	data := t.dataLocked(nifMessage | nifIcon | nifTip | nifShowTip)
	if ok, _, err := shellNotifyIcon.Call(uintptr(operation), uintptr(unsafe.Pointer(&data))); ok == 0 {
		return fmt.Errorf("Shell_NotifyIcon: %w", err)
	}
	return nil
}

func (t *Tray) balloon(text, title string, isError bool) {
	t.iconMu.Lock()
	defer t.iconMu.Unlock()
	if t.hWnd == 0 {
		return
	}
	data := t.dataLocked(nifInfo)
	copy(data.Info[:], mustUTF16(text, len(data.Info)))
	copy(data.InfoTitle[:], mustUTF16(title, len(data.InfoTitle)))
	if isError {
		data.InfoFlags = 3
	} else {
		data.InfoFlags = 1
	}
	_, _, _ = shellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&data)))
}

func mustUTF16(value string, max int) []uint16 {
	value = strings.ReplaceAll(value, "\x00", " ")
	v, _ := winapi.UTF16FromString(value)
	if max <= 0 {
		return nil
	}
	if len(v) >= max {
		v = v[:max-1]
		v = append(v, 0)
	}
	return v
}

func openURL(value string) {
	p, _ := winapi.UTF16PtrFromString("open")
	u, _ := winapi.UTF16PtrFromString(value)
	shellExecute.Call(0, uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(u)), 0, 0, 1)
}

func (t *Tray) watchEvents() {
	for {
		select {
		case <-t.done:
			return
		case event := <-t.controller.Events():
			if event.State == core.StateRunning {
				t.setFault(false)
			} else if event.Kind == core.EventError || event.State == core.StateFailed {
				t.setFault(true)
			} else {
				_ = t.refreshIcon()
			}
			if event.Kind == core.EventError || event.State == core.StateFailed {
				t.balloon(event.Message, "sing-box-drover", true)
			}
		}
	}
}
