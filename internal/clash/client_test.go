package clash

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

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
