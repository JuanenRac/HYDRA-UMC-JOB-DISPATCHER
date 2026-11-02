// HYDRA-UMC-JOB-DISPATCHER - the configuration file of the optional behaviours
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package dispatcher

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// FileConfig is the content of the JSON file named by -config: the tuned values of location
// preference, replanning and estimate ranges. Every section is optional and every switch inside is
// off unless the file turns it on, so a missing file, an empty file or `{}` change nothing.
type FileConfig struct {
	LocationCosts *LocationSection    `json:"location_costs,omitempty"`
	Replan        *ReplanSection      `json:"replan,omitempty"`
	ETAInterval   *IntervalSection    `json:"eta_interval,omitempty"`
	Maintenance   *MaintenanceSection `json:"maintenance,omitempty"`
	Events        *EventsSection      `json:"events,omitempty"`
	ToolConfirm   *ToolConfirmSection `json:"tool_confirmation,omitempty"`
}

// MaintenanceSection is the "maintenance" section: proposals of maintenance orders raised by a sustained drift. They only propose; a
// person approves or rejects each one. Priority, required_tool and location describe the job that approving one submits.
type MaintenanceSection struct {
	Enabled         bool     `json:"enabled"`
	Priority        *int     `json:"priority,omitempty"`
	RequiredTool    string   `json:"required_tool,omitempty"`
	Location        string   `json:"location,omitempty"`
	MaxOpen         *int     `json:"max_open,omitempty"`
	CooldownSeconds *float64 `json:"cooldown_seconds,omitempty"`
	ExpireSeconds   *float64 `json:"expire_seconds,omitempty"`
}

// EventsSection is the "events" section: the stream of job events and the signed webhooks. Off unless enabled. A webhook secret is
// never in the file: secret_env names the environment variable that holds it.
type EventsSection struct {
	Enabled          bool             `json:"enabled"`
	SSE              bool             `json:"sse,omitempty"`
	Buffer           *int             `json:"buffer,omitempty"`
	AllowedHosts     []string         `json:"allowed_hosts,omitempty"`
	TimeoutSeconds   *float64         `json:"timeout_seconds,omitempty"`
	MaxAttempts      *int             `json:"max_attempts,omitempty"`
	RetrySeconds     *float64         `json:"retry_seconds,omitempty"`
	KeepAliveSeconds *float64         `json:"keepalive_seconds,omitempty"`
	Webhooks         []WebhookSection `json:"webhooks,omitempty"`
}

// WebhookSection is one destination of the "events" section.
type WebhookSection struct {
	URL       string   `json:"url"`
	SecretEnv string   `json:"secret_env"`
	Types     []string `json:"types,omitempty"`
}

// ToolConfirmSection is the "tool_confirmation" section: ask the controller of the robot which tool it really carries before a job that
// needs one is given to it. url_template contains "{robot}". token_env names the environment variable with the bearer token (optional).
type ToolConfirmSection struct {
	Enabled        bool     `json:"enabled"`
	URLTemplate    string   `json:"url_template,omitempty"`
	TokenEnv       string   `json:"token_env,omitempty"`
	TTLSeconds     *float64 `json:"ttl_seconds,omitempty"`
	TimeoutSeconds *float64 `json:"timeout_seconds,omitempty"`
	OnUnconfirmed  string   `json:"on_unconfirmed,omitempty"`
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
	// StopAckTimeoutSeconds: how long a silent robot that can confirm a stop has to acknowledge the request (default 30).
	StopAckTimeoutSeconds *float64 `json:"stop_ack_timeout_seconds,omitempty"`
	// AllowUnconfirmed: send the job back to the queue even if the stop was not acknowledged (default false).
	AllowUnconfirmed bool `json:"allow_unconfirmed,omitempty"`
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
	if c.Replan != nil && c.Replan.StopAckTimeoutSeconds != nil {
		if v := *c.Replan.StopAckTimeoutSeconds; !(v > 0) || v > 86400 {
			return fmt.Errorf("configuration: replan.stop_ack_timeout_seconds must be greater than 0 and at most 86400")
		}
	}
	if m := c.Maintenance; m != nil {
		if m.Priority != nil && (*m.Priority < 0 || *m.Priority > 1000) {
			return fmt.Errorf("configuration: maintenance.priority must be between 0 and 1000")
		}
		if m.MaxOpen != nil && (*m.MaxOpen < 1 || *m.MaxOpen > 10000) {
			return fmt.Errorf("configuration: maintenance.max_open must be between 1 and 10000")
		}
		for name, v := range map[string]*float64{"cooldown_seconds": m.CooldownSeconds, "expire_seconds": m.ExpireSeconds} {
			if v != nil && (!(*v >= 0) || *v > 31536000) {
				return fmt.Errorf("configuration: maintenance.%s must be between 0 and 31536000", name)
			}
		}
	}
	if err := c.EventsSetting().Validate(); err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	if err := c.ToolConfirmSetting().Validate(); err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	if t := c.ToolConfirm; t != nil && t.Enabled {
		if u, err := url.Parse(t.URLTemplate); err != nil || !strings.Contains(t.URLTemplate, "{robot}") || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
			return fmt.Errorf("configuration: tool_confirmation.url_template must be an http(s) URL without credentials that contains {robot}")
		}
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
	cfg := ReplanConfig{Enabled: true, OnStale: on(c.Replan.OnStale), OnFault: on(c.Replan.OnFault), AllowUnconfirmed: c.Replan.AllowUnconfirmed}
	if c.Replan.StopAckTimeoutSeconds != nil {
		cfg.StopAckTimeout = time.Duration(*c.Replan.StopAckTimeoutSeconds * float64(time.Second))
	}
	return cfg
}

// IntervalSetting is the setting of the "eta_interval" section (off when absent); it goes into ETAConfig.Interval.
func (c FileConfig) IntervalSetting() IntervalConfig {
	if c.ETAInterval == nil {
		return IntervalConfig{}
	}
	return IntervalConfig{Enabled: c.ETAInterval.Enabled, LowPercentile: c.ETAInterval.LowPercentile, HighPercentile: c.ETAInterval.HighPercentile, MinSamples: c.ETAInterval.MinSamples}
}

// MaintenanceSetting is the board setting of the "maintenance" section (off when absent).
func (c FileConfig) MaintenanceSetting() MaintenanceConfig {
	if c.Maintenance == nil || !c.Maintenance.Enabled {
		return MaintenanceConfig{}
	}
	m := c.Maintenance
	cfg := DefaultMaintenanceConfig()
	cfg.Enabled = true
	cfg.RequiredTool, cfg.Location = m.RequiredTool, m.Location
	if m.Priority != nil {
		cfg.Priority = *m.Priority
	}
	if m.MaxOpen != nil {
		cfg.MaxOpen = *m.MaxOpen
	}
	if m.CooldownSeconds != nil {
		cfg.Cooldown = time.Duration(*m.CooldownSeconds * float64(time.Second))
	}
	if m.ExpireSeconds != nil {
		cfg.ExpireAfter = time.Duration(*m.ExpireSeconds * float64(time.Second))
	}
	return cfg
}

func seconds(p *float64, def time.Duration) time.Duration {
	if p == nil {
		return def
	}
	return time.Duration(*p * float64(time.Second))
}

// EventsSetting is the setting of the "events" section (off when absent).
func (c FileConfig) EventsSetting() EventsConfig {
	if c.Events == nil || !c.Events.Enabled {
		return EventsConfig{}
	}
	e := c.Events
	cfg := DefaultEventsConfig()
	cfg.Enabled, cfg.SSE, cfg.AllowedHosts = true, e.SSE, e.AllowedHosts
	if e.Buffer != nil {
		cfg.Buffer = *e.Buffer
	}
	if e.MaxAttempts != nil {
		cfg.MaxAttempts = *e.MaxAttempts
	}
	cfg.Timeout = seconds(e.TimeoutSeconds, cfg.Timeout)
	cfg.RetryDelay = seconds(e.RetrySeconds, cfg.RetryDelay)
	cfg.KeepAlive = seconds(e.KeepAliveSeconds, cfg.KeepAlive)
	for _, w := range e.Webhooks {
		cfg.Webhooks = append(cfg.Webhooks, WebhookConfig(w))
	}
	return cfg
}

// ToolConfirmSetting is the setting of the "tool_confirmation" section (off when absent). The default policy is the conservative one.
func (c FileConfig) ToolConfirmSetting() ToolConfirmConfig {
	if c.ToolConfirm == nil || !c.ToolConfirm.Enabled {
		return ToolConfirmConfig{}
	}
	t := c.ToolConfirm
	cfg := ToolConfirmConfig{Enabled: true, TTL: seconds(t.TTLSeconds, 30*time.Second), OnUnconfirmed: t.OnUnconfirmed}
	if cfg.OnUnconfirmed == "" {
		cfg.OnUnconfirmed = UnconfirmedSkip
	}
	return cfg
}
