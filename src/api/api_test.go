// HYDRA-UMC-JOB-DISPATCHER - HTTP API tests
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0 - see LICENSE
//
// Real HTTP round-trips via httptest - actual JSON encoding/decoding
// through actual handler functions, not calls straight into the engine.
package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JuanenRac/hydra-umc-job-dispatcher/src/dispatcher"
)

func post(t *testing.T, h http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestFullFlow_RegisterSubmitDispatchComplete(t *testing.T) {
	s := New(dispatcher.NewEngine())

	rec := post(t, s, "/robots", robotRequest{ID: "robot-a", Tool: "PnP", Available: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /robots status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = post(t, s, "/jobs", jobRequest{ID: "job-1", Priority: 5, RequiredTool: "PnP"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /jobs status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = post(t, s, "/dispatch", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /dispatch status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var assignments []dispatcher.Assignment
	if err := json.Unmarshal(rec.Body.Bytes(), &assignments); err != nil {
		t.Fatalf("decoding /dispatch response: %v", err)
	}
	if len(assignments) != 1 || assignments[0].JobID != "job-1" || assignments[0].RobotID != "robot-a" {
		t.Fatalf("assignments = %+v, want job-1 assigned to robot-a", assignments)
	}

	rec = post(t, s, "/jobs/complete", completeRequest{ID: "job-1", Success: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /jobs/complete status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var updated dispatcher.Job
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decoding /jobs/complete response: %v", err)
	}
	if updated.Status != dispatcher.StatusDone {
		t.Fatalf("job-1 status after complete = %q, want done", updated.Status)
	}

	rec = get(t, s, "/robots")
	var robots []dispatcher.Robot
	if err := json.Unmarshal(rec.Body.Bytes(), &robots); err != nil {
		t.Fatalf("decoding /robots response: %v", err)
	}
	if len(robots) != 1 || !robots[0].Available {
		t.Fatalf("robots = %+v, want robot-a available again after job completion", robots)
	}
}

func TestHandleJobs_RejectsMissingID(t *testing.T) {
	s := New(dispatcher.NewEngine())
	rec := post(t, s, "/jobs", jobRequest{Priority: 1})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a job with no id", rec.Code)
	}
}

func TestHandleJobs_RejectsDuplicateID(t *testing.T) {
	s := New(dispatcher.NewEngine())
	post(t, s, "/jobs", jobRequest{ID: "job-1"})
	rec := post(t, s, "/jobs", jobRequest{ID: "job-1"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for a duplicate job id", rec.Code)
	}
}

func TestHandleJobs_MethodNotAllowed(t *testing.T) {
	s := New(dispatcher.NewEngine())
	req := httptest.NewRequest(http.MethodDelete, "/jobs", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405 for DELETE /jobs", rec.Code)
	}
}
