//go:build darwin

package windows

import (
	"reflect"
	"testing"
)

func TestSplitCommandLine(t *testing.T) {
	got, err := splitCommandLine(`-restart -tun -proxy "two words"`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-restart", "-tun", "-proxy", "two words"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitCommandLine() = %#v, want %#v", got, want)
	}
	if _, err := splitCommandLine(`"unterminated`); err == nil {
		t.Fatal("unterminated quote unexpectedly parsed")
	}
}

func TestQuoteShell(t *testing.T) {
	if got := quoteShell("a'b"); got != `'a'\''b'` {
		t.Fatalf("quoteShell() = %q", got)
	}
}
