package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"sing-box-drover/internal/clash"
	"sing-box-drover/internal/config"
	"sing-box-drover/internal/core"
	"sing-box-drover/internal/handoff"
	"sing-box-drover/internal/logging"
	platform "sing-box-drover/internal/windows"
)

var (
	ErrAlreadyRunning   = errors.New("sing-box-drover is already running")
	ErrElevationHandoff = errors.New("elevated sing-box-drover instance launched")
	// ErrAutostartHandled reports that a one-shot autostart update finished. The
	// process has nothing left to do and exits without opening a tray.
	ErrAutostartHandled = errors.New("autostart update handled")
	// ErrRestartHandoff is not a duplicate start. The running instance asked for
	// a replacement and never released the single instance, so reporting it as a
	// plain duplicate would exit silently and leave nothing running.
	ErrRestartHandoff = errors.New("the previous instance did not stop in time; start sing-box-drover again")
	// ErrHandoffReported reports that this process could not take a running
	// controller over and that the controller was told why. It shows the failure,
	// so this process exits without reporting the same problem twice.
	ErrHandoffReported = errors.New("handoff failure reported to the previous instance")
	// ErrCleanupFailed keeps a failed shutdown fatal even when startup handed
	// off or already reported its original error to another controller.
	ErrCleanupFailed    = errors.New("controller cleanup failed")
	errControllerClosed = errors.New("controller is closed")
)

type Flags struct {
	Tun              bool
	Proxy            bool
	NoTun            bool
	NoProxy          bool
	Restart          bool
	AutostartEnable  bool
	AutostartDisable bool
	// AutostartOwner names the account the logon task belongs to. The elevated
	// one-shot helper runs under whatever account the UAC prompt accepted, which
	// is not necessarily the user that asked for the change.
	AutostartOwner string
	// HandoffPipe, HandoffToken and HandoffPeer carry the connection of a
	// controller handoff. They are set by the instance that started this one, and
	// their presence makes this process the replacement of that instance.
	HandoffPipe  string
	HandoffToken string
	HandoffPeer  int
}

const (
	// A restart replacement has to outlast the complete close budget of the
	// instance it replaces: that instance stops its core, delivers the terminal
	// state notification (which restores the system proxy) and only then releases
	// its instance handle. While this was a fixed 10s the handoff could expire
	// while the old instance was still stopping, and the user was left with
	// neither a core nor a tray.
	restartInstanceWait     = core.StopGracePeriod + core.StopCleanupWait + core.NotifyDrainTimeout + 5*time.Second
	restartInstanceInterval = 50 * time.Millisecond
	resumeAPIProbeAttempts  = 5
	resumeAPIProbeInterval  = 750 * time.Millisecond
	// ResumeRecoveryTimeout covers all bounded API probes and leaves a small
	// margin for scheduling and response processing.
	ResumeRecoveryTimeout = 10 * time.Second
	// optionsFileName is the INI that NewAt reads. A replacement process re-reads
	// exactly this file, so the preflight for a replacement loads it too.
	optionsFileName = "sing-box-drover.ini"
)

func ParseFlags(args []string) Flags {
	var flags Flags
	for i := 0; i < len(args); i++ {
		arg := strings.TrimLeft(strings.ToLower(strings.TrimSpace(args[i])), "-")
		switch arg {
		case "tun":
			flags.Tun = true
		case "proxy":
			flags.Proxy = true
		case "no-tun":
			flags.NoTun = true
		case "no-proxy":
			flags.NoProxy = true
		case "restart":
			flags.Restart = true
		case "autostart-enable":
			flags.AutostartEnable = true
		case "autostart-disable":
			flags.AutostartDisable = true
		case "autostart-owner":
			if i+1 < len(args) {
				i++
				flags.AutostartOwner = strings.TrimSpace(args[i])
			}
		case "handoff-pipe":
			if i+1 < len(args) {
				i++
				flags.HandoffPipe = strings.TrimSpace(args[i])
			}
		case "handoff-token":
			if i+1 < len(args) {
				i++
				flags.HandoffToken = strings.TrimSpace(args[i])
			}
		case "handoff-peer":
			if i+1 < len(args) {
				i++
				peer, err := strconv.Atoi(strings.TrimSpace(args[i]))
				if err == nil && peer > 0 {
					flags.HandoffPeer = peer
				}
			}
		}
	}
	return flags
}

// startupTunRequested reports whether this instance should start with TUN.
// A handoff passes an explicit choice so the replacement keeps the state the
// user was last running with; the configured start mode is only the default
// for a fresh start. Without the explicit "off" flag a restart would switch
// TUN back on after the user turned it off while the configuration still asks
// for it.
func startupTunRequested(sbConfig config.SingBoxConfig, options config.Options, flags Flags) bool {
	if flags.NoTun {
		return false
	}
	return sbConfig.HasTunInbound && (flags.Tun || options.TunStartMode == "on")
}

// startupProxyRequested reports whether this instance should bring the system
// proxy up, with the same explicit handoff rule as TUN.
func startupProxyRequested(options config.Options, flags Flags) bool {
	if flags.NoProxy {
		return false
	}
	return options.SystemProxyAuto || flags.Proxy
}

// handoffTunFlags and handoffProxyFlags describe a feature state for a
// replacement controller. Both always emit an explicit choice: passing nothing
// would let the replacement fall back to the configured start mode.
func handoffTunFlags(active bool) string {
	if active {
		return " -tun"
	}
	return " -no-tun"
}

func handoffProxyFlags(active bool) string {
	if active {
		return " -proxy"
	}
	return " -no-proxy"
}

// runAutostartUpdate applies a one-shot "-autostart-enable" or
// "-autostart-disable" request and stops. Success returns ErrAutostartHandled
// so the process exits without a tray; a real failure is returned so main can
// report it, which is how the elevated helper tells the user what went wrong.
func runAutostartUpdate(logPath string, flags Flags) error {
	if platform.AutostartRequiresElevation() && !platform.IsProcessElevated() {
		return platform.ErrElevationRequired
	}
	enabled := flags.AutostartEnable
	logger := logging.New(logPath)
	defer logger.Close()
	if err := platform.SetAutostart(enabled, flags.AutostartOwner); err != nil {
		logger.Log("Autostart", "autostart update failed: "+err.Error())
		return fmt.Errorf("autostart update failed: %w", err)
	}
	status := "disabled"
	if enabled {
		status = "enabled"
	}
	logger.Log("Autostart", "autostart "+status)
	return ErrAutostartHandled
}

// currentAccountName reports the account running this process. It is what the
// autostart task should be registered for: see Flags.AutostartOwner.
func currentAccountName() string {
	current, err := user.Current()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(current.Username)
}

// quoteLaunchArg quotes a value for the raw command-line string LaunchSelf
// takes. Account names rarely contain spaces, but a local account may.
func quoteLaunchArg(value string) string {
	if !strings.ContainsAny(value, " \t") {
		return value
	}
	return `"` + strings.ReplaceAll(value, `"`, "") + `"`
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
	return retryInstanceAcquisitionContext(context.Background(), acquire, wait, interval, now, sleep)
}

func retryInstanceAcquisitionContext(
	ctx context.Context,
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
	if wait < 0 {
		wait = 0
	}
	if interval <= 0 {
		interval = time.Millisecond
	}
	deadline := now().Add(wait)
	for {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		instance, first, err := acquire()
		if err != nil {
			if instance != nil {
				instance.Close()
			}
			return nil, false, err
		}
		if first {
			if err := ctx.Err(); err != nil {
				if instance != nil {
					instance.Close()
				}
				return nil, false, err
			}
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
		if sleep != nil {
			sleep(delay)
			continue
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, false, ctx.Err()
		case <-timer.C:
		}
	}
}

type Event struct {
	Kind    core.EventKind
	State   core.State
	Message string
}

type App struct {
	mu         sync.RWMutex
	selectorMu sync.Mutex
	proxyMu    sync.Mutex
	executable string
	processDir string
	options    config.Options
	source     config.ConfigSource
	config     config.SingBoxConfig
	logger     *logging.Logger
	api        *clash.Client
	apiReady   bool
	supervisor *core.Supervisor
	instance   *platform.SingleInstance
	// handedOver reports that instance ownership was given to a replacement that
	// could not be confirmed gone. The tray stays up, but a core start from this
	// process would compete with the replacement's core for the same TUN device
	// and system proxy.
	handedOver    bool
	selectors     []clash.Selector
	flags         Flags
	tunActive     bool
	proxyActive   bool
	proxySession  platform.ProxySession
	proxyOwned    bool
	closed        bool
	apiPollCancel context.CancelFunc
	events        chan Event
	// selectorUpdates wakes a watcher when the cached selector list changed. It is a
	// capacity-one signal, not a queue: the watcher re-reads the cache, so merging
	// repeated changes is intended.
	selectorUpdates chan struct{}
	// handoff is the session of a controller this process replaced. It stays set
	// until the tray confirmed the takeover or the controller shut down.
	handoff *handoff.Session

	// System proxy operations stay injectable so ownership and failure cleanup
	// can be tested without modifying the machine running the tests.
	systemProxyEnabler  func(string, int) (platform.ProxySession, error)
	systemProxyRestorer func(platform.ProxySession) (bool, error)
	selfLauncher        func(string, bool) error
	configChecker       func(string) error
	tunPrivilegeChecker func(string) error
	coreStarter         func(context.Context, string) error
	coreState           func() core.State
	// handoffLauncher and handoffTransportFn are injectable for the same reason:
	// a controller handoff needs a trackable replacement and a private channel to
	// it, and a controller built without them (a test, or a platform without a
	// handoff transport) launches its replacement without a handoff.
	handoffLauncher    func(string, bool) (handoff.LaunchedProcess, error)
	handoffTransportFn func() (handoff.Transport, error)
	// instanceAcquirer lets handoff tests avoid the real single-instance lock.
	instanceAcquirer func() (*platform.SingleInstance, bool, error)
}

func New(args []string) (*App, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return NewAt(executable, args)
}

func NewAt(executable string, args []string) (*App, error) {
	processDir := platform.RuntimeDir(executable)
	// Flags come first: an autostart update is a one-shot command that only writes
	// the scheduled task, so a malformed homepage-url or system-proxy-auto in the
	// INI must not abort it before the flags are handled. Otherwise the scheduled
	// task can no longer be removed.
	flags := ParseFlags(args)
	iniPath := filepath.Join(processDir, optionsFileName)
	if flags.AutostartEnable || flags.AutostartDisable {
		options, err := config.LoadOptions(iniPath)
		if err != nil && !filepath.IsAbs(options.LogFile) {
			// LoadOptions returns what it parsed before the failing line, but does
			// not resolve relative paths when it fails. A relative log file would be
			// written under whatever working directory the helper was started in, so
			// run the update unlogged instead of somewhere unexpected.
			options.LogFile = ""
		}
		return nil, runAutostartUpdate(options.LogFile, flags)
	}
	options, err := config.LoadOptions(iniPath)
	if err != nil {
		if flags.HandoffPipe != "" {
			// This process was started to replace a running controller, which is
			// waiting for it to report over the handoff connection. Reporting only here
			// would leave that controller with a readiness timeout instead of the
			// reason.
			return nil, reportHandoffFailure(flags, err)
		}
		return nil, err
	}
	if flags.HandoffPipe != "" {
		// This process was started to replace a running controller. It must not
		// take the instance or the core over before that controller gives them up.
		if flags.HandoffPeer <= 0 {
			return nil, errors.New("handoff launch without a peer process id")
		}
		return newHandoffReplacement(executable, processDir, options, flags)
	}
	instance, first, err := acquireInstance()
	if err != nil {
		return nil, err
	}
	if !first {
		if instance != nil {
			instance.Close()
		}
		if !flags.Restart {
			return nil, ErrAlreadyRunning
		}
		instance, first, err = retryInstanceAcquisition(acquireInstance, restartInstanceWait, restartInstanceInterval, nil, nil)
		if err != nil {
			return nil, err
		}
		if !first {
			return nil, ErrRestartHandoff
		}
	}
	logger := logging.New(options.LogFile)
	app, err := newController(executable, processDir, options, flags, logger)
	if err != nil {
		instance.Close()
		logger.Close()
		return nil, err
	}
	app.setInstance(instance)
	wantTun := app.TunActive()
	wantProxy := app.StartupProxyRequested()
	needsElevation := wantProxy && platform.SystemProxyRequiresElevation() && !platform.IsProcessElevated()
	if wantTun {
		if err := app.checkTunPrivileges(); err != nil {
			if !errors.Is(err, platform.ErrElevationRequired) {
				return nil, app.closeStartup(err)
			}
			needsElevation = true
		}
	}
	if needsElevation {
		if err := app.launchElevatedLocked(wantTun, wantProxy, wantTun); err != nil {
			return nil, app.closeStartup(fmt.Errorf("launch elevated controller for privileged features: %w", err))
		}
		return nil, app.closeStartup(ErrElevationHandoff)
	}
	if err := app.startCore(); err != nil {
		return nil, app.closeStartup(err)
	}
	return app, nil
}

// newController builds a controller from options that are already resolved. It
// reads and validates the configuration and locates the core, but it takes
// neither instance ownership nor starts the core: a handoff replacement does both
// only once the instance it replaces gives them up.
func newController(executable, processDir string, options config.Options, flags Flags, logger *logging.Logger) (*App, error) {
	source, sbConfig, err := config.ReadValidatedConfig(options.SBConfigFile)
	if err != nil {
		return nil, err
	}
	corePath := coreExecutablePath(options)
	if _, err := os.Stat(corePath); err != nil {
		return nil, fmt.Errorf("sing-box executable not found: %w", err)
	}
	app := &App{
		executable:      executable,
		processDir:      processDir,
		options:         options,
		source:          source,
		config:          sbConfig,
		logger:          logger,
		selectors:       staticSelectors(sbConfig.Selectors),
		flags:           flags,
		events:          make(chan Event, 32),
		selectorUpdates: make(chan struct{}, 1),
	}
	if sbConfig.ClashAPI.IsConfigured() {
		app.api = clash.NewClient(sbConfig.ClashAPI.ExternalController, sbConfig.ClashAPI.Secret)
	}
	app.supervisor = core.NewSupervisor(corePath, logger)
	app.coreState = app.supervisor.State
	app.supervisor.SetHandler(func(event core.Event) {
		app.handleCoreEvent(event)
	})
	app.handoffLauncher = defaultHandoffLauncher
	app.handoffTransportFn = handoff.DefaultTransport
	app.tunActive = startupTunRequested(sbConfig, options, flags)
	return app, nil
}

// defaultHandoffLauncher starts a replacement through the platform's trackable
// launcher, which is what lets a handoff wait for it to become ready.
func defaultHandoffLauncher(params string, elevate bool) (handoff.LaunchedProcess, error) {
	return platform.LaunchSelfHandoff(params, elevate)
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
	if event.State == core.StateFailed || event.State == core.StateStopped {
		if err := a.disableSystemProxyIfActive(); err != nil {
			a.logger.Log("SystemProxy", "failed to disable after core stop: "+err.Error())
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

// SelectorUpdates reports that the cached selector list changed. Pending signals
// are merged: the consumer re-reads Selectors, so a burst of changes costs one
// rebuild and the consumer always ends up on the latest snapshot.
func (a *App) SelectorUpdates() <-chan struct{} { return a.selectorUpdates }

func (a *App) notifySelectorUpdates() {
	select {
	case a.selectorUpdates <- struct{}{}:
	default:
	}
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

// StartupProxyRequested reports whether this instance should bring the system
// proxy up. The tray asks the controller instead of recomputing the condition, so
// a handoff that explicitly asked for "off" is not overruled at startup by the
// configured default.
func (a *App) StartupProxyRequested() bool {
	return startupProxyRequested(a.options, a.flags)
}

func (a *App) ensureOpen() error {
	a.mu.RLock()
	closed := a.closed
	a.mu.RUnlock()
	if closed {
		return errControllerClosed
	}
	return nil
}

// loadOptions re-reads the INI a replacement process would load. A replacement
// runs NewAt, which reads this file itself, so a preflight that reused the
// options this instance parsed at startup could validate a different
// configuration path than the replacement is about to use.
func (a *App) loadOptions() (config.Options, error) {
	return config.LoadOptions(filepath.Join(a.processDir, optionsFileName))
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
	a.notifySelectorUpdates()
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
	a.notifySelectorUpdates()
	return nil
}

func (a *App) EnableSystemProxy() error {
	a.proxyMu.Lock()
	defer a.proxyMu.Unlock()
	a.mu.RLock()
	closed, active := a.closed, a.proxyActive
	host, port := a.config.ProxyHost, a.config.ProxyPort
	supervisor := a.supervisor
	state := a.coreState
	a.mu.RUnlock()
	if closed {
		return errors.New("controller is closed")
	}
	if state == nil {
		if supervisor == nil {
			return errors.New("core supervisor is not configured")
		}
		state = supervisor.State
	}
	if state() != core.StateRunning {
		return errors.New("sing-box core is not running")
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

// restoreSystemProxyLocked requires proxyMu. A false result means the platform
// kept newer settings instead of releasing this controller's own: macOS
// captures the previous configuration and can find it changed by something
// else. Windows always clears the proxy and can only report an error.
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

func (a *App) checkTunPrivileges() error {
	check := a.tunPrivilegeChecker
	if check == nil {
		check = platform.CheckTunPrivileges
	}
	return check(filepath.Join(a.options.SBDir, platform.CoreExecutableName()))
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
	// Restart the core in this process. Replacing the controller only to reclaim
	// its Go heap made the tray and core handover observable: a replacement built
	// its own tray before this one exited. Startup options are read once at
	// launch, so an edited INI still needs a full controller restart.
	return a.restartWithConfig(a.TunActive(), false)
}

// resumeProbeEndedRecovery reports whether a failed resume probe should stop
// the recovery instead of restarting sing-box.
//
// Only the recovery context itself ending stops it. The probe error alone
// cannot decide this: the Clash API client has its own request timeout, and a
// request that times out surfaces as a wrapped context.DeadlineExceeded, which
// is indistinguishable from an expired recovery context. An API that stays
// unreachable after resume is exactly the case that has to restart sing-box.
func resumeProbeEndedRecovery(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	return ctx != nil && ctx.Err() != nil
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
		} else if resumeProbeEndedRecovery(ctx, err) {
			return ctx.Err()
		} else if logger != nil {
			logger.Log("Resume", "Clash API remained unavailable after resume probes; restarting sing-box: "+err.Error())
		}
	} else if logger != nil {
		logger.Log("Resume", "sing-box is not running after resume; restarting it")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := a.restartWithConfigContext(ctx, tun, false); err != nil {
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
	return a.launchElevatedLocked(tun, false, tun)
}

func (a *App) LaunchSystemProxyElevated() error {
	a.selectorMu.Lock()
	defer a.selectorMu.Unlock()
	return a.launchElevatedLocked(a.TunActive(), true, false)
}

func (a *App) launchElevatedLocked(tun, proxy, requireTun bool) error {
	if err := a.ensureOpen(); err != nil {
		return err
	}
	candidate, err := a.checkReplacementCandidate(context.Background(), tun, requireTun)
	if err != nil {
		return err
	}
	flags := "-restart" + handoffTunFlags(candidate.tun)
	return a.launchReplacement(flags, true, proxy)
}

func (a *App) QueryAutostart() (platform.AutostartState, error) { return platform.QueryAutostart() }
func (a *App) LaunchAutostartElevated(enabled bool) error {
	a.selectorMu.Lock()
	defer a.selectorMu.Unlock()
	if err := a.ensureOpen(); err != nil {
		return err
	}
	// The replacement only writes the scheduled task. It must not take over
	// this controller, so it is launched without -restart and without carrying
	// the TUN state; and it must not hand over the system proxy either, because
	// a helper that never starts a core could not bring the proxy back up.
	flags := "-autostart-enable"
	if !enabled {
		flags = "-autostart-disable"
	}
	// The helper runs under the account the UAC prompt accepted, so it has to be
	// told which user the task is for.
	if owner := currentAccountName(); owner != "" {
		flags += " -autostart-owner " + quoteLaunchArg(owner)
	}
	launch := a.selfLauncher
	if launch == nil {
		launch = platform.LaunchSelf
	}
	return launch(flags, true)
}

func (a *App) launchReplacement(flags string, elevated bool, wantProxy bool) error {
	a.proxyMu.Lock()
	proxyRestored, proxyErr := a.restoreSystemProxyLocked()
	a.proxyMu.Unlock()
	if proxyErr != nil {
		return fmt.Errorf("prepare system proxy handoff: %w", proxyErr)
	}
	// The replacement has to end up with the proxy in the state the user was
	// actually running with. wantProxy carries that intent when the caller
	// already knows it, which is a first start that needs elevation and has
	// nothing active yet; otherwise the state just handed back is the answer.
	flags += handoffProxyFlags(proxyRestored || wantProxy)
	// A handoff confirms the replacement before this instance gives anything up,
	// so a replacement that cannot run leaves this controller untouched. Without a
	// handoff transport the one-shot launch stays: the replacement then waits for
	// this instance to exit and release the instance itself.
	transport, transportErr := a.handoffTransport()
	if transportErr != nil {
		if a.logger != nil {
			a.logger.Log("Handoff", "Launching without a controller handoff: "+transportErr.Error())
		}
		return a.launchReplacementOnce(flags, elevated, proxyRestored)
	}
	return a.launchHandoff(flags, elevated, proxyRestored, transport)
}

// launchReplacementOnce starts a replacement that waits for this instance to
// exit and release the instance, which is all a platform without a handoff
// transport can offer.
func (a *App) launchReplacementOnce(flags string, elevated, proxyRestored bool) error {
	launch := a.selfLauncher
	if launch == nil {
		launch = platform.LaunchSelf
	}
	if err := launch(flags, elevated); err != nil {
		return a.restoreProxyAfterFailedLaunch(proxyRestored, err)
	}
	return nil
}

func (a *App) SetAutostart(enabled bool) error {
	a.selectorMu.Lock()
	defer a.selectorMu.Unlock()
	if err := a.ensureOpen(); err != nil {
		return err
	}
	if platform.AutostartRequiresElevation() && !platform.IsProcessElevated() {
		return platform.ErrElevationRequired
	}
	return platform.SetAutostart(enabled, "")
}

// closeStartup preserves cleanup errors separately from a handled startup error.
func (a *App) closeStartup(cause error) error {
	if err := a.Close(); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func (a *App) Close() error {
	a.selectorMu.Lock()
	a.proxyMu.Lock()
	a.mu.Lock()
	first := !a.closed
	a.closed = true
	pollCancel := a.apiPollCancel
	a.apiPollCancel = nil
	instance := a.instance
	supervisor := a.supervisor
	session := a.handoff
	a.handoff = nil
	a.mu.Unlock()
	_, proxyErr := a.restoreSystemProxyLocked()
	a.proxyMu.Unlock()
	a.selectorMu.Unlock()
	if first && pollCancel != nil {
		pollCancel()
	}
	if proxyErr != nil && a.logger != nil {
		a.logger.Log("System proxy", "Failed to restore system proxy during shutdown: "+proxyErr.Error())
	}
	var supervisorErr error
	if supervisor != nil {
		supervisorErr = supervisor.Close()
		if supervisorErr != nil && a.logger != nil {
			a.logger.Log("Core", "Failed to stop sing-box during shutdown: "+supervisorErr.Error())
		}
	}
	if supervisorErr == nil {
		// Instance ownership is released only once the core is confirmed gone: a
		// handle closed while its core may still run would let another controller
		// start and fight over the same system proxy. A stop that could not be
		// confirmed keeps the handle and the supervisor, so a later Close retries
		// instead of reporting a clean shutdown.
		a.mu.Lock()
		if a.instance == instance {
			a.instance = nil
		}
		a.mu.Unlock()
		if instance != nil {
			instance.Close()
		}
		if a.logger != nil {
			a.logger.Close()
		}
	}
	if session != nil {
		// The instance that started this one is waiting for a confirmation that
		// will not come. Reporting the failure lets it roll back instead of waiting
		// for its startup budget to expire.
		session.Abort("the replacement shut down before it confirmed the handoff")
	}
	if err := errors.Join(proxyErr, supervisorErr); err != nil {
		return fmt.Errorf("%w: %w", ErrCleanupFailed, err)
	}
	return nil
}
