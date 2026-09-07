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
	restored  bool
}

func (a *App) readConfigCandidate(tun, requireTun bool) (configCandidate, error) {
	if tun && !platform.IsProcessElevated() {
		return configCandidate{}, platform.ErrElevationRequired
	}
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
	if a.options.SelectorPersist {
		applyPersistedStatic(selectors, a.state)
	}
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
		restored:  a.restored,
	}
	a.source = candidate.source
	a.config = candidate.config
	a.api = candidate.api
	a.selectors = cloneSelectors(candidate.selectors)
	a.restored = false
	return previous
}

func (a *App) restoreConfigSnapshot(snapshot configSnapshot) {
	a.mu.Lock()
	a.source = snapshot.source
	a.config = snapshot.config
	a.api = snapshot.api
	a.selectors = cloneSelectors(snapshot.selectors)
	a.restored = snapshot.restored
	a.mu.Unlock()
}

func (a *App) restartWithConfig(tun, requireTun bool) error {
	candidate, err := a.readConfigCandidate(tun, requireTun)
	if err != nil {
		return err
	}
	if a.supervisor == nil {
		return errors.New("core supervisor is not configured")
	}
	previous := a.applyConfigCandidate(candidate)
	if err := a.supervisor.Start(runtimeConfigFor(candidate.config, candidate.tun)); err != nil {
		a.restoreConfigSnapshot(previous)
		return err
	}
	a.mu.Lock()
	a.tunActive = candidate.tun
	a.mu.Unlock()
	return nil
}
