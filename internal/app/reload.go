package app

import (
	"context"
	"errors"
	"path/filepath"

	"sing-box-drover/internal/clash"
	"sing-box-drover/internal/config"
	"sing-box-drover/internal/core"
	platform "sing-box-drover/internal/windows"
)

type configCandidate struct {
	source    config.ConfigSource
	config    config.SingBoxConfig
	api       *clash.Client
	selectors []clash.Selector
	tun       bool
}

type configSnapshot struct {
	source    config.ConfigSource
	config    config.SingBoxConfig
	api       *clash.Client
	selectors []clash.Selector
}

func (a *App) readConfigCandidate(tun, requireTun bool) (configCandidate, error) {
	path := a.source.FilePath
	if path == "" {
		path = a.options.SBConfigFile
	}
	return a.readConfigCandidateAt(path, tun, requireTun)
}

func (a *App) readConfigCandidateAt(path string, tun, requireTun bool) (configCandidate, error) {
	if path == "" {
		return configCandidate{}, errors.New("configuration file path is empty")
	}
	source, sbConfig, err := config.ReadValidatedConfig(path)
	if err != nil {
		return configCandidate{}, err
	}
	if tun && !sbConfig.HasTunInbound {
		if requireTun {
			return configCandidate{}, errors.New("TUN inbound is not configured")
		}
		tun = false
	}

	selectors := staticSelectors(sbConfig.Selectors)
	var api *clash.Client
	if sbConfig.ClashAPI.IsConfigured() {
		api = clash.NewClient(sbConfig.ClashAPI.ExternalController, sbConfig.ClashAPI.Secret)
	}
	return configCandidate{
		source:    source,
		config:    sbConfig,
		api:       api,
		selectors: selectors,
		tun:       tun,
	}, nil
}

// checkConfigCandidate validates the candidate the *current* process would run.
func (a *App) checkConfigCandidate(tun, requireTun bool) (configCandidate, error) {
	return a.checkConfigCandidateContext(context.Background(), tun, requireTun)
}

func (a *App) checkConfigCandidateContext(ctx context.Context, tun, requireTun bool) (configCandidate, error) {
	candidate, err := a.readConfigCandidate(tun, requireTun)
	if err != nil {
		return configCandidate{}, err
	}
	return a.checkCandidateConfig(ctx, candidate, nil)
}

// checkReplacementCandidate preflights what a replacement process will actually
// run. The replacement re-reads the INI in NewAt, so this has to load the same
// options and the same core executable: validating the file and the binary this
// instance happens to hold would pass for a replacement that is about to fail on
// a changed sb-config-file or sb-dir.
//
// The guard below only covers callers that build an App without INI options; a
// controller started by NewAt always has them resolved, and then the INI is what
// the replacement reads.
func (a *App) checkReplacementCandidate(ctx context.Context, tun, requireTun bool) (configCandidate, error) {
	options := a.options
	if options.SBConfigFile != "" {
		loaded, err := a.loadOptions()
		if err != nil {
			return configCandidate{}, err
		}
		options = loaded
	}
	path := options.SBConfigFile
	if path == "" {
		path = a.source.FilePath
	}
	candidate, err := a.readConfigCandidateAt(path, tun, requireTun)
	if err != nil {
		return configCandidate{}, err
	}
	corePath := filepath.Join(options.SBDir, platform.CoreExecutableName())
	return a.checkCandidateConfig(ctx, candidate, func(ctx context.Context, runtimeJSON string) error {
		return core.CheckExecutable(ctx, corePath, runtimeJSON)
	})
}

// validateOwnCandidate preflights what this process will actually start: the
// configuration and the core path it already holds. A replacement must not
// re-read either of them here: the file it re-read could differ from the one its
// core starts with, and then a change made while the handoff was in flight would
// be started without ever being checked.
func (a *App) validateOwnCandidate(ctx context.Context, tun, requireTun bool) error {
	if tun && !a.config.HasTunInbound {
		if requireTun {
			return errors.New("TUN inbound is not configured")
		}
		tun = false
	}
	candidate := configCandidate{
		source:    a.source,
		config:    a.config,
		api:       a.api,
		selectors: a.selectors,
		tun:       tun,
	}
	corePath := coreExecutablePath(a.options)
	_, err := a.checkCandidateConfig(ctx, candidate, func(ctx context.Context, runtimeJSON string) error {
		return core.CheckExecutable(ctx, corePath, runtimeJSON)
	})
	return err
}

// checkCandidateConfig validates the runtime JSON the candidate would start
// with. The caller's context bounds the check subprocess, so a cancelled resume
// does not keep a `sing-box check` alive behind it. A checker override selects a
// different executable; without one the supervisor's own core is used.
func (a *App) checkCandidateConfig(ctx context.Context, candidate configCandidate, checker func(context.Context, string) error) (configCandidate, error) {
	runtimeJSON := runtimeConfigFor(candidate.config, candidate.tun)
	if a.configChecker != nil {
		if err := a.configChecker(runtimeJSON); err != nil {
			return configCandidate{}, err
		}
		return candidate, nil
	}
	if checker == nil {
		if a.supervisor == nil {
			return configCandidate{}, errors.New("core configuration checker is not configured")
		}
		checker = a.supervisor.CheckContext
	}
	if err := checker(ctx, runtimeJSON); err != nil {
		return configCandidate{}, err
	}
	return candidate, nil
}

func runtimeConfigFor(cfg config.SingBoxConfig, tun bool) string {
	if tun {
		return cfg.JSONWithTUN
	}
	return cfg.JSONWithoutTUN
}

func (a *App) applyConfigCandidate(candidate configCandidate) configSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	previous := configSnapshot{
		source:    a.source,
		config:    a.config,
		api:       a.api,
		selectors: cloneSelectors(a.selectors),
	}
	a.source = candidate.source
	a.config = candidate.config
	a.api = candidate.api
	a.apiReady = false
	a.selectors = cloneSelectors(candidate.selectors)
	return previous
}

func (a *App) restoreConfigSnapshot(snapshot configSnapshot) {
	a.mu.Lock()
	a.source = snapshot.source
	a.config = snapshot.config
	a.api = snapshot.api
	a.selectors = cloneSelectors(snapshot.selectors)
	a.mu.Unlock()
}

// restartWithConfig restarts the core in this process with the configuration
// this instance holds. It is the entry point for user actions that have no
// context of their own.
func (a *App) restartWithConfig(tun, requireTun bool) error {
	return a.restartWithConfigContext(context.Background(), tun, requireTun)
}

// restartWithConfigContext is restartWithConfig for a caller that can be
// cancelled. The preflight runs a `sing-box check` subprocess and the reload
// itself replaces a running core, so a cancelled recovery must be able to stop
// before it starts either of them.
func (a *App) restartWithConfigContext(ctx context.Context, tun, requireTun bool) error {
	// A controller that handed ownership to a replacement that is still running
	// must not start a core: the replacement may already run one. Checking here
	// keeps the refusal ahead of the proxy handover and the config swap below.
	if err := a.ensureControllerOwnership(); err != nil {
		return err
	}
	candidate, err := a.checkConfigCandidateContext(ctx, tun, requireTun)
	if err != nil {
		return err
	}
	// Recheck right before the disruptive part: the preflight may have taken long
	// enough for the caller to give up, and starting a new core then would leave a
	// core running for a recovery that is already over.
	if err := ctx.Err(); err != nil {
		return err
	}
	if candidate.tun {
		if err := a.checkTunPrivileges(); err != nil {
			return err
		}
	}
	if a.supervisor == nil {
		return errors.New("core supervisor is not configured")
	}
	proxyReenable := false
	a.proxyMu.Lock()
	a.mu.RLock()
	proxyActive := a.proxyActive && a.proxyOwned
	a.mu.RUnlock()
	if proxyActive {
		proxyReenable, err = a.restoreSystemProxyLocked()
		if err != nil {
			a.proxyMu.Unlock()
			return errors.Join(errors.New("prepare system proxy for configuration change"), err)
		}
	}
	a.proxyMu.Unlock()
	previous := a.applyConfigCandidate(candidate)
	start := a.coreStarter
	if start == nil {
		start = a.supervisor.Start
	}
	if err := start(runtimeConfigFor(candidate.config, candidate.tun)); err != nil {
		a.restoreConfigSnapshot(previous)
		return err
	}
	a.mu.Lock()
	a.tunActive = candidate.tun
	a.mu.Unlock()
	if proxyReenable {
		if err := a.EnableSystemProxy(); err != nil {
			return errors.Join(errors.New("enable system proxy for new configuration"), err)
		}
	}
	return nil
}
