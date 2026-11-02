// HYDRA-UMC-JOB-DISPATCHER - events of the jobs (server-sent events and signed webhooks)
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// A job that is assigned, ends well, fails or becomes unknown is announced to whoever listens, so the
// orchestrator and the dashboards do not have to poll. Two ways, both off by default and both
// configured in the "events" section of the configuration file: a stream of server-sent events
// (GET /events, with replay from Last-Event-ID) and webhooks. A webhook only goes to a destination
// whose host is in the list of allowed hosts, is signed with HMAC-SHA256 (the secret is read from an
// environment variable, never from the file) and never follows a redirect. Publishing an event never
// blocks the engine and never changes what it does: a slow or dead receiver loses nothing but its own copy.
package dispatcher

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Event types.
const (
	EventAssigned = "assigned"
	EventDone     = "done"
	EventFailed   = "failed"
	EventUnknown  = "unknown"
)

// Event is one thing that happened to a job.
type Event struct {
	ID      int64  `json:"id"`
	Type    string `json:"type"`
	JobID   string `json:"jobId"`
	RobotID string `json:"robotId,omitempty"`
	Attempt int    `json:"attempt,omitempty"`
	AtMs    int64  `json:"atMs"`
}

// EventsConfig is the setting of the events (the zero value is off).
type EventsConfig struct {
	Enabled        bool
	SSE            bool
	Buffer         int // how many recent events are kept for replay
	AllowedHosts   []string
	Webhooks       []WebhookConfig
	Timeout        time.Duration
	MaxAttempts    int
	RetryDelay     time.Duration
	KeepAlive      time.Duration
	SubscriberSize int
}

// WebhookConfig is one destination. SecretEnv names the environment variable that holds the signing secret.
type WebhookConfig struct {
	URL       string
	SecretEnv string
	Types     []string // empty: every type
}

// DefaultEventsConfig has the values of the section when the file does not say otherwise.
func DefaultEventsConfig() EventsConfig {
	return EventsConfig{Buffer: 256, Timeout: 5 * time.Second, MaxAttempts: 3, RetryDelay: time.Second, KeepAlive: 15 * time.Second, SubscriberSize: 64}
}

// ValidEventType reports whether t is one of the event types.
func ValidEventType(t string) bool {
	switch t {
	case EventAssigned, EventDone, EventFailed, EventUnknown:
		return true
	}
	return false
}

// hostAllowed reports whether the host (with its port when the destination names one) is in the list.
func hostAllowed(u *url.URL, allowed []string) bool {
	for _, a := range allowed {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		if a == strings.ToLower(u.Host) || a == strings.ToLower(u.Hostname()) && u.Port() == "" {
			return true
		}
	}
	return false
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Validate refuses a destination that is not on the list, carries credentials, is not https (except on this machine) or has no secret.
func (c EventsConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.Buffer < 1 || c.Buffer > 100000 {
		return errors.New("events.buffer must be between 1 and 100000")
	}
	if c.MaxAttempts < 1 || c.MaxAttempts > 20 {
		return errors.New("events.max_attempts must be between 1 and 20")
	}
	if c.Timeout <= 0 || c.Timeout > time.Minute || c.RetryDelay < 0 || c.RetryDelay > time.Hour || c.KeepAlive <= 0 {
		return errors.New("events: timeout_seconds must be in (0, 60], retry_seconds in [0, 3600] and keepalive_seconds greater than 0")
	}
	for _, w := range c.Webhooks {
		u, err := url.Parse(w.URL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return fmt.Errorf("events.webhooks: %q is not an http(s) URL", w.URL)
		}
		if u.User != nil {
			return fmt.Errorf("events.webhooks: the URL of a destination must not carry credentials")
		}
		if !hostAllowed(u, c.AllowedHosts) {
			return fmt.Errorf("events.webhooks: host %q is not in events.allowed_hosts", u.Host)
		}
		if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
			return fmt.Errorf("events.webhooks: %q must be https (plain http is only accepted for this machine)", w.URL)
		}
		if w.SecretEnv == "" {
			return fmt.Errorf("events.webhooks: %q needs secret_env, because a webhook is always signed", w.URL)
		}
		for _, t := range w.Types {
			if !ValidEventType(t) {
				return fmt.Errorf("events.webhooks: unknown event type %q", t)
			}
		}
	}
	return nil
}

// EventHub keeps the recent events, serves the subscribers and delivers the webhooks.
type EventHub struct {
	cfg  EventsConfig
	mu   sync.Mutex
	ring []Event
	next int64
	subs map[chan Event]struct{}
	now  func() time.Time
	done chan struct{}
	once sync.Once
}

// NewEventHub builds a hub and starts the delivery of the webhooks of cfg. The signing secrets are read from the environment here.
func NewEventHub(cfg EventsConfig) (*EventHub, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	h := &EventHub{cfg: cfg, subs: map[chan Event]struct{}{}, now: time.Now, done: make(chan struct{})}
	for _, w := range cfg.Webhooks {
		secret := os.Getenv(w.SecretEnv)
		if secret == "" {
			return nil, fmt.Errorf("events.webhooks: the environment variable %s (secret of %s) is empty", w.SecretEnv, w.URL)
		}
		h.startWebhook(w, []byte(secret))
	}
	return h, nil
}

// Config returns the setting the hub runs with.
func (h *EventHub) Config() EventsConfig { return h.cfg }

// SSEEnabled reports whether the stream route may answer.
func (h *EventHub) SSEEnabled() bool { return h != nil && h.cfg.Enabled && h.cfg.SSE }

// Close stops the delivery goroutines.
func (h *EventHub) Close() { h.once.Do(func() { close(h.done) }) }

// Publish records an event and hands it to every subscriber without waiting: a subscriber whose queue is full is dropped and must
// reconnect with Last-Event-ID, so the engine never waits for a reader.
func (h *EventHub) Publish(kind, jobID, robotID string, attempt int) {
	if h == nil || !h.cfg.Enabled {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next++
	ev := Event{ID: h.next, Type: kind, JobID: jobID, RobotID: robotID, Attempt: attempt, AtMs: h.now().UnixMilli()}
	h.ring = append(h.ring, ev)
	if len(h.ring) > h.cfg.Buffer {
		h.ring = append([]Event(nil), h.ring[len(h.ring)-h.cfg.Buffer:]...)
	}
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
			delete(h.subs, ch)
			close(ch)
		}
	}
}

// Subscribe returns the events after sinceID (still in the buffer) and a channel with the following ones. The channel is closed when
// the subscriber falls behind. Call cancel when done.
func (h *EventHub) Subscribe(sinceID int64) (replay []Event, ch <-chan Event, cancel func()) {
	c := make(chan Event, h.cfg.SubscriberSize)
	h.mu.Lock()
	for _, ev := range h.ring {
		if ev.ID > sinceID {
			replay = append(replay, ev)
		}
	}
	h.subs[c] = struct{}{}
	h.mu.Unlock()
	return replay, c, func() {
		h.mu.Lock()
		if _, ok := h.subs[c]; ok {
			delete(h.subs, c)
			close(c)
		}
		h.mu.Unlock()
	}
}

// Sign returns the signature of a delivery: HMAC-SHA256 over "<timestamp>.<body>", in hex. A receiver recomputes it and refuses an old timestamp.
func Sign(secret []byte, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func (h *EventHub) startWebhook(w WebhookConfig, secret []byte) {
	_, ch, cancel := h.Subscribe(1 << 62) // no replay
	wanted := map[string]bool{}
	for _, t := range w.Types {
		wanted[t] = true
	}
	client := &http.Client{
		Timeout:       h.cfg.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	go func() {
		defer func() { cancel() }()
		for {
			select {
			case <-h.done:
				return
			case ev, ok := <-ch:
				if !ok {
					// fell behind: take a fresh subscription and go on (the missed events stay in the stream replay)
					_, ch, cancel = h.Subscribe(1 << 62)
					continue
				}
				if len(wanted) > 0 && !wanted[ev.Type] {
					continue
				}
				h.deliver(client, w.URL, secret, ev)
			}
		}
	}()
}

func (h *EventHub) deliver(client *http.Client, target string, secret []byte, ev Event) {
	body, _ := json.Marshal(ev)
	var last error
	for attempt := 1; attempt <= h.cfg.MaxAttempts; attempt++ {
		ts := strconv.FormatInt(h.now().Unix(), 10)
		req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
		if err != nil {
			log.Printf("[job-dispatcher] webhook: %v", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Hydra-Event", ev.Type)
		req.Header.Set("X-Hydra-Event-Id", strconv.FormatInt(ev.ID, 10))
		req.Header.Set("X-Hydra-Timestamp", ts)
		req.Header.Set("X-Hydra-Signature", "sha256="+Sign(secret, ts, body))
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode/100 == 2 {
				return
			}
			err = fmt.Errorf("answered %d", resp.StatusCode)
		}
		last = err
		select {
		case <-h.done:
			return
		case <-time.After(h.cfg.RetryDelay * time.Duration(attempt)):
		}
	}
	log.Printf("[job-dispatcher] webhook %s: event %d (%s) not delivered after %d attempts: %v", target, ev.ID, ev.Type, h.cfg.MaxAttempts, last)
}

// SetEvents installs the hub that receives the events of the engine (nil removes it, the default).
func (e *Engine) SetEvents(h *EventHub) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = h
}

// emitLocked publishes an event about j. It only appends to memory and never waits, so it is safe under the lock.
func (e *Engine) emitLocked(kind string, j *Job) {
	if e.events != nil {
		e.events.Publish(kind, j.ID, j.AssignedRobot, j.Attempt)
	}
}
