// HYDRA-UMC-JOB-DISPATCHER - tests of the job events and of the physical confirmation of the tool
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package dispatcher

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func eventsOn(t *testing.T, mutate func(*EventsConfig)) *EventHub {
	t.Helper()
	cfg := DefaultEventsConfig()
	cfg.Enabled, cfg.SSE = true, true
	if mutate != nil {
		mutate(&cfg)
	}
	hub, err := NewEventHub(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hub.Close)
	return hub
}

func TestEvents_EngineAnnouncesAssignedDoneAndFailed(t *testing.T) {
	e := NewEngine()
	hub := eventsOn(t, nil)
	e.SetEvents(hub)
	e.UpsertRobot(Robot{ID: "r1", Available: true})
	e.UpsertRobot(Robot{ID: "r2", Available: true})
	for _, id := range []string{"a", "b", "c"} {
		if err := e.AddJob(Job{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	e.DispatchOnce()
	var assigned []Job
	for _, j := range e.Jobs() {
		if j.Status == StatusAssigned {
			assigned = append(assigned, j)
		}
	}
	if len(assigned) != 2 {
		t.Fatalf("assigned %d jobs, want 2", len(assigned))
	}
	if err := e.CompleteJob(assigned[0].ID, true, assigned[0].AssignedRobot); err != nil {
		t.Fatal(err)
	}
	if err := e.CompleteJob(assigned[1].ID, false, assigned[1].AssignedRobot); err != nil {
		t.Fatal(err)
	}
	replay, _, cancel := hub.Subscribe(0)
	defer cancel()
	var kinds []string
	for _, ev := range replay {
		kinds = append(kinds, ev.Type)
	}
	if got := strings.Join(kinds, ","); got != "assigned,assigned,done,failed" {
		t.Fatalf("events %q, want assigned,assigned,done,failed", got)
	}
	for i, ev := range replay {
		if ev.ID != int64(i+1) {
			t.Fatalf("event %d has id %d: ids must be consecutive", i, ev.ID)
		}
	}
}

func TestEvents_UnknownWhenARobotGoesSilent(t *testing.T) {
	e := NewEngine()
	hub := eventsOn(t, nil)
	e.SetEvents(hub)
	now := time.Now()
	e.now = func() time.Time { return now }
	e.UpsertRobot(Robot{ID: "r1", Available: true})
	_ = e.AddJob(Job{ID: "a"})
	e.DispatchOnce()
	now = now.Add(time.Hour)
	e.DetectStaleAssignments(time.Minute)
	replay, _, cancel := hub.Subscribe(0)
	defer cancel()
	if last := replay[len(replay)-1]; last.Type != EventUnknown || last.JobID != "a" {
		t.Fatalf("last event %+v, want unknown for a", last)
	}
}

func TestEvents_OffByDefaultAndNoHubChangesNothing(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "r1", Available: true})
	_ = e.AddJob(Job{ID: "a"})
	if got := e.DispatchOnce(); len(got) != 1 {
		t.Fatalf("assignments %v", got)
	}
	var nilHub *EventHub
	nilHub.Publish(EventDone, "x", "r", 1) // must not panic
	if nilHub.SSEEnabled() {
		t.Fatal("a missing hub must not serve the stream")
	}
	off, _ := NewEventHub(EventsConfig{})
	off.Publish(EventDone, "x", "r", 1)
	replay, _, c := off.Subscribe(0)
	c()
	if len(replay) != 0 {
		t.Fatal("a hub that is off must keep nothing")
	}
}

func TestEvents_ReplayFromLastIdAndBoundedBuffer(t *testing.T) {
	hub := eventsOn(t, func(c *EventsConfig) { c.Buffer = 3 })
	for i := 0; i < 5; i++ {
		hub.Publish(EventDone, "j", "r", 1)
	}
	replay, _, cancel := hub.Subscribe(3)
	defer cancel()
	if len(replay) != 2 || replay[0].ID != 4 || replay[1].ID != 5 {
		t.Fatalf("replay %+v, want ids 4 and 5", replay)
	}
	all, _, c2 := hub.Subscribe(0)
	defer c2()
	if len(all) != 3 || all[0].ID != 3 {
		t.Fatalf("buffer %+v, want the last 3", all)
	}
}

func TestEvents_SlowSubscriberIsDroppedAndNeverBlocksPublish(t *testing.T) {
	hub := eventsOn(t, func(c *EventsConfig) { c.SubscriberSize = 2 })
	_, ch, cancel := hub.Subscribe(0)
	defer cancel()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			hub.Publish(EventAssigned, "j", "r", 1)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a reader that does not read")
	}
	n := 0
	for range ch {
		n++
	}
	if n > 2 {
		t.Fatalf("a dropped subscriber got %d events", n)
	}
}

func TestEvents_ConfigRefusesWhatIsNotAllowed(t *testing.T) {
	base := func() EventsConfig {
		c := DefaultEventsConfig()
		c.Enabled = true
		c.AllowedHosts = []string{"hooks.example.org", "127.0.0.1:9000"}
		c.Webhooks = []WebhookConfig{{URL: "https://hooks.example.org/x", SecretEnv: "S"}}
		return c
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("a valid config was refused: %v", err)
	}
	cases := map[string]func(*EventsConfig){
		"host not in list":   func(c *EventsConfig) { c.Webhooks[0].URL = "https://evil.example.net/x" },
		"plain http remote":  func(c *EventsConfig) { c.Webhooks[0].URL = "http://hooks.example.org/x" },
		"credentials in url": func(c *EventsConfig) { c.Webhooks[0].URL = "https://u:p@hooks.example.org/x" },
		"no secret":          func(c *EventsConfig) { c.Webhooks[0].SecretEnv = "" },
		"unknown type":       func(c *EventsConfig) { c.Webhooks[0].Types = []string{"exploded"} },
		"not a url":          func(c *EventsConfig) { c.Webhooks[0].URL = "ftp://hooks.example.org/x" },
		"empty allow list":   func(c *EventsConfig) { c.AllowedHosts = nil },
		"bad buffer":         func(c *EventsConfig) { c.Buffer = 0 },
	}
	for name, mutate := range cases {
		c := base()
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	loop := base()
	loop.Webhooks[0].URL = "http://127.0.0.1:9000/x"
	if err := loop.Validate(); err != nil {
		t.Errorf("plain http on this machine must be accepted: %v", err)
	}
	if err := (EventsConfig{}).Validate(); err != nil {
		t.Errorf("an off config must be valid: %v", err)
	}
}

func TestEvents_WebhookIsSignedAndRetried(t *testing.T) {
	t.Setenv("JD_TEST_HOOK_SECRET", "s3cret")
	var mu sync.Mutex
	var attempts int
	got := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		attempts++
		n := attempts
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		ts := r.Header.Get("X-Hydra-Timestamp")
		want := "sha256=" + Sign([]byte("s3cret"), ts, body)
		if r.Header.Get("X-Hydra-Signature") != want || r.Header.Get("X-Hydra-Event") != "done" {
			got <- "bad signature or event header"
		} else {
			got <- string(body)
		}
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	hub := eventsOn(t, func(c *EventsConfig) {
		c.AllowedHosts = []string{host}
		c.Webhooks = []WebhookConfig{{URL: srv.URL + "/hook", SecretEnv: "JD_TEST_HOOK_SECRET", Types: []string{"done"}}}
		c.RetryDelay = time.Millisecond
	})
	hub.Publish(EventAssigned, "j1", "r1", 1) // filtered out by types
	hub.Publish(EventDone, "j1", "r1", 1)
	select {
	case body := <-got:
		if !strings.Contains(body, `"type":"done"`) || !strings.Contains(body, `"jobId":"j1"`) {
			t.Fatalf("delivered %q", body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the webhook was not delivered after a failed attempt")
	}
}

func TestEvents_WebhookDoesNotFollowRedirects(t *testing.T) {
	t.Setenv("JD_TEST_HOOK_SECRET", "s3cret")
	hit := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit <- struct{}{} }))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()
	hub := eventsOn(t, func(c *EventsConfig) {
		c.AllowedHosts = []string{strings.TrimPrefix(redirector.URL, "http://")}
		c.Webhooks = []WebhookConfig{{URL: redirector.URL, SecretEnv: "JD_TEST_HOOK_SECRET"}}
		c.MaxAttempts, c.RetryDelay = 1, time.Millisecond
	})
	hub.Publish(EventDone, "j", "r", 1)
	select {
	case <-hit:
		t.Fatal("a redirect to a host that is not on the list was followed")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestEvents_WebhookNeedsItsSecretInTheEnvironment(t *testing.T) {
	cfg := DefaultEventsConfig()
	cfg.Enabled = true
	cfg.AllowedHosts = []string{"127.0.0.1:1"}
	cfg.Webhooks = []WebhookConfig{{URL: "http://127.0.0.1:1/x", SecretEnv: "JD_TEST_NO_SUCH_SECRET"}}
	if _, err := NewEventHub(cfg); err == nil {
		t.Fatal("a webhook without a secret must refuse to start")
	}
}

func TestSign_DependsOnSecretTimestampAndBody(t *testing.T) {
	a := Sign([]byte("k"), "1", []byte("body"))
	if a != Sign([]byte("k"), "1", []byte("body")) {
		t.Fatal("not deterministic")
	}
	for _, other := range []string{Sign([]byte("k2"), "1", []byte("body")), Sign([]byte("k"), "2", []byte("body")), Sign([]byte("k"), "1", []byte("bodY"))} {
		if other == a {
			t.Fatal("the signature ignores part of its input")
		}
	}
}

// ---- physical confirmation of the tool ----

func confirmEngine(t *testing.T, policy string, answers map[string]string) *Engine {
	t.Helper()
	e := NewEngine()
	err := e.SetToolConfirmation(ToolConfirmConfig{Enabled: true, TTL: time.Minute, OnUnconfirmed: policy}, func(id string) (string, error) {
		if tool, ok := answers[id]; ok {
			return tool, nil
		}
		return "", io.EOF
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestToolConfirmation_OffByDefaultTrustsTheDeclaredTool(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "r1", Tool: "Laser", Available: true})
	_ = e.AddJob(Job{ID: "a", RequiredTool: "Laser"})
	if got := e.DispatchOnce(); len(got) != 1 {
		t.Fatalf("assignments %v", got)
	}
	if e.ToolConfirmationEnabled() || e.ConfirmTools() != 0 {
		t.Fatal("nothing is asked while it is off")
	}
}

func TestToolConfirmation_PhysicalToolMustMatch(t *testing.T) {
	e := confirmEngine(t, UnconfirmedSkip, map[string]string{"r1": "PnP", "r2": "Laser"})
	e.UpsertRobot(Robot{ID: "r1", Tool: "Laser", Available: true}) // declares Laser, carries PnP
	e.UpsertRobot(Robot{ID: "r2", Tool: "Laser", Available: true})
	_ = e.AddJob(Job{ID: "a", RequiredTool: "Laser"})
	if n := e.ConfirmTools(); n != 2 {
		t.Fatalf("confirmed %d, want 2", n)
	}
	got := e.DispatchOnce()
	if len(got) != 1 || got[0].RobotID != "r2" {
		t.Fatalf("assignments %v, want the job on r2 (r1 declares Laser but carries PnP)", got)
	}
}

func TestToolConfirmation_UnconfirmedRobotIsSkippedByDefault(t *testing.T) {
	e := confirmEngine(t, UnconfirmedSkip, map[string]string{})
	e.UpsertRobot(Robot{ID: "r1", Tool: "Laser", Available: true})
	_ = e.AddJob(Job{ID: "a", RequiredTool: "Laser"})
	_ = e.AddJob(Job{ID: "free"}) // a job without a tool needs no confirmation
	got := e.DispatchOnce()
	if len(got) != 1 || got[0].JobID != "free" {
		t.Fatalf("assignments %v: only the job without a tool may go to an unconfirmed robot", got)
	}
	e2 := confirmEngine(t, UnconfirmedSkip, map[string]string{})
	e2.UpsertRobot(Robot{ID: "r1", Tool: "Laser", Available: true})
	_ = e2.AddJob(Job{ID: "a", RequiredTool: "Laser"})
	w := e2.WaitingJobs()
	if len(w) != 1 || w[0].Reason != WaitToolUnconfirmed {
		t.Fatalf("waiting %+v, want the reason %q", w, WaitToolUnconfirmed)
	}
}

func TestToolConfirmation_AllowPolicyTrustsTheDeclaredToolWhenUnanswered(t *testing.T) {
	e := confirmEngine(t, UnconfirmedAllow, map[string]string{})
	e.UpsertRobot(Robot{ID: "r1", Tool: "Laser", Available: true})
	_ = e.AddJob(Job{ID: "a", RequiredTool: "Laser"})
	if got := e.DispatchOnce(); len(got) != 1 {
		t.Fatalf("assignments %v", got)
	}
}

func TestToolConfirmation_AnswerExpiresAndSilenceRemovesIt(t *testing.T) {
	answers := map[string]string{"r1": "Laser"}
	e := confirmEngine(t, UnconfirmedSkip, answers)
	now := time.Now()
	e.now = func() time.Time { return now }
	e.UpsertRobot(Robot{ID: "r1", Tool: "Laser", Available: true})
	e.ConfirmTools()
	now = now.Add(2 * time.Minute) // older than the TTL
	_ = e.AddJob(Job{ID: "a", RequiredTool: "Laser"})
	if got := e.DispatchOnce(); len(got) != 0 {
		t.Fatalf("an expired confirmation was used: %v", got)
	}
	e.UpsertRobot(Robot{ID: "r1", Tool: "Laser", Available: true})
	e.ConfirmTools()
	delete(answers, "r1") // the controller stops answering
	e.ConfirmTools()
	if got := e.DispatchOnce(); len(got) != 0 {
		t.Fatalf("silence counted as a confirmation: %v", got)
	}
}

func TestToolConfirmation_NeedsAFetcherAndAValidPolicy(t *testing.T) {
	e := NewEngine()
	if err := e.SetToolConfirmation(ToolConfirmConfig{Enabled: true, TTL: time.Minute, OnUnconfirmed: UnconfirmedSkip}, nil); err == nil {
		t.Fatal("no fetcher accepted")
	}
	if err := e.SetToolConfirmation(ToolConfirmConfig{Enabled: true, TTL: time.Minute, OnUnconfirmed: "maybe"}, func(string) (string, error) { return "", nil }); err == nil {
		t.Fatal("unknown policy accepted")
	}
	if err := e.SetToolConfirmation(ToolConfirmConfig{Enabled: true, OnUnconfirmed: UnconfirmedSkip}, func(string) (string, error) { return "", nil }); err == nil {
		t.Fatal("zero ttl accepted")
	}
}

func TestHTTPToolFetcher_ReadsToolIdAndSendsTheToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/robots/arm%201/tool" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"tool_id":"Laser"}`))
	}))
	defer srv.Close()
	fetch := HTTPToolFetcher(srv.URL+"/robots/{robot}/tool", "tok", time.Second)
	tool, err := fetch("arm 1")
	if err != nil || tool != "Laser" {
		t.Fatalf("tool %q err %v", tool, err)
	}
	if _, err := HTTPToolFetcher(srv.URL+"/robots/{robot}/tool", "", time.Second)("arm 1"); err == nil {
		t.Fatal("a refused request must be an error")
	}
}

func TestConfig_EventsAndToolConfirmationSections(t *testing.T) {
	c, err := ParseConfig([]byte(`{"events":{"enabled":true,"sse":true,"allowed_hosts":["h.example.org"],"webhooks":[{"url":"https://h.example.org/x","secret_env":"S","types":["done"]}]},"tool_confirmation":{"enabled":true,"url_template":"http://127.0.0.1:18080/api/robots/{robot}/tool"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if ev := c.EventsSetting(); !ev.Enabled || !ev.SSE || len(ev.Webhooks) != 1 || ev.Buffer != 256 {
		t.Fatalf("events %+v", ev)
	}
	if tc := c.ToolConfirmSetting(); !tc.Enabled || tc.OnUnconfirmed != UnconfirmedSkip || tc.TTL != 30*time.Second {
		t.Fatalf("tool confirmation %+v", tc)
	}
	empty, _ := ParseConfig([]byte(`{}`))
	if empty.EventsSetting().Enabled || empty.ToolConfirmSetting().Enabled {
		t.Fatal("both must be off without their sections")
	}
	for name, doc := range map[string]string{
		"host not allowed": `{"events":{"enabled":true,"allowed_hosts":["a.org"],"webhooks":[{"url":"https://b.org/x","secret_env":"S"}]}}`,
		"no placeholder":   `{"tool_confirmation":{"enabled":true,"url_template":"http://127.0.0.1/tool"}}`,
		"bad policy":       `{"tool_confirmation":{"enabled":true,"url_template":"http://127.0.0.1/{robot}","on_unconfirmed":"maybe"}}`,
	} {
		if _, err := ParseConfig([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
