package config

import (
	"errors"
	"strings"
)

// NormalizeJSON accepts the JSON-with-comments/trailing-comma form commonly
// used by sing-box configurations. It deliberately leaves string contents
// untouched and only removes syntax outside strings.
func NormalizeJSON(source string) string {
	normalized, _ := normalizeJSON(source)
	return normalized
}

func normalizeJSON(source string) (string, error) {
	var b strings.Builder
	b.Grow(len(source))

	inString := false
	escaped := false
	inLineComment := false
	inBlockComment := false
	pendingComma := false
	// lastSignificant is the most recent non-whitespace byte emitted outside a
	// string. It decides whether a comma may legally appear at this position, so
	// a leading or doubled comma is rejected instead of being folded away.
	lastSignificant := byte(0)

	for i := 0; i < len(source); i++ {
		ch := source[i]
		if inLineComment {
			if ch == '\n' || ch == '\r' {
				inLineComment = false
			} else {
				continue
			}
		} else if inBlockComment {
			if ch == '*' && i+1 < len(source) && source[i+1] == '/' {
				inBlockComment = false
				i++
			}
			continue
		} else if inString {
			b.WriteByte(ch)
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inString = false
			}
			continue
		} else if ch == '/' && i+1 < len(source) {
			switch source[i+1] {
			case '/':
				inLineComment = true
				// A comment is dropped, but it still separates the tokens around
				// it: `tru/* c */e` must not be normalized into `true`.
				b.WriteByte(' ')
				i++
				continue
			case '*':
				inBlockComment = true
				b.WriteByte(' ')
				i++
				continue
			}
		}

		if ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n' {
			// Whitespace is kept: dropping it would splice neighbouring tokens
			// (`10 80` becoming `1080`) and silently change the configuration.
			b.WriteByte(ch)
			continue
		}
		if ch == ',' {
			if pendingComma {
				return b.String(), errors.New("unexpected consecutive comma")
			}
			switch lastSignificant {
			case 0, '{', '[', ',':
				// `[,]` and `{,}` are not trailing commas: nothing precedes them.
				return b.String(), errors.New("unexpected comma")
			}
			pendingComma = true
			continue
		}
		if pendingComma {
			if ch != '}' && ch != ']' {
				b.WriteByte(',')
				lastSignificant = ','
			}
			pendingComma = false
		}
		if ch == '"' {
			inString = true
			escaped = false
		}
		b.WriteByte(ch)
		lastSignificant = ch
	}

	if pendingComma {
		return b.String(), errors.New("trailing comma outside a JSON container")
	}
	if inBlockComment {
		return b.String(), errors.New("unterminated block comment")
	}
	return b.String(), nil
}
