//go:build darwin

package windows

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeDirDirectBinary(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "sing-box-drover")
	if got := RuntimeDir(executable); got != dir {
		t.Fatalf("RuntimeDir() = %q, want %q", got, dir)
	}
}

func TestRuntimeDirUsesAppBundlePackageDirectory(t *testing.T) {
	packageDir := t.TempDir()
	executable := filepath.Join(packageDir, "Sing-Box-Drover.app", "Contents", "MacOS", "sing-box-drover")
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageDir, "sing-box-drover.ini"), []byte("[sing-box-drover]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := RuntimeDir(executable); got != packageDir {
		t.Fatalf("RuntimeDir() = %q, want %q", got, packageDir)
	}
}

func TestRuntimeDirPrefersBundleContentsWhenConfiguredThere(t *testing.T) {
	packageDir := t.TempDir()
	processDir := filepath.Join(packageDir, "Sing-Box-Drover.app", "Contents", "MacOS")
	if err := os.MkdirAll(processDir, 0o755); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(processDir, "sing-box-drover")
	if err := os.WriteFile(filepath.Join(processDir, "config.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := RuntimeDir(executable); got != processDir {
		t.Fatalf("RuntimeDir() = %q, want %q", got, processDir)
	}
}

func TestSystemProxyRequiresElevation(t *testing.T) {
	if !SystemProxyRequiresElevation() {
		t.Fatal("macOS system proxy writes must request administrator privileges")
	}
}
