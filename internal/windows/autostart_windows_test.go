//go:build windows

package windows

import "testing"

func TestAutostartCreateArgsUseLimitedRunLevel(t *testing.T) {
	args := autostartCreateArgs(`C:\Users\Example User\sing-box-dover-go.exe`, `DOMAIN\user`)
	want := []string{"/Create", "/TN", taskName, "/SC", "ONLOGON", "/RU", `DOMAIN\user`, "/IT", "/RL", "LIMITED", "/TR", `"C:\Users\Example User\sing-box-dover-go.exe"`, "/F"}
	if len(args) != len(want) {
		t.Fatalf("schtasks argument count = %d, want %d: %#v", len(args), len(want), args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("schtasks argument %d = %q, want %q; args=%#v", i, args[i], want[i], args)
		}
	}
}
