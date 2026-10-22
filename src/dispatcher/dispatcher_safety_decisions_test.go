// HYDRA-UMC-JOB-DISPATCHER - src/dispatcher/dispatcher_safety_decisions_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// The decisions that keep work away from a robot that is gone, stale or already
// busy: completion fencing, the stale-heartbeat boundary, the operator requeue,
// dependency fall-out and what survives a store that fails halfway through a pass.
package dispatcher

import (
	"errors"
	"testing"
	"time"
)

// scriptedStore fails the n-th SaveRobot / SaveJob call (1-based, 0 = never).
type scriptedStore struct {
	saveRobotCalls, saveJobCalls int
	failRobotAt, failJobAt       int
	failAllJobsFrom              int // fail every SaveJob call from this one on (0 = never)
}

func (s *scriptedStore) SaveJob(JobRecord) error {
	s.saveJobCalls++
	if s.saveJobCalls == s.failJobAt || (s.failAllJobsFrom > 0 && s.saveJobCalls >= s.failAllJobsFrom) {
		return errors.New("disk full")
	}
	return nil
}

func (s *scriptedStore) SaveRobot(Robot) error {
	s.saveRobotCalls++
	if s.saveRobotCalls == s.failRobotAt {
		return errors.New("disk full")
	}
	return nil
}

func (s *scriptedStore) LoadAll() ([]JobRecord, []Robot, error) { return nil, nil, nil }

func engineWithAssignedJob(t *testing.T) *Engine {
	t.Helper()
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "robot-a", Available: true})
	if err := e.AddJob(Job{ID: "j1"}); err != nil {
		t.Fatal(err)
	}
	if got := e.DispatchOnce(); len(got) != 1 {
		t.Fatalf("setup dispatch = %v", got)
	}
	return e
}

func TestCompleteJob_UnknownJobAndUnknownRobotAreRefusedWithoutChangingAnything(t *testing.T) {
	e := engineWithAssignedJob(t)
	if err := e.CompleteJob("ghost", true, "robot-a"); !errors.Is(err, ErrUnknownJob) {
		t.Fatalf("unknown job: err = %v, want ErrUnknownJob", err)
	}

	delete(e.robots, "robot-a") // the registry lost the robot
	if err := e.CompleteJob("j1", true, "robot-a"); !errors.Is(err, ErrUnknownRobot) {
		t.Fatalf("unknown robot: err = %v, want ErrUnknownRobot", err)
	}
	if job, _ := e.Job("j1"); job.Status != StatusAssigned {
		t.Fatalf("job status = %q after a refused completion, want it still assigned", job.Status)
	}
}

func TestCompleteJob_AWrongRobotCannotTouchAnotherRobotsBookkeeping(t *testing.T) {
	e := engineWithAssignedJob(t)
	e.UpsertRobot(Robot{ID: "intruder", Available: false})
	if err := e.CompleteJob("j1", true, "intruder"); !errors.Is(err, ErrRobotMismatch) {
		t.Fatalf("err = %v, want ErrRobotMismatch", err)
	}
	for _, r := range e.Robots() {
		if r.Load != 0 {
			t.Errorf("robot %s Load = %d after a refused report, want 0", r.ID, r.Load)
		}
		if r.ID == "intruder" && r.Available {
			t.Error("the intruder was made available by a report it was not entitled to make")
		}
		if r.ID == "robot-a" && r.Available {
			t.Error("the real robot was released by someone else's report")
		}
	}
	if job, _ := e.Job("j1"); job.Status != StatusAssigned {
		t.Fatalf("job status = %q, want it still assigned", job.Status)
	}
}

func TestCompleteJob_AFinishedJobCannotBeFinishedTwice(t *testing.T) {
	e := engineWithAssignedJob(t)
	if err := e.CompleteJob("j1", false, "robot-a"); err != nil {
		t.Fatal(err)
	}
	if err := e.CompleteJob("j1", true, "robot-a"); !errors.Is(err, ErrJobNotAssigned) {
		t.Fatalf("second completion: err = %v, want ErrJobNotAssigned", err)
	}
	if job, _ := e.Job("j1"); job.Status != StatusFailed {
		t.Fatalf("status = %q, a later report must not turn a failure into a success", job.Status)
	}
	if r := e.Robots()[0]; r.Load != 1 {
		t.Fatalf("Load = %d, the duplicate report was counted twice", r.Load)
	}
}

func TestRequeueJob_UnknownJobIsRefused(t *testing.T) {
	e := NewEngine()
	if err := e.RequeueJob("ghost", ""); !errors.Is(err, ErrUnknownJob) {
		t.Fatalf("err = %v, want ErrUnknownJob", err)
	}
}

func TestRequeueJob_ARequeuedJobWhoseDependencyFailedIsUnreachableNotDispatchable(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "robot-a", Available: true})
	if err := e.AddJob(Job{ID: "pick"}); err != nil {
		t.Fatal(err)
	}
	if err := e.AddJob(Job{ID: "place", DependsOn: []string{"pick"}}); err != nil {
		t.Fatal(err)
	}
	// Force "place" into the assigned state while its dependency is failed, the
	// situation an operator requeue has to resolve safely.
	e.jobs["pick"].Status = StatusFailed
	e.jobs["place"].Status = StatusAssigned
	e.jobs["place"].AssignedRobot = "robot-a"

	if err := e.RequeueJob("place", "robot-a"); err != nil {
		t.Fatal(err)
	}
	if job, _ := e.Job("place"); job.Status != StatusUnreachable || job.AssignedRobot != "" {
		t.Fatalf("place = %+v, want unreachable and unassigned", job)
	}
	e.UpsertRobot(Robot{ID: "robot-b", Available: true})
	if got := e.DispatchOnce(); len(got) != 0 {
		t.Fatalf("a job whose dependency failed was dispatched: %v", got)
	}
}

func TestRequeueJob_AnUnknownStatusJobGoesBackToPendingAndTheSilentRobotIsHeldBack(t *testing.T) {
	e := NewEngine()
	clock := &fakeClock{t: time.Unix(10_000, 0)}
	e.now = clock.now
	e.UpsertRobot(Robot{ID: "robot-a", Available: true})
	if err := e.AddJob(Job{ID: "j1"}); err != nil {
		t.Fatal(err)
	}
	e.DispatchOnce()
	clock.t = clock.t.Add(time.Hour)
	if got := e.DetectStaleAssignments(time.Minute); len(got) != 1 {
		t.Fatalf("stale = %v", got)
	}
	if err := e.RequeueJob("j1", "robot-a"); err != nil {
		t.Fatal(err)
	}
	if job, _ := e.Job("j1"); job.Status != StatusPending || job.AssignedRobot != "" {
		t.Fatalf("j1 = %+v, want pending and unassigned", job)
	}
	if got := e.DispatchOnce(); len(got) != 0 {
		t.Fatalf("the job was handed straight back to the robot that stopped answering: %v", got)
	}
	// a stale caller naming the wrong robot cannot requeue
	if err := e.RequeueJob("j1", "robot-z"); err != nil {
		t.Fatalf("a pending job is returned as it is, got %v", err)
	}
}

func TestDetectStaleAssignments_OnlyAssignedJobsOfRobotsThatWentQuietAreFlagged(t *testing.T) {
	e := NewEngine()
	clock := &fakeClock{t: time.Unix(10_000, 0)}
	e.now = clock.now
	e.UpsertRobot(Robot{ID: "quiet", Available: true})
	e.UpsertRobot(Robot{ID: "chatty", Available: true})
	for _, id := range []string{"a", "b"} {
		if err := e.AddJob(Job{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	e.DispatchOnce()
	if err := e.AddJob(Job{ID: "waiting", RequiredTool: "nobody-has-this"}); err != nil {
		t.Fatal(err)
	}

	clock.t = clock.t.Add(90 * time.Second)
	e.UpsertRobot(Robot{ID: "chatty", Available: false}) // fresh heartbeat, still working
	got := e.DetectStaleAssignments(time.Minute)
	if len(got) != 1 {
		t.Fatalf("stale = %v, want exactly the quiet robot's job", got)
	}
	if staleJob, _ := e.Job(got[0]); staleJob.AssignedRobot != "quiet" || staleJob.Status != StatusUnknown {
		t.Fatalf("stale job = %+v", staleJob)
	}
	if job, _ := e.Job("waiting"); job.Status != StatusPending {
		t.Fatalf("a job nobody was given was touched: %+v", job)
	}
	// a second pass does not report the same job again
	if again := e.DetectStaleAssignments(time.Minute); len(again) != 0 {
		t.Fatalf("second pass flagged %v again", again)
	}
}

func TestDetectStaleAssignments_TheTimeoutBoundaryAndRemovedAndNeverSeenRobots(t *testing.T) {
	e := NewEngine()
	clock := &fakeClock{t: time.Unix(10_000, 0)}
	e.now = clock.now
	e.UpsertRobot(Robot{ID: "r1", Available: true})
	if err := e.AddJob(Job{ID: "j1"}); err != nil {
		t.Fatal(err)
	}
	e.DispatchOnce()

	clock.t = clock.t.Add(time.Minute - time.Nanosecond)
	if got := e.DetectStaleAssignments(time.Minute); len(got) != 0 {
		t.Fatalf("flagged %v just inside the timeout", got)
	}
	clock.t = clock.t.Add(time.Nanosecond) // exactly the timeout
	if got := e.DetectStaleAssignments(time.Minute); len(got) != 1 {
		t.Fatalf("stale = %v, a robot silent for exactly the timeout must be flagged", got)
	}

	// a robot that was removed from the registry is a separate concern: skipped
	e2 := NewEngine()
	e2.now = clock.now
	e2.UpsertRobot(Robot{ID: "gone", Available: true})
	if err := e2.AddJob(Job{ID: "j"}); err != nil {
		t.Fatal(err)
	}
	e2.DispatchOnce()
	delete(e2.robots, "gone")
	clock.t = clock.t.Add(time.Hour)
	if got := e2.DetectStaleAssignments(time.Minute); len(got) != 0 {
		t.Fatalf("flagged %v for a robot that is not in the registry", got)
	}

	// a robot never heard from since the registry was loaded is not called stale
	e3 := NewEngine()
	e3.now = clock.now
	e3.UpsertRobot(Robot{ID: "fresh-boot", Available: true})
	if err := e3.AddJob(Job{ID: "j"}); err != nil {
		t.Fatal(err)
	}
	e3.DispatchOnce()
	e3.robots["fresh-boot"].LastHeartbeat = time.Time{}
	clock.t = clock.t.Add(time.Hour)
	if got := e3.DetectStaleAssignments(time.Minute); len(got) != 0 {
		t.Fatalf("flagged %v for a robot with no heartbeat on record", got)
	}
}

func TestDetectStaleAssignments_AFailedSaveIsReportedButTheJobStillReadsUnknown(t *testing.T) {
	store := &scriptedStore{}
	e, err := NewEngineWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{t: time.Unix(10_000, 0)}
	e.now = clock.now
	e.UpsertRobot(Robot{ID: "r1", Available: true})
	if err := e.AddJob(Job{ID: "j1"}); err != nil {
		t.Fatal(err)
	}
	e.DispatchOnce()
	clock.t = clock.t.Add(time.Hour)
	store.failAllJobsFrom = store.saveJobCalls + 1

	if got := e.DetectStaleAssignments(time.Minute); len(got) != 1 {
		t.Fatalf("stale = %v", got)
	}
	if e.LastPersistError() == nil {
		t.Fatal("the failed save of the unknown state was not reported")
	}
	if job, _ := e.Job("j1"); job.Status != StatusUnknown {
		t.Fatalf("status = %q, the in-memory state must still tell the operator the outcome is unknown", job.Status)
	}
}

func TestComputeStatus_CancelledOrFailedDependencyMakesAJobUnreachableAndAMissingOneDoesNot(t *testing.T) {
	e := NewEngine()
	for _, id := range []string{"dep", "child"} {
		deps := []string(nil)
		if id == "child" {
			deps = []string{"dep"}
		}
		if err := e.AddJob(Job{ID: id, DependsOn: deps}); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		dep  JobStatus
		want JobStatus
	}{
		{StatusFailed, StatusUnreachable},
		{StatusCancelled, StatusUnreachable},
		{StatusUnreachable, StatusUnreachable},
		{StatusAssigned, StatusBlocked},
		{StatusUnknown, StatusBlocked},
		{StatusDone, StatusPending},
	} {
		e.jobs["dep"].Status = c.dep
		if got := e.computeStatus(e.jobs["child"]); got != c.want {
			t.Errorf("dependency %q -> child %q, want %q", c.dep, got, c.want)
		}
	}
	delete(e.jobs, "dep")
	if got := e.computeStatus(e.jobs["child"]); got != StatusPending {
		t.Errorf("dependency removed -> child %q, want pending", got)
	}
}

func TestRefreshBlocked_ADependentOfACancelledJobBecomesUnreachableEvenWhenTheSaveFails(t *testing.T) {
	store := &scriptedStore{}
	e, err := NewEngineWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.AddJob(Job{ID: "dep"}); err != nil {
		t.Fatal(err)
	}
	if err := e.AddJob(Job{ID: "child", DependsOn: []string{"dep"}}); err != nil {
		t.Fatal(err)
	}
	store.failAllJobsFrom = store.saveJobCalls + 2 // cancel's own save works, the dependent's does not

	if err := e.CancelJob("dep"); err != nil {
		t.Fatal(err)
	}
	if job, _ := e.Job("child"); job.Status != StatusUnreachable {
		t.Fatalf("child = %q, want unreachable", job.Status)
	}
	if e.LastPersistError() == nil {
		t.Fatal("the failed save of the dependent was swallowed")
	}
}

func TestDispatchOnce_ABrokenCompensatingWriteIsStillReportedAndNothingIsEmitted(t *testing.T) {
	// robot save #1 (reserve) works, job save fails, robot save #2 (undo) fails.
	store := &scriptedStore{}
	e, err := NewEngineWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	e.UpsertRobot(Robot{ID: "robot-a", Available: true})
	if err := e.AddJob(Job{ID: "j1"}); err != nil {
		t.Fatal(err)
	}
	store.failJobAt = store.saveJobCalls + 1
	store.failRobotAt = store.saveRobotCalls + 2

	if got := e.DispatchOnce(); len(got) != 0 {
		t.Fatalf("assignments = %v, none may be emitted when the job could not be saved", got)
	}
	if e.LastPersistError() == nil {
		t.Fatal("the failed compensating write was not reported")
	}
	if job, _ := e.Job("j1"); job.Status != StatusPending || job.AssignedRobot != "" {
		t.Fatalf("j1 = %+v, want pending and unassigned", job)
	}
	if r := e.Robots()[0]; !r.Available {
		t.Fatal("the robot stayed reserved in memory for a job that never started")
	}
}

func TestDispatchOnce_NeverGivesAJobToABusyOrQuietRobotOrOneWithoutTheTool(t *testing.T) {
	e := NewEngine()
	clock := &fakeClock{t: time.Unix(10_000, 0)}
	e.now = clock.now
	e.SetRobotFreshness(time.Minute)
	e.UpsertRobot(Robot{ID: "busy", Available: false, Tool: "PnP"})
	e.UpsertRobot(Robot{ID: "wrong-tool", Available: true, Tool: "Laser"})
	e.UpsertRobot(Robot{ID: "quiet", Available: true, Tool: "PnP"})
	clock.t = clock.t.Add(10 * time.Minute)
	if err := e.AddJob(Job{ID: "j1", RequiredTool: "PnP"}); err != nil {
		t.Fatal(err)
	}
	if got := e.DispatchOnce(); len(got) != 0 {
		t.Fatalf("job went to %v although no robot qualifies", got)
	}
	e.UpsertRobot(Robot{ID: "ok", Available: true, Tool: "PnP"})
	got := e.DispatchOnce()
	if len(got) != 1 || got[0].RobotID != "ok" {
		t.Fatalf("assignments = %v, want the one qualified robot", got)
	}
}
