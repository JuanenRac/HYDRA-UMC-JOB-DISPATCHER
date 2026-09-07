// HYDRA-UMC-JOB-DISPATCHER - dispatcher package persistence tests
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0 - see LICENSE
//
// External test package (dispatcher_test, not dispatcher) so these can
// import both dispatcher and the real sqlitestore implementation without
// an import cycle - sqlitestore itself imports dispatcher, not the other
// way around.
package dispatcher_test

import (
	"errors"
	"path/filepath"
	"sort"
	"testing"

	"github.com/JuanenRac/hydra-umc-job-dispatcher/src/dispatcher"
	"github.com/JuanenRac/hydra-umc-job-dispatcher/src/sqlitestore"
)

// TestRealRestart_MissionQueueSurvivesAcrossTwoEngines is the end-to-end
// proof of the audit finding this package closes: a real Engine, backed
// by a real SQLite file, that has already dispatched one job and
// completed another, handed to a genuinely SEPARATE Engine/Store pair
// pointed at the same file - as a real process restart would - and
// still reports the exact same mission state.
func TestRealRestart_MissionQueueSurvivesAcrossTwoEngines(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "job-dispatcher.db")

	store1, err := sqlitestore.Open(dbPath)
	if err != nil {
		t.Fatalf("sqlitestore.Open: %v", err)
	}
	e1, err := dispatcher.NewEngineWithStore(store1)
	if err != nil {
		t.Fatalf("NewEngineWithStore: %v", err)
	}

	e1.UpsertRobot(dispatcher.Robot{ID: "robot-a", Tool: "PnP", Available: true})
	if err := e1.AddJob(dispatcher.Job{ID: "pick-1", RequiredTool: "PnP"}); err != nil {
		t.Fatalf("AddJob(pick-1): %v", err)
	}
	if err := e1.AddJob(dispatcher.Job{ID: "place-1", RequiredTool: "PnP", DependsOn: []string{"pick-1"}}); err != nil {
		t.Fatalf("AddJob(place-1): %v", err)
	}
	assignments := e1.DispatchOnce()
	if len(assignments) != 1 || assignments[0].JobID != "pick-1" {
		t.Fatalf("expected only pick-1 to be dispatched (place-1 is still blocked), got %+v", assignments)
	}
	if err := e1.CompleteJob("pick-1", true); err != nil {
		t.Fatalf("CompleteJob(pick-1): %v", err)
	}
	// place-1 is now real-eligible (Pending) but not yet actually
	// dispatched - proving that a job's OWN un-dispatched-but-unblocked
	// state, not just a terminal one, survives the restart too.
	if job, _ := e1.Job("place-1"); job.Status != dispatcher.StatusPending {
		t.Fatalf("expected place-1 to be Pending after pick-1 completed, got %q", job.Status)
	}
	if err := store1.Close(); err != nil {
		t.Fatalf("store1.Close: %v", err)
	}

	// A genuinely separate Store/Engine pair, as a real restarted process
	// would construct - never the same Go objects as above.
	store2, err := sqlitestore.Open(dbPath)
	if err != nil {
		t.Fatalf("sqlitestore.Open (second process): %v", err)
	}
	defer store2.Close()
	e2, err := dispatcher.NewEngineWithStore(store2)
	if err != nil {
		t.Fatalf("NewEngineWithStore (second process): %v", err)
	}

	jobs := e2.Jobs()
	sort.Slice(jobs, func(i, k int) bool { return jobs[i].ID < jobs[k].ID })
	if len(jobs) != 2 {
		t.Fatalf("expected 2 restored jobs, got %d: %+v", len(jobs), jobs)
	}
	if jobs[0].ID != "pick-1" || jobs[0].Status != dispatcher.StatusDone || jobs[0].AssignedRobot != "robot-a" {
		t.Fatalf("pick-1 did not survive the restart intact: %+v", jobs[0])
	}
	if jobs[1].ID != "place-1" || jobs[1].Status != dispatcher.StatusPending {
		t.Fatalf("place-1 did not survive the restart intact: %+v", jobs[1])
	}

	robots := e2.Robots()
	if len(robots) != 1 || robots[0].ID != "robot-a" || !robots[0].Available || robots[0].Load != 1 {
		t.Fatalf("robot-a did not survive the restart intact (expected Available=true, Load=1 after completing pick-1): %+v", robots)
	}

	// Restoring FIFO order (via JobRecord.Seq) matters for real scheduling
	// fairness after a restart, not just for display - a fresh job added
	// now must sort after both restored ones in DispatchOnce's own
	// priority-tie FIFO order.
	if err := e2.AddJob(dispatcher.Job{ID: "weld-1", RequiredTool: "PnP"}); err != nil {
		t.Fatalf("AddJob(weld-1): %v", err)
	}
	e2.UpsertRobot(dispatcher.Robot{ID: "robot-b", Tool: "PnP", Available: true})
	secondPass := e2.DispatchOnce()
	dispatchedIDs := map[string]bool{}
	for _, a := range secondPass {
		dispatchedIDs[a.JobID] = true
	}
	if !dispatchedIDs["place-1"] || !dispatchedIDs["weld-1"] {
		t.Fatalf("expected both place-1 (restored) and weld-1 (new) to be dispatched once a robot is free, got %+v", secondPass)
	}
}

// brokenStore is a real dispatcher.Store whose writes fail on demand -
// used to prove Engine's own documented hard-vs-soft persist-failure
// contract, not just its happy path.
type brokenStore struct {
	failJobs   bool
	failRobots bool
}

func (b *brokenStore) SaveJob(dispatcher.JobRecord) error {
	if b.failJobs {
		return errors.New("simulated disk-full error")
	}
	return nil
}

func (b *brokenStore) SaveRobot(dispatcher.Robot) error {
	if b.failRobots {
		return errors.New("simulated disk-full error")
	}
	return nil
}

func (b *brokenStore) LoadAll() ([]dispatcher.JobRecord, []dispatcher.Robot, error) {
	return nil, nil, nil
}

func TestAddJob_FailsAndLeavesNoTraceWhenTheStoreCannotPersist(t *testing.T) {
	e, err := dispatcher.NewEngineWithStore(&brokenStore{failJobs: true})
	if err != nil {
		t.Fatalf("NewEngineWithStore: %v", err)
	}

	if err := e.AddJob(dispatcher.Job{ID: "pick-1"}); err == nil {
		t.Fatal("expected AddJob to fail when the store cannot persist a brand-new job")
	}
	if _, ok := e.Job("pick-1"); ok {
		t.Fatal("a job whose first-ever persist failed must not be left in memory either - the caller should be able to safely retry")
	}
	if len(e.Jobs()) != 0 {
		t.Fatalf("expected zero jobs after a failed AddJob, got %d", len(e.Jobs()))
	}
}

func TestCompleteJob_SoftPersistFailureIsObservableButDoesNotUndoTheTransition(t *testing.T) {
	store := &brokenStore{}
	e, err := dispatcher.NewEngineWithStore(store)
	if err != nil {
		t.Fatalf("NewEngineWithStore: %v", err)
	}
	e.UpsertRobot(dispatcher.Robot{ID: "robot-a", Available: true})
	if err := e.AddJob(dispatcher.Job{ID: "pick-1"}); err != nil {
		t.Fatalf("AddJob: %v", err)
	}
	e.DispatchOnce()
	if e.LastPersistError() != nil {
		t.Fatalf("expected no persist error yet, got %v", e.LastPersistError())
	}

	store.failJobs = true
	if err := e.CompleteJob("pick-1", true); err != nil {
		t.Fatalf("CompleteJob must still succeed even when the store write fails - the physical work already happened: %v", err)
	}
	if job, _ := e.Job("pick-1"); job.Status != dispatcher.StatusDone {
		t.Fatalf("expected pick-1 to be Done despite the store failure, got %q", job.Status)
	}
	if e.LastPersistError() == nil {
		t.Fatal("expected LastPersistError() to report the simulated store failure")
	}

	store.failJobs = false
	e.UpsertRobot(dispatcher.Robot{ID: "robot-a", Available: false}) // any successful write clears it
	if err := e.AddJob(dispatcher.Job{ID: "pick-2"}); err != nil {
		t.Fatalf("AddJob(pick-2): %v", err)
	}
	if e.LastPersistError() != nil {
		t.Fatalf("expected LastPersistError() to clear once the store is healthy again, got %v", e.LastPersistError())
	}
}
