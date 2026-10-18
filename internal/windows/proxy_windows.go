//go:build windows

package windows

import (
	"errors"
	"fmt"
	"net"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	winapi "golang.org/x/sys/windows"
)

const (
	internetPerConnFlags          = 1
	internetPerConnProxyServer    = 2
	internetPerConnProxyBypass    = 3
	internetPerConnAutoConfigURL  = 4
	internetPerConnFlagsUI        = 10
	proxyTypeDirect               = 0x00000001
	proxyTypeProxy                = 0x00000002
	proxyTypeAutoDetect           = 0x00000008
	internetOptionSettingsChanged = 39
	internetOptionPerConnection   = 75
	internetOptionRefresh         = 37
	internetOptionProxyChanged    = 95
)

type perConnOption struct {
	option uint32
	value  perConnValue
}

type perConnValue struct {
	raw uintptr
}

type perConnOptionList struct {
	size        uint32
	connection  *uint16
	optionCount uint32
	optionError uint32
	options     *perConnOption
}

type proxySettings struct {
	flags         uint32
	server        string
	bypass        string
	autoConfigURL string
}

// ProxySession records the settings that were in place before this controller
// took the system proxy over, so releasing it puts the user's own configuration
// back instead of leaving this controller's values behind.
type ProxySession struct {
	original proxySettings
}

var (
	wininet             = winapi.NewLazySystemDLL("wininet.dll")
	kernel32Proxy       = winapi.NewLazySystemDLL("kernel32.dll")
	internetSetOption   = wininet.NewProc("InternetSetOptionW")
	internetQueryOption = wininet.NewProc("InternetQueryOptionW")
	globalFree          = kernel32Proxy.NewProc("GlobalFree")
)

func proxyCallError(operation string, callErr error) error {
	if callErr != nil && callErr != syscall.Errno(0) {
		return fmt.Errorf("%s: %w", operation, callErr)
	}
	return fmt.Errorf("%s failed", operation)
}

func freeQueriedStrings(options []perConnOption) {
	for i := range options {
		switch options[i].option {
		case internetPerConnProxyServer, internetPerConnProxyBypass, internetPerConnAutoConfigURL:
			if options[i].value.raw != 0 {
				_, _, _ = globalFree.Call(options[i].value.raw)
				options[i].value.raw = 0
			}
		}
	}
}

func queryProxySettingsWithFlags(flagsOption uint32) (proxySettings, error) {
	options := []perConnOption{
		{option: flagsOption},
		{option: internetPerConnProxyServer},
		{option: internetPerConnProxyBypass},
		{option: internetPerConnAutoConfigURL},
	}
	list := perConnOptionList{
		size:        uint32(unsafe.Sizeof(perConnOptionList{})),
		optionCount: uint32(len(options)),
		options:     &options[0],
	}
	size := uint32(unsafe.Sizeof(list))
	ok, _, callErr := internetQueryOption.Call(
		0,
		internetOptionPerConnection,
		uintptr(unsafe.Pointer(&list)),
		uintptr(unsafe.Pointer(&size)),
	)
	defer freeQueriedStrings(options)
	if ok == 0 {
		return proxySettings{}, proxyCallError("InternetQueryOption", callErr)
	}
	stringValue := func(value perConnValue) string {
		if value.raw == 0 {
			return ""
		}
		return winapi.UTF16PtrToString(*(**uint16)(unsafe.Pointer(&value.raw)))
	}
	return proxySettings{
		flags:         uint32(options[0].value.raw),
		server:        stringValue(options[1].value),
		bypass:        stringValue(options[2].value),
		autoConfigURL: stringValue(options[3].value),
	}, nil
}

func queryProxySettings() (proxySettings, error) {
	settings, err := queryProxySettingsWithFlags(internetPerConnFlagsUI)
	if err == nil {
		return settings, nil
	}
	return queryProxySettingsWithFlags(internetPerConnFlags)
}

// applyProxyOptions writes one WinINet per-connection option list and tells
// the system that the proxy settings changed.
//
// The three notifications are checked: a settings change that could not be
// broadcast leaves already running clients on the previous proxy, and reporting
// that as success is how a controller ends up claiming a proxy it does not own.
func applyProxyOptions(options []perConnOption) error {
	list := perConnOptionList{
		size:        uint32(unsafe.Sizeof(perConnOptionList{})),
		optionCount: uint32(len(options)),
		options:     &options[0],
	}
	ok, _, callErr := internetSetOption.Call(0, internetOptionPerConnection, uintptr(unsafe.Pointer(&list)), uintptr(unsafe.Sizeof(list)))
	if ok == 0 {
		return proxyCallError("InternetSetOption", callErr)
	}
	for _, notification := range []struct {
		name   string
		option uintptr
	}{
		{"InternetSetOption(SETTINGS_CHANGED)", internetOptionSettingsChanged},
		{"InternetSetOption(PROXY_CHANGED)", internetOptionProxyChanged},
		{"InternetSetOption(REFRESH)", internetOptionRefresh},
	} {
		if ok, _, callErr := internetSetOption.Call(0, notification.option, 0, 0); ok == 0 {
			return proxyCallError(notification.name, callErr)
		}
	}
	return nil
}

func setProxySettings(settings proxySettings) error {
	server, err := winapi.UTF16PtrFromString(settings.server)
	if err != nil {
		return err
	}
	bypass, err := winapi.UTF16PtrFromString(settings.bypass)
	if err != nil {
		return err
	}
	autoConfigURL, err := winapi.UTF16PtrFromString(settings.autoConfigURL)
	if err != nil {
		return err
	}
	options := []perConnOption{
		{option: internetPerConnFlags},
		{option: internetPerConnProxyServer, value: perConnValue{raw: uintptr(unsafe.Pointer(server))}},
		{option: internetPerConnProxyBypass, value: perConnValue{raw: uintptr(unsafe.Pointer(bypass))}},
		{option: internetPerConnAutoConfigURL, value: perConnValue{raw: uintptr(unsafe.Pointer(autoConfigURL))}},
	}
	options[0].value.raw = uintptr(settings.flags)
	err = applyProxyOptions(options)
	runtime.KeepAlive(server)
	runtime.KeepAlive(bypass)
	runtime.KeepAlive(autoConfigURL)
	return err
}

// clearSystemProxy switches the manual proxy and any explicit PAC URL off by
// writing only the flags option, leaving the server, bypass and PAC strings
// alone. This is the partial update the official Windows client performs when
// it releases the system proxy.
func clearSystemProxy() error {
	flags := perConnOption{option: internetPerConnFlags}
	flags.value.raw = uintptr(proxyTypeDirect | proxyTypeAutoDetect)
	return applyProxyOptions([]perConnOption{flags})
}

// restoreAfterProxyVerificationFailure reports a failed verification and, when
// the rollback itself fails too, both errors. The rollback error must stay
// visible: it means the user's proxy settings are still this controller's.
func restoreAfterProxyVerificationFailure(original proxySettings, verifyErr error, restore func(proxySettings) error) error {
	if restoreErr := restore(original); restoreErr != nil {
		return errors.Join(
			fmt.Errorf("verify system proxy: %w", verifyErr),
			fmt.Errorf("restore original system proxy: %w", restoreErr),
		)
	}
	return fmt.Errorf("verify system proxy: %w", verifyErr)
}

// verifyAppliedProxySettings compares what WinINet reports against what was
// requested. Checking only that the query did not fail accepts a write that a
// group policy or another program immediately reverted.
func verifyAppliedProxySettings(desired proxySettings) error {
	applied, err := queryProxySettings()
	if err != nil {
		return err
	}
	if applied.flags != desired.flags {
		return fmt.Errorf("system proxy flags = %#x, want %#x", applied.flags, desired.flags)
	}
	if applied.server != desired.server {
		return fmt.Errorf("system proxy server = %q, want %q", applied.server, desired.server)
	}
	if applied.bypass != desired.bypass {
		return fmt.Errorf("system proxy bypass = %q, want %q", applied.bypass, desired.bypass)
	}
	return nil
}

// EnableSystemProxy points the system proxy at the local mixed inbound and
// keeps the settings it replaced.
//
// The bypass list is preserved: it is not part of the on/off switch, and a
// controller that overwrites it destroys exceptions the user set up for other
// tools. The proxy flag is replaced rather than extended, so a machine that was
// on plain DIRECT does not come back with automatic configuration discovery
// turned on.
func EnableSystemProxy(host string, port int) (ProxySession, error) {
	if strings.TrimSpace(host) == "" || strings.IndexByte(host, 0) >= 0 || port < 1 || port > 65535 {
		return ProxySession{}, fmt.Errorf("invalid system proxy address")
	}
	original, err := queryProxySettings()
	if err != nil {
		return ProxySession{}, err
	}
	desired := original
	desired.flags = proxyTypeProxy
	desired.server = "http://" + net.JoinHostPort(host, fmt.Sprint(port))
	desired.autoConfigURL = ""
	if err := setProxySettings(desired); err != nil {
		return ProxySession{}, err
	}
	if err := verifyAppliedProxySettings(desired); err != nil {
		return ProxySession{}, restoreAfterProxyVerificationFailure(original, err, setProxySettings)
	}
	return ProxySession{original: original}, nil
}

// RestoreSystemProxy puts back the settings captured before this controller
// took the system proxy over. A session without captured settings falls back to
// clearing the proxy, so a handoff from an older build still releases it.
func RestoreSystemProxy(session ProxySession) (bool, error) {
	if session.original.flags == 0 && session.original.server == "" && session.original.autoConfigURL == "" {
		if err := clearSystemProxy(); err != nil {
			return false, err
		}
		return true, nil
	}
	if err := setProxySettings(session.original); err != nil {
		return false, err
	}
	return true, nil
}
