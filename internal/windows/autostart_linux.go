//go:build linux

package windows

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type AutostartState int

const (
	AutostartUnknown AutostartState = iota
	AutostartDisabled
	AutostartEnabled
)

func autostartPath() (string, error) {
	directory, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(directory) {
		return "", errors.New("XDG_CONFIG_HOME must be an absolute path")
	}
	return filepath.Join(directory, "autostart", "sing-box-drover.desktop"), nil
}

func QueryAutostart() (AutostartState, error) {
	path, err := autostartPath()
	if err != nil {
		return AutostartUnknown, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return AutostartDisabled, nil
	}
	if err != nil {
		return AutostartUnknown, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		switch strings.TrimSpace(line) {
		case "Hidden=true", "X-GNOME-Autostart-enabled=false":
			return AutostartDisabled, nil
		}
	}
	return AutostartEnabled, nil
}

func SetAutostart(enabled bool, owner string) error {
	if IsProcessElevated() {
		return errors.New("configure Linux login startup from the regular user session")
	}
	path, err := autostartPath()
	if err != nil {
		return err
	}
	if !enabled {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	argument, err := desktopExecArg(executable)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".sing-box-drover-*.desktop")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	entry := "[Desktop Entry]\nType=Application\nName=sing-box-drover\nExec=" + argument +
		"\nTerminal=false\nX-GNOME-Autostart-enabled=true\n"
	_, writeErr := file.WriteString(entry)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func desktopExecArg(value string) (string, error) {
	if strings.ContainsAny(value, "\x00\n\r") {
		return "", fmt.Errorf("invalid executable path for login startup")
	}
	// Desktop entries unescape the key value before parsing the Exec argument.
	escaped := strings.NewReplacer("\\", "\\\\\\\\", "\"", "\\\\\"", "`", "\\\\`", "$", "\\\\$", "%", "%%").Replace(value)
	return "\"" + escaped + "\"", nil
}
