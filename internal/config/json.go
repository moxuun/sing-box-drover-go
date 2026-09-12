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

	for i := 0; i < len(source); i++ {
		ch := source[i]
		if inLineComment {
			if ch == '\n' || ch == '\r' {
				inLineComment = false
			}
			continue
		}
		if inBlockComment {
			if ch == '*' && i+1 < len(source) && source[i+1] == '/' {
				inBlockComment = false
				i++
			}
			continue
		}
		if inString {
			b.WriteByte(ch)
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inString = false
			}
			continue
		}

		if ch == '/' && i+1 < len(source) {
			switch source[i+1] {
			case '/':
				inLineComment = true
				i++
				continue
			case '*':
				inBlockComment = true
				i++
				continue
			}
		}
		if ch == ',' {
			if pendingComma {
				return b.String(), errors.New("unexpected consecutive comma")
			}
			pendingComma = true
			continue
		}
		if ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n' {
			continue
		}
		if pendingComma {
			if ch != '}' && ch != ']' {
				b.WriteByte(',')
			}
			pendingComma = false
		}
		if ch == '"' {
			inString = true
			escaped = false
		}
		b.WriteByte(ch)
	}

	if pendingComma {
		return b.String(), errors.New("trailing comma outside a JSON container")
	}
	if inBlockComment {
		return b.String(), errors.New("unterminated block comment")
	}
	return b.String(), nil
}
