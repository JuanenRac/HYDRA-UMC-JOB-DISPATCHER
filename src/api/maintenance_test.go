// HYDRA-UMC-JOB-DISPATCHER - HTTP tests of the maintenance proposals
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

func maintenanceServer(on bool) (*Server, *dispatcher.Engine) {
	e := dispatcher.NewEngine()
	b := dispatcher.NewMaintenanceBoard(e)
	if on {
		cfg := dispatcher.DefaultMaintenanceConfig()
		cfg.Enabled = true
		b.SetConfig(cfg)
	}
	s := New(e)
	s.SetMaintenance(b)
	return s, e
}

func TestMaintenance_RoutesAreUnknownPathsWhileOff(t *testing.T) {
	s, _ := maintenanceServer(false)
	for _, path := range []string{"/maintenance/proposals", "/maintenance/proposals/approve", "/maintenance/proposals/reject"} {
		for _, m := range []string{http.MethodGet, http.MethodPost} {
			req := httptest.NewRequest(m, path, strings.NewReader(`{}`))
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s %s: status %d, want 404", m, path, rec.Code)
			}
		}
	}
}

func TestMaintenance_ProposeThenApproveSubmitsAJob(t *testing.T) {
	s, e := maintenanceServer(true)
	rec := post(t, s, "/maintenance/proposals", map[string]any{"source": "motor-1", "reason": "sustained drift", "score": 3.2, "drift": 1.1})
	if rec.Code != http.StatusCreated {
		t.Fatalf("propose: %d %s", rec.Code, rec.Body)
	}
	var created struct {
		Proposal struct{ ID string } `json:"proposal"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if len(e.Jobs()) != 0 {
		t.Fatal("a proposal must not create a job")
	}
	if list := get(t, s, "/maintenance/proposals?status=proposed"); !strings.Contains(list.Body.String(), "motor-1") {
		t.Fatalf("list: %s", list.Body)
	}
	if again := post(t, s, "/maintenance/proposals", map[string]any{"source": "motor-1", "reason": "still", "score": 4, "drift": 1.5}); again.Code != http.StatusOK {
		t.Fatalf("a second proposal for the same source must be 200, got %d", again.Code)
	}
	ok := post(t, s, "/maintenance/proposals/approve", map[string]any{"id": created.Proposal.ID, "by": "ana"})
	if ok.Code != http.StatusOK || len(e.Jobs()) != 1 {
		t.Fatalf("approve: %d %s jobs=%d", ok.Code, ok.Body, len(e.Jobs()))
	}
	if again := post(t, s, "/maintenance/proposals/approve", map[string]any{"id": created.Proposal.ID}); again.Code != http.StatusConflict {
		t.Fatalf("a decided proposal answers 409, got %d", again.Code)
	}
}

func TestMaintenance_RejectAndBadInput(t *testing.T) {
	s, e := maintenanceServer(true)
	rec := post(t, s, "/maintenance/proposals", map[string]any{"source": "motor-2", "reason": "drift", "score": 1, "drift": 1})
	var created struct {
		Proposal struct{ ID string } `json:"proposal"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if rej := post(t, s, "/maintenance/proposals/reject", map[string]any{"id": created.Proposal.ID, "by": "ana", "reason": "noise"}); rej.Code != http.StatusOK || len(e.Jobs()) != 0 {
		t.Fatalf("reject: %d %s", rej.Code, rej.Body)
	}
	if bad := post(t, s, "/maintenance/proposals", map[string]any{"source": "bad source", "reason": "x"}); bad.Code != http.StatusBadRequest {
		t.Fatalf("bad source: %d", bad.Code)
	}
	if unk := post(t, s, "/maintenance/proposals", map[string]any{"source": "ok", "reason": "x", "extra": 1}); unk.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", unk.Code)
	}
	if none := post(t, s, "/maintenance/proposals/approve", map[string]any{"id": "mp-0"}); none.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d", none.Code)
	}
	if wrong := get(t, s, "/maintenance/proposals/approve"); wrong.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET approve: %d", wrong.Code)
	}
}
