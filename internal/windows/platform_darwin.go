//go:build darwin

package windows

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	appBundleSuffix = ".app"
	appContentsDir  = "Contents"
	appMacOSDir     = "MacOS"
)

func CoreExecutableName() string { return "sing-box" }

func AutostartRequiresElevation() bool { return false }

func SystemProxyRequiresElevation() bool { return true }

// RuntimeDir keeps portable builds working while allowing an app bundle to
// keep its mutable configuration and core next to the bundle rather than
// inside Contents/MacOS.
func RuntimeDir(executable string) string {
	processDir := filepath.Dir(executable)
	bundleRoot, inBundle := appBundleRoot(processDir)
	if !inBundle {
		return processDir
	}
	packageDir := filepath.Dir(bundleRoot)
	for _, candidate := range []string{processDir, packageDir} {
		if hasRuntimeFiles(candidate) {
			return candidate
		}
	}
	return packageDir
}

func appBundleRoot(processDir string) (string, bool) {
	if filepath.Base(processDir) != appMacOSDir {
		return "", false
	}
	contentsDir := filepath.Dir(processDir)
	if filepath.Base(contentsDir) != appContentsDir {
		return "", false
	}
	bundleRoot := filepath.Dir(contentsDir)
	if !strings.HasSuffix(filepath.Base(bundleRoot), appBundleSuffix) {
		return "", false
	}
	return bundleRoot, true
}

func hasRuntimeFiles(dir string) bool {
	for _, name := range []string{"sing-box-drover.ini", "config.json", CoreExecutableName()} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}
