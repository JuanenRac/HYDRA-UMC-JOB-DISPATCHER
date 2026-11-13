// HYDRA-UMC-JOB-DISPATCHER - src/dispatcher/waiting_state_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// Why pending jobs wait (every reason) and the one-document state of the queue.
package dispatcher

import (
	"testing"
	"time"
)

func waitReasons(e *Engine) map[string]string {
	out := map[string]string{}
	for _, w := range e.WaitingJobs() {
		out[w.JobID] = w.Reason
	}
	return out
}

func TestWaitingJobs_NoRobotsRegistered(t *testing.T) {
	e := NewEngine()
	_ = e.AddJob(Job{ID: "a"})
	if got := waitReasons(e)["a"]; got != WaitNoRobots {
		t.Fatalf("reason = %q, want %q", got, WaitNoRobots)
	}
}

func TestWaitingJobs_NoRobotCarriesTheRequiredTool(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "r1", Tool: "PnP", Available: true})
	_ = e.AddJob(Job{ID: "a", RequiredTool: "Laser"})
	if got := waitReasons(e)["a"]; got != WaitNoRobotWithTool {
		t.Fatalf("reason = %q, want %q", got, WaitNoRobotWithTool)
	}
}

func TestWaitingJobs_MatchingRobotsBusy(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "r1", Tool: "Laser", Available: true})
	_ = e.AddJob(Job{ID: "first", RequiredTool: "Laser", Priority: 5})
	if got := e.DispatchOnce(); len(got) != 1 {
		t.Fatalf("DispatchOnce = %v, want the first job assigned", got)
	}
	_ = e.AddJob(Job{ID: "second", RequiredTool: "Laser"})
	if got := waitReasons(e)["second"]; got != WaitRobotsBusy {
		t.Fatalf("reason = %q, want %q", got, WaitRobotsBusy)
	}
}

func TestWaitingJobs_StaleRobotIsReportedAsNotHeardFrom(t *testing.T) {
	e := NewEngine()
	clock := &fakeClock{t: time.Unix(50_000, 0)}
	e.now = clock.now
	e.SetRobotFreshness(time.Minute)
	e.UpsertRobot(Robot{ID: "r1", Available: true})
	clock.t = clock.t.Add(time.Hour)
	_ = e.AddJob(Job{ID: "a"})
	if got := waitReasons(e)["a"]; got != WaitRobotsStale {
		t.Fatalf("reason = %q, want %q", got, WaitRobotsStale)
	}
}

func TestWaitingJobs_AnAvailableMatchingRobotMeansItIsOnlyItsTurn(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "r1", Available: true})
	_ = e.AddJob(Job{ID: "a"})
	if got := waitReasons(e)["a"]; got != WaitForItsTurn {
		t.Fatalf("reason = %q, want %q", got, WaitForItsTurn)
	}
}

func TestWaitingJobs_ListedByPriorityThenArrivalAndReadOnly(t *testing.T) {
	e := NewEngine()
	_ = e.AddJob(Job{ID: "low-1", Priority: 1})
	_ = e.AddJob(Job{ID: "high", Priority: 9})
	_ = e.AddJob(Job{ID: "low-2", Priority: 1})
	_ = e.AddJob(Job{ID: "mid", Priority: 5})
	want := []string{"high", "mid", "low-1", "low-2"}
	for round := 0; round < 3; round++ {
		w := e.WaitingJobs()
		if len(w) != len(want) {
			t.Fatalf("round %d: %d waiting jobs, want %d", round, len(w), len(want))
		}
		for i, id := range want {
			if w[i].JobID != id {
				t.Fatalf("round %d: position %d is %q, want %q", round, i, w[i].JobID, id)
			}
		}
	}
	for _, id := range want {
		if j, _ := e.Job(id); j.Status != StatusPending {
			t.Fatalf("WaitingJobs changed %q to %q", id, j.Status)
		}
	}
}

func TestWaitingJobs_AssignedAndFinishedJobsAreNotListed(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "r1", Available: true})
	_ = e.AddJob(Job{ID: "run", Priority: 9})
	_ = e.AddJob(Job{ID: "wait", Priority: 1})
	e.DispatchOnce()
	w := e.WaitingJobs()
	if len(w) != 1 || w[0].JobID != "wait" {
		t.Fatalf("WaitingJobs = %+v, want only \"wait\"", w)
	}
}

func TestState_EmptyQueueHasSchemaAndNoRobots(t *testing.T) {
	s := NewEngine().State()
	if s.Schema != StateSchema {
		t.Fatalf("Schema = %q, want %q", s.Schema, StateSchema)
	}
	if len(s.Counts) != 0 || len(s.ByRobot) != 0 {
		t.Fatalf("an empty queue reported %+v", s)
	}
	if s.ByRobot == nil || s.Counts == nil {
		t.Fatalf("maps must be non-nil so the document encodes as {} and not null")
	}
}

func TestState_CountsByStatusAndListsOnlyTheJobsRobotsHold(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "r1", Available: true})
	e.UpsertRobot(Robot{ID: "r2", Available: true})
	for _, id := range []string{"j1", "j2", "j3", "j4"} {
		_ = e.AddJob(Job{ID: id})
	}
	assigned := e.DispatchOnce()
	if len(assigned) != 2 {
		t.Fatalf("DispatchOnce = %v, want two assignments for two robots", assigned)
	}
	s := e.State()
	if s.Counts[string(StatusAssigned)] != 2 || s.Counts[string(StatusPending)] != 2 {
		t.Fatalf("Counts = %v, want 2 assigned and 2 pending", s.Counts)
	}
	for _, a := range assigned {
		ids := s.ByRobot[a.RobotID]
		if len(ids) != 1 || ids[0] != a.JobID {
			t.Fatalf("ByRobot[%q] = %v, want [%q]", a.RobotID, ids, a.JobID)
		}
	}
	if err := e.CompleteJob(assigned[0].JobID, true, assigned[0].RobotID); err != nil {
		t.Fatalf("CompleteJob: %v", err)
	}
	s = e.State()
	if s.Counts[string(StatusDone)] != 1 {
		t.Fatalf("Counts = %v, want one done job", s.Counts)
	}
	if _, listed := s.ByRobot[assigned[0].RobotID]; listed {
		t.Fatalf("robot %q holds nothing now and must not be listed: %v", assigned[0].RobotID, s.ByRobot)
	}
}
