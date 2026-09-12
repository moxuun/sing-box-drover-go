package clash

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestClientReusesDefaultHTTPClientAndTransport(t *testing.T) {
	c := NewClient("127.0.0.1:9090", "secret")
	first := c.client()
	second := c.client()
	if first != second || first.Transport != second.Transport {
		t.Fatalf("default HTTP client or transport was recreated: first=%p second=%p", first, second)
	}
	transport, ok := first.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("default transport type = %T, want *http.Transport", first.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("default Clash API transport must not use the system proxy")
	}
}

func TestParseSelectorsPreservesAPIOrderAndProviderNodes(t *testing.T) {
	data := []byte(`{"proxies":{"机场A":{"type":"Selector","all":["香港01","香港02"],"now":"香港02"},"unused":{"type":"Direct","all":["x"]},"机场B":{"type":"Selector","all":["东京01","东京02"],"now":"东京01"}}}`)
	got, err := ParseSelectors(data)
	if err != nil {
		t.Fatal(err)
	}
	want := []Selector{{Name: "机场A", All: []string{"香港01", "香港02"}, Now: "香港02"}, {Name: "机场B", All: []string{"东京01", "东京02"}, Now: "东京01"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selectors mismatch: %#v != %#v", got, want)
	}
}

func TestParseSelectorsResolvesRuntimeGroupForDisplay(t *testing.T) {
	data := []byte(`{"proxies":{"GLOBAL":{"type":"Selector","all":["proxy"],"now":"proxy"},"proxy":{"type":"Selector","all":["🎈 自动选择"],"now":"🎈 自动选择"},"🎈 自动选择":{"type":"URLTest","all":["node-a","node-b"],"now":"node-b"}}}`)
	got, err := ParseSelectors(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Now != "proxy" || got[1].Now != "🎈 自动选择" || got[0].ResolvedNow != "node-b" || got[1].ResolvedNow != "node-b" {
		t.Fatalf("runtime group state was not resolved for display: %#v", got)
	}
}

func TestParseSelectorsDisplaysDirectRuntimeLeaf(t *testing.T) {
	data := []byte(`{"proxies":{"proxy":{"type":"Selector","all":["🎈 自动选择"],"now":"🎈 自动选择"},"🎈 自动选择":{"type":"URLTest","all":["direct","node"],"now":"direct"},"direct":{"type":"Direct"}}}`)
	got, err := ParseSelectors(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ResolvedNow != "direct" {
		t.Fatalf("direct runtime leaf was not resolved for display: %#v", got)
	}
}

func TestSwitchSelectorUsesBearerPutAndFlush(t *testing.T) {
	var methods []string
	var paths []string
	var body map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		paths = append(paths, r.URL.EscapedPath())
		if r.Method == http.MethodPut {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	c := NewClient(server.URL, "secret")
	if err := c.SwitchSelector(context.Background(), "机场/A", "香港 01"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(methods, []string{http.MethodPut, http.MethodDelete}) || !reflect.DeepEqual(paths, []string{"/proxies/%E6%9C%BA%E5%9C%BA%2FA", "/connections"}) {
		t.Fatalf("requests mismatch: %v %v", methods, paths)
	}
	if body["name"] != "香港 01" {
		t.Fatalf("unexpected PUT body: %#v", body)
	}
}

func TestSwitchSelectorReturnsCancellationDuringConnectionFlush(t *testing.T) {
	flushStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodDelete {
			close(flushStarted)
			<-r.Context().Done()
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- NewClient(server.URL, "secret").SwitchSelector(ctx, "proxy", "node")
	}()
	select {
	case <-flushStarted:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("connection flush did not start")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("SwitchSelector() error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SwitchSelector() did not return after cancellation")
	}
}

func TestUseNestedAutoThreshold(t *testing.T) {
	if UseNested("flat", []Selector{{All: make([]string, 100)}}) {
		t.Fatal("flat layout should remain flat")
	}
	if !UseNested("auto", []Selector{{All: make([]string, 19)}}) {
		t.Fatal("auto layout should become nested above threshold")
	}
	if UseNested("auto", []Selector{{All: make([]string, 18)}}) {
		t.Fatal("auto layout should remain flat at threshold")
	}
}
