package tray

import (
	"testing"
	"time"
)

func TestDecodeVersion4ContextMenuUsesLowWordAndIconID(t *testing.T) {
	x, y := int16(321), int16(-27)
	wParam := uintptr(uint32(uint16(x)) | uint32(uint16(y))<<16)
	lParam := uintptr(uint32(trayIconID)<<16 | uint32(wmContextMenu))
	event := decodeTrayEvent(lParam, wParam, true, trayIconID)
	if event.Kind != trayEventMenu || event.Source != traySourceContextMenu || !event.HasPoint || event.X != int32(x) || event.Y != int32(y) {
		t.Fatalf("version-4 context menu was decoded incorrectly: %#v", event)
	}

	lParam = uintptr(uint32(2)<<16 | uint32(wmContextMenu))
	if event := decodeTrayEvent(lParam, wParam, true, trayIconID); event.Kind != trayEventNone {
		t.Fatalf("notification for another icon was accepted: %#v", event)
	}
}

func TestDecodeLegacyContextMenuFallsBackToCursor(t *testing.T) {
	event := decodeTrayEvent(uintptr(wmRButtonUp), uintptr(trayIconID), false, trayIconID)
	if event.Kind != trayEventMenu || event.Source != traySourceRightButton || event.HasPoint {
		t.Fatalf("legacy right-click was decoded incorrectly: %#v", event)
	}

	event = decodeTrayEvent(uintptr(wmContextMenu), uintptr(trayIconID), false, trayIconID)
	if event.Kind != trayEventMenu || event.Source != traySourceContextMenu || event.HasPoint {
		t.Fatalf("legacy context-menu notification was decoded incorrectly: %#v", event)
	}
}

func TestDecodeActivationEvents(t *testing.T) {
	for _, notification := range []uint16{wmLButtonUp, wmMButtonUp, wmXButtonUp, ninSelect, ninKeySelect} {
		lParam := uintptr(uint32(trayIconID)<<16 | uint32(notification))
		event := decodeTrayEvent(lParam, 0, true, trayIconID)
		if event.Kind != trayEventActivate {
			t.Fatalf("activation notification %04x was not recognized: %#v", notification, event)
		}
	}
}

func TestTrayEventGateSuppressesOnlyAliases(t *testing.T) {
	base := time.Unix(100, 0)
	var gate trayEventGate
	if !gate.accept(trayEvent{Kind: trayEventActivate, Source: traySourceMouseButton}, base) {
		t.Fatal("first activation was rejected")
	}
	if gate.accept(trayEvent{Kind: trayEventActivate, Source: traySourceSelect}, base.Add(50*time.Millisecond)) {
		t.Fatal("mouse activation and NIN_SELECT were not deduplicated")
	}
	if !gate.accept(trayEvent{Kind: trayEventActivate, Source: traySourceMouseButton}, base.Add(100*time.Millisecond)) {
		t.Fatal("two mouse activations were incorrectly deduplicated")
	}

	if !gate.accept(trayEvent{Kind: trayEventMenu, Source: traySourceContextMenu}, base.Add(time.Second)) {
		t.Fatal("first context-menu notification was rejected")
	}
	if gate.accept(trayEvent{Kind: trayEventMenu, Source: traySourceRightButton}, base.Add(1100*time.Millisecond)) {
		t.Fatal("context-menu/right-button aliases were not deduplicated")
	}
	if !gate.accept(trayEvent{Kind: trayEventMenu, Source: traySourceContextMenu}, base.Add(1200*time.Millisecond)) {
		t.Fatal("two context-menu notifications were incorrectly deduplicated")
	}
}
