package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
	if flags.NoTun || flags.NoProxy {
		t.Fatalf("enabling flags were read as explicit disabling flags: %+v", flags)
	}

	disabled := ParseFlags([]string{"-restart", "-no-tun", "-no-proxy"})
	if !disabled.Restart || !disabled.NoTun || !disabled.NoProxy {
		t.Fatalf("explicit off flags were not preserved: %+v", disabled)
	}
	if disabled.Tun || disabled.Proxy {
		t.Fatalf("disabling flags were read as enabling flags: %+v", disabled)
	}
}

func TestParseFlagsReadsAutostartOwner(t *testing.T) {
	flags := ParseFlags([]string{"-autostart-enable", "-autostart-owner", `PC\someone`})
	if !flags.AutostartEnable || flags.AutostartOwner != `PC\someone` {
		t.Fatalf("autostart owner was not read: %+v", flags)
	}
	// A missing value must not swallow the next flag or invent an owner.
	if got := ParseFlags([]string{"-autostart-owner"}); got.AutostartOwner != "" {
		t.Fatalf("dangling -autostart-owner produced %q", got.AutostartOwner)
	}
	if got := ParseFlags([]string{"-autostart-owner", "-autostart-disable"}); got.AutostartOwner != "-autostart-disable" {
		t.Fatalf("trailing flag was not consumed as the owner: %q", got.AutostartOwner)
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
		{name: "explicit off overrides the configured start mode", config: config.SingBoxConfig{HasTunInbound: true}, options: config.Options{TunStartMode: "on"}, flags: Flags{NoTun: true}, want: false},
		{name: "explicit off overrides the handoff flag", config: config.SingBoxConfig{HasTunInbound: true}, options: config.Options{TunStartMode: "on"}, flags: Flags{Tun: true, NoTun: true}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := startupTunRequested(test.config, test.options, test.flags); got != test.want {
				t.Fatalf("startupTunRequested() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestStartupProxyRequested(t *testing.T) {
	tests := []struct {
		name    string
		options config.Options
		flags   Flags
		want    bool
	}{
		{name: "configured automatic proxy", options: config.Options{SystemProxyAuto: true}, want: true},
		{name: "manual proxy handoff", flags: Flags{Proxy: true}, want: true},
		{name: "disabled", options: config.Options{SystemProxyAuto: false}, want: false},
		{name: "explicit off overrides the configured automatic proxy", options: config.Options{SystemProxyAuto: true}, flags: Flags{NoProxy: true}, want: false},
		{name: "explicit off overrides the handoff flag", options: config.Options{SystemProxyAuto: true}, flags: Flags{Proxy: true, NoProxy: true}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := startupProxyRequested(test.options, test.flags); got != test.want {
				t.Fatalf("startupProxyRequested() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestStaticSelectorsUseFirstOutboundWhenDefaultIsOmitted(t *testing.T) {
	got := staticSelectors([]config.Selector{{Name: "proxy", Outbounds: []string{"first", "second"}, DefaultIndex: -1}})
	if len(got) != 1 || got[0].Now != "first" {
		t.Fatalf("static selector default = %#v, want first outbound", got)
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

func TestRefreshSelectorsDoesNotApplyCanceledResponse(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	client := clash.NewClient("http://example.invalid", "token")
	client.HTTPClient = &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		close(started)
		<-release
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"proxies":{"fresh":{"type":"Selector","all":["new"],"now":"new"}}}`)),
			Request:    req,
		}, nil
	})}
	a := &App{
		api:       client,
		selectors: []clash.Selector{{Name: "cached", All: []string{"old"}, Now: "old"}},
		events:    make(chan Event, 1),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := a.RefreshSelectors(ctx)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("selector request did not start")
	}
	cancel()
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RefreshSelectors() error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("selector request did not finish")
	}
	got := a.Selectors()
	if len(got) != 1 || got[0].Name != "cached" || got[0].Now != "old" {
		t.Fatalf("canceled response changed selector cache: %#v", got)
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

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
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

func TestResumeRecoveryTimeoutCoversProbeBudget(t *testing.T) {
	probeBudget := time.Duration(resumeAPIProbeAttempts)*time.Second +
		time.Duration(resumeAPIProbeAttempts-1)*resumeAPIProbeInterval
	if ResumeRecoveryTimeout <= probeBudget {
		t.Fatalf("resume recovery timeout = %v, want more than probe budget %v", ResumeRecoveryTimeout, probeBudget)
	}
}

func TestProbeResumeAPIRejectsResponseAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := clash.NewClient("http://example.invalid", "token")
	client.HTTPClient = &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		cancel()
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"proxies":{}}`)),
			Request:    req,
		}, nil
	})}

	err := probeResumeAPI(ctx, client, 3, time.Millisecond)
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

func TestCleanCoreStopDisablesActiveProxy(t *testing.T) {
	called := false
	a := &App{
		proxyActive: true,
		proxyOwned:  true,
		systemProxyRestorer: func(platform.ProxySession) (bool, error) {
			called = true
			return true, nil
		},
		events: make(chan Event, 1),
	}

	a.handleCoreEvent(core.Event{Kind: core.EventState, State: core.StateStopped, Message: "core stopped"})

	if !called {
		t.Fatal("clean core stop did not disable the active system proxy")
	}
	if a.SystemProxyActive() {
		t.Fatal("system proxy state remained active after clean core stop")
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

func TestEnableSystemProxyRejectsStoppedCore(t *testing.T) {
	enabled := 0
	a := &App{
		config:     config.SingBoxConfig{ProxyHost: "127.0.0.1", ProxyPort: 10808},
		supervisor: core.NewSupervisor("", nil),
		systemProxyEnabler: func(string, int) (platform.ProxySession, error) {
			enabled++
			return platform.ProxySession{}, nil
		},
	}

	if err := a.EnableSystemProxy(); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("EnableSystemProxy() error = %v, want stopped-core error", err)
	}
	if enabled != 0 || a.SystemProxyActive() {
		t.Fatalf("stopped core changed proxy state: enabled=%d active=%v", enabled, a.SystemProxyActive())
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

func TestCloseReportsSystemProxyRestoreError(t *testing.T) {
	want := errors.New("settings update failed")
	a := &App{
		proxyActive: true,
		proxyOwned:  true,
		systemProxyRestorer: func(platform.ProxySession) (bool, error) {
			return false, want
		},
	}
	if err := a.Close(); !errors.Is(err, want) {
		t.Fatalf("Close() error = %v, want %v", err, want)
	}
}

func TestCloseRetriesProxyRestoreAfterFailure(t *testing.T) {
	attempts := 0
	a := &App{
		proxyActive: true,
		proxyOwned:  true,
		systemProxyRestorer: func(platform.ProxySession) (bool, error) {
			attempts++
			if attempts == 1 {
				return false, errors.New("temporary settings failure")
			}
			return true, nil
		},
	}
	if err := a.Close(); err == nil {
		t.Fatal("first Close() unexpectedly succeeded")
	}
	if err := a.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if attempts != 2 || a.SystemProxyActive() {
		t.Fatalf("proxy cleanup retry mismatch: attempts=%d active=%v", attempts, a.SystemProxyActive())
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

	if err := a.launchReplacement("-restart -tun", true, false); err != nil {
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
		coreState:    func() core.State { return core.StateRunning },
		selfLauncher: func(string, bool) error { return launchErr },
	}

	err := a.launchReplacement("-restart", false, false)
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
	if err := a.LaunchSystemProxyElevated(); !errors.Is(err, errControllerClosed) {
		t.Fatalf("LaunchSystemProxyElevated() error = %v, want closed error", err)
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

func TestResumeProbeEndedRecovery(t *testing.T) {
	requestTimeout := errors.Join(errors.New(`Get "http://127.0.0.1:9090/proxies": request canceled`), context.DeadlineExceeded)
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	live, cancelLive := context.WithDeadline(context.Background(), time.Now().Add(time.Minute))
	defer cancelLive()
	cancelled, cancelCancelled := context.WithCancel(context.Background())
	cancelCancelled()

	cases := []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{"no probe error", context.Background(), nil, false},
		{"request timeout under a live recovery context", live, requestTimeout, false},
		{"request timeout without a recovery deadline", context.Background(), requestTimeout, false},
		{"request timeout without a recovery context", nil, requestTimeout, false},
		{"expired recovery context", expired, context.DeadlineExceeded, true},
		{"cancelled recovery context", cancelled, context.Canceled, true},
	}
	for _, tc := range cases {
		if got := resumeProbeEndedRecovery(tc.ctx, tc.err); got != tc.want {
			t.Errorf("%s: resumeProbeEndedRecovery = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRestartHandoffPreservesFeatureState(t *testing.T) {
	tests := []struct {
		name  string
		tun   bool
		proxy bool
	}{
		{name: "features the user turned off stay off"},
		{name: "tun stays on", tun: true},
		{name: "proxy stays on", proxy: true},
		{name: "features the user turned on stay on", tun: true, proxy: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			writeReloadSource(t, path, reloadConfigJSON)

			launched := ""
			controller := &App{
				source:        config.ConfigSource{FilePath: path},
				configChecker: func(string) error { return nil },
				selfLauncher: func(flags string, elevated bool) error {
					launched = flags
					return nil
				},
			}
			controller.tunActive = test.tun
			controller.proxyActive = test.proxy
			controller.proxyOwned = test.proxy
			if test.proxy {
				controller.systemProxyRestorer = func(platform.ProxySession) (bool, error) {
					return true, nil
				}
			}

			if err := controller.Restart(); err != nil {
				t.Fatalf("Restart() error = %v", err)
			}
			if launched == "" {
				t.Fatal("Restart() did not launch a replacement controller")
			}

			// The replacement parses this command line and re-derives both
			// features from it. The configuration asks for both, so only an
			// explicit handoff choice can keep them as the user left them.
			parsed := ParseFlags(strings.Fields(launched))
			startupConfig := config.SingBoxConfig{HasTunInbound: true}
			startupOptions := config.Options{TunStartMode: "on", SystemProxyAuto: true}
			if got := startupTunRequested(startupConfig, startupOptions, parsed); got != test.tun {
				t.Fatalf("replacement would start with tun=%v, want %v (flags %q)", got, test.tun, launched)
			}
			if got := startupProxyRequested(startupOptions, parsed); got != test.proxy {
				t.Fatalf("replacement would start with proxy=%v, want %v (flags %q)", got, test.proxy, launched)
			}
		})
	}
}
