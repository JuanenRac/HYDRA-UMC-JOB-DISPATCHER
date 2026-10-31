// HYDRA-UMC-JOB-DISPATCHER - the configuration file of the optional behaviours
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package dispatcher

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

// FileConfig is the content of the JSON file named by -config: the tuned values of location
// preference, replanning and estimate ranges. Every section is optional and every switch inside is
// off unless the file turns it on, so a missing file, an empty file or `{}` change nothing.
type FileConfig struct {
	LocationCosts *LocationSection `json:"location_costs,omitempty"`
	Replan        *ReplanSection   `json:"replan,omitempty"`
	ETAInterval   *IntervalSection `json:"eta_interval,omitempty"`
}

// LocationSection is the "location_costs" section: the declared table of costs between locations.
type LocationSection struct {
	Enabled bool                          `json:"enabled"`
	Costs   map[string]map[string]float64 `json:"costs"`
}

// ReplanSection is the "replan" section. on_stale and on_fault are on when the section is
// enabled unless the file says false.
type ReplanSection struct {
	Enabled bool  `json:"enabled"`
	OnStale *bool `json:"on_stale,omitempty"`
	OnFault *bool `json:"on_fault,omitempty"`
}

// IntervalSection is the "eta_interval" section; it needs the estimates (-eta) to do anything.
type IntervalSection struct {
	Enabled        bool `json:"enabled"`
	LowPercentile  int  `json:"low_percentile,omitempty"`
	HighPercentile int  `json:"high_percentile,omitempty"`
	MinSamples     int  `json:"min_samples,omitempty"`
}

// ParseConfig reads a configuration from JSON. An unknown field is an error (a misspelt switch
// must not silently stay off), as is anything after the document.
func ParseConfig(data []byte) (FileConfig, error) {
	var c FileConfig
	if len(bytes.TrimSpace(data)) == 0 {
		return c, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return FileConfig{}, fmt.Errorf("configuration: %w", err)
	}
	if dec.More() {
		return FileConfig{}, fmt.Errorf("configuration: unexpected data after the JSON document")
	}
	if err := c.Validate(); err != nil {
		return FileConfig{}, err
	}
	return c, nil
}

// LoadConfigFile reads the configuration file at path.
func LoadConfigFile(path string) (FileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return FileConfig{}, fmt.Errorf("configuration: %w", err)
	}
	return ParseConfig(data)
}

// Validate refuses a table with an invalid cost or percentiles that make no range.
func (c FileConfig) Validate() error {
	if err := c.LocationSetting().Validate(); err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	if c.ETAInterval != nil && c.ETAInterval.Enabled && !c.IntervalSetting().Valid() {
		return fmt.Errorf("configuration: eta_interval needs 0 <= low_percentile < high_percentile <= 100 and min_samples of at least 1")
	}
	return nil
}

// LocationSetting is the engine setting of the "location_costs" section (off when absent).
func (c FileConfig) LocationSetting() LocationConfig {
	if c.LocationCosts == nil {
		return LocationConfig{}
	}
	return LocationConfig{Enabled: c.LocationCosts.Enabled, Costs: c.LocationCosts.Costs}
}

// ReplanSetting is the engine setting of the "replan" section (off when absent).
func (c FileConfig) ReplanSetting() ReplanConfig {
	if c.Replan == nil || !c.Replan.Enabled {
		return ReplanConfig{}
	}
	on := func(p *bool) bool { return p == nil || *p }
	return ReplanConfig{Enabled: true, OnStale: on(c.Replan.OnStale), OnFault: on(c.Replan.OnFault)}
}

// IntervalSetting is the setting of the "eta_interval" section (off when absent); it goes into ETAConfig.Interval.
func (c FileConfig) IntervalSetting() IntervalConfig {
	if c.ETAInterval == nil {
		return IntervalConfig{}
	}
	return IntervalConfig{Enabled: c.ETAInterval.Enabled, LowPercentile: c.ETAInterval.LowPercentile, HighPercentile: c.ETAInterval.HighPercentile, MinSamples: c.ETAInterval.MinSamples}
}
