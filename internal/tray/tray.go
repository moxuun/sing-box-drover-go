package tray

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"sing-box-drover/internal/app"
	"sing-box-drover/internal/clash"
	"sing-box-drover/internal/core"
	platform "sing-box-drover/internal/windows"
)

//go:embed assets/status_plain.png
var plainStatusIcon []byte

//go:embed assets/status_green.png
var greenStatusIcon []byte

//go:embed assets/status_red.png
var redStatusIcon []byte

type selectorMenuItem struct {
	item   *mygo.MenuItem
	action selectorAction
	text   string
}

type Tray struct {
	controller *app.App
	native     *mygo.Tray
	menu       *mygo.Menu

	// proxyToggle and tunToggle are the controller calls behind the System Proxy
	// and TUN menu items. Tests replace them to record the requested target
	// without a running core; nil uses the controller directly.
	proxyToggle func(bool)
	tunToggle   func(bool)

	// Menu and icon state belongs to the mygo main thread.
	selectorItems    []selectorMenuItem
	proxyItem        *mygo.MenuItem
	tunItem          *mygo.MenuItem
	autostartItem    *mygo.MenuItem
	menuFingerprint  string
	autostartEnabled bool
	// fault is the tray-level error shown by the icon: either the core failed or
	// the menu could not be built. coreFault remembers which of the two it is, so
	// a recovered menu does not clear an error the core is still in.
	fault     bool
	coreFault bool
	iconKind  trayIconKind
	tooltip   string
	menuError string
	runErr    error

	// Controller actions run serially, without blocking native callbacks.
	actions       chan func()
	done          chan struct{}
	workerDone    chan struct{}
	stopped       chan struct{}
	closeOnce     sync.Once
	stoppedOnce   sync.Once
	cleanupMu     sync.Mutex
	closing       bool
	closeErr      error
	resumeMu      sync.Mutex
	resumeRunning bool
}

func Run(controller *app.App) error {
	if controller == nil {
		return errors.New("controller is nil")
	}
	t := &Tray{
		controller: controller,
		actions:    make(chan func(), 16),
		done:       make(chan struct{}),
		workerDone: make(chan struct{}),
		stopped:    make(chan struct{}),
	}
	go t.work()
	mygo.App.SetName("sing-box-drover")
	mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory)
	mygo.App.OnWindowAllClosed(func() {})
	mygo.App.OnBeforeQuit(func(event *mygo.QuitEvent) {
		select {
		case <-t.stopped:
			return
		default:
			event.PreventDefault()
			t.shutdown()
		}
	})
	mygo.App.OnQuit(t.nativeQuit)
	mygo.App.WhenReady(t.onReady)
	err := mygo.App.Run()
	if err != nil {
		err = t.controller.AbortHandoff(err)
	}
	// Init failures and native termination also clean up when BeforeQuit was skipped.
	t.nativeQuit()
	t.cleanupMu.Lock()
	defer t.cleanupMu.Unlock()
	return errors.Join(err, t.runErr, t.closeErr)
}

func (t *Tray) createNativeTray() error {
	var err error
	t.native, err = mygo.NewTray(mygo.TrayOptions{
		Icon:           plainStatusIcon,
		IconIsTemplate: runtime.GOOS == "darwin",
	})
	if err != nil {
		return err
	}
	t.menu = nil
	t.iconKind = trayIconPlain
	t.tooltip = ""
	if err := t.rebuildMenu(t.controller.Selectors()); err != nil {
		return err
	}
	t.refreshIcon()
	if runtime.GOOS == "windows" {
		t.native.OnClick(func() {
			shift := shiftPressed()
			// Read the state the user acted on now, not when the queued action
			// runs, so a core restart in between cannot invert the request.
			hasTun := t.controller.HasTunInbound()
			tunActive := t.controller.TunActive()
			proxyActive := t.controller.SystemProxyActive()
			t.enqueue(func() {
				if shift {
					if hasTun {
						t.toggleTun(!tunActive)
					}
				} else if !tunActive {
					t.toggleProxy(!proxyActive)
				}
			})
		})
	}
	t.native.OnRightClick(func() {
		t.syncMenuChecks()
		t.enqueue(t.refreshAutostart)
		if err := t.showMenu(); err != nil {
			t.report(err)
		}
	})
	return nil
}

func (t *Tray) onReady() {
	if err := t.createNativeTray(); err != nil {
		t.startupFailed(err)
		return
	}
	mygo.Power.OnResume(func() {
		if !t.beginResumeRecovery() {
			return
		}
		if !t.enqueue(func() {
			defer t.finishResumeRecovery()
			ctx, cancel := context.WithTimeout(context.Background(), app.ResumeRecoveryTimeout)
			defer cancel()
			t.report(t.controller.RecoverAfterResume(ctx))
		}) {
			t.finishResumeRecovery()
		}
	})
	t.enqueue(func() {
		// The native tray and menu exist before the parent is told to retire.
		if err := t.controller.ConfirmHandoff(); err != nil {
			t.startupFailed(err)
			return
		}
		t.refreshAutostart()
		if t.controller.StartupProxyRequested() {
			t.toggleProxy(true)
		}
		go t.watchEvents()
		go t.watchSelectorCache()
	})
}

func (t *Tray) startupFailed(err error) {
	t.cleanupMu.Lock()
	t.runErr = err
	t.cleanupMu.Unlock()
	t.shutdown()
}

func (t *Tray) work() {
	defer close(t.workerDone)
	for {
		select {
		case <-t.done:
			return
		case action := <-t.actions:
			select {
			case <-t.done:
				return
			default:
				action()
			}
		}
	}
}

func (t *Tray) enqueue(action func()) bool {
	select {
	case <-t.done:
		return false
	default:
	}
	select {
	case <-t.done:
		return false
	case t.actions <- action:
		return true
	default:
		t.onMain(func() { t.syncMenuChecks() })
		t.notify("Too many pending operations; try again after the current operation finishes")
		return false
	}
}

func (t *Tray) shutdown() {
	t.closeOnce.Do(func() { close(t.done) })
	t.cleanupMu.Lock()
	select {
	case <-t.stopped:
		t.cleanupMu.Unlock()
		return
	default:
	}
	if t.closing {
		t.cleanupMu.Unlock()
		return
	}
	t.closing = true
	t.cleanupMu.Unlock()
	go func() {
		<-t.workerDone
		t.cleanupMu.Lock()
		startupErr := t.runErr
		t.cleanupMu.Unlock()
		if startupErr != nil {
			reported := t.controller.AbortHandoff(startupErr)
			t.cleanupMu.Lock()
			t.runErr = reported
			t.cleanupMu.Unlock()
		}
		err := t.closeController(t.controller.Close)
		if err == nil || startupErr != nil {
			t.stoppedOnce.Do(func() { close(t.stopped) })
			mygo.App.Quit()
			return
		}
		t.cleanupMu.Lock()
		t.closing = false
		t.cleanupMu.Unlock()
		mygo.RunOnMain(func() {
			t.fault = true
			t.syncMenuChecks()
			t.refreshIcon()
		})
		t.notify("Shutdown failed; choose Quit to retry:\n" + err.Error())
	}()
}

func (t *Tray) closeController(closeController func() error) error {
	t.cleanupMu.Lock()
	defer t.cleanupMu.Unlock()
	select {
	case <-t.stopped:
		return t.closeErr
	default:
	}
	t.closeErr = closeController()
	if t.closeErr == nil {
		t.stoppedOnce.Do(func() { close(t.stopped) })
	}
	return t.closeErr
}

func (t *Tray) nativeQuit() {
	// Native termination cannot wait for a worker blocked on this main thread.
	t.closeOnce.Do(func() { close(t.done) })
	_ = t.closeController(t.controller.Close)
	t.stoppedOnce.Do(func() { close(t.stopped) })
	if t.native != nil {
		t.native.Destroy()
		t.native = nil
	}
}

func (t *Tray) onMain(action func()) {
	select {
	case <-t.done:
		return
	default:
	}
	mygo.RunOnMain(func() {
		select {
		case <-t.done:
			return
		default:
			action()
		}
	})
}

func validMenuText(text string) bool {
	return utf8.ValidString(text) && !strings.ContainsRune(text, '\x00')
}

// dispatchMenuItem is also used by the Windows bitmap popup. Quit stays
// available after failed cleanup; other operations cannot enter a closed worker.
func (t *Tray) dispatchMenuItem(item *mygo.MenuItem) {
	select {
	case <-t.done:
		if item.ID != "quit" {
			return
		}
	default:
	}
	// Match MyGo's checked-state update before Click.
	switch item.Type {
	case mygo.MenuItemCheckbox:
		item.SetChecked(!item.IsChecked())
	case mygo.MenuItemRadio:
		item.SetChecked(true)
	}
	if item.Click != nil {
		item.Click(item, nil)
	}
}

func (t *Tray) buildMenu(selectors []clash.Selector) (*mygo.Menu, error) {
	// Validate before allocating menu items: native C/UTF-16 APIs truncate NUL.
	for _, selector := range selectors {
		if !validMenuText(selector.Name) {
			return nil, errors.New("invalid selector group text")
		}
		for _, value := range selector.All {
			if !validMenuText(selectorOptionText(selector, value)) {
				return nil, errors.New("invalid selector item text")
			}
		}
	}
	t.selectorItems = nil
	t.proxyItem = &mygo.MenuItem{
		ID: "proxy", Label: "System Proxy", Type: mygo.MenuItemCheckbox,
		Checked: t.controller.SystemProxyActive(),
		Click: func(item *mygo.MenuItem, _ *mygo.Window) {
			// Capture the target the user asked for. Recomputing it when the
			// queued action runs would read state a core restart has changed in
			// between: a TUN restart restores the system proxy first, so a queued
			// "off" would turn it back on.
			enabled := item.IsChecked()
			t.enqueue(func() { t.toggleProxy(enabled) })
		},
	}
	items := []*mygo.MenuItem{t.proxyItem}
	t.tunItem = nil
	if t.controller.HasTunInbound() {
		t.tunItem = &mygo.MenuItem{
			ID: "tun", Label: "TUN", Type: mygo.MenuItemCheckbox, Checked: t.controller.TunActive(),
			Click: func(item *mygo.MenuItem, _ *mygo.Window) {
				// Capture the target the user asked for: tunActive only updates after
				// a restart finishes, so a queued "off" recomputed later would still
				// see the old value and turn TUN back on.
				enabled := item.IsChecked()
				t.enqueue(func() { t.toggleTun(enabled) })
			},
		}
		items = append(items, t.tunItem)
	}
	if len(selectors) > 0 {
		items = append(items, mygo.Separator())
		nested := clash.UseNested(t.controller.Options().SelectorMenuLayout, selectors)
		for groupIndex, selector := range selectors {
			group := &mygo.MenuItem{Label: selector.Name, Disabled: !nested}
			items = append(items, group)
			for optionIndex, value := range selector.All {
				action := selectorAction{selector: selector.Name, value: value}
				text := selectorOptionText(selector, value)
				item := &mygo.MenuItem{
					ID:    fmt.Sprintf("selector:%d:%d", groupIndex, optionIndex),
					Label: text, Type: mygo.MenuItemCheckbox, Checked: value == selector.Now,
					Click: func(_ *mygo.MenuItem, _ *mygo.Window) {
						t.enqueue(func() { t.switchSelector(action) })
					},
				}
				t.selectorItems = append(t.selectorItems, selectorMenuItem{item: item, action: action, text: text})
				if nested {
					group.Submenu = append(group.Submenu, item)
				} else {
					items = append(items, item)
				}
			}
			if !nested {
				items = append(items, mygo.Separator())
			}
		}
	}
	labels := map[string]string{"windows": "Windows", "darwin": "macOS", "linux": "Linux"}
	t.autostartItem = &mygo.MenuItem{
		ID: "autostart", Label: "Start with " + labels[runtime.GOOS], Type: mygo.MenuItemCheckbox,
		Checked:  t.autostartEnabled,
		Disabled: runtime.GOOS == "darwin" && platform.IsProcessElevated(),
		Click: func(item *mygo.MenuItem, _ *mygo.Window) {
			enabled := item.IsChecked()
			t.enqueue(func() { t.toggleAutostart(enabled) })
		},
	}
	items = append(items,
		mygo.Separator(), t.autostartItem,
		&mygo.MenuItem{ID: "restart", Label: "Restart core", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { t.enqueue(t.restart) }},
		&mygo.MenuItem{ID: "homepage", Label: "Homepage", Click: func(_ *mygo.MenuItem, _ *mygo.Window) {
			t.enqueue(func() { t.report(openURL(t.controller.Options().HomepageURL)) })
		}},
		mygo.Separator(),
		&mygo.MenuItem{ID: "quit", Label: "Quit", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { t.shutdown() }},
	)
	return mygo.NewMenu(items), nil
}

func (t *Tray) rebuildMenu(selectors []clash.Selector) error {
	fingerprint := selectorFingerprint(selectors, t.controller.Options().SelectorMenuLayout) + fmt.Sprint(t.controller.HasTunInbound())
	if t.menu != nil && t.menuFingerprint == fingerprint {
		t.syncMenuChecks()
		return nil
	}
	menu, err := t.buildMenu(selectors)
	if err != nil {
		return err
	}
	t.menu = menu
	t.menuFingerprint = fingerprint
	if runtime.GOOS != "windows" {
		t.native.SetMenu(menu)
	}
	return nil
}

func selectorFingerprint(selectors []clash.Selector, layout string) string {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(layout))
	for _, selector := range selectors {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(selector.Name))
		for _, value := range selector.All {
			_, _ = hash.Write([]byte{0})
			_, _ = hash.Write([]byte(value))
		}
	}
	return fmt.Sprintf("%x", hash.Sum64())
}

func setMenuChecked(item *mygo.MenuItem, checked bool) {
	if item != nil && item.IsChecked() != checked {
		item.SetChecked(checked)
	}
}

func (t *Tray) syncMenuChecks() {
	setMenuChecked(t.proxyItem, t.controller.SystemProxyActive())
	setMenuChecked(t.tunItem, t.controller.TunActive())
	setMenuChecked(t.autostartItem, t.autostartEnabled)
	for _, selector := range t.controller.Selectors() {
		for i := range t.selectorItems {
			item := &t.selectorItems[i]
			if item.action.selector != selector.Name {
				continue
			}
			text := selectorOptionText(selector, item.action.value)
			if text != item.text && validMenuText(text) {
				item.item.SetLabel(text)
				item.text = text
			}
			setMenuChecked(item.item, item.action.value == selector.Now)
		}
	}
}

func (t *Tray) report(err error) {
	t.onMain(func() {
		t.fault = err != nil
		t.syncMenuChecks()
		t.refreshIcon()
	})
	if err != nil {
		t.notify(err.Error())
	}
}

func (t *Tray) switchSelector(action selectorAction) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	t.report(t.controller.SwitchSelector(ctx, action.selector, action.value))
}

func (t *Tray) toggleProxy(enabled bool) {
	if t.proxyToggle != nil {
		t.proxyToggle(enabled)
		return
	}
	var err error
	if enabled {
		err = t.controller.EnableSystemProxy()
		if errors.Is(err, platform.ErrElevationRequired) {
			if err = t.controller.LaunchSystemProxyElevated(); err == nil {
				t.shutdown()
				return
			}
		}
	} else {
		err = t.controller.DisableSystemProxy()
	}
	t.report(err)
}

func (t *Tray) toggleTun(enabled bool) {
	if t.tunToggle != nil {
		t.tunToggle(enabled)
		return
	}
	err := t.controller.ToggleTun(enabled)
	if errors.Is(err, platform.ErrElevationRequired) {
		if err = t.controller.LaunchElevated(enabled); err == nil {
			t.shutdown()
			return
		}
	}
	t.report(err)
}

func (t *Tray) toggleAutostart(enabled bool) {
	err := t.controller.SetAutostart(enabled)
	if errors.Is(err, platform.ErrElevationRequired) {
		// The Windows helper changes only the logon task; keep this tray alive.
		err = t.controller.LaunchAutostartElevated(enabled)
	} else if err == nil {
		t.onMain(func() { t.autostartEnabled = enabled })
	}
	t.report(err)
}

func (t *Tray) refreshAutostart() {
	state, err := t.controller.QueryAutostart()
	if err == nil {
		t.onMain(func() {
			t.autostartEnabled = state == platform.AutostartEnabled
			t.syncMenuChecks()
		})
	}
}

func (t *Tray) restart() {
	// The replacement creates its tray before confirming the handoff.
	t.onMain(func() {
		if t.native != nil {
			t.native.Destroy()
			t.native = nil
		}
	})
	if err := t.controller.Restart(); err != nil {
		var trayErr error
		t.onMain(func() { trayErr = t.createNativeTray() })
		if trayErr != nil {
			t.startupFailed(errors.Join(err, trayErr))
			return
		}
		t.report(err)
		return
	}
	t.shutdown()
}

func openURL(value string) error {
	if !validMenuText(value) {
		return errors.New("invalid URL text")
	}
	return mygo.Shell.OpenExternal(value)
}

func (t *Tray) runtimeStatus() trayRuntimeStatus {
	return trayRuntimeStatus{
		coreState:   t.controller.CoreState(),
		systemProxy: t.controller.SystemProxyActive(),
		tun:         t.controller.TunActive(),
		fault:       t.fault,
	}
}

func (t *Tray) refreshIcon() {
	if t.native == nil {
		return
	}
	status := t.runtimeStatus()
	kind := status.iconKind()
	if t.iconKind != kind {
		icons := map[trayIconKind][]byte{trayIconPlain: plainStatusIcon, trayIconGreen: greenStatusIcon, trayIconRed: redStatusIcon}
		if err := t.native.SetIcon(icons[kind], runtime.GOOS == "darwin" && kind == trayIconPlain); err != nil {
			fmt.Fprintln(os.Stderr, "tray icon:", err)
		} else {
			t.iconKind = kind
		}
	}
	if tooltip := status.tooltip(); tooltip != t.tooltip {
		t.native.SetToolTip(tooltip)
		t.tooltip = tooltip
	}
}

func (t *Tray) notify(message string) {
	go func() {
		select {
		case <-t.stopped:
			return
		default:
		}
		if err := mygo.NewNotification(mygo.NotificationOptions{
			Title: "sing-box-drover", Body: strings.ReplaceAll(message, "\x00", " "),
		}).Show(); err != nil {
			fmt.Fprintln(os.Stderr, "notification:", err, message)
		}
	}()
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

func (t *Tray) watchEvents() {
	for {
		select {
		case <-t.done:
			return
		case event, ok := <-t.controller.Events():
			if !ok {
				return
			}
			t.onMain(func() {
				if event.State == core.StateRunning {
					t.coreFault = false
					t.fault = false
				} else if event.Kind == core.EventError || event.State == core.StateFailed {
					t.coreFault = true
					t.fault = true
				}
				t.refreshIcon()
			})
			if event.Kind == core.EventError || event.State == core.StateFailed {
				t.notify(event.Message)
			}
		}
	}
}

func (t *Tray) publishMenu(selectors []clash.Selector) {
	if t.native == nil {
		return
	}
	if err := t.rebuildMenu(selectors); err != nil {
		t.fault = true
		t.refreshIcon()
		if message := err.Error(); message != t.menuError {
			t.menuError = message
			t.notify(message)
		}
		return
	}
	t.menuError = ""
	// A menu that builds again means the condition that turned the icon red is
	// gone. Leaving fault set would keep reporting an error the user already
	// recovered from, until the next core restart happened to clear it.
	if t.fault && !t.coreFault {
		t.fault = false
		t.refreshIcon()
	}
}

func (t *Tray) watchSelectorCache() {
	cacheTicker := time.NewTicker(750 * time.Millisecond)
	refreshTicker := time.NewTicker(30 * time.Second)
	defer cacheTicker.Stop()
	defer refreshTicker.Stop()
	for {
		select {
		case <-t.done:
			return
		case <-cacheTicker.C:
			t.onMain(func() { t.publishMenu(t.controller.Selectors()) })
		case <-refreshTicker.C:
			t.enqueue(t.refreshSelectorsOnce)
		}
	}
}

func (t *Tray) refreshSelectorsOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	selectors, err := t.controller.RefreshSelectors(ctx)
	if err != nil {
		return
	}
	t.onMain(func() { t.publishMenu(selectors) })
}
