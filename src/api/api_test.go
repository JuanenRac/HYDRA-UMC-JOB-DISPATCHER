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

func TestHandleJobs_RejectsWhitespaceIDAndSelfDependency(t *testing.T) {
	s := New(dispatcher.NewEngine())
	for _, request := range []jobRequest{
		{ID: "   "},
		{ID: "self", DependsOn: []string{"self"}},
	} {
		rec := post(t, s, "/jobs", request)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("POST /jobs invalid request status = %d, want 400, body = %s", rec.Code, rec.Body.String())
		}
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

func TestHandleSubmitJob_DuplicateDedupKeyReturns200Unchanged(t *testing.T) {
	s := New(dispatcher.NewEngine())

	rec := post(t, s, "/jobs/submit", submitRequest{ID: "job-1", Priority: 1, DedupKey: "req-abc"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("first POST /jobs/submit status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var first submitResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
		t.Fatalf("decoding first response: %v", err)
	}
	if first.Result != dispatcher.SubmitCreated {
		t.Fatalf("first Result = %q, want %q", first.Result, dispatcher.SubmitCreated)
	}

	// Same DedupKey, different client-generated ID - the real shape of a
	// retried HTTP request.
	rec = post(t, s, "/jobs/submit", submitRequest{ID: "job-1-retry", Priority: 1, DedupKey: "req-abc"})
	if rec.Code != http.StatusOK {
		t.Fatalf("second POST /jobs/submit status = %d, want 200 (duplicate, not created), body = %s", rec.Code, rec.Body.String())
	}
	var second submitResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &second); err != nil {
		t.Fatalf("decoding second response: %v", err)
	}
	if second.Result != dispatcher.SubmitDuplicate || second.ID != first.ID {
		t.Fatalf("second = %+v, want the original job-1 returned as a duplicate", second)
	}

	rec = get(t, s, "/jobs")
	var jobs []dispatcher.Job
	if err := json.Unmarshal(rec.Body.Bytes(), &jobs); err != nil {
		t.Fatalf("decoding /jobs response: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("GET /jobs returned %d job(s), want exactly 1 - the retry must not create a second job", len(jobs))
	}
}

func TestHandleSubmitJob_RetryAfterFailureRunsExactlyOnce(t *testing.T) {
	s := New(dispatcher.NewEngine())
	post(t, s, "/robots", robotRequest{ID: "robot-a", Available: true})

	rec := post(t, s, "/jobs/submit", submitRequest{ID: "job-1", DedupKey: "req-abc"})
	var created submitResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	post(t, s, "/dispatch", nil)
	rec = post(t, s, "/jobs/complete", completeRequest{ID: created.ID, Success: false})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /jobs/complete (fail) status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = post(t, s, "/jobs/submit", submitRequest{ID: "job-1-retry", DedupKey: "req-abc"})
	if rec.Code != http.StatusOK {
		t.Fatalf("retry POST /jobs/submit status = %d, want 200 (retried, not created), body = %s", rec.Code, rec.Body.String())
	}
	var retried submitResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &retried); err != nil {
		t.Fatalf("decoding retry response: %v", err)
	}
	if retried.Result != dispatcher.SubmitRetried || retried.ID != created.ID || retried.Status != dispatcher.StatusPending {
		t.Fatalf("retried = %+v, want the same job reset to pending", retried)
	}

	rec = post(t, s, "/dispatch", nil)
	var assignments []dispatcher.Assignment
	_ = json.Unmarshal(rec.Body.Bytes(), &assignments)
	if len(assignments) != 1 || assignments[0].JobID != created.ID {
		t.Fatalf("assignments = %+v, want the retried job dispatched exactly once", assignments)
	}
}

func TestHandleSubmitJob_RejectsMissingID(t *testing.T) {
	s := New(dispatcher.NewEngine())
	rec := post(t, s, "/jobs/submit", submitRequest{Priority: 1})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a job with no id", rec.Code)
	}
}

func TestHandleSubmitJob_MethodNotAllowed(t *testing.T) {
	s := New(dispatcher.NewEngine())
	req := httptest.NewRequest(http.MethodGet, "/jobs/submit", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405 for GET /jobs/submit", rec.Code)
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
