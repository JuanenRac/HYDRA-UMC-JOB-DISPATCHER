// HYDRA-UMC-JOB-DISPATCHER - physical confirmation of the tool of a robot
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// A robot declares the tool it carries in its heartbeat, but the tool head itself knows better: the
// controller of the robot reads the identifier of the head over the bus. With the confirmation on, a job
// that needs a tool is only given to a robot whose controller says it carries that tool. The question is
// asked (outside the lock of the engine) just before a dispatch pass; the answers are kept for a short
// time and a robot nobody has confirmed is not offered the job unless the configuration says "allow".
// Off by default: without it nothing is asked and the declared tool is trusted as before.
package dispatcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Policies for a robot whose tool could not be confirmed.
const (
	UnconfirmedSkip  = "skip"  // not offered the job (the default, the conservative one)
	UnconfirmedAllow = "allow" // the declared tool is trusted
)

// ToolConfirmConfig is the setting of the confirmation (the zero value is off).
type ToolConfirmConfig struct {
	Enabled       bool
	TTL           time.Duration // how long a confirmation is valid
	OnUnconfirmed string
}

// ToolFetcher asks the controller of a robot which tool it really carries.
type ToolFetcher func(robotID string) (string, error)

type toolSeen struct {
	tool string
	at   time.Time
}

type toolConfirmation struct {
	cfg   ToolConfirmConfig
	fetch ToolFetcher
	seen  map[string]toolSeen
}

// Validate refuses a policy or a lifetime that makes no sense.
func (c ToolConfirmConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.OnUnconfirmed != UnconfirmedSkip && c.OnUnconfirmed != UnconfirmedAllow {
		return fmt.Errorf("tool_confirmation.on_unconfirmed must be %q or %q", UnconfirmedSkip, UnconfirmedAllow)
	}
	if c.TTL <= 0 || c.TTL > 24*time.Hour {
		return errors.New("tool_confirmation.ttl_seconds must be greater than 0 and at most 86400")
	}
	return nil
}

// SetToolConfirmation switches the confirmation on (fetch must not be nil) or off.
func (e *Engine) SetToolConfirmation(cfg ToolConfirmConfig, fetch ToolFetcher) error {
	if cfg.Enabled && fetch == nil {
		return errors.New("tool confirmation needs a way to ask the controller")
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if !cfg.Enabled {
		e.toolCheck = toolConfirmation{}
		return nil
	}
	e.toolCheck = toolConfirmation{cfg: cfg, fetch: fetch, seen: e.toolCheck.seen}
	if e.toolCheck.seen == nil {
		e.toolCheck.seen = map[string]toolSeen{}
	}
	return nil
}

// ToolConfirmationEnabled reports whether the confirmation is on.
func (e *Engine) ToolConfirmationEnabled() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.toolCheck.cfg.Enabled
}

// ConfirmTools asks the controller of every available robot and keeps the answers. It talks to the network, so it never holds the
// lock of the engine while it waits. A robot that does not answer keeps no confirmation. Returns how many were confirmed.
func (e *Engine) ConfirmTools() int {
	e.mu.Lock()
	if !e.toolCheck.cfg.Enabled {
		e.mu.Unlock()
		return 0
	}
	fetch := e.toolCheck.fetch
	ids := make([]string, 0, len(e.robots))
	for id, r := range e.robots {
		if r.Available {
			ids = append(ids, id)
		}
	}
	e.mu.Unlock()
	answers := map[string]string{}
	for _, id := range ids {
		if tool, err := fetch(id); err == nil {
			answers[id] = tool
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.toolCheck.cfg.Enabled {
		return 0
	}
	now := e.now()
	for id := range e.toolCheck.seen {
		if _, ok := e.robots[id]; !ok {
			delete(e.toolCheck.seen, id)
		}
	}
	for _, id := range ids {
		if tool, ok := answers[id]; ok {
			e.toolCheck.seen[id] = toolSeen{tool: tool, at: now}
		} else {
			delete(e.toolCheck.seen, id) // silence is not a confirmation
		}
	}
	return len(answers)
}

// toolConfirmedLocked reports whether r may take j as far as the physical tool is concerned. Caller must hold e.mu.
func (e *Engine) toolConfirmedLocked(r *Robot, j *Job, now time.Time) bool {
	if !e.toolCheck.cfg.Enabled || j.RequiredTool == "" {
		return true
	}
	if s, ok := e.toolCheck.seen[r.ID]; ok && now.Sub(s.at) <= e.toolCheck.cfg.TTL && !s.at.After(now) {
		return s.tool == j.RequiredTool
	}
	return e.toolCheck.cfg.OnUnconfirmed == UnconfirmedAllow
}

// HTTPToolFetcher asks a service with GET on urlTemplate, where "{robot}" is replaced by the escaped robot id. The answer is JSON with
// "tool_id" (or "tool"). The token, when not empty, goes as a bearer token. Redirects are not followed.
func HTTPToolFetcher(urlTemplate, token string, timeout time.Duration) ToolFetcher {
	client := &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return func(robotID string) (string, error) {
		target := strings.ReplaceAll(urlTemplate, "{robot}", url.PathEscape(robotID))
		req, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			return "", err
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("answered %d", resp.StatusCode)
		}
		var body struct {
			ToolID string `json:"tool_id"`
			Tool   string `json:"tool"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body); err != nil {
			return "", err
		}
		if body.ToolID != "" {
			return body.ToolID, nil
		}
		return body.Tool, nil
	}
}
