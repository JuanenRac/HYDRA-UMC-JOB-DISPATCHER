// HYDRA-UMC-JOB-DISPATCHER - src/sqlitestore/sqlitestore_closed_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package sqlitestore

import (
	"strings"
	"testing"

	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/dispatcher"
)

// A store that has lost its database must report every failure instead of
// pretending a transition was saved: the engine surfaces these errors on
// /health.
func TestClosedStore_EveryOperationReportsAnError(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := store.SaveJob(dispatcher.JobRecord{Job: dispatcher.Job{ID: "j1"}}); err == nil || !strings.Contains(err.Error(), "j1") {
		t.Errorf("SaveJob on a closed store = %v, want an error naming the job", err)
	}
	if err := store.SaveRobot(dispatcher.Robot{ID: "r1"}); err == nil {
		t.Errorf("SaveRobot on a closed store returned nil")
	}
	if _, _, err := store.LoadAll(); err == nil {
		t.Errorf("LoadAll on a closed store returned nil")
	}
}

func TestReopeningAnUpToDateDatabaseIsIdempotent(t *testing.T) {
	path := t.TempDir() + "/again.db"
	for i := 0; i < 3; i++ {
		store, err := Open(path)
		if err != nil {
			t.Fatalf("Open #%d: %v", i, err)
		}
		if err := store.SaveJob(dispatcher.JobRecord{Job: dispatcher.Job{ID: "j", Priority: i}}); err != nil {
			t.Fatalf("SaveJob #%d: %v", i, err)
		}
		store.Close()
	}
	store, err := Open(path)
	if err != nil {
		t.Fatalf("final Open: %v", err)
	}
	defer store.Close()
	jobs, _, err := store.LoadAll()
	if err != nil || len(jobs) != 1 || jobs[0].Priority != 2 {
		t.Fatalf("after three reopen cycles got %+v (%v), want one job with priority 2", jobs, err)
	}
}
