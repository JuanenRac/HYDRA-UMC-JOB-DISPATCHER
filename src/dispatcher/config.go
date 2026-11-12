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
	LocationCosts  *LocationSection       `json:"location_costs,omitempty"`
	Replan         *ReplanSection         `json:"replan,omitempty"`
	ETAInterval    *IntervalSection       `json:"eta_interval,omitempty"`
	Maintenance    *MaintenanceSection    `json:"maintenance,omitempty"`
	Events         *EventsSection         `json:"events,omitempty"`
	ToolConfirm    *ToolConfirmSection    `json:"tool_confirmation,omitempty"`
	HealthGRPC     *HealthGRPCSection     `json:"health_grpc,omitempty"`
	Metrics        *MetricsSection        `json:"metrics,omitempty"`
	MissionState   *MissionStateSection   `json:"mission_state,omitempty"`
	ProposalVerify *ProposalVerifySection `json:"proposal_verify,omitempty"`
	Archive        *ArchiveSection        `json:"archive,omitempty"`
}

// ArchiveSection is the "archive" section: how long a job that ended (done, failed or cancelled) stays in the live queue before it is archived,
// and how often the service looks for such jobs. An archived job stays in the database and is read with GET /jobs?archived=1. Days is 30 and
// every_minutes 60 when absent; days 0 turns the archive off. It needs the database (-db); a change of this section needs a restart.
type ArchiveSection struct {
	Days         *float64 `json:"days,omitempty"`
	EveryMinutes *float64 `json:"every_minutes,omitempty"`
}

// ArchiveSetting is the setting of the "archive" section: 30 days and a look every hour when the section is absent.
func (c FileConfig) ArchiveSetting() ArchiveConfig {
	cfg := ArchiveConfig{After: DefaultArchiveAfter, Every: DefaultArchiveEvery}
	if c.Archive == nil {
		return cfg
	}
	if c.Archive.Days != nil {
		cfg.After = time.Duration(*c.Archive.Days * 24 * float64(time.Hour))
	}
	if c.Archive.EveryMinutes != nil {
		cfg.Every = time.Duration(*c.Archive.EveryMinutes * float64(time.Minute))
	}
	return cfg
}

// Validate refuses a number of days outside 0 to 3650 and a look more often than every minute or less often than every day.
func (a *ArchiveSection) Validate() error {
	if a == nil {
		return nil
	}
	if a.Days != nil && !(*a.Days >= 0 && *a.Days <= 3650) {
		return fmt.Errorf("archive.days must be between 0 and 3650 (0 turns the archive off)")
	}
	if a.EveryMinutes != nil && !(*a.EveryMinutes >= 1 && *a.EveryMinutes <= 1440) {
		return fmt.Errorf("archive.every_minutes must be between 1 and 1440")
	}
	return nil
}

// ProposalVerifySection is the "proposal_verify" section: a job that carries a signed trajectory proposal is queued only when HYDRA-UMC-SERVER
// (POST /api/trajectory/proposals/verify) finds the proposal valid for the robot named with it. Off unless enabled. The credential is a file:
// identity_key_file (the signing key of the SERVER, a short token is signed for every request) or token_file (a token of the SERVER), exactly
// one of them. require refuses a job without a proposal. A change of this section needs a restart.
type ProposalVerifySection struct {
	Enabled         bool     `json:"enabled"`
	ServerURL       string   `json:"server_url,omitempty"`
	IdentityKeyFile string   `json:"identity_key_file,omitempty"`
	TokenFile       string   `json:"token_file,omitempty"`
	TimeoutSeconds  *float64 `json:"timeout_seconds,omitempty"`
	Require         bool     `json:"require,omitempty"`
	AllowPlainHTTP  bool     `json:"allow_plain_http,omitempty"`
}

// ProposalVerifySetting is the setting of the "proposal_verify" section (off when absent).
func (c FileConfig) ProposalVerifySetting() ProposalVerifyConfig {
	if c.ProposalVerify == nil || !c.ProposalVerify.Enabled {
		return ProposalVerifyConfig{}
	}
	p := c.ProposalVerify
	return ProposalVerifyConfig{
		Enabled: true, ServerURL: p.ServerURL, IdentityKeyFile: p.IdentityKeyFile, TokenFile: p.TokenFile,
		Timeout: seconds(p.TimeoutSeconds, 5*time.Second), Require: p.Require, AllowPlainHTTP: p.AllowPlainHTTP,
	}
}

// MissionStateSection is the "mission_state" section: the state of the queue (job-dispatch-state/1) published retained on an MQTT topic,
// read-only for whoever reads it. Off unless enabled. The user and the password of the broker are never in the file: user_env and
// password_env name the environment variables that hold them. A change of this section needs a restart.
type MissionStateSection struct {
	Enabled         bool     `json:"enabled"`
	URL             string   `json:"url,omitempty"`
	Topic           string   `json:"topic,omitempty"`
	UserEnv         string   `json:"user_env,omitempty"`
	PasswordEnv     string   `json:"password_env,omitempty"`
	IntervalSeconds *float64 `json:"interval_seconds,omitempty"`
	RefreshSeconds  *float64 `json:"refresh_seconds,omitempty"`
}

// MetricsSection is the "metrics" section: GET /metrics in the text format of Prometheus. Off unless enabled (closed, the route answers 404). Open, it
// answers a caller on this machine, or any caller that presents the bearer token when the API has one. A change of this section needs a restart.
type MetricsSection struct {
	Enabled bool `json:"enabled"`
}

// HealthGRPCSection is the "health_grpc" section: the health service of the shared contract over gRPC. Off unless enabled; it
// listens on loopback unless listen says otherwise, and an address other than loopback needs a token. token_env names the
// environment variable with the bearer token (when absent the token of the HTTP API is used). A change of this section needs a
// restart.
type HealthGRPCSection struct {
	Enabled              bool     `json:"enabled"`
	Listen               string   `json:"listen,omitempty"`
	TokenEnv             string   `json:"token_env,omitempty"`
	MaxConnections       *int     `json:"max_connections,omitempty"`
	MaxWatchers          *int     `json:"max_watchers,omitempty"`
	WatchIntervalSeconds *float64 `json:"watch_interval_seconds,omitempty"`
	KeepaliveSeconds     *float64 `json:"keepalive_seconds,omitempty"`
}

// Validate refuses values outside the ranges the service can work with.
func (h *HealthGRPCSection) Validate() error {
	if h == nil || !h.Enabled {
		return nil
	}
	for name, v := range map[string]*int{"max_connections": h.MaxConnections, "max_watchers": h.MaxWatchers} {
		if v != nil && (*v < 1 || *v > 1024) {
			return fmt.Errorf("health_grpc.%s must be between 1 and 1024", name)
		}
	}
	if v := h.WatchIntervalSeconds; v != nil && !(*v >= 0.1 && *v <= 60) {
		return fmt.Errorf("health_grpc.watch_interval_seconds must be between 0.1 and 60")
	}
	if v := h.KeepaliveSeconds; v != nil && !(*v >= 1 && *v <= 3600) {
		return fmt.Errorf("health_grpc.keepalive_seconds must be between 1 and 3600")
	}
	return nil
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
	// PersistStopAcks: keep the stop acknowledgements and list them at GET /jobs/stop-acks for a standby (default false).
	PersistStopAcks bool `json:"persist_stop_acks,omitempty"`
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
	if err := c.MissionStateSetting(os.Getenv).Validate(); err != nil {
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
	if err := c.ProposalVerifySetting().Validate(); err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	if err := c.HealthGRPC.Validate(); err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	if err := c.Archive.Validate(); err != nil {
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
	cfg := ReplanConfig{Enabled: true, OnStale: on(c.Replan.OnStale), OnFault: on(c.Replan.OnFault), AllowUnconfirmed: c.Replan.AllowUnconfirmed, PersistStopAcks: c.Replan.PersistStopAcks}
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

// MissionStateSetting is the setting of the "mission_state" section (off when absent); getenv reads the user and the password of the broker.
func (c FileConfig) MissionStateSetting(getenv func(string) string) MissionStateConfig {
	if c.MissionState == nil || !c.MissionState.Enabled {
		return MissionStateConfig{}
	}
	m := c.MissionState
	cfg := MissionStateConfig{Enabled: true, URL: m.URL, Topic: StateTopic, Interval: 5 * time.Second, Refresh: 60 * time.Second}
	if m.Topic != "" {
		cfg.Topic = m.Topic
	}
	cfg.Interval = seconds(m.IntervalSeconds, cfg.Interval)
	cfg.Refresh = seconds(m.RefreshSeconds, cfg.Refresh)
	if m.UserEnv != "" {
		cfg.User = getenv(m.UserEnv)
	}
	if m.PasswordEnv != "" {
		cfg.Password = getenv(m.PasswordEnv)
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
