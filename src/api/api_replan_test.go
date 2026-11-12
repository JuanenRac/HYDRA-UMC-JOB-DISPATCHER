// HYDRA-UMC-JOB-DISPATCHER - HTTP tests of POST /jobs/replan and of the replanning answer of detect-stale
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/dispatcher"
)

// Characterization: while replanning is off the route does not exist, for every method.
func TestReplan_RouteIsAnUnknownPathWhileOff(t *testing.T) {
	s := New(dispatcher.NewEngine())
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		req := httptest.NewRequest(m, "/jobs/replan", strings.NewReader(`{"robotId":"r1"}`))
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", m, rec.Code)
		}
	}
}

// Characterization: the detect-stale answer has only unknownJobIds while replanning is off.
func TestDetectStale_AnswerHasNoRequeuedFieldWhileReplanIsOff(t *testing.T) {
	s := New(dispatcher.NewEngine())
	rec := post(t, s, "/jobs/detect-stale", map[string]any{"timeoutSeconds": 30})
	if strings.TrimSpace(rec.Body.String()) != `{"unknownJobIds":[]}` {
		t.Fatalf("answer = %s", rec.Body.String())
	}
}

func TestReplan_RouteSendsTheJobsOfARobotBack(t *testing.T) {
	e := dispatcher.NewEngine()
	e.SetReplan(dispatcher.ReplanConfig{Enabled: true})
	s := New(e)
	post(t, s, "/robots", robotBody{ID: "r1", Available: true})
	post(t, s, "/jobs", jobRequest{ID: "j1"})
	post(t, s, "/dispatch", nil)

	if rec := get(t, s, "/jobs/replan"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status %d, want 405", rec.Code)
	}
	if rec := post(t, s, "/jobs/replan", map[string]any{}); rec.Code != http.StatusBadRequest {
		t.Fatalf("no robot: status %d, want 400", rec.Code)
	}
	if rec := post(t, s, "/jobs/replan", map[string]any{"robotId": "ghost"}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown robot: status %d, want 404", rec.Code)
	}
	rec := post(t, s, "/jobs/replan", map[string]any{"robotId": "r1", "reason": "cable cut"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Requeued []string `json:"requeuedJobIds"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Requeued) != 1 || out.Requeued[0] != "j1" {
		t.Fatalf("answer = %s (%v)", rec.Body.String(), err)
	}
	if job, _ := e.Job("j1"); job.Status != dispatcher.StatusPending {
		t.Fatalf("job = %+v, want pending", job)
	}
	if again := post(t, s, "/jobs/replan", map[string]any{"robotId": "r1"}); !strings.Contains(again.Body.String(), `"requeuedJobIds":[]`) {
		t.Fatalf("second call = %s, want an empty list", again.Body.String())
	}
}

func TestDetectStale_AnswerListsTheRequeuedJobsWhenReplanIsOn(t *testing.T) {
	e := dispatcher.NewEngine()
	e.SetReplan(dispatcher.ReplanConfig{Enabled: true, OnStale: true})
	s := New(e)
	rec := post(t, s, "/jobs/detect-stale", map[string]any{"timeoutSeconds": 30})
	if strings.TrimSpace(rec.Body.String()) != `{"requeuedJobIds":[],"unknownJobIds":[]}` {
		t.Fatalf("answer = %s", rec.Body.String())
	}
}

// The stop-request routes do not exist while replanning is off, for every method.
func TestStopRoutes_AreUnknownPathsWhileReplanIsOff(t *testing.T) {
	s := New(dispatcher.NewEngine())
	for _, path := range []string{"/jobs/stop-requests", "/jobs/stop-ack"} {
		for _, m := range []string{http.MethodGet, http.MethodPost} {
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, httptest.NewRequest(m, path, strings.NewReader(`{"robotId":"r1","jobId":"j1"}`)))
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s %s: status %d, want 404", m, path, rec.Code)
			}
		}
	}
}

// A silent robot that declared it can confirm a stop is asked first and its job goes back only once
// it acknowledges (a real clock here: the matrix with a simulated clock is in the dispatcher tests).
func TestStopRoutes_AskThenAcknowledgeSendsTheJobBack(t *testing.T) {
	e := dispatcher.NewEngine()
	e.SetReplan(dispatcher.ReplanConfig{Enabled: true, OnStale: true, StopAckTimeout: time.Hour})
	s := New(e)
	post(t, s, "/robots", map[string]any{"id": "r1", "available": true, "stopCapable": true})
	post(t, s, "/jobs", jobRequest{ID: "j1"})
	post(t, s, "/dispatch", nil)
	time.Sleep(20 * time.Millisecond)

	rec := post(t, s, "/jobs/detect-stale", map[string]any{"timeoutSeconds": 0.001})
	var ans struct {
		Unknown, Requeued, StopRequested []string
	}
	var raw map[string][]string
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("answer %s: %v", rec.Body.String(), err)
	}
	ans.Unknown, ans.Requeued, ans.StopRequested = raw["unknownJobIds"], raw["requeuedJobIds"], raw["stopRequestedJobIds"]
	if len(ans.Unknown) != 0 || len(ans.Requeued) != 0 || len(ans.StopRequested) != 1 || ans.StopRequested[0] != "j1" {
		t.Fatalf("answer = %s, want only a stop request for j1", rec.Body.String())
	}
	var reqs []dispatcher.StopRequest
	if err := json.Unmarshal(get(t, s, "/jobs/stop-requests").Body.Bytes(), &reqs); err != nil || len(reqs) != 1 || reqs[0].JobID != "j1" || reqs[0].RobotID != "r1" {
		t.Fatalf("stop requests = %+v err %v", reqs, err)
	}

	if rec := post(t, s, "/jobs/stop-ack", map[string]any{"robotId": "r1"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing job: status %d, want 400", rec.Code)
	}
	if rec := post(t, s, "/jobs/stop-ack", map[string]any{"robotId": "r1", "jobId": "nope"}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown job: status %d, want 404", rec.Code)
	}
	if rec := post(t, s, "/jobs/stop-ack", map[string]any{"robotId": "r2", "jobId": "j1"}); rec.Code != http.StatusConflict {
		t.Fatalf("other robot: status %d, want 409", rec.Code)
	}
	if rec := get(t, s, "/jobs/stop-ack"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET stop-ack: status %d, want 405", rec.Code)
	}
	if rec := post(t, s, "/jobs/stop-ack", map[string]any{"robotId": "r1", "jobId": "j1"}); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"requeuedJobIds":["j1"]}` {
		t.Fatalf("ack: status %d body %s", rec.Code, rec.Body.String())
	}
	if rec := post(t, s, "/jobs/stop-ack", map[string]any{"robotId": "r1", "jobId": "j1"}); rec.Code != http.StatusConflict {
		t.Fatalf("second ack: status %d, want 409", rec.Code)
	}
	job, _ := e.Job("j1")
	if job.Status != dispatcher.StatusPending || job.AssignedRobot != "" {
		t.Fatalf("job = %+v, want pending without a robot", job)
	}
}

// The kept acknowledgements are a route only while replan.persist_stop_acks is on; before that it is an unknown path.
func TestStopAcks_RouteOffUnlessPersisting(t *testing.T) {
	e := dispatcher.NewEngine()
	e.SetReplan(dispatcher.ReplanConfig{Enabled: true, OnStale: true})
	s := New(e)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/jobs/stop-acks", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
}

func TestStopAcks_RouteListsAndRefusesOtherMethods(t *testing.T) {
	e := dispatcher.NewEngine()
	e.SetReplan(dispatcher.ReplanConfig{Enabled: true, OnStale: true, PersistStopAcks: true})
	s := New(e)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/jobs/stop-acks", nil))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/jobs/stop-acks", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status %d, want 405", rec.Code)
	}
}
