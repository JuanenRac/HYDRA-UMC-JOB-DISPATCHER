// HYDRA-UMC-JOB-DISPATCHER - tests of the production events
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// The engine runs on an injectable clock, so the cycle times are exact. The two files in testdata are the valid and the invalid example of the
// ProductionEvent contract of HYDRA-UMC-SDK.
package dispatcher

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type collected struct {
	mu     sync.Mutex
	events []ProductionEvent
}

func (c *collected) sink(e ProductionEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
	return nil
}

func (c *collected) all() []ProductionEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ProductionEvent(nil), c.events...)
}

// assigned returns an engine whose job "j1" has been given to robot "r1" at the clock's time.
func assigned(t *testing.T, robot string) (*Engine, *etaClock) {
	t.Helper()
	clock := &etaClock{t: time.Unix(1_800_000_000, 0)}
	e := NewEngine()
	e.now = clock.get
	mustJob(t, e, Job{ID: "j1", RequiredTool: "PnP"})
	e.UpsertRobot(Robot{ID: robot, Tool: "PnP", Available: true})
	if got := e.DispatchOnce(); len(got) != 1 {
		t.Fatalf("expected one assignment, got %v", got)
	}
	return e, clock
}

func TestProductionEvents_OffByDefault(t *testing.T) {
	e, clock := assigned(t, "r1")
	if e.ProductionEventsEnabled() {
		t.Fatal("the production events must be off by default")
	}
	clock.advance(10 * time.Second)
	if err := e.CompleteJob("j1", true, "r1"); err != nil {
		t.Fatal(err)
	}
}

func TestProductionEvents_AJobThatEndedWellAndOneThatFailed(t *testing.T) {
	for _, success := range []bool{true, false} {
		e, clock := assigned(t, "r1")
		var got collected
		e.SetProductionEvents(got.sink)
		clock.advance(12500 * time.Millisecond)
		if err := e.CompleteJob("j1", success, "r1"); err != nil {
			t.Fatal(err)
		}
		events := got.all()
		if len(events) != 1 {
			t.Fatalf("success=%v: want one event, got %v", success, events)
		}
		event := events[0]
		if event.SourceID != "r1" || event.JobID != "j1" || event.Good != success || event.CycleTimeS != 12.5 || event.SchemaVersion != "1.0" {
			t.Errorf("success=%v: unexpected event %+v", success, event)
		}
		if want := clock.get().UTC().Format("2006-01-02T15:04:05"); !strings.HasPrefix(event.AtUTC, want) || !strings.HasSuffix(event.AtUTC, "Z") {
			t.Errorf("at_utc %q does not describe %s UTC", event.AtUTC, want)
		}
		if err := event.Validate(); err != nil {
			t.Errorf("an emitted event must satisfy the contract: %v", err)
		}
	}
}

func TestProductionEvents_ARefusedCompletionEmitsNothing(t *testing.T) {
	e, clock := assigned(t, "r1")
	var got collected
	e.SetProductionEvents(got.sink)
	clock.advance(time.Second)
	if err := e.CompleteJob("j1", true, "someone-else"); err == nil {
		t.Fatal("a report from another robot must be refused")
	}
	if err := e.CompleteJob("nope", true, "r1"); err == nil {
		t.Fatal("an unknown job must be refused")
	}
	if n := len(got.all()); n != 0 {
		t.Fatalf("nothing ended, yet %d event(s) were emitted", n)
	}
	if err := e.CompleteJob("j1", true, "r1"); err != nil {
		t.Fatal(err)
	}
	if err := e.CompleteJob("j1", true, "r1"); err == nil {
		t.Fatal("a second report of the same job must be refused")
	}
	if n := len(got.all()); n != 1 {
		t.Fatalf("want exactly one event, got %d", n)
	}
}

func TestProductionEvents_ACycleThatTheContractCannotDescribeIsSkippedNotInvented(t *testing.T) {
	cases := map[string]struct {
		robot string
		took  time.Duration
	}{
		"no time passed":       {"r1", 0},
		"longer than a week":   {"r1", 8 * 24 * time.Hour},
		"robot id with spaces": {"robot one", time.Second},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e, clock := assigned(t, c.robot)
			var got collected
			e.SetProductionEvents(got.sink)
			clock.advance(c.took)
			if err := e.CompleteJob("j1", true, c.robot); err != nil {
				t.Fatalf("the completion itself must still work: %v", err)
			}
			if job, _ := e.Job("j1"); job.Status != StatusDone {
				t.Fatalf("the job must be done, is %s", job.Status)
			}
			if n := len(got.all()); n != 0 {
				t.Fatalf("an event the contract refuses was emitted: %v", got.all())
			}
		})
	}
}

func TestProductionEvents_ASinkThatFailsOrReadsTheEngineDoesNotBreakTheCompletion(t *testing.T) {
	e, clock := assigned(t, "r1")
	e.SetProductionEvents(func(ProductionEvent) error {
		// the sink runs after the lock has been released: asking the engine here must not deadlock
		if _, ok := e.Job("j1"); !ok {
			return errors.New("the job vanished")
		}
		return errors.New("the receiver is down")
	})
	clock.advance(time.Second)
	if err := e.CompleteJob("j1", true, "r1"); err != nil {
		t.Fatalf("a failing sink must not fail the completion: %v", err)
	}
	if job, _ := e.Job("j1"); job.Status != StatusDone {
		t.Fatalf("the job must be done, is %s", job.Status)
	}
}

func TestProductionEvents_SwitchedOffAgain(t *testing.T) {
	e, clock := assigned(t, "r1")
	var got collected
	e.SetProductionEvents(got.sink)
	e.SetProductionEvents(nil)
	clock.advance(time.Second)
	if err := e.CompleteJob("j1", true, "r1"); err != nil {
		t.Fatal(err)
	}
	if len(got.all()) != 0 || e.ProductionEventsEnabled() {
		t.Fatal("setting a nil sink must switch the events off")
	}
}

func readFixture(t *testing.T, name string) ProductionEvent {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var event ProductionEvent
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return event
}

func TestProductionEvent_Contract(t *testing.T) {
	valid := readFixture(t, "production-event.valid.json")
	if err := valid.Validate(); err != nil {
		t.Fatalf("the valid example of the contract was refused: %v", err)
	}
	// the invalid example has good as a string: it cannot even be read into the typed event
	data, _ := os.ReadFile(filepath.Join("testdata", "production-event.invalid.json"))
	var event ProductionEvent
	if err := json.Unmarshal(data, &event); err == nil {
		t.Fatal("good must be a boolean")
	}
	mutate := map[string]func(*ProductionEvent){
		"schema version":    func(p *ProductionEvent) { p.SchemaVersion = "2.0" },
		"empty source":      func(p *ProductionEvent) { p.SourceID = "" },
		"source with space": func(p *ProductionEvent) { p.SourceID = "a b" },
		"source too long":   func(p *ProductionEvent) { p.SourceID = strings.Repeat("a", 65) },
		"job with slash":    func(p *ProductionEvent) { p.JobID = "a/b" },
		"not a date":        func(p *ProductionEvent) { p.AtUTC = "yesterday" },
		"zero cycle":        func(p *ProductionEvent) { p.CycleTimeS = 0 },
		"negative cycle":    func(p *ProductionEvent) { p.CycleTimeS = -1 },
		"over a week":       func(p *ProductionEvent) { p.CycleTimeS = MaxCycleTimeSeconds + 1 },
		"long reason":       func(p *ProductionEvent) { p.RejectReason = strings.Repeat("x", 121) },
	}
	for name, change := range mutate {
		bad := valid
		change(&bad)
		if bad.Validate() == nil {
			t.Errorf("%s: the contract must refuse it", name)
		}
	}
	edge := valid
	edge.CycleTimeS = MaxCycleTimeSeconds
	edge.JobID = ""
	if err := edge.Validate(); err != nil {
		t.Errorf("a cycle of exactly one week without a job is valid: %v", err)
	}
	// the wire form carries only the fields of the closed schema
	wire, _ := json.Marshal(valid)
	var fields map[string]any
	_ = json.Unmarshal(wire, &fields)
	for key := range fields {
		switch key {
		case "schema_version", "source_id", "at_utc", "good", "cycle_time_s", "job_id", "reject_reason":
		default:
			t.Errorf("field %q is not in the contract", key)
		}
	}
}

func TestJSONLinesSink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	sink := JSONLinesSink(path)
	valid := readFixture(t, "production-event.valid.json")
	for i := 0; i < 3; i++ {
		if err := sink(valid); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d", len(lines))
	}
	var back ProductionEvent
	if err := json.Unmarshal([]byte(lines[0]), &back); err != nil || back != valid {
		t.Fatalf("a line must read back as the event: %v %+v", err, back)
	}
	if err := JSONLinesSink(filepath.Join(t.TempDir(), "missing", "events.jsonl"))(valid); err == nil {
		t.Fatal("a file in a folder that does not exist must be an error")
	}
}

func TestHTTPSink(t *testing.T) {
	var seen struct {
		sync.Mutex
		auth, kind string
		body       ProductionEvent
	}
	status := http.StatusNoContent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Lock()
		defer seen.Unlock()
		seen.auth, seen.kind = r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&seen.body)
		w.WriteHeader(status)
	}))
	defer server.Close()
	valid := readFixture(t, "production-event.valid.json")
	if err := HTTPSink(server.URL, "secret", time.Second)(valid); err != nil {
		t.Fatal(err)
	}
	seen.Lock()
	if seen.auth != "Bearer secret" || seen.kind != "application/json" || seen.body != valid {
		t.Errorf("unexpected request: auth %q, content type %q, body %+v", seen.auth, seen.kind, seen.body)
	}
	seen.Unlock()
	if err := HTTPSink(server.URL, "", time.Second)(valid); err != nil {
		t.Fatal(err)
	}
	seen.Lock()
	if seen.auth != "" {
		t.Errorf("no token was given, yet %q was sent", seen.auth)
	}
	seen.Unlock()
	status = http.StatusInternalServerError
	if err := HTTPSink(server.URL, "", time.Second)(valid); err == nil {
		t.Fatal("a 500 must be an error")
	}
	if err := HTTPSink("http://127.0.0.1:1/", "", 200*time.Millisecond)(valid); err == nil {
		t.Fatal("a receiver that is not there must be an error")
	}
	if err := HTTPSink("://bad", "", time.Second)(valid); err == nil {
		t.Fatal("a malformed URL must be an error")
	}
}

func TestAsyncSink(t *testing.T) {
	release := make(chan struct{})
	var got collected
	slow := func(e ProductionEvent) error {
		<-release
		return got.sink(e)
	}
	sink, stop := AsyncSink(slow, 2)
	valid := readFixture(t, "production-event.valid.json")
	accepted, dropped := 0, 0
	for i := 0; i < 8; i++ {
		if err := sink(valid); err != nil {
			dropped++
		} else {
			accepted++
		}
	}
	if dropped == 0 || accepted < 2 {
		t.Fatalf("a full queue must drop events without blocking: accepted %d, dropped %d", accepted, dropped)
	}
	close(release)
	stop()
	stop() // stopping twice is harmless
	if n := len(got.all()); n != accepted {
		t.Fatalf("every accepted event must be delivered before stop returns: got %d, accepted %d", n, accepted)
	}
	if err := sink(valid); err == nil {
		t.Fatal("a closed sink must say so")
	}
}
