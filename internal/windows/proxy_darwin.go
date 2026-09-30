//go:build darwin

package windows

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const networksetupTimeout = 10 * time.Second

type macProxySetting struct {
	Enabled       bool
	Server        string
	Port          int
	Authenticated bool
}

type macAutoProxy struct {
	URL     string
	Enabled bool
}

type macProxySettings struct {
	Service       string
	HTTP          macProxySetting
	HTTPS         macProxySetting
	SOCKS         macProxySetting
	BypassDomains []string
	AutoProxy     macAutoProxy
	AutoDiscovery bool
}

// ProxySession records the settings replaced by this controller and the
// normalized settings networksetup actually applied.
type ProxySession struct {
	original macProxySettings
	applied  macProxySettings
}

func runMacCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	// Keep networksetup diagnostics and values stable regardless of the user's
	// terminal locale. GUI-launched apps usually inherit no locale, but users
	// can also start the controller from a localized shell.
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		return output, ctx.Err()
	}
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail != "" {
			return output, fmt.Errorf("%s: %w (%s)", name, err, detail)
		}
		return output, fmt.Errorf("%s: %w", name, err)
	}
	return output, nil
}

func runNetworksetup(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), networksetupTimeout)
	defer cancel()
	return runMacCommand(ctx, "/usr/sbin/networksetup", args...)
}

func parseMacProxySetting(output []byte) (macProxySetting, error) {
	setting := macProxySetting{Port: 0}
	for _, rawLine := range strings.Split(string(output), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(rawLine), ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "enabled":
			switch strings.ToLower(value) {
			case "yes", "on", "1":
				setting.Enabled = true
			case "no", "off", "0":
				setting.Enabled = false
			default:
				return macProxySetting{}, fmt.Errorf("invalid proxy enabled value %q", value)
			}
		case "server":
			if value != "(null)" {
				setting.Server = value
			}
		case "port":
			if value == "" || value == "(null)" {
				continue
			}
			port, err := strconv.Atoi(value)
			if err != nil {
				return macProxySetting{}, fmt.Errorf("invalid proxy port %q: %w", value, err)
			}
			setting.Port = port
		case "authenticated proxy enabled":
			switch strings.ToLower(value) {
			case "yes", "on", "1":
				setting.Authenticated = true
			case "no", "off", "0":
				setting.Authenticated = false
			default:
				return macProxySetting{}, fmt.Errorf("invalid authenticated proxy value %q", value)
			}
		}
	}
	return setting, nil
}

func parseMacAutoProxy(output []byte) (macAutoProxy, error) {
	auto := macAutoProxy{}
	for _, rawLine := range strings.Split(string(output), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(rawLine), ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "url":
			if value != "(null)" {
				auto.URL = value
			}
		case "enabled":
			switch strings.ToLower(value) {
			case "yes", "on", "1":
				auto.Enabled = true
			case "no", "off", "0":
				auto.Enabled = false
			default:
				return macAutoProxy{}, fmt.Errorf("invalid automatic proxy enabled value %q", value)
			}
		}
	}
	return auto, nil
}

func parseMacEnabled(output []byte) (bool, error) {
	for _, rawLine := range strings.Split(string(output), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(rawLine), ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if key != "enabled" && key != "auto proxy discovery" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "yes", "on", "1":
			return true, nil
		case "no", "off", "0":
			return false, nil
		default:
			return false, fmt.Errorf("invalid enabled value %q", strings.TrimSpace(value))
		}
	}
	return false, errors.New("enabled value not found")
}

func parseMacBypassDomains(output []byte) []string {
	var domains []string
	for _, rawLine := range strings.Split(string(output), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if strings.Contains(lower, "there aren't any bypass domains") || strings.Contains(lower, "there are no bypass domains") {
			return nil
		}
		domains = append(domains, line)
	}
	return domains
}

func parseDefaultInterface(output []byte) string {
	for _, rawLine := range strings.Split(string(output), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(rawLine), ":")
		if ok && strings.EqualFold(strings.TrimSpace(key), "interface") {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

type macNetworkService struct {
	Name    string
	Device  string
	Enabled bool
}

func parseNetworkServiceOrder(output []byte) []macNetworkService {
	var services []macNetworkService
	current := -1
	for _, rawLine := range strings.Split(string(output), "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "(") {
			if close := strings.Index(line, ") "); close >= 0 {
				name := strings.TrimSpace(line[close+2:])
				if name == "" {
					continue
				}
				index := strings.TrimSpace(line[1:close])
				enabled := !strings.HasPrefix(index, "*")
				index = strings.TrimPrefix(index, "*")
				if _, err := strconv.Atoi(index); err != nil {
					continue
				}
				services = append(services, macNetworkService{
					Name:    name,
					Enabled: enabled,
				})
				current = len(services) - 1
				continue
			}
		}
		if current < 0 {
			continue
		}
		lower := strings.ToLower(line)
		index := strings.Index(lower, "device:")
		if index < 0 {
			continue
		}
		device := strings.TrimSpace(line[index+len("device:"):])
		if end := strings.IndexByte(device, ')'); end >= 0 {
			device = strings.TrimSpace(device[:end])
		}
		if device != "" {
			services[current].Device = device
		}
	}
	return services
}

func selectNetworkService(defaultDevice string, services []macNetworkService, active func(string) bool) string {
	defaultDevice = strings.TrimSpace(defaultDevice)
	if defaultDevice != "" {
		for _, service := range services {
			if service.Enabled && service.Device == defaultDevice {
				return service.Name
			}
		}
	}
	for _, service := range services {
		if !service.Enabled || service.Device == "" {
			continue
		}
		if active != nil && active(service.Device) {
			return service.Name
		}
	}
	return ""
}

func interfaceHasUsableAddress(device string) bool {
	iface, err := net.InterfaceByName(device)
	if err != nil || iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
		return false
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return false
	}
	for _, addr := range addrs {
		var ip net.IP
		switch value := addr.(type) {
		case *net.IPNet:
			ip = value.IP
		case *net.IPAddr:
			ip = value.IP
		}
		if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
			continue
		}
		return true
	}
	return false
}

func activeNetworkService() (string, error) {
	orderOutput, err := runNetworksetup("-listnetworkserviceorder")
	if err != nil {
		return "", fmt.Errorf("list network services: %w", err)
	}
	services := parseNetworkServiceOrder(orderOutput)
	if len(services) == 0 {
		return "", errors.New("list network services: no services found")
	}

	defaultDevice := ""
	ctx, cancel := context.WithTimeout(context.Background(), networksetupTimeout)
	routeOutput, routeErr := runMacCommand(ctx, "/sbin/route", "-n", "get", "default")
	cancel()
	if routeErr == nil {
		defaultDevice = parseDefaultInterface(routeOutput)
	}
	service := selectNetworkService(defaultDevice, services, interfaceHasUsableAddress)
	if service == "" {
		if routeErr != nil {
			return "", fmt.Errorf("find active network service: %w", routeErr)
		}
		return "", errors.New("find active network service: no enabled service has a usable address")
	}
	return service, nil
}

func readMacProxySettings(service string) (macProxySettings, error) {
	settings := macProxySettings{Service: service}
	commands := []struct {
		args string
		dst  *macProxySetting
	}{
		{"-getwebproxy", &settings.HTTP},
		{"-getsecurewebproxy", &settings.HTTPS},
		{"-getsocksfirewallproxy", &settings.SOCKS},
	}
	for _, command := range commands {
		output, err := runNetworksetup(command.args, service)
		if err != nil {
			return macProxySettings{}, err
		}
		*command.dst, err = parseMacProxySetting(output)
		if err != nil {
			return macProxySettings{}, fmt.Errorf("%s: %w", command.args, err)
		}
	}
	output, err := runNetworksetup("-getproxybypassdomains", service)
	if err != nil {
		return macProxySettings{}, err
	}
	settings.BypassDomains = parseMacBypassDomains(output)
	output, err = runNetworksetup("-getautoproxyurl", service)
	if err != nil {
		return macProxySettings{}, err
	}
	settings.AutoProxy, err = parseMacAutoProxy(output)
	if err != nil {
		return macProxySettings{}, err
	}
	output, err = runNetworksetup("-getproxyautodiscovery", service)
	if err != nil {
		return macProxySettings{}, err
	}
	settings.AutoDiscovery, err = parseMacEnabled(output)
	if err != nil {
		return macProxySettings{}, err
	}
	return settings, nil
}

func usableMacProxySetting(value macProxySetting) macProxySetting {
	if strings.TrimSpace(value.Server) == "" {
		value.Server = "127.0.0.1"
	}
	if value.Port < 1 || value.Port > 65535 {
		value.Port = 7890
	}
	return value
}

func setMacProxyEntry(kind, stateKind, service string, value macProxySetting) error {
	if value.Authenticated {
		return fmt.Errorf("%s: refusing to replace an authenticated proxy because its credentials cannot be restored", kind)
	}
	if !value.Enabled {
		_, err := runNetworksetup(stateKind, service, "off")
		return err
	}
	value = usableMacProxySetting(value)
	if _, err := runNetworksetup(kind, service, value.Server, strconv.Itoa(value.Port)); err != nil {
		return err
	}
	_, err := runNetworksetup(stateKind, service, "on")
	return err
}

func setMacProxySettings(settings macProxySettings) error {
	if strings.TrimSpace(settings.Service) == "" {
		return errors.New("network service is empty")
	}
	var errs []error
	for _, command := range []struct {
		kind      string
		stateKind string
		value     macProxySetting
	}{
		{"-setwebproxy", "-setwebproxystate", settings.HTTP},
		{"-setsecurewebproxy", "-setsecurewebproxystate", settings.HTTPS},
		{"-setsocksfirewallproxy", "-setsocksfirewallproxystate", settings.SOCKS},
	} {
		if err := setMacProxyEntry(command.kind, command.stateKind, settings.Service, command.value); err != nil {
			errs = append(errs, err)
		}
	}
	bypass := append([]string(nil), settings.BypassDomains...)
	if len(bypass) == 0 {
		bypass = []string{"Empty"}
	}
	if _, err := runNetworksetup(append([]string{"-setproxybypassdomains", settings.Service}, bypass...)...); err != nil {
		errs = append(errs, err)
	}
	if settings.AutoProxy.URL == "" {
		if _, err := runNetworksetup("-setautoproxystate", settings.Service, "off"); err != nil {
			errs = append(errs, err)
		}
	} else {
		if _, err := runNetworksetup("-setautoproxyurl", settings.Service, settings.AutoProxy.URL); err != nil {
			errs = append(errs, err)
		} else {
			state := "off"
			if settings.AutoProxy.Enabled {
				state = "on"
			}
			if _, err := runNetworksetup("-setautoproxystate", settings.Service, state); err != nil {
				errs = append(errs, err)
			}
		}
	}
	discovery := "off"
	if settings.AutoDiscovery {
		discovery = "on"
	}
	if _, err := runNetworksetup("-setproxyautodiscovery", settings.Service, discovery); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func macProxySettingsEqual(left, right macProxySettings) bool {
	if left.Service != right.Service ||
		!macProxySettingEqual(left.HTTP, right.HTTP) ||
		!macProxySettingEqual(left.HTTPS, right.HTTPS) ||
		!macProxySettingEqual(left.SOCKS, right.SOCKS) ||
		left.AutoProxy != right.AutoProxy ||
		left.AutoDiscovery != right.AutoDiscovery ||
		len(left.BypassDomains) != len(right.BypassDomains) {
		return false
	}
	for i := range left.BypassDomains {
		if left.BypassDomains[i] != right.BypassDomains[i] {
			return false
		}
	}
	return true
}

func macProxySettingEqual(left, right macProxySetting) bool {
	if left.Enabled != right.Enabled {
		return false
	}
	if !left.Enabled {
		// networksetup cannot always preserve an inactive endpoint exactly, and
		// an inactive server/port has no effect on proxy behavior.
		return true
	}
	return left.Server == right.Server && left.Port == right.Port
}

func validateMacProxyAddress(host string, port int) error {
	if strings.TrimSpace(host) == "" || strings.ContainsAny(host, "\x00\r\n") || port < 1 || port > 65535 {
		return errors.New("invalid system proxy address")
	}
	return nil
}

func restoreMacSettingsAfterFailure(original macProxySettings, operationErr error) error {
	if restoreErr := setMacProxySettings(original); restoreErr != nil {
		return errors.Join(operationErr, fmt.Errorf("restore original system proxy: %w", restoreErr))
	}
	return operationErr
}

// EnableSystemProxy applies the mixed inbound to all macOS proxy protocols on
// the current primary network service. PAC and auto-discovery are disabled so
// they cannot override the explicit proxy, and their original values are
// restored on shutdown.
func EnableSystemProxy(host string, port int) (ProxySession, error) {
	if os.Geteuid() != 0 {
		return ProxySession{}, ErrElevationRequired
	}
	if err := validateMacProxyAddress(host, port); err != nil {
		return ProxySession{}, err
	}
	service, err := activeNetworkService()
	if err != nil {
		return ProxySession{}, err
	}
	original, err := readMacProxySettings(service)
	if err != nil {
		return ProxySession{}, err
	}
	if original.HTTP.Authenticated || original.HTTPS.Authenticated || original.SOCKS.Authenticated {
		return ProxySession{}, errors.New("authenticated system proxy settings cannot be replaced safely")
	}
	desired := original
	desired.HTTP = macProxySetting{Enabled: true, Server: host, Port: port}
	desired.HTTPS = desired.HTTP
	desired.SOCKS = desired.HTTP
	desired.AutoProxy.Enabled = false
	desired.AutoDiscovery = false
	if err := setMacProxySettings(desired); err != nil {
		return ProxySession{}, restoreMacSettingsAfterFailure(original, err)
	}
	applied, err := readMacProxySettings(service)
	if err != nil {
		return ProxySession{}, restoreMacSettingsAfterFailure(original, fmt.Errorf("verify system proxy: %w", err))
	}
	if !macProxySettingsEqual(applied, desired) {
		return ProxySession{}, restoreMacSettingsAfterFailure(original, errors.New("verify system proxy: applied settings differ from requested settings"))
	}
	return ProxySession{original: original, applied: applied}, nil
}

// RestoreSystemProxy restores the captured settings only while networksetup
// still contains the values applied by this controller. A user or another
// program changing the proxy takes ownership and must not be overwritten.
func RestoreSystemProxy(session ProxySession) (bool, error) {
	if os.Geteuid() != 0 {
		return false, ErrElevationRequired
	}
	current, err := readMacProxySettings(session.applied.Service)
	if err != nil {
		return false, err
	}
	if !macProxySettingsEqual(current, session.applied) {
		return false, nil
	}
	if err := setMacProxySettings(session.original); err != nil {
		return false, err
	}
	return true, nil
}
