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
	"sing-box-drover/internal/state"
	platform "sing-box-drover/internal/windows"
)

var ErrAlreadyRunning = errors.New("sing-box-drover is already running")

type Flags struct {
	Tun              bool
	Restart          bool
	AutostartEnable  bool
	AutostartDisable bool
}

const (
	restartInstanceWait     = 10 * time.Second
	restartInstanceInterval = 50 * time.Millisecond
)

func ParseFlags(args []string) Flags {
	var flags Flags
	for _, arg := range args {
		arg = strings.TrimLeft(strings.ToLower(strings.TrimSpace(arg)), "-")
		switch arg {
		case "tun":
			flags.Tun = true
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
	mu          sync.RWMutex
	selectorMu  sync.Mutex
	executable  string
	processDir  string
	options     config.Options
	source      config.ConfigSource
	config      config.SingBoxConfig
	logger      *logging.Logger
	state       *state.File
	api         *clash.Client
	supervisor  *core.Supervisor
	instance    *platform.SingleInstance
	selectors   []clash.Selector
	flags       Flags
	tunActive   bool
	proxyActive bool
	restored    bool
	closed      bool
	updateStop  context.CancelFunc
	events      chan Event
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
	source, err := config.ReadConfigSource(options.SBConfigFile)
	if err != nil {
		instance.Close()
		logger.Close()
		return nil, err
	}
	sbConfig, err := config.ReadSingBoxConfig(source.JSONText)
	if err != nil {
		instance.Close()
		logger.Close()
		return nil, err
	}
	if err := config.CheckSingBoxConfig(sbConfig); err != nil {
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
	appState := state.Load(filepath.Join(processDir, "sing-box-drover.state.json"))
	selectors := staticSelectors(sbConfig.Selectors)
	if options.SelectorPersist {
		applyPersistedStatic(selectors, appState)
	}
	app := &App{
		executable: executable,
		processDir: processDir,
		options:    options,
		source:     source,
		config:     sbConfig,
		logger:     logger,
		state:      appState,
		instance:   instance,
		selectors:  selectors,
		flags:      flags,
		events:     make(chan Event, 32),
	}
	if sbConfig.ClashAPI.IsConfigured() {
		app.api = clash.NewClient(sbConfig.ClashAPI.ExternalController, sbConfig.ClashAPI.Secret)
	}
	app.supervisor = core.NewSupervisor(corePath, logger)
	app.supervisor.SetHandler(func(event core.Event) {
		app.handleCoreEvent(event)
	})
	app.tunActive = sbConfig.HasTunInbound && platform.IsProcessElevated() && (flags.Tun || options.TunStartMode == "on")
	if err := app.supervisor.Start(app.runtimeConfig(app.tunActive)); err != nil {
		app.Close()
		return nil, err
	}
	if source.IsBPF() && source.BPFProfile.IsRemote() && source.BPFProfile.AutoUpdate {
		ctx, cancel := context.WithCancel(context.Background())
		app.updateStop = cancel
		updater := &config.BPFUpdater{Path: source.FilePath, Profile: source.BPFProfile, UserAgent: "sing-box-drover", Logf: func(format string, values ...any) {
			logger.Log("ConfigUpdater", fmt.Sprintf(format, values...))
		}}
		go updater.Run(ctx)
	}
	return app, nil
}

func staticSelectors(values []config.Selector) []clash.Selector {
	result := make([]clash.Selector, 0, len(values))
	for _, value := range values {
		selector := clash.Selector{Name: value.Name, All: append([]string(nil), value.Outbounds...)}
		if value.DefaultIndex >= 0 && value.DefaultIndex < len(value.Outbounds) {
			selector.Now = value.Outbounds[value.DefaultIndex]
		} else {
			selector.Now = value.DefaultName
		}
		result = append(result, selector)
	}
	return result
}

func applyPersistedStatic(selectors []clash.Selector, saved *state.File) {
	if saved == nil {
		return
	}
	for i := range selectors {
		value, ok := saved.GetSelector(selectors[i].Name)
		if ok && clash.ContainsOption(selectors[i], value) {
			selectors[i].Now = value
		}
	}
}

func (a *App) runtimeConfig(tun bool) string {
	if tun {
		return a.config.JSONWithTUN
	}
	return a.config.JSONWithoutTUN
}

func (a *App) handleCoreEvent(event core.Event) {
	a.emit(Event{Kind: event.Kind, State: event.State, Message: event.Message})
	if event.State == core.StateRunning && a.api != nil {
		go a.waitForAPI()
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

func (a *App) waitForAPI() {
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		a.mu.RLock()
		closed := a.closed
		a.mu.RUnlock()
		if closed {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := a.RefreshSelectors(ctx)
		cancel()
		if err == nil {
			return
		}
		time.Sleep(500 * time.Millisecond)
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

func (a *App) HasTunInbound() bool { return a.config.HasTunInbound }
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
func (a *App) Options() config.Options { return a.options }
func (a *App) Flags() Flags            { return a.flags }
func (a *App) Events() <-chan Event    { return a.events }

func (a *App) RefreshSelectors(ctx context.Context) ([]clash.Selector, error) {
	a.selectorMu.Lock()
	defer a.selectorMu.Unlock()
	a.mu.RLock()
	closed := a.closed
	a.mu.RUnlock()
	if closed {
		return a.Selectors(), errors.New("controller is closed")
	}
	if a.api == nil {
		return a.Selectors(), errors.New("Clash API is not configured")
	}
	fresh, err := a.api.FetchSelectors(ctx)
	if err != nil {
		return a.Selectors(), err
	}
	a.mu.RLock()
	closed = a.closed
	a.mu.RUnlock()
	if closed {
		return a.Selectors(), errors.New("controller is closed")
	}
	a.mu.Lock()
	a.selectors = cloneSelectors(fresh)
	a.mu.Unlock()
	if a.options.SelectorPersist {
		a.restorePersisted(ctx, fresh)
	}
	return a.Selectors(), nil
}

func (a *App) restorePersisted(ctx context.Context, selectors []clash.Selector) {
	a.mu.Lock()
	if a.restored {
		a.mu.Unlock()
		a.persistSelectors(selectors)
		return
	}
	a.restored = true
	a.mu.Unlock()
	values := make(map[string]string, len(selectors))
	for _, selector := range selectors {
		selected := selector.Now
		if saved, ok := selectorState(a.state, selector.Name); ok {
			if clash.ContainsOption(selector, saved) {
				if saved != selector.Now {
					if err := a.api.SwitchSelector(ctx, selector.Name, saved); err == nil {
						selected = saved
					}
				}
			} // stale saved options intentionally fall back to API's now value.
		}
		if selected != "" {
			values[selector.Name] = selected
		}
	}
	a.mu.Lock()
	for i := range a.selectors {
		if value, ok := values[a.selectors[i].Name]; ok {
			a.selectors[i].Now = value
		}
	}
	a.mu.Unlock()
	a.persistSelectors(a.Selectors())
}

func selectorState(saved *state.File, name string) (string, bool) {
	if saved == nil {
		return "", false
	}
	return saved.GetSelector(name)
}

func (a *App) persistSelectors(selectors []clash.Selector) {
	if a.state == nil {
		return
	}
	values := make(map[string]string, len(selectors))
	scope := make([]string, 0, len(selectors))
	for _, selector := range selectors {
		scope = append(scope, selector.Name)
		if selector.Now != "" {
			values[selector.Name] = selector.Now
		}
	}
	if err := a.state.SyncSelectors(values, scope); err != nil {
		a.logger.Log("State", "failed to persist selector state: "+err.Error())
	}
}

func (a *App) SwitchSelector(ctx context.Context, selectorName, value string) error {
	a.selectorMu.Lock()
	defer a.selectorMu.Unlock()
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
	if a.options.SelectorPersist {
		a.persistSelectors(a.Selectors())
	}
	return nil
}

func (a *App) EnableSystemProxy() error {
	if err := platform.EnableSystemProxy(a.config.ProxyHost, a.config.ProxyPort); err != nil {
		return err
	}
	a.mu.Lock()
	a.proxyActive = true
	a.mu.Unlock()
	return nil
}

func (a *App) DisableSystemProxy() error {
	if err := platform.DisableSystemProxy(); err != nil {
		return err
	}
	a.mu.Lock()
	a.proxyActive = false
	a.mu.Unlock()
	return nil
}

func (a *App) ToggleTun(enabled bool) error {
	a.selectorMu.Lock()
	defer a.selectorMu.Unlock()
	if !a.config.HasTunInbound {
		return errors.New("TUN inbound is not configured")
	}
	if enabled && !platform.IsProcessElevated() {
		return platform.ErrElevationRequired
	}
	a.resetRestoration()
	if err := a.supervisor.Start(a.runtimeConfig(enabled)); err != nil {
		return err
	}
	a.mu.Lock()
	a.tunActive = enabled
	a.mu.Unlock()
	return nil
}

func (a *App) Restart() error {
	a.selectorMu.Lock()
	defer a.selectorMu.Unlock()
	tun := a.TunActive()
	if tun && !platform.IsProcessElevated() {
		return platform.ErrElevationRequired
	}
	a.resetRestoration()
	if err := a.supervisor.Start(a.runtimeConfig(tun)); err != nil {
		return err
	}
	return nil
}

func (a *App) resetRestoration() {
	a.mu.Lock()
	a.restored = false
	a.mu.Unlock()
}

func (a *App) LaunchElevated(tun bool) error {
	flags := "-restart"
	if tun {
		flags += " -tun"
	}
	return platform.LaunchSelf(flags, true)
}

func (a *App) QueryAutostart() (platform.AutostartState, error) { return platform.QueryAutostart() }
func (a *App) LaunchAutostartElevated(enabled bool) error {
	flags := "-restart"
	if enabled {
		flags += " -autostart-enable"
	} else {
		flags += " -autostart-disable"
	}
	return platform.LaunchSelf(flags, true)
}

func (a *App) SetAutostart(enabled bool) error {
	if !platform.IsProcessElevated() {
		return platform.ErrElevationRequired
	}
	return platform.SetAutostart(enabled)
}

func (a *App) Close() error {
	a.selectorMu.Lock()
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		a.selectorMu.Unlock()
		return nil
	}
	a.closed = true
	stop := a.updateStop
	a.updateStop = nil
	instance := a.instance
	a.instance = nil
	proxyActive := a.proxyActive
	a.proxyActive = false
	a.mu.Unlock()
	a.selectorMu.Unlock()
	if stop != nil {
		stop()
	}
	if a.options.SystemProxyAuto && proxyActive {
		_ = platform.DisableSystemProxy()
	}
	var err error
	if a.supervisor != nil {
		err = a.supervisor.Close()
	}
	if instance != nil {
		instance.Close()
	}
	if a.logger != nil {
		a.logger.Close()
	}
	return err
}
