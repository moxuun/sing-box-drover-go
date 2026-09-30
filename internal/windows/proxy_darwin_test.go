//go:build darwin

package windows

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestParseMacProxySetting(t *testing.T) {
	setting, err := parseMacProxySetting([]byte("Enabled: Yes\nServer: 127.0.0.1\nPort: 7890\nAuthenticated Proxy Enabled: 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !setting.Enabled || setting.Server != "127.0.0.1" || setting.Port != 7890 || !setting.Authenticated {
		t.Fatalf("unexpected proxy setting: %#v", setting)
	}
}

func TestParseMacProxySettingRejectsInvalidPort(t *testing.T) {
	if _, err := parseMacProxySetting([]byte("Enabled: No\nPort: nope\n")); err == nil {
		t.Fatal("invalid port unexpectedly parsed")
	}
}

func TestParseMacAutoProxyAndDiscovery(t *testing.T) {
	auto, err := parseMacAutoProxy([]byte("URL: http://127.0.0.1:9090/proxy.pac\nEnabled: Yes\n"))
	if err != nil {
		t.Fatal(err)
	}
	if auto.URL != "http://127.0.0.1:9090/proxy.pac" || !auto.Enabled {
		t.Fatalf("unexpected auto proxy: %#v", auto)
	}
	enabled, err := parseMacEnabled([]byte("Auto Proxy Discovery: Off\n"))
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Fatal("disabled discovery parsed as enabled")
	}
	enabled, err = parseMacEnabled([]byte("Auto Proxy Discovery: On\n"))
	if err != nil || !enabled {
		t.Fatalf("enabled discovery = %v, err=%v", enabled, err)
	}
	if _, err := parseMacEnabled([]byte("Unexpected: value\n")); err == nil {
		t.Fatal("missing discovery value unexpectedly parsed")
	}
}

func TestParseMacBypassDomains(t *testing.T) {
	domains := parseMacBypassDomains([]byte("127.0.0.1\nlocalhost\n*.local\n"))
	if strings.Join(domains, ",") != "127.0.0.1,localhost,*.local" {
		t.Fatalf("unexpected bypass domains: %#v", domains)
	}
	if domains := parseMacBypassDomains([]byte("There aren't any bypass domains set on Wi-Fi.\n")); len(domains) != 0 {
		t.Fatalf("empty bypass output parsed as %#v", domains)
	}
}

func TestParseNetworkServiceOrder(t *testing.T) {
	output := []byte(`An asterisk (*) denotes that a network service is disabled.
(1) USB LAN
(Hardware Port: USB 10/100/1000 LAN, Device: en8)

(*2) Disabled VPN
(Hardware Port: VPN, Device: utun9)

(3) Wi-Fi
(Hardware Port: Wi-Fi, Device: en0)
`)
	services := parseNetworkServiceOrder(output)
	if len(services) != 3 || services[0].Name != "USB LAN" || services[0].Device != "en8" || !services[0].Enabled ||
		services[1].Name != "Disabled VPN" || services[1].Device != "utun9" || services[1].Enabled ||
		services[2].Name != "Wi-Fi" || services[2].Device != "en0" || !services[2].Enabled {
		t.Fatalf("unexpected services: %#v", services)
	}
	if got := parseDefaultInterface([]byte("gateway: 192.168.1.1\ninterface: en0\n")); got != "en0" {
		t.Fatalf("default interface = %q", got)
	}
}

func TestSelectNetworkServiceFallsBackFromTunDefaultRoute(t *testing.T) {
	services := []macNetworkService{
		{Name: "Disabled VPN", Device: "utun9", Enabled: false},
		{Name: "USB LAN", Device: "en8", Enabled: true},
		{Name: "Wi-Fi", Device: "en0", Enabled: true},
	}
	active := func(device string) bool { return device == "en0" }
	if got := selectNetworkService("utun0", services, active); got != "Wi-Fi" {
		t.Fatalf("fallback service = %q, want Wi-Fi", got)
	}
	if got := selectNetworkService("en8", services, active); got != "USB LAN" {
		t.Fatalf("default-route service = %q, want USB LAN", got)
	}
	if got := selectNetworkService("utun0", services, func(string) bool { return false }); got != "" {
		t.Fatalf("inactive services = %q, want no selection", got)
	}
}

func TestValidateMacProxyAddress(t *testing.T) {
	for _, test := range []struct {
		host string
		port int
		ok   bool
	}{
		{"127.0.0.1", 7890, true},
		{"localhost", 1080, true},
		{"", 7890, false},
		{"127.0.0.1", 0, false},
		{"127.0.0.1", 65536, false},
		{"127.0.0.1\nmalicious", 7890, false},
	} {
		err := validateMacProxyAddress(test.host, test.port)
		if (err == nil) != test.ok {
			t.Fatalf("validateMacProxyAddress(%q, %d) error = %v, ok=%v", test.host, test.port, err, test.ok)
		}
	}
}

func TestMacProxySettingsEqual(t *testing.T) {
	left := macProxySettings{
		Service:       "Wi-Fi",
		HTTP:          macProxySetting{Enabled: true, Server: "127.0.0.1", Port: 7890},
		BypassDomains: []string{"localhost", "*.local"},
	}
	right := left
	right.BypassDomains = append([]string(nil), left.BypassDomains...)
	if !macProxySettingsEqual(left, right) {
		t.Fatal("equal proxy settings reported as different")
	}
	right.BypassDomains[1] = "example.com"
	if macProxySettingsEqual(left, right) {
		t.Fatal("different bypass domains reported as equal")
	}
}

func TestMacProxySettingsEqualIgnoresInactiveEndpoint(t *testing.T) {
	left := macProxySettings{
		Service: "Wi-Fi",
		HTTP:    macProxySetting{Enabled: false, Server: "127.0.0.1", Port: 7890},
	}
	right := left
	right.HTTP = macProxySetting{Enabled: false, Server: "127.0.0.2", Port: 8080}
	if !macProxySettingsEqual(left, right) {
		t.Fatal("inactive proxy endpoints should be behaviorally equal")
	}
	left.HTTP.Enabled = true
	right.HTTP.Enabled = true
	if macProxySettingsEqual(left, right) {
		t.Fatal("active proxy endpoints with different addresses should not be equal")
	}
}

func TestEnableSystemProxyRequiresElevation(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	if _, err := EnableSystemProxy("127.0.0.1", 7890); !errors.Is(err, ErrElevationRequired) {
		t.Fatalf("EnableSystemProxy() error = %v, want %v", err, ErrElevationRequired)
	}
}

func TestEnableSystemProxyRoundTrip(t *testing.T) {
	if os.Getenv("SING_BOX_DROVER_TEST_SYSTEM_PROXY") != "1" {
		t.Skip("set SING_BOX_DROVER_TEST_SYSTEM_PROXY=1 and run as root to modify and restore the current macOS system proxy")
	}
	if os.Geteuid() != 0 {
		t.Skip("networksetup writes require root privileges")
	}
	service, err := activeNetworkService()
	if err != nil {
		t.Fatal(err)
	}
	before, err := readMacProxySettings(service)
	if err != nil {
		t.Fatal(err)
	}
	session, err := EnableSystemProxy("127.0.0.1", 7891)
	if err != nil {
		t.Fatal(err)
	}
	current, err := readMacProxySettings(service)
	if err != nil {
		t.Fatal(err)
	}
	if !current.HTTP.Enabled || current.HTTP.Server != "127.0.0.1" || current.HTTP.Port != 7891 ||
		!current.HTTPS.Enabled || !current.SOCKS.Enabled {
		t.Fatalf("system proxy was not applied: %#v", current)
	}
	restored, err := RestoreSystemProxy(session)
	if err != nil {
		t.Fatal(err)
	}
	if !restored {
		t.Fatal("system proxy restoration was relinquished")
	}
	after, err := readMacProxySettings(service)
	if err != nil {
		t.Fatal(err)
	}
	if !macProxySettingsEqual(after, before) {
		t.Fatalf("system proxy was not restored:\nbefore=%#v\nafter=%#v", before, after)
	}
}
