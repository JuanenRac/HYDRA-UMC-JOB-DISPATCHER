// HYDRA-UMC-JOB-DISPATCHER - src/api/api_security_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// The token, the body limit and the location of a job over real HTTP round-trips.
package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/dispatcher"
)

func withToken(t *testing.T, h http.Handler, method, path, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestToken_EveryRouteButHealthDemandsIt(t *testing.T) {
	s := New(dispatcher.NewEngine())
	s.RequireToken("s3cret-token")

	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/jobs"}, {http.MethodGet, "/robots"}, {http.MethodPost, "/robots"},
		{http.MethodPost, "/jobs"}, {http.MethodPost, "/jobs/submit"}, {http.MethodPost, "/jobs/complete"},
		{http.MethodPost, "/jobs/cancel"}, {http.MethodPost, "/dispatch"}, {http.MethodPost, "/jobs/detect-stale"},
	} {
		if rec := withToken(t, s, c.method, c.path, "", []byte("{}")); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a token = %d, want 401", c.method, c.path, rec.Code)
		}
		if rec := withToken(t, s, c.method, c.path, "wrong-token", []byte("{}")); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with a wrong token = %d, want 401", c.method, c.path, rec.Code)
		}
	}
	if rec := withToken(t, s, http.MethodGet, "/health", "", nil); rec.Code != http.StatusOK {
		t.Errorf("GET /health without a token = %d, want 200 (liveness stays open)", rec.Code)
	}
	if rec := withToken(t, s, http.MethodGet, "/jobs", "s3cret-token", nil); rec.Code != http.StatusOK {
		t.Errorf("GET /jobs with the token = %d, want 200", rec.Code)
	}
}

func TestToken_NoTokenConfiguredLeavesTheAPIOpenAsBefore(t *testing.T) {
	s := New(dispatcher.NewEngine())
	if rec := withToken(t, s, http.MethodGet, "/jobs", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("GET /jobs without any token configured = %d, want 200", rec.Code)
	}
}

func TestBody_OverTheLimitIs413AndTheServerKeepsServing(t *testing.T) {
	s := New(dispatcher.NewEngine())
	big, _ := json.Marshal(map[string]string{"id": "job-1", "requiredTool": strings.Repeat("x", MaxBodyBytes+1024)})
	for _, path := range []string{"/jobs", "/jobs/submit", "/robots", "/jobs/complete", "/jobs/cancel"} {
		if rec := withToken(t, s, http.MethodPost, path, "", big); rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("POST %s with a %d byte body = %d, want 413", path, len(big), rec.Code)
		}
	}
	if rec := post(t, s, "/jobs", jobRequest{ID: "job-ok"}); rec.Code != http.StatusCreated {
		t.Fatalf("a normal request after the oversized ones = %d, want 201", rec.Code)
	}
}

func TestBody_AMalformedBodyIsStill400(t *testing.T) {
	s := New(dispatcher.NewEngine())
	if rec := withToken(t, s, http.MethodPost, "/jobs", "", []byte("not json")); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed JSON = %d, want 400", rec.Code)
	}
}

func TestLocation_TravelsThroughTheAPI(t *testing.T) {
	s := New(dispatcher.NewEngine())
	post(t, s, "/robots", robotBody{ID: "far", Location: "cell-2", Available: true})
	post(t, s, "/robots", robotBody{ID: "near", Location: "cell-1", Available: true})
	if rec := post(t, s, "/jobs", jobRequest{ID: "job-1", Location: "cell-1"}); rec.Code != http.StatusCreated {
		t.Fatalf("POST /jobs = %d, body = %s", rec.Code, rec.Body.String())
	}
	post(t, s, "/dispatch", nil)
	var job dispatcher.Job
	if err := json.Unmarshal(get(t, s, "/jobs").Body.Bytes(), &[]*dispatcher.Job{&job}); err != nil {
		t.Fatalf("decoding /jobs: %v", err)
	}
	if job.Location != "cell-1" || job.AssignedRobot != "near" {
		t.Fatalf("job = %+v, want location cell-1 assigned to near", job)
	}

	rec := post(t, s, "/jobs/submit", submitRequest{ID: "job-2", Location: "cell-2", DedupKey: "k"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /jobs/submit = %d, body = %s", rec.Code, rec.Body.String())
	}
	var submitted submitResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &submitted); err != nil || submitted.Location != "cell-2" {
		t.Fatalf("submit response = %+v (%v), want the location back", submitted, err)
	}
}

func TestBody_TheLimitCanBeChangedAndANonsenseValueKeepsTheDefault(t *testing.T) {
	s := New(dispatcher.NewEngine())
	s.SetMaxBodyBytes(0)
	s.SetMaxBodyBytes(-5)
	body, _ := json.Marshal(map[string]string{"id": "job-1", "requiredTool": strings.Repeat("x", 1024)})
	if rec := withToken(t, s, http.MethodPost, "/jobs", "", body); rec.Code != http.StatusCreated {
		t.Fatalf("a 1 KiB body with the default limit = %d, want 201", rec.Code)
	}
	s.SetMaxBodyBytes(512)
	body, _ = json.Marshal(map[string]string{"id": "job-2", "requiredTool": strings.Repeat("x", 1024)})
	if rec := withToken(t, s, http.MethodPost, "/jobs", "", body); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a 1 KiB body with a 512 byte limit = %d, want 413", rec.Code)
	}
}
