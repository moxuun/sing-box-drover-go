package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sing-box-drover/internal/clash"
	"sing-box-drover/internal/config"
	"sing-box-drover/internal/core"
	platform "sing-box-drover/internal/windows"
)

func TestParseFlagsIncludesProxyHandoff(t *testing.T) {
	flags := ParseFlags([]string{"-restart", "-tun", "-proxy"})
	if !flags.Restart || !flags.Tun || !flags.Proxy {
		t.Fatalf("handoff flags were not preserved: %+v", flags)
	}
}

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

func TestProbeResumeAPIRetriesTransientFailure(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"proxies":{}}`))
	}))
	defer server.Close()

	err := probeResumeAPI(context.Background(), clash.NewClient(server.URL, "token"), 5, time.Millisecond)
	if err != nil {
		t.Fatalf("probeResumeAPI() error = %v", err)
	}
	if calls != 3 {
		t.Fatalf("API probe calls = %d, want 3", calls)
	}
}

func TestProbeResumeAPIReturnsLastErrorAfterBoundedRetries(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	err := probeResumeAPI(context.Background(), clash.NewClient(server.URL, "token"), 3, time.Millisecond)
	if err == nil {
		t.Fatal("probeResumeAPI() unexpectedly succeeded")
	}
	if calls != 3 {
		t.Fatalf("API probe calls = %d, want 3", calls)
	}
	if !strings.Contains(err.Error(), "503") {
		t.Fatalf("last API error = %v, want HTTP 503 context", err)
	}
}

func TestProbeResumeAPIReturnsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("cancelled API probe issued a request")
	}))
	defer server.Close()

	err := probeResumeAPI(ctx, clash.NewClient(server.URL, "token"), 3, time.Millisecond)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("probeResumeAPI() error = %v, want context cancellation", err)
	}
}

func TestRecoverAfterResumeHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := &App{supervisor: core.NewSupervisor("", nil)}

	err := a.RecoverAfterResume(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RecoverAfterResume() error = %v, want context cancellation", err)
	}
}

func TestAPIPollCancellationStopsStaleRetry(t *testing.T) {
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
		proxyOwned:  true,
		tunActive:   true,
		systemProxyRestorer: func(platform.ProxySession) (bool, error) {
			called = true
			return true, nil
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
		systemProxyRestorer: func(platform.ProxySession) (bool, error) {
			called = true
			return true, nil
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
		proxyOwned:  true,
		systemProxyRestorer: func(platform.ProxySession) (bool, error) {
			return false, want
		},
		events: make(chan Event, 1),
	}

	a.handleCoreEvent(core.Event{Kind: core.EventState, State: core.StateFailed, Message: "core exited"})

	if !a.SystemProxyActive() {
		t.Fatal("proxy state was cleared even though system cleanup failed")
	}
}

func TestCloseRestoresManuallyEnabledSystemProxy(t *testing.T) {
	restores := 0
	a := &App{
		proxyActive: true,
		proxyOwned:  true,
		systemProxyRestorer: func(platform.ProxySession) (bool, error) {
			restores++
			return true, nil
		},
	}

	if err := a.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if restores != 1 {
		t.Fatalf("restore calls = %d, want 1", restores)
	}
	if a.SystemProxyActive() {
		t.Fatal("system proxy remained active after close")
	}
}

func TestRestoreRelinquishesOwnershipAfterExternalChange(t *testing.T) {
	a := &App{
		proxyActive: true,
		proxyOwned:  true,
		systemProxyRestorer: func(platform.ProxySession) (bool, error) {
			return false, nil
		},
	}

	if err := a.DisableSystemProxy(); err != nil {
		t.Fatalf("DisableSystemProxy() error = %v", err)
	}
	if a.SystemProxyActive() {
		t.Fatal("controller kept claiming externally changed proxy settings")
	}
}

func TestReplacementHandoffPreservesActiveSystemProxy(t *testing.T) {
	restored := 0
	launchedFlags := ""
	a := &App{
		proxyActive: true,
		proxyOwned:  true,
		systemProxyRestorer: func(platform.ProxySession) (bool, error) {
			restored++
			return true, nil
		},
		selfLauncher: func(flags string, elevated bool) error {
			launchedFlags = flags
			if !elevated {
				t.Fatal("replacement did not preserve elevation request")
			}
			return nil
		},
	}

	if err := a.launchReplacement("-restart -tun", true); err != nil {
		t.Fatalf("launchReplacement() error = %v", err)
	}
	if restored != 1 {
		t.Fatalf("restore calls = %d, want 1", restored)
	}
	if launchedFlags != "-restart -tun -proxy" {
		t.Fatalf("replacement flags = %q", launchedFlags)
	}
}

func TestReplacementLaunchFailureReenablesSystemProxy(t *testing.T) {
	launchErr := errors.New("launch failed")
	enabled := 0
	a := &App{
		config:      config.SingBoxConfig{ProxyHost: "127.0.0.1", ProxyPort: 10808},
		proxyActive: true,
		proxyOwned:  true,
		systemProxyRestorer: func(platform.ProxySession) (bool, error) {
			return true, nil
		},
		systemProxyEnabler: func(host string, port int) (platform.ProxySession, error) {
			enabled++
			return platform.ProxySession{}, nil
		},
		selfLauncher: func(string, bool) error { return launchErr },
	}

	err := a.launchReplacement("-restart", false)
	if !errors.Is(err, launchErr) {
		t.Fatalf("launchReplacement() error = %v, want %v", err, launchErr)
	}
	if enabled != 1 || !a.SystemProxyActive() {
		t.Fatalf("proxy was not recovered after launch failure: enabled=%d active=%v", enabled, a.SystemProxyActive())
	}
}

func TestClosedControllerRejectsLifecycleAndSelectorOperations(t *testing.T) {
	launched := false
	a := &App{
		closed: true,
		selfLauncher: func(string, bool) error {
			launched = true
			return nil
		},
		configChecker: func(string) error {
			t.Fatal("closed controller ran configuration check")
			return nil
		},
	}
	if err := a.Restart(); !errors.Is(err, errControllerClosed) {
		t.Fatalf("Restart() error = %v, want closed error", err)
	}
	if err := a.ToggleTun(true); !errors.Is(err, errControllerClosed) {
		t.Fatalf("ToggleTun() error = %v, want closed error", err)
	}
	if err := a.LaunchElevated(true); !errors.Is(err, errControllerClosed) {
		t.Fatalf("LaunchElevated() error = %v, want closed error", err)
	}
	if err := a.LaunchAutostartElevated(true); !errors.Is(err, errControllerClosed) {
		t.Fatalf("LaunchAutostartElevated() error = %v, want closed error", err)
	}
	if err := a.SwitchSelector(context.Background(), "proxy", "node"); !errors.Is(err, errControllerClosed) {
		t.Fatalf("SwitchSelector() error = %v, want closed error", err)
	}
	if launched {
		t.Fatal("closed controller launched a replacement")
	}
}
