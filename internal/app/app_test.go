package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"sing-box-drover/internal/clash"
	"sing-box-drover/internal/config"
	"sing-box-drover/internal/core"
	"sing-box-drover/internal/state"
	platform "sing-box-drover/internal/windows"
)

func TestRetryInstanceAcquisitionWaitsForRelease(t *testing.T) {
	now := time.Time{}
	attempts := 0
	var sleeps []time.Duration
	instance, first, err := retryInstanceAcquisition(
		func() (*platform.SingleInstance, bool, error) {
			attempts++
			if attempts < 3 {
				return nil, false, nil
			}
			return nil, true, nil
		},
		10*time.Millisecond,
		2*time.Millisecond,
		func() time.Time { return now },
		func(delay time.Duration) {
			sleeps = append(sleeps, delay)
			now = now.Add(delay)
		},
	)
	if err != nil || !first || instance != nil {
		t.Fatalf("unexpected retry result: instance=%v first=%v err=%v", instance, first, err)
	}
	if attempts != 3 || len(sleeps) != 2 || sleeps[0] != 2*time.Millisecond || sleeps[1] != 2*time.Millisecond {
		t.Fatalf("retry timing mismatch: attempts=%d sleeps=%v", attempts, sleeps)
	}
}

func TestRetryInstanceAcquisitionTimesOut(t *testing.T) {
	now := time.Time{}
	attempts := 0
	_, first, err := retryInstanceAcquisition(
		func() (*platform.SingleInstance, bool, error) {
			attempts++
			return nil, false, nil
		},
		5*time.Millisecond,
		2*time.Millisecond,
		func() time.Time { return now },
		func(delay time.Duration) { now = now.Add(delay) },
	)
	if err != nil || first || attempts != 4 {
		t.Fatalf("unexpected timeout result: first=%v err=%v attempts=%d", first, err, attempts)
	}
}

func TestRetryInstanceAcquisitionPropagatesError(t *testing.T) {
	want := errors.New("mutex failure")
	_, first, err := retryInstanceAcquisition(
		func() (*platform.SingleInstance, bool, error) { return nil, false, want },
		10*time.Second,
		50*time.Millisecond,
		time.Now,
		func(time.Duration) { t.Fatal("sleep should not run after an acquisition error") },
	)
	if first || !errors.Is(err, want) {
		t.Fatalf("unexpected error result: first=%v err=%v", first, err)
	}
}

func TestStartupTunRequested(t *testing.T) {
	tests := []struct {
		name    string
		config  config.SingBoxConfig
		options config.Options
		flags   Flags
		want    bool
	}{
		{name: "configured start mode", config: config.SingBoxConfig{HasTunInbound: true}, options: config.Options{TunStartMode: "on"}, want: true},
		{name: "command line flag", config: config.SingBoxConfig{HasTunInbound: true}, flags: Flags{Tun: true}, want: true},
		{name: "no tun inbound", options: config.Options{TunStartMode: "on"}, want: false},
		{name: "disabled", config: config.SingBoxConfig{HasTunInbound: true}, options: config.Options{TunStartMode: "off"}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := startupTunRequested(test.config, test.options, test.flags); got != test.want {
				t.Fatalf("startupTunRequested() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRefreshSelectorsRestoresOnlyExistingOptions(t *testing.T) {
	var switched []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"proxies":{"proxy":{"type":"Selector","all":["机场A","香港01"],"now":"机场A"},"other":{"type":"Selector","all":["东京01"],"now":"东京01"}}}`))
		case http.MethodPut:
			switched = append(switched, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	stateFile := state.Load(t.TempDir() + "/state.json")
	if err := stateFile.SyncSelectors(map[string]string{"proxy": "香港01", "other": "不存在"}, []string{"proxy", "other"}); err != nil {
		t.Fatal(err)
	}
	a := &App{
		options:   config.Options{SelectorPersist: true},
		state:     stateFile,
		api:       clash.NewClient(server.URL, "token"),
		selectors: []clash.Selector{{Name: "old", All: []string{"x"}, Now: "x"}},
		events:    make(chan Event, 4),
	}
	got, err := a.RefreshSelectors(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Now != "香港01" || got[1].Now != "东京01" {
		t.Fatalf("restored selectors mismatch: %#v", got)
	}
	if !a.apiReady {
		t.Fatal("successful Clash API request did not mark it ready")
	}
	if len(switched) != 1 || switched[0] != "/proxies/proxy" {
		t.Fatalf("unexpected restore requests: %#v", switched)
	}
	if saved, ok := stateFile.GetSelector("other"); !ok || saved != "东京01" {
		t.Fatalf("stale state was not reconciled: %q %v", saved, ok)
	}
}

func TestApplyPersistedStaticIgnoresStaleOptions(t *testing.T) {
	saved := state.Load(t.TempDir() + "/state.json")
	if err := saved.SyncSelectors(map[string]string{"proxy": "香港01", "other": "不存在"}, []string{"proxy", "other"}); err != nil {
		t.Fatal(err)
	}
	selectors := []clash.Selector{
		{Name: "proxy", All: []string{"机场A", "香港01"}, Now: "机场A"},
		{Name: "other", All: []string{"东京01"}, Now: "东京01"},
	}
	applyPersistedStatic(selectors, saved)
	if selectors[0].Now != "香港01" || selectors[1].Now != "东京01" {
		t.Fatalf("static persistence mismatch: %#v", selectors)
	}
}

func TestRefreshSelectorsKeepsCacheWhenAPIFails(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			_, _ = w.Write([]byte(`{"proxies":{"proxy":{"type":"Selector","all":["香港01"],"now":"香港01"}}}`))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	a := &App{api: clash.NewClient(server.URL, "token"), events: make(chan Event, 1)}
	first, err := a.RefreshSelectors(context.Background())
	if err != nil || len(first) != 1 || first[0].Name != "proxy" {
		t.Fatalf("initial refresh failed: %#v %v", first, err)
	}
	second, err := a.RefreshSelectors(context.Background())
	if err == nil || len(second) != 1 || second[0].Now != "香港01" {
		t.Fatalf("cache was not retained on failure: %#v %v", second, err)
	}
}

func TestAPIPollCancellationStopsStaleRetryAndRestore(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		close(started)
		<-release
		_, _ = w.Write([]byte(`{"proxies":{"fresh":{"type":"Selector","all":["node"],"now":"node"}}}`))
	}))
	defer server.Close()
	a := &App{
		api:       clash.NewClient(server.URL, "token"),
		options:   config.Options{SelectorPersist: true},
		selectors: []clash.Selector{{Name: "cached", All: []string{"old"}, Now: "old"}},
		events:    make(chan Event, 1),
	}
	pollCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		a.waitForAPI(pollCtx)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("readiness poll did not issue its first request")
	}
	cancel()
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("superseded readiness poll did not stop")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("superseded readiness poll retried: %d requests", got)
	}
	got := a.Selectors()
	if len(got) != 1 || got[0].Name != "cached" || got[0].Now != "old" {
		t.Fatalf("superseded readiness poll changed the selector cache: %#v", got)
	}
}

func TestCoreFailureDisablesActiveProxyWithoutChangingTunState(t *testing.T) {
	called := false
	a := &App{
		proxyActive: true,
		tunActive:   true,
		systemProxyDisabler: func() error {
			called = true
			return nil
		},
		events: make(chan Event, 1),
	}

	a.handleCoreEvent(core.Event{Kind: core.EventState, State: core.StateFailed, Message: "core exited"})

	if !called {
		t.Fatal("core failure did not disable the active system proxy")
	}
	if a.SystemProxyActive() {
		t.Fatal("system proxy state remained active after successful cleanup")
	}
	if !a.TunActive() {
		t.Fatal("core failure unexpectedly changed independent TUN state")
	}
}

func TestCoreFailureDoesNotDisableInactiveProxy(t *testing.T) {
	called := false
	a := &App{
		systemProxyDisabler: func() error {
			called = true
			return nil
		},
		events: make(chan Event, 1),
	}

	a.handleCoreEvent(core.Event{Kind: core.EventState, State: core.StateFailed, Message: "core failed to start"})

	if called {
		t.Fatal("core failure disabled a proxy that the controller had not enabled")
	}
	if a.SystemProxyActive() {
		t.Fatal("inactive proxy state changed")
	}
}

func TestCoreFailureKeepsProxyStateWhenCleanupFails(t *testing.T) {
	want := errors.New("settings update failed")
	a := &App{
		proxyActive: true,
		systemProxyDisabler: func() error {
			return want
		},
		events: make(chan Event, 1),
	}

	a.handleCoreEvent(core.Event{Kind: core.EventState, State: core.StateFailed, Message: "core exited"})

	if !a.SystemProxyActive() {
		t.Fatal("proxy state was cleared even though system cleanup failed")
	}
}
