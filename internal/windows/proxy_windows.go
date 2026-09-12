//go:build windows

package windows

import (
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

// ProxySession records the settings replaced by this controller and the
// normalized settings WinINet actually applied.
type ProxySession struct {
	original proxySettings
	applied  proxySettings
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
	list := perConnOptionList{
		size:        uint32(unsafe.Sizeof(perConnOptionList{})),
		optionCount: uint32(len(options)),
		options:     &options[0],
	}
	ok, _, callErr := internetSetOption.Call(0, internetOptionPerConnection, uintptr(unsafe.Pointer(&list)), uintptr(unsafe.Sizeof(list)))
	runtime.KeepAlive(server)
	runtime.KeepAlive(bypass)
	runtime.KeepAlive(autoConfigURL)
	if ok == 0 {
		return proxyCallError("InternetSetOption", callErr)
	}
	_, _, _ = internetSetOption.Call(0, internetOptionSettingsChanged, 0, 0)
	_, _, _ = internetSetOption.Call(0, internetOptionProxyChanged, 0, 0)
	_, _, _ = internetSetOption.Call(0, internetOptionRefresh, 0, 0)
	return nil
}

func proxySettingsEqual(left, right proxySettings) bool {
	return left.flags == right.flags &&
		left.server == right.server &&
		left.bypass == right.bypass &&
		left.autoConfigURL == right.autoConfigURL
}

func EnableSystemProxy(host string, port int) (ProxySession, error) {
	if strings.TrimSpace(host) == "" || strings.IndexByte(host, 0) >= 0 || port < 1 || port > 65535 {
		return ProxySession{}, fmt.Errorf("invalid system proxy address")
	}
	original, err := queryProxySettings()
	if err != nil {
		return ProxySession{}, err
	}
	desired := original
	desired.flags = proxyTypeDirect | proxyTypeProxy
	desired.server = "http://" + net.JoinHostPort(host, fmt.Sprint(port))
	desired.bypass = "<local>"
	if err := setProxySettings(desired); err != nil {
		return ProxySession{}, err
	}
	applied, err := queryProxySettings()
	if err != nil {
		_ = setProxySettings(original)
		return ProxySession{}, fmt.Errorf("verify system proxy: %w", err)
	}
	return ProxySession{original: original, applied: applied}, nil
}

// RestoreSystemProxy restores the captured settings only while WinINet still
// contains the values applied by this controller. A user or another program
// changing the proxy takes ownership and must not be overwritten.
func RestoreSystemProxy(session ProxySession) (bool, error) {
	current, err := queryProxySettings()
	if err != nil {
		return false, err
	}
	if !proxySettingsEqual(current, session.applied) {
		return false, nil
	}
	if err := setProxySettings(session.original); err != nil {
		return false, err
	}
	return true, nil
}
