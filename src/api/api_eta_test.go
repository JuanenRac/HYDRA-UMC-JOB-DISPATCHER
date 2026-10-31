// HYDRA-UMC-JOB-DISPATCHER - HTTP tests of GET /dispatch/eta
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

// Characterization: while the estimates are off the route does not exist, for every method.
func TestETA_RouteIsAnUnknownPathWhileOff(t *testing.T) {
	s := New(dispatcher.NewEngine())
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		req := httptest.NewRequest(m, "/dispatch/eta", strings.NewReader(""))
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", m, rec.Code)
		}
	}
	// the job list answers the same fields as before
	post(t, s, "/jobs", jobRequest{ID: "j1"})
	rec := get(t, s, "/jobs/j1")
	if strings.Contains(strings.ToLower(rec.Body.String()), "eta") {
		t.Errorf("a job must not carry estimate fields while they are off: %s", rec.Body.String())
	}
}

func TestETA_RouteAnswersWhenOn(t *testing.T) {
	e := dispatcher.NewEngine()
	e.SetETA(dispatcher.ETAConfig{Enabled: true, DefaultDuration: time.Minute})
	s := New(e)
	post(t, s, "/robots", robotBody{ID: "r1", Tool: "PnP", Available: true})
	post(t, s, "/jobs", jobRequest{ID: "j1", RequiredTool: "PnP"})
	post(t, s, "/jobs", jobRequest{ID: "j2", RequiredTool: "PnP"})

	rec := get(t, s, "/dispatch/eta")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var q dispatcher.QueueETA
	if err := json.Unmarshal(rec.Body.Bytes(), &q); err != nil {
		t.Fatal(err)
	}
	if len(q.Jobs) != 2 || !q.Jobs[0].Estimated || q.Jobs[1].StartMs != q.Jobs[0].EndMs || q.DrainMs != q.Jobs[1].EndMs {
		t.Fatalf("unexpected queue estimate: %+v", q)
	}

	one := get(t, s, "/dispatch/eta?job=j2")
	var j dispatcher.JobETA
	if err := json.Unmarshal(one.Body.Bytes(), &j); err != nil || one.Code != http.StatusOK || j.JobID != "j2" {
		t.Fatalf("one job: %d %s (%v)", one.Code, one.Body.String(), err)
	}
	if rec := get(t, s, "/dispatch/eta?job=nope"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown job: status %d, want 404", rec.Code)
	}
	if rec := post(t, s, "/dispatch/eta", map[string]string{}); rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET" {
		t.Errorf("POST: status %d allow %q, want 405 GET", rec.Code, rec.Header().Get("Allow"))
	}
}

func TestETA_RouteNeedsTheTokenLikeTheOthers(t *testing.T) {
	e := dispatcher.NewEngine()
	e.SetETA(dispatcher.ETAConfig{Enabled: true})
	s := New(e)
	s.RequireToken("secret")
	if rec := get(t, s, "/dispatch/eta"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401 without the token", rec.Code)
	}
}
