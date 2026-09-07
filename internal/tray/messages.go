package tray

import "time"

const (
	wmLButtonUp   = 0x0202
	wmRButtonUp   = 0x0205
	wmMButtonUp   = 0x0208
	wmXButtonUp   = 0x020c
	wmContextMenu = 0x007b
	ninSelect     = 0x0400 // WM_USER + 0
	ninKeySelect  = 0x0401 // WM_USER + 1
	trayIconID    = 1
)

type trayEventKind uint8

const (
	trayEventNone trayEventKind = iota
	trayEventMenu
	trayEventActivate
)

type trayEventSource uint8

const (
	traySourceNone trayEventSource = iota
	traySourceContextMenu
	traySourceRightButton
	traySourceMouseButton
	traySourceSelect
	traySourceKeySelect
)

type trayEvent struct {
	Kind     trayEventKind
	Source   trayEventSource
	X, Y     int32
	HasPoint bool
}

// decodeTrayEvent decodes the callback parameter layout selected by
// NOTIFYICON_VERSION_4. In that mode LOWORD(lParam) is the notification and
// HIWORD(lParam) is the 16-bit icon ID; legacy mode keeps the icon ID in
// wParam and the notification in lParam.
func decodeTrayEvent(lParam, wParam uintptr, version4 bool, iconID uint16) trayEvent {
	var notification uint16
	if version4 {
		if uint16(lParam>>16) != iconID {
			return trayEvent{}
		}
		notification = uint16(lParam)
	} else {
		if uint32(wParam) != uint32(iconID) {
			return trayEvent{}
		}
		notification = uint16(lParam)
	}

	event := trayEvent{}
	switch notification {
	case wmContextMenu:
		event = trayEvent{Kind: trayEventMenu, Source: traySourceContextMenu}
	case wmRButtonUp:
		event = trayEvent{Kind: trayEventMenu, Source: traySourceRightButton}
	case wmLButtonUp, wmMButtonUp, wmXButtonUp:
		event = trayEvent{Kind: trayEventActivate, Source: traySourceMouseButton}
	case ninSelect:
		event = trayEvent{Kind: trayEventActivate, Source: traySourceSelect}
	case ninKeySelect:
		event = trayEvent{Kind: trayEventActivate, Source: traySourceKeySelect}
	default:
		return trayEvent{}
	}
	if version4 {
		event.X = int32(int16(uint16(wParam)))
		event.Y = int32(int16(uint16(wParam >> 16)))
		event.HasPoint = event.Kind == trayEventMenu
	}
	return event
}

const trayAliasWindow = 250 * time.Millisecond

type trayEventGate struct {
	source trayEventSource
	kind   trayEventKind
	at     time.Time
}

// accept suppresses only the documented/fallback aliases for one operation:
// WM_CONTEXTMENU with WM_RBUTTONUP, and a mouse-up with NIN_SELECT. Two
// successive events of the same source remain separate clicks/activations.
func (g *trayEventGate) accept(event trayEvent, now time.Time) bool {
	if event.Kind == trayEventNone {
		return false
	}
	if now.Sub(g.at) >= 0 && now.Sub(g.at) <= trayAliasWindow && trayEventAliases(g.kind, g.source, event.Kind, event.Source) {
		return false
	}
	g.kind = event.Kind
	g.source = event.Source
	g.at = now
	return true
}

func trayEventAliases(kind trayEventKind, source trayEventSource, nextKind trayEventKind, nextSource trayEventSource) bool {
	if kind != nextKind {
		return false
	}
	if kind == trayEventMenu {
		return (source == traySourceContextMenu && nextSource == traySourceRightButton) ||
			(source == traySourceRightButton && nextSource == traySourceContextMenu)
	}
	if kind == trayEventActivate {
		return (source == traySourceMouseButton && nextSource == traySourceSelect) ||
			(source == traySourceSelect && nextSource == traySourceMouseButton)
	}
	return false
}
