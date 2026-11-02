// =============================================================================
// HYDRA-UMC-JOB-DISPATCHER - reload.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
// =============================================================================
package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/dispatcher"
)

// configReloader re-reads the configuration file given with -config and installs the values the engine can change while it runs: the
// location costs, the replanning and the ranges of the estimates. The switches that shape the process itself (-eta, the address, the
// token, the database) are not in the file and stay as they were started.
//
// It fails closed: a file that cannot be read, is not valid, or asks for something the running switches do not allow (a range for the
// estimates without -eta) changes nothing, and the values in force stay exactly as they were.
type configReloader struct {
	path       string
	engine     *dispatcher.Engine
	board      *dispatcher.MaintenanceBoard
	etaOn      bool
	etaDefault time.Duration
	etaWindow  int

	mu sync.Mutex // two reloads never interleave
}

// withBoard lets a reload also install the "maintenance" section.
func (r *configReloader) withBoard(b *dispatcher.MaintenanceBoard) *configReloader {
	r.board = b
	return r
}

func newConfigReloader(path string, engine *dispatcher.Engine, etaOn bool, etaDefault time.Duration, etaWindow int) *configReloader {
	return &configReloader{path: path, engine: engine, etaOn: etaOn, etaDefault: etaDefault, etaWindow: etaWindow}
}

// apply installs a configuration that has already been checked.
func (r *configReloader) apply(cfg dispatcher.FileConfig) error {
	if err := r.engine.SetLocationCosts(cfg.LocationSetting()); err != nil {
		return err
	}
	if r.etaOn {
		r.engine.SetETA(dispatcher.ETAConfig{Enabled: true, DefaultDuration: r.etaDefault, Window: r.etaWindow, Interval: cfg.IntervalSetting()})
	}
	r.engine.SetReplan(cfg.ReplanSetting())
	if r.board != nil {
		r.board.SetConfig(cfg.MaintenanceSetting())
	}
	return nil
}

// Reload is what POST /admin/reload-config runs. It returns a short summary of the values now in force, or the reason nothing changed.
func (r *configReloader) Reload() (map[string]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cfg, err := dispatcher.LoadConfigFile(r.path)
	if err != nil {
		return nil, err
	}
	if problem := configProblem(cfg, r.etaOn); problem != "" {
		return nil, fmt.Errorf("%s", problem)
	}
	if err := r.apply(cfg); err != nil {
		return nil, err
	}
	replan := cfg.ReplanSetting()
	return map[string]any{
		"replan": map[string]any{
			"enabled":               replan.Enabled,
			"allowUnconfirmed":      replan.AllowUnconfirmed,
			"stopAckTimeoutSeconds": replan.StopAckTimeout.Seconds(),
		},
		"locationCosts": cfg.LocationSetting().Enabled,
		"etaInterval":   r.etaOn && cfg.IntervalSetting().Enabled,
		"maintenance":   cfg.MaintenanceSetting().Enabled,
	}, nil
}
