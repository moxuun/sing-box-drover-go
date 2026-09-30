//go:build darwin

package tray

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"fyne.io/systray"
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

const notificationScript = `on run argv
display notification (item 2 of argv) with title (item 1 of argv)
end run`

type selectorMenuItem struct {
	item   *systray.MenuItem
	action selectorAction
	text   string
}

type Tray struct {
	controller       *app.App
	done             chan struct{}
	closeOnce        sync.Once
	closeErr         error
	menuMu           sync.Mutex
	selectorItems    []selectorMenuItem
	proxyItem        *systray.MenuItem
	tunItem          *systray.MenuItem
	autostartItem    *systray.MenuItem
	menuFingerprint  string
	statusMu         sync.Mutex
	fault            bool
	autostartEnabled bool
	iconMu           sync.Mutex
	resumeMu         sync.Mutex
	resumeRunning    bool
}

func Run(controller *app.App) error {
	if controller == nil {
		return errors.New("controller is nil")
	}
	tray := &Tray{controller: controller, done: make(chan struct{})}
	systray.Run(tray.onReady, tray.onExit)
	// fyne.io/systray's macOS Quit path stops NSApp without necessarily
	// invoking applicationWillTerminate, so do not rely solely on onExit for
	// proxy/core cleanup.
	tray.shutdown()
	return tray.closeErr
}

func (t *Tray) onReady() {
	systray.SetRemovalAllowed(false)
	t.refreshIcon()
	if state, err := t.controller.QueryAutostart(); err == nil {
		t.autostartEnabled = state == platform.AutostartEnabled
	}
	t.rebuildMenu(t.controller.Selectors())
	if t.controller.Options().SystemProxyAuto || t.controller.Flags().Proxy {
		if err := t.controller.EnableSystemProxy(); err != nil {
			t.setFault(true)
			t.notify("sing-box-drover", "System proxy could not be enabled: "+err.Error())
		} else {
			t.setFault(false)
		}
	}
	go t.watchEvents()
	go t.watchMenuOpened()
	go t.watchSelectorCache()
	go t.watchSignals()
	watchSystemWake(t.recoverAfterResume)
}

func (t *Tray) onExit() {
	t.shutdown()
}

func (t *Tray) shutdown() {
	t.closeOnce.Do(func() {
		close(t.done)
		t.closeErr = t.controller.Close()
	})
}

func (t *Tray) watchClicks(item *systray.MenuItem, action func()) {
	go func() {
		for {
			select {
			case <-t.done:
				return
			case _, ok := <-item.ClickedCh:
				if !ok {
					return
				}
				action()
			}
		}
	}()
}

func (t *Tray) rebuildMenu(selectors []clash.Selector) {
	t.menuMu.Lock()
	defer t.menuMu.Unlock()
	fingerprint := selectorFingerprint(selectors, t.controller.Options().SelectorMenuLayout)
	if fingerprint == t.menuFingerprint && t.proxyItem != nil {
		t.syncMenuChecksLocked()
		return
	}
	systray.ResetMenu()
	t.selectorItems = nil

	checked := t.controller.SystemProxyActive()
	t.proxyItem = systray.AddMenuItemCheckbox("System Proxy", "Enable or disable the macOS system proxy", checked)
	t.watchClicks(t.proxyItem, func() { t.toggleProxy(!t.controller.SystemProxyActive()) })
	if t.controller.HasTunInbound() {
		t.tunItem = systray.AddMenuItemCheckbox("TUN", "Enable or disable the TUN inbound", t.controller.TunActive())
		t.watchClicks(t.tunItem, func() { t.toggleTun(!t.controller.TunActive()) })
	} else {
		t.tunItem = nil
	}
	if len(selectors) > 0 {
		systray.AddSeparator()
		nested := clash.UseNested(t.controller.Options().SelectorMenuLayout, selectors)
		for _, selector := range selectors {
			if nested {
				parent := systray.AddMenuItem(selector.Name, "Select an outbound")
				for _, value := range selector.All {
					text := selectorOptionText(selector, value)
					item := parent.AddSubMenuItemCheckbox(text, value, value == selector.Now)
					action := selectorAction{selector: selector.Name, value: value}
					t.selectorItems = append(t.selectorItems, selectorMenuItem{item: item, action: action, text: text})
					t.watchClicks(item, func() { t.switchSelector(action) })
				}
				continue
			}
			group := systray.AddMenuItem(selector.Name, "Available outbounds")
			group.Disable()
			for _, value := range selector.All {
				text := selectorOptionText(selector, value)
				item := systray.AddMenuItemCheckbox(text, value, value == selector.Now)
				action := selectorAction{selector: selector.Name, value: value}
				t.selectorItems = append(t.selectorItems, selectorMenuItem{item: item, action: action, text: text})
				t.watchClicks(item, func() { t.switchSelector(action) })
			}
			systray.AddSeparator()
		}
	}
	systray.AddSeparator()
	if state, err := t.controller.QueryAutostart(); err == nil {
		t.autostartEnabled = state == platform.AutostartEnabled
	}
	t.autostartItem = systray.AddMenuItemCheckbox("Start with macOS", "Start sing-box-drover when you log in", t.autostartEnabled)
	t.watchClicks(t.autostartItem, func() { t.toggleAutostart(!t.autostartEnabled) })
	if platform.IsProcessElevated() {
		t.autostartItem.Disable()
		t.autostartItem.SetTooltip("Set login startup before enabling TUN or the system proxy")
	}

	restartItem := systray.AddMenuItem("Restart core", "Restart the sing-box process")
	t.watchClicks(restartItem, t.restart)
	homepageItem := systray.AddMenuItem("Homepage", "Open the configured homepage")
	t.watchClicks(homepageItem, t.openHomepage)
	systray.AddSeparator()
	quitItem := systray.AddMenuItem("Quit", "Quit sing-box-drover")
	t.watchClicks(quitItem, func() { t.shutdown(); systray.Quit() })

	t.menuFingerprint = fingerprint
	t.syncMenuChecksLocked()
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

func (t *Tray) syncMenuChecks() {
	t.menuMu.Lock()
	defer t.menuMu.Unlock()
	t.syncMenuChecksLocked()
}

func (t *Tray) syncMenuChecksLocked() {
	if t.proxyItem != nil {
		if t.controller.SystemProxyActive() {
			t.proxyItem.Check()
		} else {
			t.proxyItem.Uncheck()
		}
	}
	if t.tunItem != nil {
		if t.controller.TunActive() {
			t.tunItem.Check()
		} else {
			t.tunItem.Uncheck()
		}
	}
	if t.autostartItem != nil {
		if t.autostartEnabled {
			t.autostartItem.Check()
		} else {
			t.autostartItem.Uncheck()
		}
	}
	selectors := t.controller.Selectors()
	for _, selector := range selectors {
		for _, item := range t.selectorItems {
			if item.action.selector != selector.Name {
				continue
			}
			text := selectorOptionText(selector, item.action.value)
			if item.text != text {
				item.item.SetTitle(text)
				item.text = text
			}
			if item.action.value == selector.Now {
				item.item.Check()
			} else {
				item.item.Uncheck()
			}
		}
	}
}

func (t *Tray) switchSelector(action selectorAction) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	err := t.controller.SwitchSelector(ctx, action.selector, action.value)
	cancel()
	if err != nil {
		t.setFault(true)
		t.notify("sing-box-drover", err.Error())
		return
	}
	t.setFault(false)
	t.syncMenuChecks()
}

func (t *Tray) toggleProxy(enabled bool) {
	var err error
	if enabled {
		err = t.controller.EnableSystemProxy()
		if errors.Is(err, platform.ErrElevationRequired) {
			if launchErr := t.controller.LaunchSystemProxyElevated(); launchErr != nil {
				t.setFault(true)
				t.notify("sing-box-drover", "Elevation was not accepted: "+launchErr.Error())
				return
			}
			t.shutdown()
			systray.Quit()
			return
		}
	} else {
		err = t.controller.DisableSystemProxy()
	}
	if err != nil {
		t.setFault(true)
		t.notify("sing-box-drover", err.Error())
		return
	}
	t.setFault(false)
	t.syncMenuChecks()
}

func (t *Tray) toggleTun(enabled bool) {
	if err := t.controller.ToggleTun(enabled); err != nil {
		if errors.Is(err, platform.ErrElevationRequired) {
			if launchErr := t.controller.LaunchElevated(enabled); launchErr != nil {
				t.setFault(true)
				t.notify("sing-box-drover", "Elevation was not accepted: "+launchErr.Error())
				return
			}
			t.shutdown()
			systray.Quit()
			return
		}
		t.setFault(true)
		t.notify("sing-box-drover", err.Error())
		return
	}
	t.setFault(false)
	t.syncMenuChecks()
}

func (t *Tray) toggleAutostart(enabled bool) {
	if err := t.controller.SetAutostart(enabled); err != nil {
		if errors.Is(err, platform.ErrElevationRequired) {
			if launchErr := t.controller.LaunchAutostartElevated(enabled); launchErr != nil {
				t.setFault(true)
				t.notify("sing-box-drover", "Elevation was not accepted: "+launchErr.Error())
				return
			}
			t.shutdown()
			systray.Quit()
			return
		}
		t.setFault(true)
		t.notify("sing-box-drover", err.Error())
		return
	}
	t.menuMu.Lock()
	t.autostartEnabled = enabled
	t.menuMu.Unlock()
	t.setFault(false)
	t.syncMenuChecks()
}

func (t *Tray) restart() {
	if err := t.controller.Restart(); err != nil {
		t.setFault(true)
		t.notify("sing-box-drover", err.Error())
		return
	}
	t.shutdown()
	systray.Quit()
}

func (t *Tray) openHomepage() {
	if err := openMacURL(t.controller.Options().HomepageURL); err != nil {
		t.setFault(true)
		t.notify("sing-box-drover", "Open homepage failed: "+err.Error())
	}
}

func openMacURL(value string) error {
	if err := exec.Command("/usr/bin/open", value).Run(); err != nil {
		return fmt.Errorf("open: %w", err)
	}
	return nil
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
	if err := t.refreshIcon(); err != nil {
		t.notify("sing-box-drover", err.Error())
	}
}

func (t *Tray) refreshIcon() error {
	t.iconMu.Lock()
	defer t.iconMu.Unlock()
	select {
	case <-t.done:
		return nil
	default:
	}
	status := t.runtimeStatus()
	switch status.iconKind() {
	case trayIconGreen:
		systray.SetIcon(greenStatusIcon)
	case trayIconRed:
		systray.SetIcon(redStatusIcon)
	default:
		systray.SetTemplateIcon(plainStatusIcon, plainStatusIcon)
	}
	systray.SetTooltip(status.tooltip())
	return nil
}

func (t *Tray) notify(title, message string) {
	select {
	case <-t.done:
		return
	default:
	}
	message = strings.ReplaceAll(message, "\x00", " ")
	_ = exec.Command("/usr/bin/osascript", "-e", notificationScript, "--", title, message).Run()
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
		t.notify("sing-box-drover", "Resume recovery failed: "+err.Error())
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

func (t *Tray) watchSignals() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	select {
	case <-t.done:
		return
	case <-signals:
		t.shutdown()
		systray.Quit()
	}
}

func (t *Tray) watchEvents() {
	for {
		select {
		case <-t.done:
			return
		case event := <-t.controller.Events():
			if event.State == core.StateRunning {
				t.setFault(false)
				go t.refreshSelectorsOnce()
			} else if event.Kind == core.EventError || event.State == core.StateFailed {
				t.setFault(true)
				t.notify("sing-box-drover", event.Message)
			} else {
				if err := t.refreshIcon(); err != nil {
					t.notify("sing-box-drover", err.Error())
				}
			}
		}
	}
}

func (t *Tray) watchMenuOpened() {
	for {
		select {
		case <-t.done:
			return
		case <-systray.TrayOpenedCh:
			t.syncMenuChecks()
		}
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
			t.rebuildMenu(t.controller.Selectors())
		case <-refreshTicker.C:
			t.refreshSelectorsOnce()
		}
	}
}

func (t *Tray) refreshSelectorsOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	selectors, err := t.controller.RefreshSelectors(ctx)
	cancel()
	if err != nil {
		return
	}
	t.rebuildMenu(selectors)
}
