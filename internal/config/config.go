package config

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type ConfigSource struct {
	FilePath string
	JSONText string
}

type Selector struct {
	Name         string
	Outbounds    []string
	DefaultName  string
	DefaultIndex int
}

type ClashAPIConfig struct {
	ExternalController string
	Secret             string
}

func (c ClashAPIConfig) IsConfigured() bool { return strings.TrimSpace(c.ExternalController) != "" }

type SingBoxConfig struct {
	ClashAPI       ClashAPIConfig
	Selectors      []Selector
	HasSelector    bool
	ProxyHost      string
	ProxyPort      int
	HasTunInbound  bool
	JSONWithTUN    string
	JSONWithoutTUN string
}

type Options struct {
	SBDir              string
	SBConfigFile       string
	HomepageURL        string
	TunStartMode       string
	SystemProxyAuto    bool
	SelectorMenuLayout string
	LogFile            string
	BaseDir            string
}

const defaultHomepageURL = "https://github.com/hdrover/sing-box-drover"

func DefaultOptions() Options {
	return Options{SBConfigFile: "config.json", HomepageURL: defaultHomepageURL, TunStartMode: "off", SelectorMenuLayout: "auto"}
}

func validateHomepageURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("invalid homepage-url: %q (use an http:// or https:// URL)", value)
	}
	return nil
}

// parseBool uses on/off as the canonical spelling. Numeric and common textual
// aliases remain accepted so existing local configurations can be migrated
// without changing behavior, while unknown values are rejected.
func parseBool(value string, fallback bool) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return fallback, nil
	case "1", "true", "yes", "on", "enabled":
		return true, nil
	case "0", "false", "no", "off", "disabled":
		return false, nil
	default:
		return fallback, fmt.Errorf("invalid boolean value %q (use on or off)", value)
	}
}

func parseTunStartMode(value string) (string, error) {
	enabled, err := parseBool(value, false)
	if err != nil {
		return "", fmt.Errorf("invalid tun-start-mode: %w", err)
	}
	if enabled {
		return "on", nil
	}
	return "off", nil
}

// LoadOptions reads the small INI file used by the reference application.
// Unknown keys/sections are ignored to stay compatible with future releases.
func LoadOptions(path string) (Options, error) {
	o := DefaultOptions()
	o.BaseDir = filepath.Dir(path)
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			resolveOptions(&o)
			return o, nil
		}
		return o, err
	}
	section := ""
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		if section != "" && section != "sing-box-drover" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch key {
		case "sb-dir":
			o.SBDir = value
		case "sb-config-file":
			o.SBConfigFile = value
		case "homepage-url":
			if value == "" {
				o.HomepageURL = defaultHomepageURL
				break
			}
			if err := validateHomepageURL(value); err != nil {
				return o, err
			}
			o.HomepageURL = value
		case "tun-start-mode":
			mode, err := parseTunStartMode(value)
			if err != nil {
				return o, err
			}
			o.TunStartMode = mode
		case "system-proxy-auto":
			enabled, err := parseBool(value, o.SystemProxyAuto)
			if err != nil {
				return o, fmt.Errorf("invalid system-proxy-auto: %w", err)
			}
			o.SystemProxyAuto = enabled
		case "selector-menu-layout":
			switch strings.ToLower(value) {
			case "flat", "nested":
				o.SelectorMenuLayout = strings.ToLower(value)
			default:
				o.SelectorMenuLayout = "auto"
			}
		case "log-file":
			o.LogFile = value
		}
	}
	resolveOptions(&o)
	return o, nil
}

func isWindowsAbs(path string) bool {
	return (len(path) >= 2 && ((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) && path[1] == ':') || strings.HasPrefix(path, `\\`)
}

func resolvePath(base, value string) string {
	if value == "" {
		return value
	}
	if filepath.IsAbs(value) || isWindowsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(base, value))
}

func resolveOptions(o *Options) {
	if o.BaseDir == "" {
		o.BaseDir = "."
	}
	if o.SBDir == "" {
		o.SBDir = o.BaseDir
	} else {
		o.SBDir = resolvePath(o.BaseDir, o.SBDir)
	}
	if o.SBConfigFile == "" {
		o.SBConfigFile = "config.json"
	}
	if !filepath.IsAbs(o.SBConfigFile) && !isWindowsAbs(o.SBConfigFile) {
		configured := o.SBConfigFile
		baseCandidate := resolvePath(o.BaseDir, configured)
		if _, err := os.Stat(baseCandidate); err == nil {
			o.SBConfigFile = baseCandidate
		} else {
			sbCandidate := resolvePath(o.SBDir, configured)
			if _, err := os.Stat(sbCandidate); err == nil {
				o.SBConfigFile = sbCandidate
			} else {
				// Always anchor a missing relative path to the executable/config
				// directory. Task Scheduler may start us with an unrelated CWD.
				o.SBConfigFile = baseCandidate
			}
		}
	}
	if o.LogFile != "" && !filepath.IsAbs(o.LogFile) && !isWindowsAbs(o.LogFile) {
		o.LogFile = resolvePath(o.BaseDir, o.LogFile)
	}
}

func readUTF8(data []byte) string {
	if len(data) >= 3 && data[0] == 0xef && data[1] == 0xbb && data[2] == 0xbf {
		data = data[3:]
	}
	return string(data)
}

func ReadConfigSource(path string) (ConfigSource, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ConfigSource{}, fmt.Errorf("configuration file not found")
		}
		return ConfigSource{}, fmt.Errorf("failed to read configuration file: %w", err)
	}
	return ConfigSource{FilePath: path, JSONText: readUTF8(data)}, nil
}

func valueString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return ""
	}
}

func randomSecret() string {
	var token [16]byte
	if _, err := rand.Read(token[:]); err == nil {
		return fmt.Sprintf("%x", token)
	}
	return fmt.Sprintf("sing-box-drover-%d", os.Getpid())
}

func mapObject(value any) map[string]any {
	obj, _ := value.(map[string]any)
	return obj
}

func addDefaultClashAPI(root map[string]any, cfg *SingBoxConfig) {
	if !cfg.HasSelector {
		return
	}
	if _, ok := root["experimental"]; !ok {
		root["experimental"] = map[string]any{}
	}
	experimental, ok := root["experimental"].(map[string]any)
	if !ok {
		return
	}
	if _, exists := experimental["clash_api"]; exists {
		return
	}
	cfg.ClashAPI = ClashAPIConfig{ExternalController: "127.0.0.1:9090", Secret: randomSecret()}
	experimental["clash_api"] = map[string]any{"external_controller": cfg.ClashAPI.ExternalController, "secret": cfg.ClashAPI.Secret}
}

func parseJSON(text string) (map[string]any, error) {
	var root any
	text = strings.TrimPrefix(text, "\ufeff")
	dec := json.NewDecoder(strings.NewReader(NormalizeJSON(text)))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("configuration file is corrupted or contains invalid JSON: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("invalid JSON: multiple root values")
		}
		return nil, fmt.Errorf("configuration file is corrupted or contains invalid JSON: %w", err)
	}
	obj := mapObject(root)
	if obj == nil {
		return nil, errors.New("invalid JSON")
	}
	return obj, nil
}

func ReadSingBoxConfig(text string) (SingBoxConfig, error) {
	root, err := parseJSON(text)
	if err != nil {
		return SingBoxConfig{}, err
	}
	cfg := SingBoxConfig{}
	if inbounds, ok := root["inbounds"].([]any); ok {
		for _, value := range inbounds {
			obj := mapObject(value)
			if obj == nil {
				continue
			}
			typ := valueString(obj["type"])
			switch {
			case strings.EqualFold(typ, "mixed"):
				cfg.ProxyHost = valueString(obj["listen"])
				port, _ := strconv.Atoi(valueString(obj["listen_port"]))
				cfg.ProxyPort = port
			case strings.EqualFold(typ, "tun"):
				cfg.HasTunInbound = true
			}
		}
	}
	if outbounds, ok := root["outbounds"].([]any); ok {
		for _, value := range outbounds {
			obj := mapObject(value)
			if obj == nil || !strings.EqualFold(valueString(obj["type"]), "selector") {
				continue
			}
			cfg.HasSelector = true
			sel := Selector{Name: valueString(obj["tag"]), DefaultName: valueString(obj["default"]), DefaultIndex: -1}
			if values, ok := obj["outbounds"].([]any); ok {
				for _, v := range values {
					name := valueString(v)
					if name == "" {
						continue
					}
					sel.Outbounds = append(sel.Outbounds, name)
					if name == sel.DefaultName {
						sel.DefaultIndex = len(sel.Outbounds) - 1
					}
				}
			}
			if len(sel.Outbounds) > 0 {
				cfg.Selectors = append(cfg.Selectors, sel)
			}
		}
	}
	cfg.ClashAPI = ClashAPIConfig{}
	if experimental, ok := root["experimental"].(map[string]any); ok {
		if clash, ok := experimental["clash_api"].(map[string]any); ok {
			cfg.ClashAPI.ExternalController = valueString(clash["external_controller"])
			cfg.ClashAPI.Secret = valueString(clash["secret"])
		}
	}
	addDefaultClashAPI(root, &cfg)
	withTun, err := json.Marshal(root)
	if err != nil {
		return SingBoxConfig{}, err
	}
	cfg.JSONWithTUN = string(withTun)
	if cfg.HasTunInbound {
		filtered := make([]any, 0)
		if inbounds, ok := root["inbounds"].([]any); ok {
			for _, value := range inbounds {
				obj := mapObject(value)
				if obj != nil && strings.EqualFold(valueString(obj["type"]), "tun") {
					continue
				}
				filtered = append(filtered, value)
			}
			root["inbounds"] = filtered
		}
	}
	withoutTun, err := json.Marshal(root)
	if err != nil {
		return SingBoxConfig{}, err
	}
	cfg.JSONWithoutTUN = string(withoutTun)
	return cfg, nil
}

func CheckSingBoxConfig(cfg SingBoxConfig) error {
	if cfg.ProxyHost == "" || cfg.ProxyPort < 1 {
		return errors.New("no suitable mixed inbound found for the system proxy")
	}
	return nil
}
