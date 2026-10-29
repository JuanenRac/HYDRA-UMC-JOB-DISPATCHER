// HYDRA-UMC-JOB-DISPATCHER - src/sqlitestore/sqlitestore_batch_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// All-or-nothing batches, robot deletion, job timestamps and attempts that survive a restart,
// the write-ahead log on a file database and the migration of a database without those columns.
package sqlitestore

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/dispatcher"
)

func TestSaveBatch_WritesEverythingOrNothing(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// a trigger makes the robot named "bad" fail, as a full disk or a constraint would
	if _, err := store.db.Exec(`CREATE TRIGGER no_bad_robot BEFORE INSERT ON robots WHEN NEW.id = 'bad' BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
		t.Fatal(err)
	}
	jobs := []dispatcher.JobRecord{{Job: dispatcher.Job{ID: "j1", Status: dispatcher.StatusDone}, Seq: 1}}
	if err := store.SaveBatch(jobs, []dispatcher.Robot{{ID: "ok"}, {ID: "bad"}}); err == nil {
		t.Fatal("the failing robot must fail the batch")
	}
	loadedJobs, loadedRobots, err := store.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(loadedJobs) != 0 || len(loadedRobots) != 0 {
		t.Fatalf("a failed batch left %d jobs and %d robots behind, want none", len(loadedJobs), len(loadedRobots))
	}
	if err := store.SaveBatch(jobs, []dispatcher.Robot{{ID: "ok"}}); err != nil {
		t.Fatal(err)
	}
	loadedJobs, loadedRobots, _ = store.LoadAll()
	if len(loadedJobs) != 1 || len(loadedRobots) != 1 {
		t.Fatalf("jobs=%d robots=%d, want 1 and 1", len(loadedJobs), len(loadedRobots))
	}
}

func TestDeleteRobot_RemovesOnlyThatRobot(t *testing.T) {
	store, _ := Open(":memory:")
	defer store.Close()
	_ = store.SaveRobot(dispatcher.Robot{ID: "a"})
	_ = store.SaveRobot(dispatcher.Robot{ID: "b"})
	if err := store.DeleteRobot("a"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteRobot("never-existed"); err != nil {
		t.Fatalf("deleting an unknown robot must not fail: %v", err)
	}
	_, robots, _ := store.LoadAll()
	if len(robots) != 1 || robots[0].ID != "b" {
		t.Fatalf("robots = %+v, want only b", robots)
	}
}

func TestTimestampsAndAttemptSurviveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.sqlite3")
	store, _ := Open(path)
	rec := dispatcher.JobRecord{Job: dispatcher.Job{ID: "x", Status: dispatcher.StatusAssigned, SubmittedAtMs: 111, AssignedAtMs: 222, CompletedAtMs: 333, Attempt: 4}, Seq: 7}
	if err := store.SaveJob(rec); err != nil {
		t.Fatal(err)
	}
	store.Close()
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	jobs, _, _ := again.LoadAll()
	if len(jobs) != 1 || jobs[0].SubmittedAtMs != 111 || jobs[0].AssignedAtMs != 222 || jobs[0].CompletedAtMs != 333 || jobs[0].Attempt != 4 {
		t.Fatalf("job after restart = %+v", jobs)
	}
}

func TestOpen_UsesTheWriteAheadLogOnAFileDatabase(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "queue.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var mode string
	if err := store.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q (%v), want wal", mode, err)
	}
}

func TestOpen_MigratesADatabaseWithoutTheTimestampColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.sqlite3")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE jobs (id TEXT PRIMARY KEY, priority INTEGER NOT NULL, required_tool TEXT NOT NULL,
		depends_on TEXT NOT NULL, status TEXT NOT NULL, assigned_robot TEXT NOT NULL, dedup_key TEXT NOT NULL, seq INTEGER NOT NULL, location TEXT NOT NULL DEFAULT '');
		INSERT INTO jobs VALUES ('old', 0, '', '[]', 'pending', '', '', 1, '');`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open must migrate the old database: %v", err)
	}
	defer store.Close()
	jobs, _, err := store.LoadAll()
	if err != nil || len(jobs) != 1 || jobs[0].ID != "old" || jobs[0].Attempt != 0 || jobs[0].SubmittedAtMs != 0 {
		t.Fatalf("jobs = %+v err = %v", jobs, err)
	}
}

// The whole path: an engine on a real database, a restart in the middle, and nothing is half done.
func TestEngineOnSQLite_CompletionIsAtomicAcrossARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.sqlite3")
	store, _ := Open(path)
	e, err := dispatcher.NewEngineWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	e.UpsertRobot(dispatcher.Robot{ID: "r", Available: true})
	_ = e.AddJob(dispatcher.Job{ID: "a"})
	_ = e.AddJob(dispatcher.Job{ID: "b", DependsOn: []string{"a"}})
	e.DispatchOnce()
	if err := e.CompleteJobAttempt("a", true, "r", 1); err != nil {
		t.Fatal(err)
	}
	store.Close()

	store2, _ := Open(path)
	defer store2.Close()
	e2, err := dispatcher.NewEngineWithStore(store2)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := e2.Job("a")
	b, _ := e2.Job("b")
	robots := e2.Robots()
	if a.Status != dispatcher.StatusDone || a.Attempt != 1 || a.CompletedAtMs == 0 || b.Status != dispatcher.StatusPending || !robots[0].Available || robots[0].Load != 1 {
		t.Fatalf("after the restart: a=%+v b=%q robot=%+v", a, b.Status, robots[0])
	}
}
