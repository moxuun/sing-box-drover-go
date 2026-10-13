//go:build linux

package windows

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type linuxProxySetting struct {
	schema, key       string
	original, applied string
}

type ProxySession struct {
	settings []linuxProxySetting
}

func runGSettings(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "gsettings", args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("gsettings %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func EnableSystemProxy(host string, port int) (ProxySession, error) {
	// GNOME settings do not control KDE or arbitrary Wayland compositors.
	gnome := false
	for _, desktop := range strings.Split(strings.ToUpper(os.Getenv("XDG_CURRENT_DESKTOP")), ":") {
		if desktop == "GNOME" {
			gnome = true
		}
	}
	if !gnome {
		return ProxySession{}, errors.New("Linux system proxy currently supports GNOME sessions only; use TUN or configure your desktop proxy manually")
	}
	return enableLinuxProxy(runGSettings, host, port)
}

func enableLinuxProxy(run func(...string) (string, error), host string, port int) (ProxySession, error) {
	if strings.TrimSpace(host) == "" || strings.ContainsAny(host, "\x00\n\r\t \"'\\") || port < 1 || port > 65535 {
		return ProxySession{}, errors.New("invalid Linux proxy host or port")
	}
	const schema = "org.gnome.system.proxy"
	settings := []linuxProxySetting{
		{schema: schema + ".http", key: "host", applied: "'" + host + "'"},
		{schema: schema + ".http", key: "port", applied: strconv.Itoa(port)},
		{schema: schema + ".http", key: "use-authentication", applied: "false"},
		{schema: schema + ".https", key: "host", applied: "'" + host + "'"},
		{schema: schema + ".https", key: "port", applied: strconv.Itoa(port)},
		{schema: schema + ".socks", key: "host", applied: "'" + host + "'"},
		{schema: schema + ".socks", key: "port", applied: strconv.Itoa(port)},
		{schema: schema, key: "use-same-proxy", applied: "false"},
		{schema: schema, key: "mode", applied: "'manual'"},
	}
	// Read every original value before the first write.
	for i := range settings {
		setting := &settings[i]
		writable, err := run("writable", setting.schema, setting.key)
		if err != nil {
			return ProxySession{}, err
		}
		if writable != "true" {
			return ProxySession{}, fmt.Errorf("GNOME proxy setting %s.%s is not writable", setting.schema, setting.key)
		}
		setting.original, err = run("get", setting.schema, setting.key)
		if err != nil {
			return ProxySession{}, err
		}
	}
	for i := range settings {
		setting := &settings[i]
		_, err := run("set", setting.schema, setting.key, setting.applied)
		if err == nil {
			value, readErr := run("get", setting.schema, setting.key)
			err = readErr
			if err == nil && value != setting.applied {
				err = fmt.Errorf("GNOME did not apply proxy setting %s.%s: got %s, want %s", setting.schema, setting.key, value, setting.applied)
			}
		}
		if err != nil {
			return ProxySession{}, errors.Join(err, restoreLinuxSettings(run, settings[:i+1]))
		}
	}
	return ProxySession{settings: settings}, nil
}

func restoreLinuxSettings(run func(...string) (string, error), settings []linuxProxySetting) error {
	var result error
	for i := len(settings) - 1; i >= 0; i-- {
		setting := settings[i]
		_, err := run("set", setting.schema, setting.key, setting.original)
		if err == nil {
			value, readErr := run("get", setting.schema, setting.key)
			err = readErr
			if err == nil && value != setting.original {
				err = fmt.Errorf("GNOME did not restore proxy setting %s.%s", setting.schema, setting.key)
			}
		}
		result = errors.Join(result, err)
	}
	return result
}

func RestoreSystemProxy(session ProxySession) (bool, error) {
	return restoreLinuxProxy(runGSettings, session)
}

func restoreLinuxProxy(run func(...string) (string, error), session ProxySession) (bool, error) {
	if len(session.settings) == 0 {
		return false, nil
	}
	remaining := make([]linuxProxySetting, 0, len(session.settings))
	for _, setting := range session.settings {
		value, err := run("get", setting.schema, setting.key)
		if err != nil {
			return false, err
		}
		if value == setting.original {
			// An earlier restoration attempt may already have restored this key.
			continue
		}
		if value != setting.applied {
			// Respect settings changed by the user or another controller.
			return false, nil
		}
		remaining = append(remaining, setting)
	}
	if err := restoreLinuxSettings(run, remaining); err != nil {
		return false, err
	}
	return true, nil
}
