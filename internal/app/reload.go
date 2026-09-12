package app

import (
	"errors"

	"sing-box-drover/internal/clash"
	"sing-box-drover/internal/config"
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

func (a *App) checkConfigCandidate(tun, requireTun bool) (configCandidate, error) {
	candidate, err := a.readConfigCandidate(tun, requireTun)
	if err != nil {
		return configCandidate{}, err
	}
	check := a.configChecker
	if check == nil && a.supervisor != nil {
		check = a.supervisor.Check
	}
	if check == nil {
		return configCandidate{}, errors.New("core configuration checker is not configured")
	}
	if err := check(runtimeConfigFor(candidate.config, candidate.tun)); err != nil {
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

func (a *App) restartWithConfig(tun, requireTun bool) error {
	candidate, err := a.checkConfigCandidate(tun, requireTun)
	if err != nil {
		return err
	}
	if candidate.tun && !platform.IsProcessElevated() {
		return platform.ErrElevationRequired
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
