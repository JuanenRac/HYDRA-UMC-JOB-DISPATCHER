// HYDRA-UMC-JOB-DISPATCHER - src/api/api_archive_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// GET /jobs?archived=1 reads the jobs that left the live queue.
package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/dispatcher"
	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/sqlitestore"
)

func TestListJobs_ArchivedReadsTheArchiveApartFromTheQueue(t *testing.T) {
	store, err := sqlitestore.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine, err := dispatcher.NewEngineWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	s := New(engine)
	post(t, s, "/robots", robotBody{ID: "r1", Available: true})
	post(t, s, "/jobs", jobRequest{ID: "old"})
	post(t, s, "/jobs", jobRequest{ID: "new"})
	post(t, s, "/dispatch", nil)
	if rec := post(t, s, "/jobs/complete", map[string]any{"id": "old", "success": true, "robotId": "r1"}); rec.Code != http.StatusOK {
		t.Fatalf("complete: %d %s", rec.Code, rec.Body.String())
	}
	engine.SetArchiveAfter(time.Nanosecond)
	time.Sleep(5 * time.Millisecond)
	archived, err := engine.ArchiveFinished()
	if err != nil || len(archived) != 1 || archived[0] != "old" {
		t.Fatalf("archived %v, %v", archived, err)
	}

	rec := get(t, s, "/jobs")
	var live []dispatcher.Job
	if err := json.Unmarshal(rec.Body.Bytes(), &live); err != nil || len(live) != 1 || live[0].ID != "new" {
		t.Fatalf("GET /jobs = %s, %v; want only the live job", rec.Body.String(), err)
	}
	rec = get(t, s, "/jobs?archived=1")
	var old []dispatcher.Job
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &old) != nil || len(old) != 1 || old[0].ID != "old" {
		t.Fatalf("GET /jobs?archived=1 = %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Total-Count") != "1" {
		t.Fatalf("X-Total-Count = %q, want 1", rec.Header().Get("X-Total-Count"))
	}
	if rec = get(t, s, "/jobs?archived=0"); rec.Code != http.StatusOK {
		t.Fatalf("archived=0 = %d", rec.Code)
	}
	for _, bad := range []string{"/jobs?archived=2", "/jobs?archived=1&status=done", "/jobs?archived=1&robot=r1"} {
		if rec = get(t, s, bad); rec.Code != http.StatusBadRequest {
			t.Fatalf("GET %s = %d, want 400", bad, rec.Code)
		}
	}
}

func TestListJobs_ArchivedWithoutADatabaseIs404(t *testing.T) {
	s := New(dispatcher.NewEngine())
	if rec := get(t, s, "/jobs?archived=1"); rec.Code != http.StatusNotFound {
		t.Fatalf("GET /jobs?archived=1 without a database = %d, want 404", rec.Code)
	}
}
