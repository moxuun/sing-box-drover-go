// Command go-overlay prepares the Windows workaround for golang/go#81956.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	toolchainVersion = "go1.27.2"
	originalSHA256   = "bd3c834a29e31c93e56d81da54d4a874561814a86550de1456c8d6d40a9c5fda"
)

func main() {
	goroot := flag.String("goroot", runtime.GOROOT(), "GOROOT of the Go 1.27.2 build toolchain")
	output := flag.String("output", "output/.go-overlay/overlay.json", "generated overlay file")
	flag.Parse()
	if err := generate(*goroot, *output); err != nil {
		fmt.Fprintln(os.Stderr, "Go memory workaround:", err)
		os.Exit(1)
	}
}

func generate(goroot, output string) error {
	if value := os.Getenv("GOFIPS140"); value != "" && value != "off" {
		return fmt.Errorf("GOFIPS140=%q is unsupported; this workaround is for non-FIPS builds", value)
	}
	for _, setting := range strings.Split(os.Getenv("GODEBUG"), ",") {
		if strings.HasPrefix(setting, "fips140=") && setting != "fips140=off" {
			return fmt.Errorf("GODEBUG setting %q is unsupported; this workaround is for non-FIPS builds", setting)
		}
	}
	version, err := os.ReadFile(filepath.Join(goroot, "VERSION"))
	if err != nil {
		return err
	}
	if strings.SplitN(string(version), "\n", 2)[0] != toolchainVersion {
		return fmt.Errorf("requires %s; review golang/go#81956 before changing the toolchain", toolchainVersion)
	}
	goBinary := "go"
	if runtime.GOOS == "windows" {
		goBinary += ".exe"
	}
	command := exec.Command(filepath.Join(goroot, "bin", goBinary), "env", "GOFIPS140")
	command.Env = append(os.Environ(), "GOROOT="+goroot, "GOTOOLCHAIN=local")
	fips, err := command.Output()
	if err != nil {
		return fmt.Errorf("read effective GOFIPS140 setting: %w", err)
	}
	if value := strings.TrimSpace(string(fips)); value != "off" {
		return fmt.Errorf("effective GOFIPS140=%q is unsupported; this workaround is for non-FIPS builds", value)
	}
	original, err := filepath.Abs(filepath.Join(goroot, "src", "crypto", "internal", "fips140", "drbg", "entropy_fips140.go"))
	if err != nil {
		return err
	}
	source, err := os.ReadFile(original)
	if err != nil {
		return err
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(source)); got != originalSHA256 {
		return fmt.Errorf("unexpected entropy_fips140.go SHA-256 %s; refusing to patch changed source", got)
	}
	patched := strings.Replace(string(source), `import entropy "crypto/internal/entropy/v1.0.0"`, `import (
	entropy "crypto/internal/entropy/v1.0.0"
	"sync"
)`, 1)
	patched = strings.Replace(patched, `// It should end up in the .noptrbss section, and become backed by physical pages
// at first use. This ensures that programs that do not use the FIPS 140-3 module
// do not incur any memory use or initialization penalties.
var memory entropy.ScratchBuffer`, `// Allocate once, only when FIPS entropy generation is used. See golang/go#81956.
var memory = sync.OnceValue(func() *entropy.ScratchBuffer {
	return new(entropy.ScratchBuffer)
})`, 1)
	patched = strings.ReplaceAll(patched, "entropy.Seed(&memory)", "entropy.Seed(memory())")
	formatted, err := format.Source([]byte(patched))
	if err != nil {
		return err
	}
	overlayPath, err := filepath.Abs(output)
	if err != nil {
		return err
	}
	directory := filepath.Dir(overlayPath)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	replacement := filepath.Join(directory, "entropy_fips140.go")
	if err := os.WriteFile(replacement, formatted, 0o644); err != nil {
		return err
	}
	data, err := json.MarshalIndent(struct {
		Replace map[string]string
	}{Replace: map[string]string{original: replacement}}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(overlayPath, append(data, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Println(overlayPath)
	return nil
}
