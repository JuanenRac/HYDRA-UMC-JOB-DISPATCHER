// HYDRA-UMC-JOB-DISPATCHER - HTTP tests of the gate of signed trajectory proposals
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/dispatcher"
)

// gateServer builds a dispatcher API whose proposals are checked by a stand-in for HYDRA-UMC-SERVER that answers with the given status and body.
func gateServer(t *testing.T, require bool, status *int, body *string) (*Server, *dispatcher.Engine) {
	t.Helper()
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(*status)
		_, _ = w.Write([]byte(*body))
	}))
	t.Cleanup(stub.Close)
	keyPath := filepath.Join(t.TempDir(), "identity.key")
	if err := os.WriteFile(keyPath, []byte("0123456789abcdef0123456789abcdef-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	verifier, err := dispatcher.NewProposalVerifier(dispatcher.ProposalVerifyConfig{Enabled: true, ServerURL: stub.URL, IdentityKeyFile: keyPath, Timeout: 2 * time.Second, Require: require})
	if err != nil {
		t.Fatalf("NewProposalVerifier: %v", err)
	}
	e := dispatcher.NewEngine()
	s := New(e)
	s.SetProposalVerifier(verifier)
	return s, e
}

func TestProposalGate_OffTheFieldsAreIgnored(t *testing.T) {
	s := New(dispatcher.NewEngine())
	rec := post(t, s, "/jobs", map[string]any{"id": "j1", "proposal": map[string]any{"signature": "x"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d %s, want 201", rec.Code, rec.Body)
	}
}

func TestProposalGate_AValidProposalQueuesTheJobOnBothRoutes(t *testing.T) {
	status, body := http.StatusOK, `{"valid":true,"reason":"ok","key_id":"k1"}`
	s, e := gateServer(t, false, &status, &body)
	proposal := map[string]any{"signature": "s", "robot_id": "7"}
	if rec := post(t, s, "/jobs", map[string]any{"id": "j1", "proposal": proposal, "expectedRobotId": "7"}); rec.Code != http.StatusCreated {
		t.Fatalf("POST /jobs: %d %s", rec.Code, rec.Body)
	}
	if rec := post(t, s, "/jobs/submit", map[string]any{"id": "j2", "dedupKey": "d2", "proposal": proposal, "expectedRobotId": "7"}); rec.Code != http.StatusCreated {
		t.Fatalf("POST /jobs/submit: %d %s", rec.Code, rec.Body)
	}
	if _, ok := e.Job("j1"); !ok {
		t.Error("j1 was not queued")
	}
	if _, ok := e.Job("j2"); !ok {
		t.Error("j2 was not queued")
	}
}

func TestProposalGate_AnInvalidProposalIsRefusedAndNothingIsQueued(t *testing.T) {
	status, body := http.StatusOK, `{"valid":false,"reason":"bad-signature"}`
	s, e := gateServer(t, false, &status, &body)
	for _, path := range []string{"/jobs", "/jobs/submit"} {
		rec := post(t, s, path, map[string]any{"id": "j1", "dedupKey": "d", "proposal": map[string]any{"signature": "x"}, "expectedRobotId": "7"})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d %s, want 422", path, rec.Code, rec.Body)
		}
	}
	if _, ok := e.Job("j1"); ok {
		t.Error("a job with an invalid proposal was queued")
	}
}

func TestProposalGate_FailsClosedWhenTheServerCannotAnswer(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusServiceUnavailable, http.StatusInternalServerError} {
		status, body := code, `{}`
		s, e := gateServer(t, false, &status, &body)
		rec := post(t, s, "/jobs", map[string]any{"id": "j1", "proposal": map[string]any{"signature": "x"}, "expectedRobotId": "7"})
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("server HTTP %d: status %d %s, want 503", code, rec.Code, rec.Body)
		}
		if _, ok := e.Job("j1"); ok {
			t.Errorf("server HTTP %d: the job was queued", code)
		}
	}
}

func TestProposalGate_TheRobotIsNeededAndAJobWithoutAProposalFollowsRequire(t *testing.T) {
	status, body := http.StatusOK, `{"valid":true,"reason":"ok"}`
	s, _ := gateServer(t, false, &status, &body)
	if rec := post(t, s, "/jobs", map[string]any{"id": "j1", "proposal": map[string]any{"signature": "x"}}); rec.Code != http.StatusBadRequest {
		t.Errorf("a proposal without the robot: %d %s, want 400", rec.Code, rec.Body)
	}
	if rec := post(t, s, "/jobs", map[string]any{"id": "j2"}); rec.Code != http.StatusCreated {
		t.Errorf("a job without a proposal, require off: %d %s, want 201", rec.Code, rec.Body)
	}
	strict, e := gateServer(t, true, &status, &body)
	if rec := post(t, strict, "/jobs", map[string]any{"id": "j3"}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a job without a proposal, require on: %d %s, want 422", rec.Code, rec.Body)
	}
	if _, ok := e.Job("j3"); ok {
		t.Error("a job without a proposal was queued with require on")
	}
}
