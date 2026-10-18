// HYDRA-UMC-JOB-DISPATCHER - src/dispatcher/dispatcher_recovery_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// Fair sharing when a robot keeps failing, dependency cycles on retry, retrying a cancelled job,
// the operator requeue and partial heartbeats.
package dispatcher

import (
	"errors"
	"testing"
)

func TestFairness_AFailingRobotDoesNotAttractTheWholeQueue(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "bad", Available: true})
	e.UpsertRobot(Robot{ID: "good", Available: true})
	counts := map[string]int{}
	for _, id := range []string{"a", "b", "c", "d"} {
		if err := e.AddJob(Job{ID: id}); err != nil {
			t.Fatal(err)
		}
		got := e.DispatchOnce()
		if len(got) != 1 {
			t.Fatalf("job %s: assignments = %v", id, got)
		}
		counts[got[0].RobotID]++
		if err := e.CompleteJob(id, got[0].RobotID != "bad", got[0].RobotID); err != nil {
			t.Fatal(err)
		}
	}
	if counts["good"] == 0 {
		t.Fatalf("the good robot never worked: %v", counts)
	}
}

func TestSubmitRetry_RefusesADependencyCycle(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "r", Available: true})
	if _, _, err := e.SubmitJob(Job{ID: "x", DedupKey: "kx"}); err != nil {
		t.Fatal(err)
	}
	e.DispatchOnce()
	if err := e.CompleteJob("x", false, "r"); err != nil {
		t.Fatal(err)
	}
	if err := e.AddJob(Job{ID: "y", DependsOn: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	_, _, err := e.SubmitJob(Job{ID: "x", DedupKey: "kx", DependsOn: []string{"y"}})
	if !errors.Is(err, ErrInvalidJob) {
		t.Fatalf("retrying x with a dependency on y (which depends on x) = %v, want ErrInvalidJob", err)
	}
	if x, _ := e.Job("x"); x.Status != StatusFailed {
		t.Fatalf("x = %q after the refused retry, want failed (untouched)", x.Status)
	}
}

func TestSubmitRetry_ACancelledJobCanBeRetriedAndKeepsItsNewLocation(t *testing.T) {
	e := NewEngine()
	if _, _, err := e.SubmitJob(Job{ID: "j", DedupKey: "k", Location: "cell-1"}); err != nil {
		t.Fatal(err)
	}
	if err := e.AddJob(Job{ID: "dep", DependsOn: []string{"j"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.CancelJob("j"); err != nil {
		t.Fatal(err)
	}
	if dep, _ := e.Job("dep"); dep.Status != StatusUnreachable {
		t.Fatalf("dependent = %q, want unreachable", dep.Status)
	}
	job, result, err := e.SubmitJob(Job{ID: "j", DedupKey: "k", Location: "cell-2"})
	if err != nil || result != SubmitRetried {
		t.Fatalf("retry of a cancelled job = %v %v, want retried", result, err)
	}
	if job.Status != StatusPending || job.Location != "cell-2" {
		t.Fatalf("retried job = %+v, want pending in cell-2", job)
	}
	if dep, _ := e.Job("dep"); dep.Status != StatusBlocked {
		t.Fatalf("dependent = %q after the retry, want blocked again", dep.Status)
	}
}

func TestRequeueJob_FreesAJobOfARobotThatWillNeverReport(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "dead", Available: true})
	if err := e.AddJob(Job{ID: "j"}); err != nil {
		t.Fatal(err)
	}
	e.DispatchOnce()
	if err := e.RequeueJob("j", "someone-else"); !errors.Is(err, ErrRobotMismatch) {
		t.Fatalf("requeue naming the wrong robot = %v, want ErrRobotMismatch", err)
	}
	if err := e.RequeueJob("j", "dead"); err != nil {
		t.Fatal(err)
	}
	j, _ := e.Job("j")
	if j.Status != StatusPending || j.AssignedRobot != "" {
		t.Fatalf("job after requeue = %+v, want pending with no robot", j)
	}
	e.UpsertRobot(Robot{ID: "alive", Available: true})
	got := e.DispatchOnce()
	if len(got) != 1 || got[0].RobotID != "alive" {
		t.Fatalf("assignments = %v, want the job on the live robot, not the one that was declared down", got)
	}
	if err := e.RequeueJob("nope", ""); !errors.Is(err, ErrUnknownJob) {
		t.Fatalf("requeue of an unknown job = %v", err)
	}
	if err := e.CompleteJob("j", true, "alive"); err != nil {
		t.Fatal(err)
	}
	if err := e.RequeueJob("j", ""); !errors.Is(err, ErrJobFinished) {
		t.Fatalf("requeue of a done job = %v, want ErrJobFinished", err)
	}
	// the dead robot comes back and says so
	e.UpsertRobot(Robot{ID: "dead", Available: true})
	for _, r := range e.Robots() {
		if r.ID == "dead" && !r.Available {
			t.Fatal("a robot that heartbeats available again must be offered work again")
		}
	}
}

func TestUpdateRobot_APartialHeartbeatKeepsWhatItDoesNotName(t *testing.T) {
	e := NewEngine()
	tool, loc, yes := "PnP", "cell-1", true
	e.UpdateRobot(RobotUpdate{ID: "r", Tool: &tool, Location: &loc, Available: &yes})
	e.UpdateRobot(RobotUpdate{ID: "r"})
	r := e.Robots()[0]
	if r.Tool != "PnP" || r.Location != "cell-1" || !r.Available {
		t.Fatalf("robot after a bare heartbeat = %+v, want tool, location and availability kept", r)
	}
	// a bare heartbeat during an assignment must not flag the robot as self-unavailable
	if err := e.AddJob(Job{ID: "j"}); err != nil {
		t.Fatal(err)
	}
	e.DispatchOnce()
	e.UpdateRobot(RobotUpdate{ID: "r"})
	if err := e.CompleteJob("j", true, "r"); err != nil {
		t.Fatal(err)
	}
	if r := e.Robots()[0]; !r.Available {
		t.Fatalf("robot after finishing = %+v, want available", r)
	}
}
