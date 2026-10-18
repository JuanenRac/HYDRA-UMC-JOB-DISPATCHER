// HYDRA-UMC-JOB-DISPATCHER - HTTP API edge-case tests
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// Method guards, malformed bodies, status mapping of the engine's sentinel
// errors, and the fencing that keeps a stale robot from ending a job that was
// handed to somebody else.
package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JuanenRac/hydra-umc-job-dispatcher/src/dispatcher"
)

func raw(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestEdge_WrongMethodIs405WithAnAllowHeader(t *testing.T) {
	s := New(dispatcher.NewEngine())
	cases := []struct{ method, path, allow string }{
		{http.MethodGet, "/dispatch", "POST"},
		{http.MethodGet, "/jobs/complete", "POST"},
		{http.MethodGet, "/jobs/requeue", "POST"},
		{http.MethodPost, "/health", "GET"},
		{http.MethodPost, "/jobs/someid", "GET"},
		{http.MethodDelete, "/robots", "GET, POST"},
	}
	for _, c := range cases {
		rec := raw(t, s, c.method, c.path, "")
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 405", c.method, c.path, rec.Code)
		}
		if got := rec.Header().Get("Allow"); got != c.allow {
			t.Errorf("%s %s Allow = %q, want %q", c.method, c.path, got, c.allow)
		}
	}
}

func TestEdge_MalformedJSONIs400OnEveryPostRoute(t *testing.T) {
	s := New(dispatcher.NewEngine())
	for _, path := range []string{"/jobs", "/jobs/submit", "/jobs/complete", "/jobs/cancel", "/jobs/requeue", "/robots", "/jobs/detect-stale"} {
		for _, body := range []string{"{", "not json", "[1,2]", `{"id":`} {
			if rec := raw(t, s, http.MethodPost, path, body); rec.Code != http.StatusBadRequest {
				t.Errorf("POST %s body %q = %d, want 400", path, body, rec.Code)
			}
		}
	}
}

func TestEdge_RobotsRequireAnID(t *testing.T) {
	s := New(dispatcher.NewEngine())
	if rec := raw(t, s, http.MethodPost, "/robots", `{"tool":"PnP"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /robots without id = %d, want 400", rec.Code)
	}
	if rec := raw(t, s, http.MethodGet, "/robots", ""); strings.TrimSpace(rec.Body.String()) == "null" {
		t.Fatalf("GET /robots with none registered must be a JSON array, got %s", rec.Body.String())
	}
}

func TestEdge_DispatchWithNothingToDoIsAnEmptyArray(t *testing.T) {
	s := New(dispatcher.NewEngine())
	rec := raw(t, s, http.MethodPost, "/dispatch", "")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("POST /dispatch = %d %q, want 200 []", rec.Code, rec.Body.String())
	}
}

func TestEdge_SubmitWithAnUnknownDependencyIs400(t *testing.T) {
	s := New(dispatcher.NewEngine())
	rec := raw(t, s, http.MethodPost, "/jobs/submit", `{"id":"j1","dependsOn":["ghost"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("submit with an unknown dependency = %d %s, want 400", rec.Code, rec.Body.String())
	}
	if rec := raw(t, s, http.MethodGet, "/jobs/j1", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("the rejected job must not exist, GET = %d", rec.Code)
	}
}

func TestEdge_JobByIDWithEmptyIDIs404(t *testing.T) {
	s := New(dispatcher.NewEngine())
	if rec := raw(t, s, http.MethodGet, "/jobs/", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("GET /jobs/ = %d, want 404", rec.Code)
	}
}

func TestEdge_RequeueOfAFinishedJobIs409AndLeavesItDone(t *testing.T) {
	s := New(dispatcher.NewEngine())
	raw(t, s, http.MethodPost, "/robots", `{"id":"robot-a","available":true}`)
	raw(t, s, http.MethodPost, "/jobs", `{"id":"j1"}`)
	raw(t, s, http.MethodPost, "/dispatch", "")
	raw(t, s, http.MethodPost, "/jobs/complete", `{"id":"j1","success":true,"robotId":"robot-a"}`)
	if rec := raw(t, s, http.MethodPost, "/jobs/requeue", `{"id":"j1","robotId":"robot-a"}`); rec.Code != http.StatusConflict {
		t.Fatalf("requeue of a done job = %d %s, want 409", rec.Code, rec.Body.String())
	}
	var job dispatcher.Job
	if err := json.Unmarshal(raw(t, s, http.MethodGet, "/jobs/j1", "").Body.Bytes(), &job); err != nil || job.Status != dispatcher.StatusDone {
		t.Fatalf("job after a refused requeue = %+v (%v), want done", job, err)
	}
}

func TestEdge_ARobotThatLostItsJobCannotCompleteIt(t *testing.T) {
	s := New(dispatcher.NewEngine())
	raw(t, s, http.MethodPost, "/robots", `{"id":"robot-a","available":true}`)
	raw(t, s, http.MethodPost, "/jobs", `{"id":"j1"}`)
	raw(t, s, http.MethodPost, "/dispatch", "")
	if rec := raw(t, s, http.MethodPost, "/jobs/requeue", `{"id":"j1","robotId":"robot-a"}`); rec.Code != http.StatusOK {
		t.Fatalf("requeue = %d %s", rec.Code, rec.Body.String())
	}
	// The requeued job is pending again: the old robot's late report must not
	// mark it done, whatever it claims.
	rec := raw(t, s, http.MethodPost, "/jobs/complete", `{"id":"j1","success":true,"robotId":"robot-a"}`)
	if rec.Code == http.StatusOK {
		t.Fatalf("a stale robot completed a requeued job: %s", rec.Body.String())
	}
	var job dispatcher.Job
	if err := json.Unmarshal(raw(t, s, http.MethodGet, "/jobs/j1", "").Body.Bytes(), &job); err != nil || job.Status == dispatcher.StatusDone {
		t.Fatalf("job after the stale report = %+v (%v), must not be done", job, err)
	}
}

func TestEdge_ARequeuedRobotIsHeldUnavailableUntilItsNextHeartbeat(t *testing.T) {
	s := New(dispatcher.NewEngine())
	raw(t, s, http.MethodPost, "/robots", `{"id":"robot-a","available":true}`)
	raw(t, s, http.MethodPost, "/jobs", `{"id":"j1"}`)
	raw(t, s, http.MethodPost, "/dispatch", "")
	raw(t, s, http.MethodPost, "/jobs/requeue", `{"id":"j1","robotId":"robot-a"}`)
	rec := raw(t, s, http.MethodPost, "/dispatch", "")
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("the requeued job went straight back to the robot that lost it: %s", rec.Body.String())
	}
}

func TestEdge_DetectStaleRejectsNegativeAndZeroTimeouts(t *testing.T) {
	s := New(dispatcher.NewEngine())
	for _, body := range []string{`{"timeoutSeconds":0}`, `{"timeoutSeconds":-5}`, `{}`} {
		if rec := raw(t, s, http.MethodPost, "/jobs/detect-stale", body); rec.Code != http.StatusBadRequest {
			t.Errorf("detect-stale %s = %d, want 400", body, rec.Code)
		}
	}
}
