package tray

import (
	"strings"
	"testing"

	"github.com/egoist/mygo"
	"sing-box-drover/internal/app"
	"sing-box-drover/internal/clash"
)

func TestSelectorOptionTextDisplaysResolvedRuntimeGroup(t *testing.T) {
	selector := clash.Selector{Name: "proxy", Now: "🎈 自动选择", ResolvedNow: "node-b"}
	if got := selectorOptionText(selector, selector.Now); got != "🎈 自动选择（当前：node-b）" {
		t.Fatalf("selectorOptionText() = %q", got)
	}
	if got := selectorOptionText(selector, "node-a"); got != "node-a" {
		t.Fatalf("selectorOptionText() changed an unselected option: %q", got)
	}
}

func TestBuildMenuRejectsInvalidSelectorText(t *testing.T) {
	for _, test := range []struct {
		selector clash.Selector
		want     string
	}{
		{selector: clash.Selector{Name: "valid", All: []string{"bad\x00node"}}, want: "selector item"},
		{selector: clash.Selector{Name: "bad\x00group", All: []string{"node"}}, want: "selector group"},
		{selector: clash.Selector{Name: "invalid", All: []string{string([]byte{0xff})}}, want: "selector item"},
	} {
		tray := &Tray{controller: &app.App{}}
		menu, err := tray.buildMenu([]clash.Selector{test.selector})
		if menu != nil || err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("buildMenu() = (%v, %v), want %s error", menu, err, test.want)
		}
	}
}

func TestSharedMenuPreservesSelectorRoutingAndChecks(t *testing.T) {
	tray := &Tray{controller: &app.App{}}
	menu, err := tray.buildMenu([]clash.Selector{
		{Name: "proxy", All: []string{"US node", "node-b"}, Now: "node-b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if item := menu.ItemByID("proxy"); item == nil || item.Type != mygo.MenuItemCheckbox {
		t.Fatal("system proxy checkbox missing")
	}
	if item := menu.ItemByID("tun"); item != nil {
		t.Fatal("TUN item present without a TUN inbound")
	}
	for _, id := range []string{"autostart", "restart", "homepage", "quit"} {
		if menu.ItemByID(id) == nil {
			t.Fatalf("menu item %q missing", id)
		}
	}
	if got := menu.ItemByID("selector:0:0").Label; got != "🇺🇸 node" {
		t.Fatalf("selector label = %q", got)
	}
	if !menu.ItemByID("selector:0:1").IsChecked() || menu.ItemByID("selector:0:0").IsChecked() {
		t.Fatal("selection checkmarks do not reflect the core")
	}
	if action := tray.selectorItems[0].action; action.selector != "proxy" || action.value != "US node" {
		t.Fatalf("display text changed the routed selector value: %#v", action)
	}
}

func TestOpenURLRejectsUnencodableText(t *testing.T) {
	if err := openURL("https://example.test/\x00"); err == nil {
		t.Fatal("openURL accepted a URL containing NUL")
	}
}

// TestMenuToggleUsesTheClickedTargetWhenControllerStateDiffers guards the
// regression where a queued menu action recomputed its target from controller
// state instead of the checkmark the user clicked. It replays the reported
// sequence: enable System Proxy, enable TUN, disable System Proxy, disable TUN.
//
// A TUN change restarts the core, and the restart temporarily restores the
// system proxy. During that window the controller reports the proxy as off
// while the menu still shows it on. The queued "off" used to read the restored
// state, invert it, and turn the proxy back on.
func TestMenuToggleUsesTheClickedTargetWhenControllerStateDiffers(t *testing.T) {
	tray := &Tray{
		controller: &app.App{},
		actions:    make(chan func(), 4),
		done:       make(chan struct{}),
	}
	var requested []bool
	tray.proxyToggle = func(enabled bool) { requested = append(requested, enabled) }
	menu, err := tray.buildMenu(nil)
	if err != nil {
		t.Fatal(err)
	}
	item := menu.ItemByID("proxy")
	if item == nil || item.Type != mygo.MenuItemCheckbox {
		t.Fatal("system proxy checkbox missing")
	}
	// run drains the action the click queued, as the worker would.
	run := func() {
		t.Helper()
		select {
		case action := <-tray.actions:
			action()
		default:
			t.Fatal("click did not queue an action")
		}
	}

	// The menu shows the proxy on, so this click means "off".
	item.SetChecked(true)
	tray.dispatchMenuItem(item)
	// A core restart restored the proxy while this action waited, so the
	// controller now reports off, the opposite of what the user clicked.
	if tray.controller.SystemProxyActive() {
		t.Fatal("test needs the controller to disagree with the displayed checkmark")
	}
	run()

	if len(requested) != 1 || requested[0] {
		t.Fatalf("proxy toggle requested %v, want [false], the clicked target", requested)
	}
}
