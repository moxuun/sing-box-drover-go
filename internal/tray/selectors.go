package tray

import "sing-box-drover/internal/clash"

type selectorAction struct{ selector, value string }

func selectorOptionText(selector clash.Selector, value string) string {
	text := displaySelectorValue(value)
	if value == selector.Now && selector.ResolvedNow != "" && selector.ResolvedNow != value {
		return text + "（当前：" + displaySelectorValue(selector.ResolvedNow) + "）"
	}
	return text
}
