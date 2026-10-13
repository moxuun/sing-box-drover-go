package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"sing-box-drover/internal/config"
	"sing-box-drover/internal/handoff"
	"sing-box-drover/internal/logging"
	platform "sing-box-drover/internal/windows"
)

// ErrHandoffAborted reports that the instance this process was replacing took the
// controller back while the replacement was still starting. Nothing was handed
// over, so the replacement exits without a tray.
var ErrHandoffAborted = handoff.ErrAborted

// ErrHandoffOwnershipLost reports that this controller gave instance ownership to
// a replacement that is still running, so it must not start a core again: the
// replacement may already run one.
var ErrHandoffOwnershipLost = errors.New("this controller handed the instance to a running replacement")

const (
	// singleInstanceName is the mutex that makes one process the controller.
	singleInstanceName = "Local\\sing-box-drover"
	// handoffInstanceWait bounds the replacement's attempt to take instance
	// ownership. The instance being replaced releases it before it lets the
	// replacement proceed, so this only covers that release being in flight.
	handoffInstanceWait = 5 * time.Second
)

func acquireInstance() (*platform.SingleInstance, bool, error) {
	return platform.AcquireSingleInstance(singleInstanceName)
}

// coreExecutablePath is the core a controller and its replacement run.
func coreExecutablePath(options config.Options) string {
	return filepath.Join(options.SBDir, platform.CoreExecutableName())
}

// newHandoffReplacement runs the replacement side of a controller handoff.
//
// The replacement validates everything it needs before it reports readiness and
// takes nothing over before the previous instance gives ownership up, so a
// replacement that cannot run leaves the controller it was replacing untouched.
// Any failure here releases what was taken and leaves this process without a
// controller: the caller reports the error and exits.
func newHandoffReplacement(executable, processDir string, options config.Options, flags Flags) (*App, error) {
	logger := logging.New(options.LogFile)
	conn, err := handoff.Connect(context.Background(), flags.HandoffPipe, flags.HandoffToken, flags.HandoffPeer)
	if err != nil {
		logger.Close()
		return nil, fmt.Errorf("connect to the instance being replaced: %w", err)
	}
	var controller *App
	session, err := handoff.RunChild(context.Background(), handoff.ChildOptions{
		Conn: conn,
		Validate: func(ctx context.Context) error {
			replacement, err := newController(executable, processDir, options, flags, logger)
			if err != nil {
				return err
			}
			controller = replacement
			return replacement.validateHandoff(ctx)
		},
		Acquire: func(ctx context.Context) error {
			instance, first, err := retryInstanceAcquisition(acquireInstance, handoffInstanceWait, restartInstanceInterval, nil, nil)
			if err != nil {
				return err
			}
			if !first {
				return errors.New("another controller owns the instance")
			}
			controller.setInstance(instance)
			return nil
		},
		Start: func(ctx context.Context) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return controller.startCore()
		},
		Log: func(message string) { logger.Log("Handoff", message) },
	})
	if err != nil {
		// The replacement must not keep a core or the instance ownership of a
		// handoff that did not complete.
		_ = conn.Close()
		if controller != nil {
			_ = controller.Close()
		}
		logger.Close()
		return nil, handoffFailure(err)
	}
	controller.setHandoffSession(session)
	return controller, nil
}

// reportHandoffFailure tells the instance that launched this process why it cannot
// take over. It covers the failures that happen before the replacement side of the
// protocol is set up: the previous instance is waiting on the handoff connection,
// and without this it could only report a readiness timeout instead of the reason.
func reportHandoffFailure(flags Flags, cause error) error {
	conn, err := handoff.Connect(context.Background(), flags.HandoffPipe, flags.HandoffToken, flags.HandoffPeer)
	if err != nil {
		return errors.Join(cause, fmt.Errorf("connect to the instance being replaced: %w", err))
	}
	defer conn.Close()
	if err := conn.Send(handoff.Message{Kind: handoff.KindFailed, Reason: cause.Error()}); err != nil {
		return errors.Join(cause, fmt.Errorf("report the handoff failure: %w", err))
	}
	return fmt.Errorf("%w: %w", ErrHandoffReported, cause)
}

// handoffFailure marks a handoff failure that the instance being replaced was
// already told about: that instance shows the message, so this process exits
// without reporting the same failure a second time.
func handoffFailure(err error) error {
	if errors.Is(err, handoff.ErrReported) {
		return fmt.Errorf("%w: %w", ErrHandoffReported, err)
	}
	return err
}

// validateHandoff checks what this replacement was asked to run without touching
// the controller it replaces. A failure here is reported as "not ready", which
// leaves the previous instance running.
func (a *App) validateHandoff(ctx context.Context) error {
	tun := a.TunActive()
	if (tun || (a.StartupProxyRequested() && platform.SystemProxyRequiresElevation())) && !platform.IsProcessElevated() {
		// Relaunching here would start a third process instead of handing over,
		// and the elevation the previous instance asked for was not granted.
		return errors.New("the handoff needs administrator elevation")
	}
	// The configuration and the core this replacement was asked to run are the ones
	// it already holds: it starts exactly those, so a re-read here could validate a
	// file that is not the one the core starts with.
	return a.validateOwnCandidate(ctx, tun, tun)
}

// startCore starts the core of the configuration this instance holds.
func (a *App) startCore() error {
	if err := a.ensureControllerOwnership(); err != nil {
		return err
	}
	start := a.coreStarter
	if start == nil {
		start = a.supervisor.Start
	}
	return start(a.runtimeConfig(a.TunActive()))
}

// ensureControllerOwnership rejects a core start from a controller that handed
// instance ownership to a replacement. Ownership only comes back by taking the
// instance lock again, so a start that would otherwise run without it is refused
// instead of competing with the replacement's core for the TUN device and the
// system proxy.
func (a *App) ensureControllerOwnership() error {
	a.mu.RLock()
	handedOver := a.handedOver
	a.mu.RUnlock()
	if handedOver {
		return ErrHandoffOwnershipLost
	}
	return nil
}

func (a *App) setInstance(instance *platform.SingleInstance) {
	a.mu.Lock()
	a.instance = instance
	a.handedOver = false
	a.mu.Unlock()
}

func (a *App) setHandoffSession(session *handoff.Session) {
	a.mu.Lock()
	a.handoff = session
	a.mu.Unlock()
}

// ConfirmHandoff tells the instance that was replaced that this controller owns
// the tray. It is a no-op for a process that did not replace one, and it returns
// ErrHandoffAborted when that instance took the controller back instead.
func (a *App) ConfirmHandoff() error {
	a.mu.Lock()
	session := a.handoff
	a.handoff = nil
	a.mu.Unlock()
	if session == nil {
		return nil
	}
	if err := session.Confirm(); err != nil {
		if a.logger != nil {
			a.logger.Log("Handoff", "Failed to confirm the controller handoff: "+err.Error())
		}
		return err
	}
	return nil
}

// launchHandoff transfers the controller to a replacement started with the
// platform's trackable launcher.
//
// The replacement confirms that it owns the controller and that its tray is up
// before this instance gives anything up; a failure after that point rolls back
// here, and the rollback restarts this instance's core only once the replacement
// is confirmed gone.
func (a *App) launchHandoff(flags string, elevated bool, proxyRestored bool, transport handoff.Transport) error {
	launch := a.handoffLauncher
	if launch == nil {
		launch = defaultHandoffLauncher
	}
	err := handoff.RunParent(context.Background(), handoff.ParentOptions{
		Transport: transport,
		Flags:     flags,
		Elevate:   elevated,
		Launch:    launch,
		Transfer:  a.suspendForHandoff,
		Release:   a.releaseInstance,
		Reacquire: a.reacquireInstance,
		Restore: func(ctx context.Context) error {
			return a.resumeAfterHandoff(ctx, proxyRestored)
		},
		Log: func(message string) {
			if a.logger != nil {
				a.logger.Log("Handoff", message)
			}
		},
	})
	if err == nil {
		return nil
	}
	return a.restoreProxyAfterFailedLaunch(proxyRestored, err)
}

// suspendForHandoff stops this instance's core and waits for the state
// notifications it queued, so the replacement does not run a callback that
// belongs to the core this instance just stopped. It does not close the
// controller: a handoff that fails after this point resumes it in this process.
//
// The callers of a handoff launch hold selectorMu (the tray's restart and
// elevated-launch paths), so this must not take it again.
func (a *App) suspendForHandoff(ctx context.Context) error {
	a.mu.RLock()
	supervisor := a.supervisor
	a.mu.RUnlock()
	if supervisor == nil {
		return errors.New("core supervisor is not configured")
	}
	return supervisor.Quiesce(ctx)
}

// releaseInstance gives up instance ownership so the replacement can take it. The
// controller remembers the handover: until the lock is taken back, its own start
// paths stay refused.
func (a *App) releaseInstance() {
	a.mu.Lock()
	instance := a.instance
	a.instance = nil
	a.handedOver = true
	a.mu.Unlock()
	if instance != nil {
		instance.Close()
	}
}

// reacquireInstance takes instance ownership back during a rollback.
func (a *App) reacquireInstance() error {
	instance, first, err := acquireInstance()
	if err != nil {
		return err
	}
	if !first {
		if instance != nil {
			instance.Close()
		}
		return errors.New("another controller took instance ownership")
	}
	a.setInstance(instance)
	return nil
}

// resumeAfterHandoff brings this instance's runtime back during a rollback. It
// runs only once the replacement is confirmed gone.
func (a *App) resumeAfterHandoff(ctx context.Context, proxyRestored bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.startCore(); err != nil {
		return err
	}
	if !proxyRestored {
		return nil
	}
	if err := a.EnableSystemProxy(); err != nil {
		return fmt.Errorf("restore the system proxy: %w", err)
	}
	return nil
}

// restoreProxyAfterFailedLaunch puts the system proxy back when the replacement
// never took it over. A successful handoff keeps it off: the replacement owns it.
func (a *App) restoreProxyAfterFailedLaunch(proxyRestored bool, cause error) error {
	if !proxyRestored {
		return cause
	}
	if err := a.EnableSystemProxy(); err != nil {
		return errors.Join(cause, fmt.Errorf("restore system proxy after launch failure: %w", err))
	}
	return cause
}

// handoffTransport returns the transport for a controller handoff. A controller
// without one has to use the one-shot launch path: transferring ownership without
// a way to confirm the replacement would risk two controllers owning the system
// proxy.
func (a *App) handoffTransport() (handoff.Transport, error) {
	if a.handoffTransportFn == nil {
		return nil, errors.New("this controller has no handoff transport")
	}
	return a.handoffTransportFn()
}
