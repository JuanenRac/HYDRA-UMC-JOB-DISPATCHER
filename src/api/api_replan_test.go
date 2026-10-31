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
