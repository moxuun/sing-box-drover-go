package tray

import "testing"

func TestDisplaySelectorValueReplacesCountryCode(t *testing.T) {
	tests := map[string]string{
		"US-01":        "🇺🇸-01",
		"JP Tokyo":     "🇯🇵 Tokyo",
		"uk London":    "🇬🇧 London",
		"proxy 🇺🇸 01":  "proxy 🇺🇸 01",
		"unknown-node": "unknown-node",
		"US-JP-01":     "🇺🇸-JP-01",
	}
	for input, want := range tests {
		if got := displaySelectorValue(input); got != want {
			t.Errorf("displaySelectorValue(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestGeneratedFlagSetIsCompleteEnoughForAssetLookup(t *testing.T) {
	for _, code := range []string{"US", "JP", "LA", "XK"} {
		if canonicalFlagCode(code) != code {
			t.Fatalf("canonicalFlagCode(%q) is not backed by the generated asset set", code)
		}
	}
	if canonicalFlagCode("ZZ") != "" {
		t.Fatal("canonicalFlagCode accepted a code without an asset")
	}
}

func TestSelectorDisplayInfoFindsCountryMarker(t *testing.T) {
	tests := map[string]struct {
		input string
		text  string
		code  string
	}{
		"existing flag": {input: "mysub/🇺🇸 serv xtls-reality", text: "mysub/ serv xtls-reality", code: "US"},
		"ascii code":    {input: "mysub/jp serv hysteria2", text: "mysub/ serv hysteria2", code: "JP"},
		"laos flag":     {input: "mysub/🇱🇦 LegendVPS-LA", text: "mysub/ LegendVPS-LA", code: "LA"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			text, code := selectorDisplayInfo(test.input)
			if text != test.text || code != test.code {
				t.Fatalf("selectorDisplayInfo(%q) = (%q, %q), want (%q, %q)", test.input, text, code, test.text, test.code)
			}
		})
	}
}
