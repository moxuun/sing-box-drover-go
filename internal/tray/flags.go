package tray

import (
	"strings"
	"unicode/utf8"
)

// knownFlagCodes is generated from the bundled flag asset set. Keeping the
// code list separate from rendering lets selector parsing work on every
// platform while the native bitmap remains Windows-specific.
func canonicalFlagCode(code string) string {
	code = strings.ToUpper(code)
	if code == "UK" {
		// UK is commonly used in node names; the ISO/RGI flag is GB.
		code = "GB"
	}
	if _, ok := knownFlagCodes[code]; ok {
		return code
	}
	return ""
}

func countryFlagEmoji(code string) string {
	if len(code) != 2 {
		return ""
	}
	return string([]rune{
		rune(0x1F1E6 + int(code[0]-'A')),
		rune(0x1F1E6 + int(code[1]-'A')),
	})
}

func displaySelectorValue(value string) string {
	for i := 0; i < len(value); {
		if isASCIIAlpha(value[i]) {
			start := i
			for i < len(value) && isASCIIAlpha(value[i]) {
				i++
			}
			token := value[start:i]
			if len(token) == 2 {
				if code := canonicalFlagCode(token); code != "" {
					return value[:start] + countryFlagEmoji(code) + value[i:]
				}
			}
			continue
		}
		_, size := utf8.DecodeRuneInString(value[i:])
		i += size
	}
	return value
}

// selectorDisplayInfo returns the menu text and the first country code found
// in a selector value. Country flags are normally already present in values
// returned by sing-box, while compatible cores may expose the same location
// as an ASCII code (for example, "mysub/us"). The menu uses the code to add a
// native bitmap because the default Windows menu font renders regional
// indicator emoji as the two-letter code instead of a visible flag.
func selectorDisplayInfo(value string) (text, code string) {
	for i := 0; i < len(value); {
		r, size := utf8.DecodeRuneInString(value[i:])
		if code, next, ok := countryCodeFromFlag(value, i, r, size); ok {
			return value[:i] + value[next:], code
		}
		if isASCIIAlpha(value[i]) {
			start := i
			for i < len(value) && isASCIIAlpha(value[i]) {
				i++
			}
			token := value[start:i]
			if len(token) == 2 {
				if code := canonicalFlagCode(token); code != "" {
					return value[:start] + value[i:], code
				}
			}
			continue
		}
		i += size
	}
	return value, ""
}

func countryCodeFromFlag(value string, offset int, first rune, firstSize int) (code string, next int, ok bool) {
	if first < 0x1F1E6 || first > 0x1F1FF {
		return "", 0, false
	}
	second, secondSize := utf8.DecodeRuneInString(value[offset+firstSize:])
	if second < 0x1F1E6 || second > 0x1F1FF {
		return "", 0, false
	}
	code = string([]rune{rune('A' + first - 0x1F1E6), rune('A' + second - 0x1F1E6)})
	if code = canonicalFlagCode(code); code == "" {
		return "", 0, false
	}
	return code, offset + firstSize + secondSize, true
}

func isASCIIAlpha(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z')
}
