//go:build windows

package windows

import (
	"fmt"
	"unsafe"

	winapi "golang.org/x/sys/windows"
)

const (
	internetPerConnFlags          = 1
	internetPerConnProxyServer    = 2
	internetPerConnProxyBypass    = 3
	proxyTypeDirect               = 0x00000001
	proxyTypeProxy                = 0x00000002
	internetOptionSettingsChanged = 39
	internetOptionPerConnection   = 75
	internetOptionRefresh         = 37
)

type perConnOption struct {
	option uint32
	_      uint32
	value  uintptr
}

type perConnOptionList struct {
	size        uint32
	_           uint32
	connection  *uint16
	optionCount uint32
	optionError uint32
	options     *perConnOption
}

var (
	wininet           = winapi.NewLazySystemDLL("wininet.dll")
	internetSetOption = wininet.NewProc("InternetSetOptionW")
)

func setProxy(proxy string) error {
	var options [3]perConnOption
	list := perConnOptionList{size: uint32(unsafe.Sizeof(perConnOptionList{})), options: &options[0]}
	if proxy == "" {
		list.optionCount = 1
		options[0] = perConnOption{option: internetPerConnFlags, value: proxyTypeDirect}
	} else {
		proxyPtr, err := winapi.UTF16PtrFromString(proxy)
		if err != nil {
			return err
		}
		bypassPtr, err := winapi.UTF16PtrFromString("<local>")
		if err != nil {
			return err
		}
		list.optionCount = 3
		options[0] = perConnOption{option: internetPerConnFlags, value: proxyTypeDirect | proxyTypeProxy}
		options[1] = perConnOption{option: internetPerConnProxyServer, value: uintptr(unsafe.Pointer(proxyPtr))}
		options[2] = perConnOption{option: internetPerConnProxyBypass, value: uintptr(unsafe.Pointer(bypassPtr))}
	}
	ok, _, callErr := internetSetOption.Call(0, internetOptionPerConnection, uintptr(unsafe.Pointer(&list)), uintptr(unsafe.Sizeof(list)))
	if ok == 0 {
		if callErr != nil {
			return fmt.Errorf("InternetSetOption: %w", callErr)
		}
		return fmt.Errorf("InternetSetOption failed")
	}
	_, _, _ = internetSetOption.Call(0, internetOptionSettingsChanged, 0, 0)
	_, _, _ = internetSetOption.Call(0, internetOptionRefresh, 0, 0)
	return nil
}

func EnableSystemProxy(host string, port int) error {
	if host == "" || port < 1 || port > 65535 {
		return fmt.Errorf("invalid system proxy address")
	}
	address := fmt.Sprintf("%s:%d", host, port)
	return setProxy(fmt.Sprintf("http=%s;https=%s;socks=%s", address, address, address))
}

func DisableSystemProxy() error { return setProxy("") }
