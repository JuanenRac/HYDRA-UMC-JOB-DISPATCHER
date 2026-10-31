// HYDRA-UMC-JOB-DISPATCHER - characterization of dispatching, stale robots and estimates
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// These tests pin the behaviour of the engine as it is with every optional switch off (location
// costs, replanning, estimate intervals). They must keep passing unchanged when those switches exist.
package dispatcher

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// With the estimates on and nothing else switched on, the answer has exactly these fields: no interval appears.
func TestCharacterization_ETAAnswerShapeIsFixed(t *testing.T) {
	e, clock := etaEngine(ETAConfig{Enabled: true, DefaultDuration: time.Minute})
	e.UpsertRobot(Robot{ID: "r1", Tool: "PnP", Available: true})
	mustJob(t, e, Job{ID: "a", RequiredTool: "PnP"})
	mustJob(t, e, Job{ID: "b", RequiredTool: "Laser"})
	q, _ := e.QueueETA()
	raw, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	now := clock.t.UnixMilli()
	want := `{"generatedAtMs":` + itoa(now) + `,"drainMs":` + itoa(now+60000) + `,"observed":[],"jobs":[` +
		`{"jobId":"a","status":"pending","estimated":true,"robotId":"r1","startMs":` + itoa(now) + `,"endMs":` + itoa(now+60000) + `,"durationMs":60000,"basis":"default"},` +
		`{"jobId":"b","status":"pending","estimated":false,"reason":"no_robot_with_required_tool"}]}`
	if string(raw) != want {
		t.Fatalf("answer changed:\n got %s\nwant %s", raw, want)
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// The estimate does not model the location preference: the job goes to the robot that is free the earliest.
func TestCharacterization_ETAIgnoresLocation(t *testing.T) {
	e, _ := etaEngine(ETAConfig{Enabled: true, DefaultDuration: time.Minute})
	e.UpsertRobot(Robot{ID: "far", Tool: "PnP", Location: "B", Available: true})
	e.UpsertRobot(Robot{ID: "near", Tool: "PnP", Location: "A", Available: true})
	// "near" has done more work, so the load tie-break prefers "far" in the estimate.
	e.robots["near"].Load = 5
	mustJob(t, e, Job{ID: "j", RequiredTool: "PnP", Location: "A"})
	q, _ := e.QueueETA()
	if got := etaByID(t, q, "j").RobotID; got != "far" {
		t.Fatalf("estimate chose %q, want far (location is not modelled)", got)
	}
	// ... while the real dispatch prefers the robot in the job's location
	e.DispatchOnce()
	if got := assignedRobot(t, e, "j"); got != "near" {
		t.Fatalf("dispatch chose %q, want near", got)
	}
}

// A robot that goes silent leaves its job as unknown: nothing is requeued by itself.
func TestCharacterization_StaleRobotLeavesTheJobUnknown(t *testing.T) {
	e := NewEngine()
	clock := &fakeClock{t: time.Unix(10_000, 0)}
	e.now = clock.now
	e.UpsertRobot(Robot{ID: "r1", Available: true})
	mustJobPlain(t, e, Job{ID: "j"})
	e.DispatchOnce()
	clock.t = clock.t.Add(time.Hour)
	if got := e.DetectStaleAssignments(time.Minute); !reflect.DeepEqual(got, []string{"j"}) {
		t.Fatalf("stale = %v", got)
	}
	job, _ := e.Job("j")
	if job.Status != StatusUnknown || job.AssignedRobot != "r1" {
		t.Fatalf("job = %+v, want unknown on r1", job)
	}
	if got := e.DispatchOnce(); len(got) != 0 {
		t.Fatalf("an unknown job must not be dispatched again, got %v", got)
	}
}

// A robot that reports itself unavailable in the middle of a job keeps the job assigned.
func TestCharacterization_MidTaskFaultKeepsTheJobAssigned(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "r1", Available: true})
	mustJobPlain(t, e, Job{ID: "j"})
	e.DispatchOnce()
	e.UpsertRobot(Robot{ID: "r1", Available: false})
	job, _ := e.Job("j")
	if job.Status != StatusAssigned || job.AssignedRobot != "r1" {
		t.Fatalf("job = %+v, want still assigned to r1", job)
	}
}

func mustJobPlain(t *testing.T, e *Engine, j Job) {
	t.Helper()
	if err := e.AddJob(j); err != nil {
		t.Fatalf("AddJob(%q): %v", j.ID, err)
	}
}
