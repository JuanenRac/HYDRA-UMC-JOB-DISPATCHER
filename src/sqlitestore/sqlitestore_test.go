// HYDRA-UMC-JOB-DISPATCHER - src/sqlitestore/sqlitestore_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0 - see LICENSE
package sqlitestore

import (
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/JuanenRac/hydra-umc-job-dispatcher/src/dispatcher"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestLoadAll_EmptyDatabaseReturnsEmptyNotError(t *testing.T) {
	store := openTestStore(t)
	jobs, robots, err := store.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll on an empty database returned an error: %v", err)
	}
	if len(jobs) != 0 || len(robots) != 0 {
		t.Fatalf("expected no jobs/robots, got %d jobs, %d robots", len(jobs), len(robots))
	}
}

func TestSaveJob_RoundTripsEveryField(t *testing.T) {
	store := openTestStore(t)
	record := dispatcher.JobRecord{
		Job: dispatcher.Job{
			ID:            "weld-1",
			Priority:      7,
			RequiredTool:  "Welder",
			DependsOn:     []string{"pick-1", "place-1"},
			Status:        dispatcher.StatusBlocked,
			AssignedRobot: "",
			DedupKey:      "order-42",
		},
		Seq: 3,
	}
	if err := store.SaveJob(record); err != nil {
		t.Fatalf("SaveJob: %v", err)
	}

	jobs, _, err := store.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if !reflect.DeepEqual(jobs[0], record) {
		t.Fatalf("round-tripped job = %+v, want %+v", jobs[0], record)
	}
}

func TestSaveJob_UpsertsRatherThanDuplicating(t *testing.T) {
	store := openTestStore(t)
	original := dispatcher.JobRecord{Job: dispatcher.Job{ID: "pick-1", Status: dispatcher.StatusPending, DependsOn: []string{}}, Seq: 1}
	if err := store.SaveJob(original); err != nil {
		t.Fatalf("SaveJob(original): %v", err)
	}

	updated := original
	updated.Status = dispatcher.StatusAssigned
	updated.AssignedRobot = "robot-a"
	if err := store.SaveJob(updated); err != nil {
		t.Fatalf("SaveJob(updated): %v", err)
	}

	jobs, _, err := store.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected exactly 1 row after re-saving the same job ID, got %d", len(jobs))
	}
	if jobs[0].Status != dispatcher.StatusAssigned || jobs[0].AssignedRobot != "robot-a" {
		t.Fatalf("expected the row to reflect the latest save, got %+v", jobs[0])
	}
}

func TestSaveRobot_RoundTripsAndUpserts(t *testing.T) {
	store := openTestStore(t)
	robot := dispatcher.Robot{ID: "robot-a", Location: "Cell 3", Tool: "PnP", Available: true, Load: 4}
	if err := store.SaveRobot(robot); err != nil {
		t.Fatalf("SaveRobot: %v", err)
	}
	robot.Available = false
	robot.Load = 5
	if err := store.SaveRobot(robot); err != nil {
		t.Fatalf("SaveRobot (update): %v", err)
	}

	_, robots, err := store.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(robots) != 1 {
		t.Fatalf("expected exactly 1 robot row, got %d", len(robots))
	}
	if !reflect.DeepEqual(robots[0], robot) {
		t.Fatalf("round-tripped robot = %+v, want %+v", robots[0], robot)
	}
}

// TestRealRestart_DataSurvivesReopeningTheSameFile is the test that
// actually matters for the finding this package closes: a real
// file on disk (not :memory:, which by definition never survives a
// process restart), closed and reopened as a genuinely separate *Store,
// still has everything the first process wrote to it.
func TestRealRestart_DataSurvivesReopeningTheSameFile(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "job-dispatcher.db")

	first, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open (first process): %v", err)
	}
	if err := first.SaveJob(dispatcher.JobRecord{Job: dispatcher.Job{ID: "pick-1", Status: dispatcher.StatusDone, DependsOn: []string{}}, Seq: 1}); err != nil {
		t.Fatalf("SaveJob: %v", err)
	}
	if err := first.SaveJob(dispatcher.JobRecord{Job: dispatcher.Job{ID: "place-1", Status: dispatcher.StatusAssigned, AssignedRobot: "robot-a", DependsOn: []string{"pick-1"}}, Seq: 2}); err != nil {
		t.Fatalf("SaveJob: %v", err)
	}
	if err := first.SaveRobot(dispatcher.Robot{ID: "robot-a", Tool: "PnP", Available: false, Load: 1}); err != nil {
		t.Fatalf("SaveRobot: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close (first process): %v", err)
	}

	// A real second Store, as if this were a freshly-started process
	// pointed at the same real file - not the same Go object.
	second, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open (second process): %v", err)
	}
	defer second.Close()

	jobs, robots, err := second.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll (second process): %v", err)
	}
	sort.Slice(jobs, func(i, k int) bool { return jobs[i].Seq < jobs[k].Seq })
	if len(jobs) != 2 || jobs[0].ID != "pick-1" || jobs[0].Status != dispatcher.StatusDone {
		t.Fatalf("pick-1 did not survive the restart intact: %+v", jobs)
	}
	if jobs[1].ID != "place-1" || jobs[1].Status != dispatcher.StatusAssigned || jobs[1].AssignedRobot != "robot-a" || len(jobs[1].DependsOn) != 1 || jobs[1].DependsOn[0] != "pick-1" {
		t.Fatalf("place-1 did not survive the restart intact: %+v", jobs[1])
	}
	if len(robots) != 1 || robots[0].ID != "robot-a" || robots[0].Available || robots[0].Load != 1 {
		t.Fatalf("robot-a did not survive the restart intact: %+v", robots)
	}
}

func TestOpen_RejectsAnUnwritableDirectory(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "does-not-exist", "job-dispatcher.db"))
	if err == nil {
		t.Fatal("expected an error opening a database inside a directory that does not exist")
	}
}
