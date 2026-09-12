package clash

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Selector struct {
	Name        string
	All         []string
	Now         string
	ResolvedNow string
}

type Client struct {
	BaseURL    string
	Secret     string
	HTTPClient *http.Client
	Timeout    time.Duration

	defaultClient *http.Client
}

func NewClient(controller, secret string) *Client {
	controller = strings.TrimSpace(controller)
	if controller != "" && !strings.Contains(controller, "://") {
		controller = "http://" + controller
	}
	client := &Client{BaseURL: strings.TrimRight(controller, "/"), Secret: secret, Timeout: time.Second}
	client.defaultClient = &http.Client{Timeout: client.Timeout, Transport: &http.Transport{Proxy: nil}}
	return client
}

func (c *Client) client() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	if c.defaultClient == nil {
		c.defaultClient = &http.Client{Timeout: c.Timeout, Transport: &http.Transport{Proxy: nil}}
	}
	return c.defaultClient
}

func (c *Client) Do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	if c == nil || c.BaseURL == "" {
		return nil, errors.New("Clash API is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Secret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %s: HTTP %s", method, path, resp.Status)
	}
	return data, nil
}

func (c *Client) CheckReady(ctx context.Context) error {
	_, err := c.Do(ctx, http.MethodGet, "/version", nil)
	return err
}

type proxyState struct {
	Name string
	Type string
	All  []string
	Now  string
}

// ParseSelectors preserves both the order of the proxies object and every
// option in each selector's all array. No recursive provider traversal is
// attempted: compatible cores already expose provider-backed nodes in all.
// When a selector points at another runtime group, ResolvedNow records the
// group's current leaf for display only; it is never used as a switch value.
func ParseSelectors(data []byte) ([]Selector, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var token json.Token
	var err error
	if token, err = dec.Token(); err != nil {
		return nil, fmt.Errorf("invalid /proxies response: %w", err)
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return nil, errors.New("invalid /proxies response: root is not an object")
	}
	var proxies []proxyState
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("invalid /proxies response: non-string key")
		}
		if key != "proxies" {
			var ignored json.RawMessage
			if err := dec.Decode(&ignored); err != nil {
				return nil, err
			}
			continue
		}
		proxyToken, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("invalid proxies object: %w", err)
		}
		proxyObject, ok := proxyToken.(json.Delim)
		if !ok || proxyObject != '{' {
			return nil, errors.New("invalid proxies object: not an object")
		}
		for dec.More() {
			nameToken, err := dec.Token()
			if err != nil {
				return nil, err
			}
			name, ok := nameToken.(string)
			if !ok {
				return nil, errors.New("invalid proxies object: non-string key")
			}
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				return nil, err
			}
			var p struct {
				Type string   `json:"type"`
				All  []string `json:"all"`
				Now  string   `json:"now"`
			}
			if err := json.Unmarshal(raw, &p); err != nil {
				continue
			}
			proxies = append(proxies, proxyState{Name: name, Type: p.Type, All: append([]string(nil), p.All...), Now: p.Now})
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if extra, err := dec.Token(); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("invalid /proxies response: extra token %v", extra)
		}
		return nil, err
	}
	byName := make(map[string]proxyState, len(proxies))
	for _, proxy := range proxies {
		byName[proxy.Name] = proxy
	}
	selectors := make([]Selector, 0, len(proxies))
	for _, proxy := range proxies {
		if !strings.EqualFold(proxy.Type, "Selector") {
			continue
		}
		selector := Selector{Name: proxy.Name, All: proxy.All, Now: proxy.Now}
		if resolved, ok := resolveProxyNow(proxy.Now, byName, map[string]bool{}); ok && resolved != proxy.Now {
			selector.ResolvedNow = resolved
		}
		selectors = append(selectors, selector)
	}
	return selectors, nil
}

func resolveProxyNow(name string, proxies map[string]proxyState, visiting map[string]bool) (string, bool) {
	proxy, ok := proxies[name]
	if !ok {
		return name, true
	}
	if visiting[name] {
		return "", false
	}
	if proxy.Now == "" {
		return name, true
	}
	visiting[name] = true
	defer delete(visiting, name)
	return resolveProxyNow(proxy.Now, proxies, visiting)
}

func (c *Client) FetchSelectors(ctx context.Context) ([]Selector, error) {
	data, err := c.Do(ctx, http.MethodGet, "/proxies", nil)
	if err != nil {
		return nil, err
	}
	return ParseSelectors(data)
}

func (c *Client) SwitchSelector(ctx context.Context, selector, value string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	path := "/proxies/" + url.PathEscape(selector)
	body, err := json.Marshal(struct {
		Name string `json:"name"`
	}{value})
	if err != nil {
		return err
	}
	if _, err := c.Do(ctx, http.MethodPut, path, body); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Match the reference controller: flush existing connections after a
	// successful selector switch, but do not turn a successful switch into a
	// failure if an older/custom core omits this endpoint.
	if _, err := c.Do(ctx, http.MethodDelete, "/connections", nil); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func ContainsOption(selector Selector, value string) bool {
	for _, option := range selector.All {
		if option == value {
			return true
		}
	}
	return false
}

func UseNested(layout string, selectors []Selector) bool {
	switch strings.ToLower(strings.TrimSpace(layout)) {
	case "nested":
		return true
	case "flat":
		return false
	default:
		rows := 2 * len(selectors)
		for _, selector := range selectors {
			rows += len(selector.All)
		}
		return rows > 20
	}
}
