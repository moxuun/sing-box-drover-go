package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"sing-box-drover/internal/clash"
	"sing-box-drover/internal/config"
	"sing-box-drover/internal/core"
	"sing-box-drover/internal/logging"
	platform "sing-box-drover/internal/windows"
)

var (
	ErrAlreadyRunning   = errors.New("sing-box-drover is already running")
	ErrElevationHandoff = errors.New("elevated sing-box-drover instance launched")
	errControllerClosed = errors.New("controller is closed")
)

type Flags struct {
	Tun              bool
	Proxy            bool
	Restart          bool
	AutostartEnable  bool
	AutostartDisable bool
}

const (
	restartInstanceWait     = 10 * time.Second
	restartInstanceInterval = 50 * time.Millisecond
	resumeAPIProbeAttempts  = 5
	resumeAPIProbeInterval  = 750 * time.Millisecond
)

func ParseFlags(args []string) Flags {
	var flags Flags
	for _, arg := range args {
		arg = strings.TrimLeft(strings.ToLower(strings.TrimSpace(arg)), "-")
		switch arg {
		case "tun":
			flags.Tun = true
		case "proxy":
			flags.Proxy = true
		case "restart":
			flags.Restart = true
		case "autostart-enable":
			flags.AutostartEnable = true
		case "autostart-disable":
			flags.AutostartDisable = true
		}
	}
	return flags
}

func startupTunRequested(sbConfig config.SingBoxConfig, options config.Options, flags Flags) bool {
	return sbConfig.HasTunInbound && (flags.Tun || options.TunStartMode == "on")
}

// retryInstanceAcquisition keeps the restart handoff bounded while allowing
// the old controller to release its mutex after the replacement was launched.
// The clock and sleep functions are injected so the handoff policy can be
// tested without starting a second GUI process.
func retryInstanceAcquisition(
	acquire func() (*platform.SingleInstance, bool, error),
	wait, interval time.Duration,
	now func() time.Time,
	sleep func(time.Duration),
) (*platform.SingleInstance, bool, error) {
	if acquire == nil {
		return nil, false, errors.New("instance acquisition is not configured")
	}
	if now == nil {
		now = time.Now
	}
	if sleep == nil {
		sleep = time.Sleep
	}
	if wait < 0 {
		wait = 0
	}
	if interval <= 0 {
		interval = time.Millisecond
	}
	deadline := now().Add(wait)
	for {
		instance, first, err := acquire()
		if err != nil {
			if instance != nil {
				instance.Close()
			}
			return nil, false, err
		}
		if first {
			return instance, true, nil
		}
		if instance != nil {
			instance.Close()
		}
		remaining := deadline.Sub(now())
		if remaining <= 0 {
			return nil, false, nil
		}
		delay := interval
		if delay > remaining {
			delay = remaining
		}
		sleep(delay)
	}
}

type Event struct {
	Kind    core.EventKind
	State   core.State
	Message string
}

type App struct {
	mu            sync.RWMutex
	selectorMu    sync.Mutex
	proxyMu       sync.Mutex
	executable    string
	processDir    string
	options       config.Options
	source        config.ConfigSource
	config        config.SingBoxConfig
	logger        *logging.Logger
	api           *clash.Client
	apiReady      bool
	supervisor    *core.Supervisor
	instance      *platform.SingleInstance
	selectors     []clash.Selector
	flags         Flags
	tunActive     bool
	proxyActive   bool
	proxySession  platform.ProxySession
	proxyOwned    bool
	closed        bool
	apiPollCancel context.CancelFunc
	events        chan Event

	// System proxy operations stay injectable so ownership and failure cleanup
	// can be tested without modifying the machine running the tests.
	systemProxyEnabler  func(string, int) (platform.ProxySession, error)
	systemProxyRestorer func(platform.ProxySession) (bool, error)
	selfLauncher        func(string, bool) error
	configChecker       func(string) error
	coreStarter         func(string) error
}

func New(args []string) (*App, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return NewAt(executable, args)
}

func NewAt(executable string, args []string) (*App, error) {
	processDir := filepath.Dir(executable)
	options, err := config.LoadOptions(filepath.Join(processDir, "sing-box-drover.ini"))
	if err != nil {
		return nil, err
	}
	flags := ParseFlags(args)
	acquire := func() (*platform.SingleInstance, bool, error) {
		return platform.AcquireSingleInstance("Local\\sing-box-drover")
	}
	instance, first, err := acquire()
	if err != nil {
		return nil, err
	}
	if !first {
		if flags.Restart {
			if instance != nil {
				instance.Close()
			}
			instance, first, err = retryInstanceAcquisition(acquire, restartInstanceWait, restartInstanceInterval, nil, nil)
			if err != nil {
				return nil, err
			}
		} else {
			if instance != nil {
				instance.Close()
			}
			return nil, ErrAlreadyRunning
		}
	}
	if !first {
		return nil, ErrAlreadyRunning
	}
	logger := logging.New(options.LogFile)
	source, sbConfig, err := config.ReadValidatedConfig(options.SBConfigFile)
	if err != nil {
		instance.Close()
		logger.Close()
		return nil, err
	}
	corePath := filepath.Join(options.SBDir, "sing-box.exe")
	if _, err := os.Stat(corePath); err != nil {
		instance.Close()
		logger.Close()
		return nil, fmt.Errorf("sing-box executable not found: %w", err)
	}
	selectors := staticSelectors(sbConfig.Selectors)
	app := &App{
		executable: executable,
		processDir: processDir,
		options:    options,
		source:     source,
		config:     sbConfig,
		logger:     logger,
		instance:   instance,
		selectors:  selectors,
		flags:      flags,
		events:     make(chan Event, 32),
	}
	if sbConfig.ClashAPI.IsConfigured() {
		app.api = clash.NewClient(sbConfig.ClashAPI.ExternalController, sbConfig.ClashAPI.Secret)
	}
	app.supervisor = core.NewSupervisor(corePath, logger)
	app.configChecker = app.supervisor.Check
	app.coreStarter = app.supervisor.Start
	app.supervisor.SetHandler(func(event core.Event) {
		app.handleCoreEvent(event)
	})
	if flags.AutostartEnable || flags.AutostartDisable {
		enabled := flags.AutostartEnable
		if err := app.SetAutostart(enabled); err != nil {
			_ = app.Close()
			return nil, fmt.Errorf("autostart update failed: %w", err)
		}
		status := "disabled"
		if enabled {
			status = "enabled"
		}
		logger.Log("Autostart", "autostart "+status)
	}
	wantTun := startupTunRequested(sbConfig, options, flags)
	if wantTun && !platform.IsProcessElevated() {
		if err := app.LaunchElevated(true); err != nil {
			_ = app.Close()
			return nil, fmt.Errorf("launch elevated controller for TUN: %w", err)
		}
		_ = app.Close()
		return nil, ErrElevationHandoff
	}
	app.tunActive = wantTun
	if err := app.supervisor.Start(app.runtimeConfig(app.tunActive)); err != nil {
		app.Close()
		return nil, err
	}
	return app, nil
}

func staticSelectors(values []config.Selector) []clash.Selector {
	result := make([]clash.Selector, 0, len(values))
	for _, value := range values {
		selector := clash.Selector{Name: value.Name, All: append([]string(nil), value.Outbounds...)}
		if value.DefaultIndex >= 0 && value.DefaultIndex < len(value.Outbounds) {
			selector.Now = value.Outbounds[value.DefaultIndex]
		} else if value.DefaultName != "" {
			selector.Now = value.DefaultName
		} else if len(value.Outbounds) > 0 {
			// sing-box selects the first outbound when `default` is omitted.
			selector.Now = value.Outbounds[0]
		}
		result = append(result, selector)
	}
	return result
}

func (a *App) runtimeConfig(tun bool) string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if tun {
		return a.config.JSONWithTUN
	}
	return a.config.JSONWithoutTUN
}

func (a *App) handleCoreEvent(event core.Event) {
	if event.State == core.StateFailed {
		if err := a.disableSystemProxyIfActive(); err != nil {
			a.logger.Log("SystemProxy", "failed to disable after core failure: "+err.Error())
		}
	}
	a.emit(Event{Kind: event.Kind, State: event.State, Message: event.Message})
	a.mu.Lock()
	apiConfigured := a.api != nil
	if event.State != core.StateRunning {
		a.apiReady = false
	}
	a.mu.Unlock()
	if event.State == core.StateRunning && apiConfigured {
		a.startAPIPoll()
	} else if event.State != core.StateRunning {
		a.stopAPIPoll()
	}
}

func (a *App) emit(event Event) {
	a.mu.RLock()
	closed := a.closed
	a.mu.RUnlock()
	if closed {
		return
	}
	select {
	case a.events <- event:
	default:
	}
}

func (a *App) startAPIPoll() {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	if a.apiPollCancel != nil {
		a.apiPollCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.apiPollCancel = cancel
	a.mu.Unlock()
	go a.waitForAPI(ctx)
}

func (a *App) stopAPIPoll() {
	a.mu.Lock()
	cancel := a.apiPollCancel
	a.apiPollCancel = nil
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *App) waitForAPI(ctx context.Context) {
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return
		}
		requestCtx, cancel := context.WithTimeout(ctx, time.Second)
		_, err := a.RefreshSelectors(requestCtx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			return
		}
		delay := 500 * time.Millisecond
		if remaining := time.Until(deadline); remaining < delay {
			delay = remaining
		}
		if delay <= 0 {
			return
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return
		}
	}
}

func cloneSelectors(values []clash.Selector) []clash.Selector {
	result := make([]clash.Selector, len(values))
	for i, value := range values {
		result[i] = value
		result[i].All = append([]string(nil), value.All...)
	}
	return result
}

func (a *App) Selectors() []clash.Selector {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return cloneSelectors(a.selectors)
}

func (a *App) HasTunInbound() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.config.HasTunInbound
}
func (a *App) TunActive() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.tunActive
}
func (a *App) SystemProxyActive() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.proxyActive
}
func (a *App) CoreState() core.State {
	a.mu.RLock()
	supervisor := a.supervisor
	a.mu.RUnlock()
	if supervisor == nil {
		return core.StateStopped
	}
	return supervisor.State()
}
func (a *App) Options() config.Options { return a.options }
func (a *App) Flags() Flags            { return a.flags }
func (a *App) Events() <-chan Event    { return a.events }

func (a *App) ensureOpen() error {
	a.mu.RLock()
	closed := a.closed
	a.mu.RUnlock()
	if closed {
		return errControllerClosed
	}
	return nil
}

func (a *App) RefreshSelectors(ctx context.Context) ([]clash.Selector, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	a.selectorMu.Lock()
	defer a.selectorMu.Unlock()
	if err := a.ensureOpen(); err != nil {
		return a.Selectors(), err
	}
	if err := ctx.Err(); err != nil {
		return a.Selectors(), err
	}
	if a.api == nil {
		return a.Selectors(), errors.New("Clash API is not configured")
	}
	fresh, err := a.api.FetchSelectors(ctx)
	if err != nil {
		return a.Selectors(), err
	}
	if err := ctx.Err(); err != nil {
		return a.Selectors(), err
	}
	if err := a.ensureOpen(); err != nil {
		return a.Selectors(), err
	}
	a.mu.Lock()
	a.selectors = cloneSelectors(fresh)
	a.apiReady = true
	a.mu.Unlock()
	return a.Selectors(), nil
}

func (a *App) SwitchSelector(ctx context.Context, selectorName, value string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	a.selectorMu.Lock()
	defer a.selectorMu.Unlock()
	if err := a.ensureOpen(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.api == nil {
		return errors.New("Clash API is not configured")
	}
	a.mu.RLock()
	var found *clash.Selector
	for i := range a.selectors {
		if a.selectors[i].Name == selectorName {
			copy := a.selectors[i]
			found = &copy
			break
		}
	}
	a.mu.RUnlock()
	if found == nil || !clash.ContainsOption(*found, value) {
		return fmt.Errorf("selector option is not available: %s/%s", selectorName, value)
	}
	if err := a.api.SwitchSelector(ctx, selectorName, value); err != nil {
		return err
	}
	a.mu.Lock()
	for i := range a.selectors {
		if a.selectors[i].Name == selectorName {
			a.selectors[i].Now = value
		}
	}
	a.mu.Unlock()
	return nil
}

func (a *App) EnableSystemProxy() error {
	a.proxyMu.Lock()
	defer a.proxyMu.Unlock()
	a.mu.RLock()
	closed, active := a.closed, a.proxyActive
	host, port := a.config.ProxyHost, a.config.ProxyPort
	a.mu.RUnlock()
	if closed {
		return errors.New("controller is closed")
	}
	if active {
		return nil
	}
	enable := a.systemProxyEnabler
	if enable == nil {
		enable = platform.EnableSystemProxy
	}
	session, err := enable(host, port)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.proxyActive = true
	a.proxySession = session
	a.proxyOwned = true
	a.mu.Unlock()
	return nil
}

func (a *App) DisableSystemProxy() error {
	a.proxyMu.Lock()
	defer a.proxyMu.Unlock()
	_, err := a.restoreSystemProxyLocked()
	return err
}

func (a *App) disableSystemProxyIfActive() error {
	a.proxyMu.Lock()
	defer a.proxyMu.Unlock()
	_, err := a.restoreSystemProxyLocked()
	return err
}

// restoreSystemProxyLocked requires proxyMu. A false result means the system
// proxy changed externally, so the controller relinquished ownership without
// overwriting the newer setting.
func (a *App) restoreSystemProxyLocked() (bool, error) {
	a.mu.RLock()
	active, owned, session := a.proxyActive, a.proxyOwned, a.proxySession
	a.mu.RUnlock()
	if !active || !owned {
		return false, nil
	}
	restore := a.systemProxyRestorer
	if restore == nil {
		restore = platform.RestoreSystemProxy
	}
	restored, err := restore(session)
	if err != nil {
		return false, err
	}
	a.mu.Lock()
	a.proxyActive = false
	a.proxyOwned = false
	a.proxySession = platform.ProxySession{}
	a.mu.Unlock()
	return restored, nil
}

func (a *App) ToggleTun(enabled bool) error {
	a.selectorMu.Lock()
	defer a.selectorMu.Unlock()
	if err := a.ensureOpen(); err != nil {
		return err
	}
	return a.restartWithConfig(enabled, enabled)
}

func (a *App) Restart() error {
	a.selectorMu.Lock()
	defer a.selectorMu.Unlock()
	if err := a.ensureOpen(); err != nil {
		return err
	}
	// Start a replacement controller so repeated restarts do not retain the
	// old controller's Go heap and runtime resources.
	tun := a.TunActive()
	if _, err := a.checkConfigCandidate(tun, false); err != nil {
		return err
	}
	flags := "-restart"
	if tun {
		flags += " -tun"
	}
	return a.launchReplacement(flags, platform.IsProcessElevated())
}

func probeResumeAPI(ctx context.Context, api *clash.Client, attempts int, interval time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if attempts < 1 {
		return errors.New("resume API probe attempts must be positive")
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := api.FetchSelectors(ctx); err == nil {
			if err := ctx.Err(); err != nil {
				return err
			}
			return nil
		} else {
			lastErr = err
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
		}
		if attempt+1 == attempts {
			break
		}
		timer := time.NewTimer(interval)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		}
	}
	return lastErr
}

func (a *App) RecoverAfterResume(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	a.selectorMu.Lock()
	defer a.selectorMu.Unlock()

	a.mu.RLock()
	closed := a.closed
	api := a.api
	apiReady := a.apiReady
	supervisor := a.supervisor
	tun := a.tunActive
	logger := a.logger
	a.mu.RUnlock()
	if closed {
		return nil
	}
	if supervisor == nil {
		return errors.New("core supervisor is not configured")
	}

	state := supervisor.State()
	if state == core.StateStarting || state == core.StateStopping {
		return nil
	}
	if state == core.StateRunning {
		if api == nil || !apiReady {
			return nil
		}
		if err := probeResumeAPI(ctx, api, resumeAPIProbeAttempts, resumeAPIProbeInterval); err == nil {
			return nil
		} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		} else if logger != nil {
			logger.Log("Resume", "Clash API remained unavailable after resume probes; restarting sing-box: "+err.Error())
		}
	} else if logger != nil {
		logger.Log("Resume", "sing-box is not running after resume; restarting it")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := a.restartWithConfig(tun, false); err != nil {
		return err
	}
	if logger != nil {
		logger.Log("Resume", "sing-box restarted after system resume")
	}
	return nil
}

func (a *App) LaunchElevated(tun bool) error {
	a.selectorMu.Lock()
	defer a.selectorMu.Unlock()
	if err := a.ensureOpen(); err != nil {
		return err
	}
	if _, err := a.checkConfigCandidate(tun, tun); err != nil {
		return err
	}
	flags := "-restart"
	if tun {
		flags += " -tun"
	}
	return a.launchReplacement(flags, true)
}

func (a *App) QueryAutostart() (platform.AutostartState, error) { return platform.QueryAutostart() }
func (a *App) LaunchAutostartElevated(enabled bool) error {
	a.selectorMu.Lock()
	defer a.selectorMu.Unlock()
	if err := a.ensureOpen(); err != nil {
		return err
	}
	tun := a.TunActive()
	if _, err := a.checkConfigCandidate(tun, false); err != nil {
		return err
	}
	flags := "-restart"
	if tun {
		flags += " -tun"
	}
	if enabled {
		flags += " -autostart-enable"
	} else {
		flags += " -autostart-disable"
	}
	return a.launchReplacement(flags, true)
}

func (a *App) launchReplacement(flags string, elevated bool) error {
	a.proxyMu.Lock()
	proxyRestored, proxyErr := a.restoreSystemProxyLocked()
	a.proxyMu.Unlock()
	if proxyErr != nil {
		return fmt.Errorf("prepare system proxy handoff: %w", proxyErr)
	}
	if proxyRestored {
		flags += " -proxy"
	}
	launch := a.selfLauncher
	if launch == nil {
		launch = platform.LaunchSelf
	}
	if err := launch(flags, elevated); err != nil {
		if proxyRestored {
			if restoreErr := a.EnableSystemProxy(); restoreErr != nil {
				return errors.Join(err, fmt.Errorf("restore system proxy after launch failure: %w", restoreErr))
			}
		}
		return err
	}
	return nil
}

func (a *App) SetAutostart(enabled bool) error {
	a.selectorMu.Lock()
	defer a.selectorMu.Unlock()
	if err := a.ensureOpen(); err != nil {
		return err
	}
	if !platform.IsProcessElevated() {
		return platform.ErrElevationRequired
	}
	return platform.SetAutostart(enabled)
}

func (a *App) Close() error {
	a.selectorMu.Lock()
	a.proxyMu.Lock()
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		_, proxyErr := a.restoreSystemProxyLocked()
		a.proxyMu.Unlock()
		a.selectorMu.Unlock()
		return proxyErr
	}
	a.closed = true
	pollCancel := a.apiPollCancel
	a.apiPollCancel = nil
	instance := a.instance
	a.instance = nil
	a.mu.Unlock()
	_, proxyErr := a.restoreSystemProxyLocked()
	a.proxyMu.Unlock()
	a.selectorMu.Unlock()
	if pollCancel != nil {
		pollCancel()
	}
	if proxyErr != nil && a.logger != nil {
		a.logger.Log("System proxy", "Failed to restore system proxy during shutdown: "+proxyErr.Error())
	}
	var supervisorErr error
	if a.supervisor != nil {
		supervisorErr = a.supervisor.Close()
	}
	if instance != nil {
		instance.Close()
	}
	if a.logger != nil {
		a.logger.Close()
	}
	return errors.Join(proxyErr, supervisorErr)
}
