// HYDRA-UMC-JOB-DISPATCHER - dispatcher package tests
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0 - see LICENSE
package dispatcher

import "testing"

func TestDispatchOnce_ToolAwareRouting(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "robot-a", Tool: "PnP", Available: true})
	e.UpsertRobot(Robot{ID: "robot-b", Tool: "Laser", Available: true})

	if err := e.AddJob(Job{ID: "job-1", Priority: 1, RequiredTool: "Laser"}); err != nil {
		t.Fatalf("AddJob: %v", err)
	}

	assignments := e.DispatchOnce()
	if len(assignments) != 1 || assignments[0].RobotID != "robot-b" {
		t.Fatalf("assignments = %+v, want job-1 routed to robot-b (the Laser-equipped robot)", assignments)
	}
}

func TestDispatchOnce_HighPriorityBypassesQueue(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "robot-a", Available: true})

	if err := e.AddJob(Job{ID: "normal-job", Priority: 1}); err != nil {
		t.Fatalf("AddJob normal-job: %v", err)
	}
	if err := e.AddJob(Job{ID: "emergency-fix", Priority: 100}); err != nil {
		t.Fatalf("AddJob emergency-fix: %v", err)
	}

	// Only one robot available - the emergency job (submitted SECOND but
	// with higher priority) must be the one that gets it.
	assignments := e.DispatchOnce()
	if len(assignments) != 1 || assignments[0].JobID != "emergency-fix" {
		t.Fatalf("assignments = %+v, want emergency-fix to bypass normal-job", assignments)
	}

	normal, _ := e.Job("normal-job")
	if normal.Status != StatusPending {
		t.Fatalf("normal-job status = %q, want still pending (no robot left for it)", normal.Status)
	}
}

func TestDispatchOnce_MultiStageDependency(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "robot-a", Available: true})

	if err := e.AddJob(Job{ID: "pick", Priority: 1}); err != nil {
		t.Fatalf("AddJob pick: %v", err)
	}
	if err := e.AddJob(Job{ID: "place", Priority: 1, DependsOn: []string{"pick"}}); err != nil {
		t.Fatalf("AddJob place: %v", err)
	}

	place, _ := e.Job("place")
	if place.Status != StatusBlocked {
		t.Fatalf("place status = %q, want blocked until pick is done", place.Status)
	}

	// "place" must not be dispatched before "pick" is done, even though
	// it's the only other pending job and a robot is free.
	assignments := e.DispatchOnce()
	if len(assignments) != 1 || assignments[0].JobID != "pick" {
		t.Fatalf("assignments = %+v, want only pick dispatched first", assignments)
	}

	if err := e.CompleteJob("pick", true); err != nil {
		t.Fatalf("CompleteJob pick: %v", err)
	}

	place, _ = e.Job("place")
	if place.Status != StatusPending {
		t.Fatalf("place status after pick done = %q, want pending (unblocked)", place.Status)
	}

	assignments = e.DispatchOnce()
	if len(assignments) != 1 || assignments[0].JobID != "place" {
		t.Fatalf("assignments = %+v, want place dispatched now that pick is done", assignments)
	}
}

func TestDispatchOnce_LoadBalancesAcrossIdleRobots(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "robot-a", Available: true})
	e.UpsertRobot(Robot{ID: "robot-b", Available: true})

	// Give robot-a a prior completion so it has Load=1; robot-b starts at 0.
	if err := e.AddJob(Job{ID: "warmup", Priority: 1}); err != nil {
		t.Fatal(err)
	}
	assignments := e.DispatchOnce()
	if err := e.CompleteJob(assignments[0].JobID, true); err != nil {
		t.Fatal(err)
	}

	if err := e.AddJob(Job{ID: "next", Priority: 1}); err != nil {
		t.Fatal(err)
	}
	assignments = e.DispatchOnce()
	if len(assignments) != 1 {
		t.Fatalf("assignments = %+v, want exactly 1", assignments)
	}
	// Whichever robot got "warmup" now has Load=1; the other (Load=0)
	// must be preferred for the next job.
	if assignments[0].RobotID == "robot-a" && assignments[0].JobID == "next" {
		robots := e.Robots()
		for _, r := range robots {
			if r.ID == "robot-a" && r.Load > 0 {
				t.Fatalf("robot-a had Load=%d but was still picked over the idle, unused robot", r.Load)
			}
		}
	}
}

func TestAddJob_RejectsDuplicateID(t *testing.T) {
	e := NewEngine()
	if err := e.AddJob(Job{ID: "job-1"}); err != nil {
		t.Fatal(err)
	}
	if err := e.AddJob(Job{ID: "job-1"}); err == nil {
		t.Fatal("expected an error for a duplicate job ID, got nil")
	}
}

func TestAddJob_RejectsUnknownDependency(t *testing.T) {
	e := NewEngine()
	if err := e.AddJob(Job{ID: "job-1", DependsOn: []string{"does-not-exist"}}); err == nil {
		t.Fatal("expected an error for a dependency on a nonexistent job, got nil")
	}
}

func TestCompleteJob_RejectsNotAssigned(t *testing.T) {
	e := NewEngine()
	if err := e.AddJob(Job{ID: "job-1"}); err != nil {
		t.Fatal(err)
	}
	// job-1 is still Pending - never dispatched - so completing it must fail.
	if err := e.CompleteJob("job-1", true); err == nil {
		t.Fatal("expected an error completing a job that was never assigned, got nil")
	}
}

func TestDispatchOnce_NoMatchingToolLeavesJobPending(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "robot-a", Tool: "PnP", Available: true})

	if err := e.AddJob(Job{ID: "job-1", RequiredTool: "Laser"}); err != nil {
		t.Fatal(err)
	}
	assignments := e.DispatchOnce()
	if len(assignments) != 0 {
		t.Fatalf("assignments = %+v, want none - no robot has the required tool", assignments)
	}
	j, _ := e.Job("job-1")
	if j.Status != StatusPending {
		t.Fatalf("job-1 status = %q, want still pending", j.Status)
	}
}
