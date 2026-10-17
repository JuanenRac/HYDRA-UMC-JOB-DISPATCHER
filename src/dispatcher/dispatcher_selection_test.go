// HYDRA-UMC-JOB-DISPATCHER - src/dispatcher/dispatcher_selection_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0 - see LICENSE
//
// Which robot is offered a job: a robot that stopped answering must not receive new work, and a
// job that names a location prefers the robots there.
package dispatcher

import (
	"testing"
	"time"
)

func assignedRobot(t *testing.T, e *Engine, jobID string) string {
	t.Helper()
	job, ok := e.Job(jobID)
	if !ok {
		t.Fatalf("job %q does not exist", jobID)
	}
	return job.AssignedRobot
}

func TestRobotFreshness_ARobotThatStoppedAnsweringIsNotOfferedNewWork(t *testing.T) {
	e := NewEngine()
	clock := &fakeClock{t: time.Unix(10_000, 0)}
	e.now = clock.now
	e.SetRobotFreshness(time.Minute)

	e.UpsertRobot(Robot{ID: "silent", Available: true})
	clock.t = clock.t.Add(5 * time.Minute) // silent for five minutes
	e.UpsertRobot(Robot{ID: "alive", Available: true, Location: "far"})

	if err := e.AddJob(Job{ID: "job-1"}); err != nil {
		t.Fatalf("AddJob: %v", err)
	}
	e.DispatchOnce()
	if got := assignedRobot(t, e, "job-1"); got != "alive" {
		t.Fatalf("job-1 went to %q, want the robot that is alive", got)
	}
}

func TestRobotFreshness_WhenEveryRobotIsSilentTheJobWaits(t *testing.T) {
	e := NewEngine()
	clock := &fakeClock{t: time.Unix(10_000, 0)}
	e.now = clock.now
	e.SetRobotFreshness(time.Minute)

	e.UpsertRobot(Robot{ID: "silent", Available: true})
	clock.t = clock.t.Add(time.Hour)
	if err := e.AddJob(Job{ID: "job-1"}); err != nil {
		t.Fatalf("AddJob: %v", err)
	}
	if got := e.DispatchOnce(); len(got) != 0 {
		t.Fatalf("DispatchOnce assigned %v, want nothing: the only robot has not answered for an hour", got)
	}
	job, _ := e.Job("job-1")
	if job.Status != StatusPending {
		t.Fatalf("job-1 Status = %q, want %q (it waits, it is not failed)", job.Status, StatusPending)
	}

	// the robot checks in again: it is eligible at once
	e.UpsertRobot(Robot{ID: "silent", Available: true})
	if got := e.DispatchOnce(); len(got) != 1 {
		t.Fatalf("after a heartbeat DispatchOnce assigned %v, want job-1", got)
	}
}

func TestRobotFreshness_DisabledByDefault(t *testing.T) {
	e := NewEngine()
	clock := &fakeClock{t: time.Unix(10_000, 0)}
	e.now = clock.now
	e.UpsertRobot(Robot{ID: "silent", Available: true})
	clock.t = clock.t.Add(24 * time.Hour)
	if err := e.AddJob(Job{ID: "job-1"}); err != nil {
		t.Fatalf("AddJob: %v", err)
	}
	if got := e.DispatchOnce(); len(got) != 1 {
		t.Fatalf("without SetRobotFreshness every available robot stays eligible, DispatchOnce assigned %v", got)
	}
}

func TestRobotFreshness_ARobotWithNoRecordedHeartbeatIsNotOfferedWorkWhileTheCheckIsOn(t *testing.T) {
	e := NewEngine()
	clock := &fakeClock{t: time.Unix(10_000, 0)}
	e.now = clock.now
	e.SetRobotFreshness(time.Minute)
	e.UpsertRobot(Robot{ID: "restored", Available: true})
	e.robots["restored"].LastHeartbeat = time.Time{} // loaded from a database that never recorded one
	if err := e.AddJob(Job{ID: "job-1"}); err != nil {
		t.Fatalf("AddJob: %v", err)
	}
	if got := e.DispatchOnce(); len(got) != 0 {
		t.Fatalf("DispatchOnce assigned %v to a robot never heard from", got)
	}
}

func TestLocation_AJobPrefersARobotInItsOwnLocationEvenWithAHigherLoad(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "a-idle-elsewhere", Available: true, Location: "cell-2"})
	e.UpsertRobot(Robot{ID: "z-busy-here", Available: true, Location: "cell-1"})
	e.robots["z-busy-here"].Load = 9

	if err := e.AddJob(Job{ID: "job-1", Location: "cell-1"}); err != nil {
		t.Fatalf("AddJob: %v", err)
	}
	e.DispatchOnce()
	if got := assignedRobot(t, e, "job-1"); got != "z-busy-here" {
		t.Fatalf("job-1 went to %q, want the robot in cell-1", got)
	}
}

func TestLocation_WhenNoRobotThereIsAvailableAnotherOneTakesTheJob(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "elsewhere", Available: true, Location: "cell-2"})
	e.UpsertRobot(Robot{ID: "here-but-busy", Available: false, Location: "cell-1"})

	if err := e.AddJob(Job{ID: "job-1", Location: "cell-1"}); err != nil {
		t.Fatalf("AddJob: %v", err)
	}
	e.DispatchOnce()
	if got := assignedRobot(t, e, "job-1"); got != "elsewhere" {
		t.Fatalf("job-1 went to %q, want the only available robot (location is a preference, not a requirement)", got)
	}
}

func TestLocation_AmongRobotsInTheLocationTheLowestLoadWins(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "r1", Available: true, Location: "cell-1"})
	e.UpsertRobot(Robot{ID: "r2", Available: true, Location: "cell-1"})
	e.robots["r1"].Load = 4
	e.robots["r2"].Load = 1
	if err := e.AddJob(Job{ID: "job-1", Location: "cell-1"}); err != nil {
		t.Fatalf("AddJob: %v", err)
	}
	e.DispatchOnce()
	if got := assignedRobot(t, e, "job-1"); got != "r2" {
		t.Fatalf("job-1 went to %q, want r2 (lower load)", got)
	}
}

func TestLocation_AJobWithoutOneKeepsTheOldBehaviour(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "r1", Available: true, Location: "cell-1"})
	e.UpsertRobot(Robot{ID: "r2", Available: true, Location: "cell-2"})
	e.robots["r1"].Load = 3
	if err := e.AddJob(Job{ID: "job-1"}); err != nil {
		t.Fatalf("AddJob: %v", err)
	}
	e.DispatchOnce()
	if got := assignedRobot(t, e, "job-1"); got != "r2" {
		t.Fatalf("job-1 went to %q, want the least loaded robot r2", got)
	}
}
