// HYDRA-UMC-JOB-DISPATCHER - src/sqlitestore/sqlitestore_heartbeat_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// The last heartbeat of a robot and the location of a job survive a restart, and a database
// written by the version before them is migrated in place.
package sqlitestore

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/JuanenRac/hydra-umc-job-dispatcher/src/dispatcher"
)

func TestHeartbeat_IsPersistedAndRestored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.sqlite3")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	beat := time.Unix(1_700_000_000, 123_456_789)
	if err := store.SaveRobot(dispatcher.Robot{ID: "r1", Location: "cell-1", Tool: "PnP", Available: true, LastHeartbeat: beat}); err != nil {
		t.Fatalf("SaveRobot: %v", err)
	}
	if err := store.SaveRobot(dispatcher.Robot{ID: "never", Available: true}); err != nil {
		t.Fatalf("SaveRobot: %v", err)
	}
	store.Close()

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	_, robots, err := reopened.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	byID := map[string]dispatcher.Robot{}
	for _, r := range robots {
		byID[r.ID] = r
	}
	if !byID["r1"].LastHeartbeat.Equal(beat) {
		t.Fatalf("r1 LastHeartbeat = %v, want %v", byID["r1"].LastHeartbeat, beat)
	}
	if !byID["never"].LastHeartbeat.IsZero() {
		t.Fatalf("a robot that never checked in came back with heartbeat %v, want the zero time", byID["never"].LastHeartbeat)
	}
}

func TestHeartbeat_ARestartedEngineStillDetectsAStaleAssignment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.sqlite3")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	engine, err := dispatcher.NewEngineWithStore(store)
	if err != nil {
		t.Fatalf("NewEngineWithStore: %v", err)
	}
	engine.UpsertRobot(dispatcher.Robot{ID: "robot-a", Available: true})
	if err := engine.AddJob(dispatcher.Job{ID: "job-1"}); err != nil {
		t.Fatalf("AddJob: %v", err)
	}
	engine.DispatchOnce()
	store.Close()

	// the process restarts a long time later: the robot never came back
	time.Sleep(20 * time.Millisecond)
	store2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store2.Close()
	engine2, err := dispatcher.NewEngineWithStore(store2)
	if err != nil {
		t.Fatalf("NewEngineWithStore after restart: %v", err)
	}
	unknown := engine2.DetectStaleAssignments(10 * time.Millisecond)
	if len(unknown) != 1 || unknown[0] != "job-1" {
		t.Fatalf("DetectStaleAssignments after a restart = %v, want [job-1]: the robot went silent and its last heartbeat is persisted", unknown)
	}
}

func TestMigration_ADatabaseFromTheVersionBeforeIsUpgradedInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.sqlite3")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	// the schema as the previous version created it: no jobs.location, no robots.last_heartbeat_ns
	if _, err := old.Exec(`
CREATE TABLE jobs (id TEXT PRIMARY KEY, priority INTEGER NOT NULL, required_tool TEXT NOT NULL, depends_on TEXT NOT NULL, status TEXT NOT NULL, assigned_robot TEXT NOT NULL, dedup_key TEXT NOT NULL, seq INTEGER NOT NULL);
CREATE TABLE robots (id TEXT PRIMARY KEY, location TEXT NOT NULL, tool TEXT NOT NULL, available INTEGER NOT NULL, load INTEGER NOT NULL);
INSERT INTO jobs VALUES ('old-job', 3, 'PnP', '[]', 'pending', '', '', 1);
INSERT INTO robots VALUES ('old-robot', 'cell-9', 'PnP', 1, 2);
`); err != nil {
		t.Fatalf("seeding the old schema: %v", err)
	}
	old.Close()

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open must migrate an old database, got: %v", err)
	}
	defer store.Close()
	jobs, robots, err := store.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll on a migrated database: %v", err)
	}
	if len(jobs) != 1 || jobs[0].ID != "old-job" || jobs[0].Location != "" || jobs[0].Priority != 3 {
		t.Fatalf("migrated jobs = %+v", jobs)
	}
	if len(robots) != 1 || robots[0].ID != "old-robot" || !robots[0].LastHeartbeat.IsZero() || robots[0].Load != 2 {
		t.Fatalf("migrated robots = %+v", robots)
	}
	// and it can be opened a second time (the migration is idempotent) and written with the new columns
	store.Close()
	again, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer again.Close()
	if err := again.SaveJob(dispatcher.JobRecord{Job: dispatcher.Job{ID: "new-job", Location: "cell-1", Status: dispatcher.StatusPending}, Seq: 2}); err != nil {
		t.Fatalf("SaveJob with a location: %v", err)
	}
	jobs, _, _ = again.LoadAll()
	found := false
	for _, j := range jobs {
		if j.ID == "new-job" && j.Location == "cell-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the location of a job was not persisted: %+v", jobs)
	}
}
