// HYDRA-UMC-JOB-DISPATCHER - src/dispatcher/dispatcher_batch_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// Atomic writes, heartbeats from the future, robot removal, the paged job list, assignment
// attempts and job timestamps.
package dispatcher

import (
	"errors"
	"testing"
	"time"
)

// recordingStore is a Store (and BatchStore and RobotDeleter) that remembers how it was called.
type recordingStore struct {
	singleJobs, singleRobots int
	batches                  []batchCall
	deleted                  []string
	deleteErr                error
	jobs                     map[string]JobRecord
	robots                   map[string]Robot
}

type batchCall struct {
	jobs   []string
	robots []string
}

func newRecordingStore() *recordingStore {
	return &recordingStore{jobs: map[string]JobRecord{}, robots: map[string]Robot{}}
}

func (s *recordingStore) SaveJob(r JobRecord) error { s.singleJobs++; s.jobs[r.ID] = r; return nil }
func (s *recordingStore) SaveRobot(r Robot) error   { s.singleRobots++; s.robots[r.ID] = r; return nil }
func (s *recordingStore) LoadAll() ([]JobRecord, []Robot, error) {
	var js []JobRecord
	var rs []Robot
	for _, j := range s.jobs {
		js = append(js, j)
	}
	for _, r := range s.robots {
		rs = append(rs, r)
	}
	return js, rs, nil
}
func (s *recordingStore) SaveBatch(jobs []JobRecord, robots []Robot) error {
	call := batchCall{}
	for _, j := range jobs {
		call.jobs = append(call.jobs, j.ID)
		s.jobs[j.ID] = j
	}
	for _, r := range robots {
		call.robots = append(call.robots, r.ID)
		s.robots[r.ID] = r
	}
	s.batches = append(s.batches, call)
	return nil
}
func (s *recordingStore) DeleteRobot(id string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.deleted = append(s.deleted, id)
	delete(s.robots, id)
	return nil
}

// A completion changes the job, its robot and the jobs that depended on it: all of that must be
// ONE write to the store, not three or four independent ones.
func TestCompleteJob_WritesTheJobTheRobotAndTheDependentsInOneTransaction(t *testing.T) {
	store := newRecordingStore()
	e, err := NewEngineWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	e.UpsertRobot(Robot{ID: "r", Available: true})
	if err := e.AddJob(Job{ID: "first"}); err != nil {
		t.Fatal(err)
	}
	if err := e.AddJob(Job{ID: "second", DependsOn: []string{"first"}}); err != nil {
		t.Fatal(err)
	}
	e.DispatchOnce()
	singles := store.singleJobs
	if err := e.CompleteJob("first", true, "r"); err != nil {
		t.Fatal(err)
	}
	if store.singleJobs != singles {
		t.Fatalf("a completion wrote %d job(s) one by one; it must use the batch", store.singleJobs-singles)
	}
	if len(store.batches) != 1 {
		t.Fatalf("batches = %d, want exactly 1", len(store.batches))
	}
	b := store.batches[0]
	if len(b.jobs) != 2 || len(b.robots) != 1 || b.robots[0] != "r" {
		t.Fatalf("batch = %+v, want both jobs and the robot", b)
	}
	if store.jobs["second"].Status != StatusPending || store.jobs["first"].Status != StatusDone {
		t.Fatalf("stored statuses: first=%q second=%q", store.jobs["first"].Status, store.jobs["second"].Status)
	}
}

func TestCancelRequeueAndStaleSweepAreSingleTransactionsToo(t *testing.T) {
	store := newRecordingStore()
	e, _ := NewEngineWithStore(store)
	now := time.Unix(1_800_000_000, 0)
	e.now = func() time.Time { return now }
	e.UpsertRobot(Robot{ID: "r", Available: true})
	for _, id := range []string{"a", "b", "c"} {
		_ = e.AddJob(Job{ID: id})
	}
	_ = e.AddJob(Job{ID: "dep", DependsOn: []string{"c"}})
	e.DispatchOnce() // a goes to r
	before := len(store.batches)

	if err := e.CancelJob("c"); err != nil {
		t.Fatal(err)
	}
	if len(store.batches) != before+1 || len(store.batches[before].jobs) != 2 {
		t.Fatalf("cancel: batches = %+v, want one batch with c and its dependent", store.batches[before:])
	}

	now = now.Add(time.Hour)
	if got := e.DetectStaleAssignments(time.Minute); len(got) != 1 {
		t.Fatalf("stale = %v", got)
	}
	if len(store.batches) != before+2 {
		t.Fatalf("the stale sweep must write in one batch, batches = %d", len(store.batches)-before)
	}
	if err := e.RequeueJob("a", "r"); err != nil {
		t.Fatal(err)
	}
	last := store.batches[len(store.batches)-1]
	if len(last.jobs) != 1 || len(last.robots) != 1 {
		t.Fatalf("requeue batch = %+v, want the job and the robot together", last)
	}
}

// A store that does not know batches still works: the engine writes the records one by one.
func TestCompleteJob_FallsBackToSingleWritesForAStoreWithoutBatches(t *testing.T) {
	type onlyStore interface {
		Store
	}
	inner := newRecordingStore()
	var s onlyStore = struct{ Store }{inner}
	e, _ := NewEngineWithStore(s)
	e.UpsertRobot(Robot{ID: "r", Available: true})
	_ = e.AddJob(Job{ID: "x"})
	e.DispatchOnce()
	if err := e.CompleteJob("x", true, "r"); err != nil {
		t.Fatal(err)
	}
	if len(inner.batches) != 0 || inner.jobs["x"].Status != StatusDone || !inner.robots["r"].Available {
		t.Fatalf("batches=%d stored job=%q robot available=%v", len(inner.batches), inner.jobs["x"].Status, inner.robots["r"].Available)
	}
}

// A heartbeat dated after "now" (the clock stepped back since it was recorded) is no proof that
// the robot is alive.
func TestHeartbeatFromTheFuture_IsNotFreshAndMakesAnAssignmentStale(t *testing.T) {
	e := NewEngine()
	now := time.Unix(1_800_000_000, 0)
	e.now = func() time.Time { return now }
	e.SetRobotFreshness(30 * time.Second)
	e.UpsertRobot(Robot{ID: "r", Available: true})
	_ = e.AddJob(Job{ID: "x"})
	if got := e.DispatchOnce(); len(got) != 1 {
		t.Fatalf("a fresh robot must get the job: %v", got)
	}
	_ = e.CompleteJob("x", true, "r")

	// the clock steps back by an hour: the stored heartbeat is now an hour in the future
	now = now.Add(-time.Hour)
	_ = e.AddJob(Job{ID: "y"})
	if got := e.DispatchOnce(); len(got) != 0 {
		t.Fatalf("a robot whose last heartbeat is in the future must not be offered work: %v", got)
	}
	// a new heartbeat on the corrected clock makes it eligible again
	e.UpsertRobot(Robot{ID: "r", Available: true})
	if got := e.DispatchOnce(); len(got) != 1 {
		t.Fatalf("after a fresh heartbeat the robot is eligible again: %v", got)
	}

	now = now.Add(-2 * time.Hour) // and back again while y is assigned
	if got := e.DetectStaleAssignments(time.Hour); len(got) != 1 || got[0] != "y" {
		t.Fatalf("an assignment whose robot heartbeat is in the future must be stale, got %v", got)
	}
}

func TestHeartbeatSkewWithinAMinuteIsTolerated(t *testing.T) {
	e := NewEngine()
	now := time.Unix(1_800_000_000, 0)
	e.now = func() time.Time { return now }
	e.SetRobotFreshness(30 * time.Second)
	e.UpsertRobot(Robot{ID: "r", Available: true})
	now = now.Add(-10 * time.Second)
	_ = e.AddJob(Job{ID: "x"})
	if got := e.DispatchOnce(); len(got) != 1 {
		t.Fatalf("a few seconds of clock correction must not stop the dispatch: %v", got)
	}
}

func TestRemoveRobot(t *testing.T) {
	store := newRecordingStore()
	e, _ := NewEngineWithStore(store)
	e.UpsertRobot(Robot{ID: "busy", Available: true})
	e.UpsertRobot(Robot{ID: "idle", Available: true})
	_ = e.AddJob(Job{ID: "x", RequiredTool: ""})
	got := e.DispatchOnce()
	busy := got[0].RobotID
	idle := "busy"
	if busy == "busy" {
		idle = "idle"
	}
	if err := e.RemoveRobot(busy); !errors.Is(err, ErrRobotBusy) {
		t.Fatalf("removing a robot with a job = %v, want ErrRobotBusy", err)
	}
	if err := e.RemoveRobot("ghost"); !errors.Is(err, ErrUnknownRobot) {
		t.Fatalf("removing an unknown robot = %v, want ErrUnknownRobot", err)
	}
	store.deleteErr = errors.New("disk full")
	if err := e.RemoveRobot(idle); err == nil {
		t.Fatal("a failed delete must be returned")
	}
	if len(e.Robots()) != 2 {
		t.Fatal("a failed delete must leave the robot registered")
	}
	store.deleteErr = nil
	if err := e.RemoveRobot(idle); err != nil {
		t.Fatal(err)
	}
	if len(e.Robots()) != 1 || len(store.deleted) != 1 || store.deleted[0] != idle {
		t.Fatalf("robots=%v deleted=%v", e.Robots(), store.deleted)
	}
	// an Unknown job still reserves its robot: the late report needs somebody to come from
	e.now = func() time.Time { return time.Now().Add(time.Hour) }
	e.DetectStaleAssignments(time.Minute)
	if err := e.RemoveRobot(busy); !errors.Is(err, ErrRobotBusy) {
		t.Fatalf("a robot with an unknown job = %v, want ErrRobotBusy", err)
	}
}

func TestJobsPage_FiltersSkipsLimitsAndCountsTheMatches(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "r", Available: true})
	for _, id := range []string{"j1", "j2", "j3", "j4", "j5"} {
		_ = e.AddJob(Job{ID: id})
	}
	e.DispatchOnce() // j1 assigned
	_ = e.CompleteJob("j1", true, "r")
	all, total := e.JobsPage("", 0, 0)
	if len(all) != 5 || total != 5 {
		t.Fatalf("all = %d total = %d", len(all), total)
	}
	page, total := e.JobsPage("", 1, 2)
	if len(page) != 2 || page[0].ID != "j2" || page[1].ID != "j3" || total != 5 {
		t.Fatalf("page = %+v total = %d, want j2,j3 of 5", page, total)
	}
	pending, total := e.JobsPage(StatusPending, 0, 0)
	if len(pending) != 4 || total != 4 {
		t.Fatalf("pending = %d total = %d, want 4", len(pending), total)
	}
	if past, total := e.JobsPage("", 99, 3); len(past) != 0 || total != 5 {
		t.Fatalf("an offset past the end = %d jobs, total %d", len(past), total)
	}
	if !ValidStatus(StatusUnknown) || ValidStatus("bogus") {
		t.Fatal("ValidStatus is wrong")
	}
}

func TestAttempts_AreCountedPerAssignmentAndFenceLateReports(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "r", Available: true})
	_ = e.AddJob(Job{ID: "x"})
	e.DispatchOnce()
	if j, _ := e.Job("x"); j.Attempt != 1 {
		t.Fatalf("first assignment: Attempt = %d, want 1", j.Attempt)
	}
	if err := e.RequeueJob("x", "r"); err != nil {
		t.Fatal(err)
	}
	e.UpsertRobot(Robot{ID: "r", Available: true}) // the robot reports again
	e.DispatchOnce()
	if j, _ := e.Job("x"); j.Attempt != 2 {
		t.Fatalf("second assignment: Attempt = %d, want 2", j.Attempt)
	}
	if err := e.CompleteJobAttempt("x", true, "r", 1); !errors.Is(err, ErrAttemptMismatch) {
		t.Fatalf("a report for attempt 1 = %v, want ErrAttemptMismatch", err)
	}
	if j, _ := e.Job("x"); j.Status != StatusAssigned {
		t.Fatalf("a refused report must change nothing, status = %q", j.Status)
	}
	if err := e.CompleteJobAttempt("x", true, "r", 2); err != nil {
		t.Fatalf("the report for the current attempt: %v", err)
	}
}

func TestAttemptZeroMeansNotStated(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "r", Available: true})
	_ = e.AddJob(Job{ID: "x"})
	e.DispatchOnce()
	if err := e.CompleteJob("x", true, "r"); err != nil {
		t.Fatalf("CompleteJob without an attempt must keep working: %v", err)
	}
}

func TestJobTimestamps_RecordSubmissionAssignmentAndCompletion(t *testing.T) {
	e := NewEngine()
	clock := time.UnixMilli(1_800_000_000_000)
	e.now = func() time.Time { return clock }
	e.UpsertRobot(Robot{ID: "r", Available: true})
	_ = e.AddJob(Job{ID: "x"})
	j, _ := e.Job("x")
	if j.SubmittedAtMs != 1_800_000_000_000 || j.AssignedAtMs != 0 || j.CompletedAtMs != 0 {
		t.Fatalf("after submit: %+v", j)
	}
	clock = clock.Add(5 * time.Second)
	e.DispatchOnce()
	j, _ = e.Job("x")
	if j.AssignedAtMs != 1_800_000_005_000 {
		t.Fatalf("AssignedAtMs = %d", j.AssignedAtMs)
	}
	clock = clock.Add(20 * time.Second)
	_ = e.CompleteJob("x", true, "r")
	j, _ = e.Job("x")
	if j.CompletedAtMs != 1_800_000_025_000 {
		t.Fatalf("CompletedAtMs = %d", j.CompletedAtMs)
	}
	if cycle := j.CompletedAtMs - j.AssignedAtMs; cycle != 20_000 {
		t.Fatalf("cycle time = %d ms, want 20000", cycle)
	}
}
