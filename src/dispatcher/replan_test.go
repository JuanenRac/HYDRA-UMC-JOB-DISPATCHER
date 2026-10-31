// HYDRA-UMC-JOB-DISPATCHER - tests of replanning
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package dispatcher

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"time"
)

func replanEngine(cfg ReplanConfig) (*Engine, *fakeClock) {
	clock := &fakeClock{t: time.Unix(10_000, 0)}
	e := NewEngine()
	e.now = clock.now
	e.SetReplan(cfg)
	return e, clock
}

func TestReplan_OffByDefaultRefusesAndChangesNothing(t *testing.T) {
	e, _ := replanEngine(ReplanConfig{})
	e.UpsertRobot(Robot{ID: "r1", Available: true})
	mustJobPlain(t, e, Job{ID: "j"})
	e.DispatchOnce()
	if _, err := e.ReplanRobot("r1"); err != ErrReplanOff {
		t.Fatalf("err = %v, want ErrReplanOff", err)
	}
	if job, _ := e.Job("j"); job.Status != StatusAssigned {
		t.Fatalf("job = %+v, want still assigned", job)
	}
}

func TestReplan_ARobotThatFailedGivesItsJobsBackInOrder(t *testing.T) {
	e, _ := replanEngine(ReplanConfig{Enabled: true})
	e.UpsertRobot(Robot{ID: "r1", Tool: "PnP", Available: true})
	e.UpsertRobot(Robot{ID: "r2", Tool: "PnP", Available: true})
	mustJobPlain(t, e, Job{ID: "a", RequiredTool: "PnP"})
	mustJobPlain(t, e, Job{ID: "b", RequiredTool: "PnP"})
	mustJobPlain(t, e, Job{ID: "c", RequiredTool: "PnP", DependsOn: []string{"a"}})
	e.DispatchOnce()
	failing := assignedRobot(t, e, "a")
	ids, err := e.ReplanRobot(failing)
	if err != nil || !reflect.DeepEqual(ids, []string{"a"}) {
		t.Fatalf("ids = %v err = %v, want [a]", ids, err)
	}
	a, _ := e.Job("a")
	if a.Status != StatusPending || a.AssignedRobot != "" || a.Attempt != 1 {
		t.Fatalf("a = %+v, want pending, no robot, attempt kept at 1", a)
	}
	if c, _ := e.Job("c"); c.Status != StatusBlocked {
		t.Fatalf("c = %v, still blocked on a", c.Status)
	}
	// the failed robot is not offered the job again; the other one is busy with b, so a waits
	if got := e.DispatchOnce(); len(got) != 0 {
		t.Fatalf("assigned %v, want nothing: the other robot is busy and the failed one is held back", got)
	}
	// a second call finds nothing left to requeue
	if again, _ := e.ReplanRobot(failing); len(again) != 0 {
		t.Fatalf("second call requeued %v, want nothing", again)
	}
	// the failed robot checks in again and takes the job: attempt 2
	e.UpsertRobot(Robot{ID: failing, Tool: "PnP", Available: true})
	if got := e.DispatchOnce(); len(got) != 1 || got[0].JobID != "a" {
		t.Fatalf("after a heartbeat got %v, want a", got)
	}
	if a, _ = e.Job("a"); a.Attempt != 2 {
		t.Fatalf("attempt = %d, want 2", a.Attempt)
	}
}

func TestReplan_TheReportOfTheAbandonedAttemptIsRefused(t *testing.T) {
	e, _ := replanEngine(ReplanConfig{Enabled: true})
	e.UpsertRobot(Robot{ID: "r1", Available: true})
	e.UpsertRobot(Robot{ID: "r2", Available: false})
	mustJobPlain(t, e, Job{ID: "j"})
	e.DispatchOnce()
	_, _ = e.ReplanRobot("r1")
	e.UpsertRobot(Robot{ID: "r2", Available: true})
	e.DispatchOnce()
	if err := e.CompleteJobAttempt("j", true, "r1", 1); err == nil {
		t.Fatal("the late report of the abandoned attempt must be refused")
	}
	if err := e.CompleteJobAttempt("j", true, "r2", 2); err != nil {
		t.Fatalf("the report of the current attempt: %v", err)
	}
}

func TestReplan_UnknownRobotAndEmptyRobot(t *testing.T) {
	e, _ := replanEngine(ReplanConfig{Enabled: true})
	if _, err := e.ReplanRobot("ghost"); err == nil {
		t.Fatal("an unknown robot must be refused")
	}
	e.UpsertRobot(Robot{ID: "idle", Available: true})
	ids, err := e.ReplanRobot("idle")
	if err != nil || len(ids) != 0 {
		t.Fatalf("ids = %v err = %v", ids, err)
	}
	if e.Robots()[0].Available {
		t.Fatal("a robot declared failed must be held unavailable")
	}
}

func TestReplan_ASilentRobotJobsGoBackToTheQueueInsteadOfUnknown(t *testing.T) {
	e, clock := replanEngine(ReplanConfig{Enabled: true, OnStale: true})
	e.UpsertRobot(Robot{ID: "r1", Available: true})
	e.UpsertRobot(Robot{ID: "r2", Available: true})
	mustJobPlain(t, e, Job{ID: "j1"})
	mustJobPlain(t, e, Job{ID: "j2"})
	e.DispatchOnce()
	clock.t = clock.t.Add(10 * time.Minute)
	e.UpsertRobot(Robot{ID: "r2", Available: false}) // r2 is alive, r1 is not
	unknown, requeued := e.DetectStaleAssignmentsReplan(time.Minute)
	if len(unknown) != 0 || len(requeued) != 1 {
		t.Fatalf("unknown = %v requeued = %v, want one requeued and none unknown", unknown, requeued)
	}
	if job, _ := e.Job(requeued[0]); job.Status != StatusPending {
		t.Fatalf("requeued job = %+v, want pending", job)
	}
	// the old name keeps answering the unknown ones only
	if got := e.DetectStaleAssignments(time.Minute); len(got) != 0 {
		t.Fatalf("DetectStaleAssignments = %v, want none: nothing is left assigned to a silent robot", got)
	}
}

func TestReplan_StaleDetectionIsUnchangedWhenOnStaleIsOff(t *testing.T) {
	e, clock := replanEngine(ReplanConfig{Enabled: true, OnStale: false})
	e.UpsertRobot(Robot{ID: "r1", Available: true})
	mustJobPlain(t, e, Job{ID: "j"})
	e.DispatchOnce()
	clock.t = clock.t.Add(time.Hour)
	unknown, requeued := e.DetectStaleAssignmentsReplan(time.Minute)
	if !reflect.DeepEqual(unknown, []string{"j"}) || len(requeued) != 0 {
		t.Fatalf("unknown = %v requeued = %v", unknown, requeued)
	}
}

func TestReplan_AMidTaskFaultSendsTheJobBackOnlyWhenOnFaultIsOn(t *testing.T) {
	for _, on := range []bool{false, true} {
		e, _ := replanEngine(ReplanConfig{Enabled: true, OnFault: on})
		e.UpsertRobot(Robot{ID: "r1", Available: true})
		mustJobPlain(t, e, Job{ID: "j"})
		e.DispatchOnce()
		e.UpsertRobot(Robot{ID: "r1", Available: false})
		job, _ := e.Job("j")
		want := StatusAssigned
		if on {
			want = StatusPending
		}
		if job.Status != want {
			t.Fatalf("onFault=%v: job = %v, want %v", on, job.Status, want)
		}
	}
}

func TestReplan_TheEstimateIsRecalculatedWithoutTheFailedRobot(t *testing.T) {
	e, _ := etaEngine(ETAConfig{Enabled: true, DefaultDuration: time.Minute})
	e.SetReplan(ReplanConfig{Enabled: true})
	e.UpsertRobot(Robot{ID: "r1", Tool: "PnP", Available: true})
	e.UpsertRobot(Robot{ID: "r2", Tool: "PnP", Available: true})
	mustJob(t, e, Job{ID: "a", RequiredTool: "PnP"})
	mustJob(t, e, Job{ID: "b", RequiredTool: "PnP"})
	mustJob(t, e, Job{ID: "c", RequiredTool: "PnP"})
	e.DispatchOnce()
	before, _ := e.QueueETA()
	drainBefore := before.DrainMs
	failing := assignedRobot(t, e, "a")
	if _, err := e.ReplanRobot(failing); err != nil {
		t.Fatal(err)
	}
	after, _ := e.QueueETA()
	for _, j := range after.Jobs {
		if j.Estimated && j.RobotID == failing {
			t.Fatalf("job %s is still expected on the failed robot", j.JobID)
		}
	}
	if len(after.Jobs) != 3 {
		t.Fatalf("jobs = %d, want 3 (a is back in the queue)", len(after.Jobs))
	}
	if after.DrainMs <= drainBefore {
		t.Fatalf("drain %d is not later than before (%d): one robot less must take longer", after.DrainMs, drainBefore)
	}
}

// Property: under random failures, replans, completions and silences, no job is ever duplicated or
// lost, no robot holds two jobs, and a job never runs before its dependencies are done.
func TestReplan_PropertyNoJobDuplicatedLostOrOverlapped(t *testing.T) {
	for seed := int64(1); seed <= 30; seed++ {
		if a, b := runReplanSimulation(t, seed), runReplanSimulation(t, seed); !reflect.DeepEqual(a, b) {
			t.Fatalf("seed %d: two runs of the same scenario differ:\n%v\n%v", seed, a, b)
		}
	}
}

func runReplanSimulation(t *testing.T, seed int64) []string {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	e, clock := replanEngine(ReplanConfig{Enabled: true, OnStale: true, OnFault: true})
	_ = e.SetLocationCosts(LocationConfig{Enabled: true, Costs: map[string]map[string]float64{"A": {"B": 2, "C": 9}, "B": {"C": 3}}})
	tools := []string{"PnP", "Laser", ""}
	locs := []string{"A", "B", "C", ""}
	robots := 2 + rng.Intn(4)
	for i := 0; i < robots; i++ {
		e.UpsertRobot(Robot{ID: fmt.Sprintf("r%d", i), Tool: tools[rng.Intn(2)], Location: locs[rng.Intn(3)], Available: true})
	}
	total := 6 + rng.Intn(14)
	for i := 0; i < total; i++ {
		var deps []string
		for d := 0; d < i; d++ {
			if rng.Intn(6) == 0 && len(deps) < 2 {
				deps = append(deps, fmt.Sprintf("j%d", d))
			}
		}
		mustJobPlain(t, e, Job{ID: fmt.Sprintf("j%d", i), Priority: rng.Intn(4), RequiredTool: tools[rng.Intn(3)], Location: locs[rng.Intn(4)], DependsOn: deps})
	}
	var trace []string
	assigned := func() []Job {
		var out []Job
		for _, j := range e.Jobs() {
			if j.Status == StatusAssigned {
				out = append(out, j)
			}
		}
		return out
	}
	for step := 0; step < 120; step++ {
		switch rng.Intn(6) {
		case 0, 1:
			for _, a := range e.DispatchOnce() {
				trace = append(trace, "assign "+a.JobID+"->"+a.RobotID)
			}
		case 2:
			if as := assigned(); len(as) > 0 {
				j := as[rng.Intn(len(as))]
				_ = e.CompleteJob(j.ID, rng.Intn(5) != 0, j.AssignedRobot)
				trace = append(trace, "complete "+j.ID)
			}
		case 3:
			id := fmt.Sprintf("r%d", rng.Intn(robots))
			ids, err := e.ReplanRobot(id)
			if err != nil {
				t.Fatalf("seed %d: %v", seed, err)
			}
			trace = append(trace, fmt.Sprintf("replan %s %v", id, ids))
			for _, jid := range ids {
				if j, _ := e.Job(jid); j.Status == StatusAssigned || j.AssignedRobot != "" {
					t.Fatalf("seed %d: requeued job %s is still held: %+v", seed, jid, j)
				}
			}
		case 4:
			id := fmt.Sprintf("r%d", rng.Intn(robots))
			robot := Robot{ID: id}
			for _, r := range e.Robots() {
				if r.ID == id {
					robot = r
				}
			}
			robot.Available = rng.Intn(2) == 0
			e.UpsertRobot(robot)
			trace = append(trace, fmt.Sprintf("heartbeat %s %v", id, robot.Available))
		case 5:
			clock.t = clock.t.Add(time.Duration(rng.Intn(90)) * time.Second)
			for i := 0; i < robots; i++ {
				if rng.Intn(2) == 0 {
					id := fmt.Sprintf("r%d", i)
					for _, r := range e.Robots() {
						if r.ID == id {
							e.UpsertRobot(r)
						}
					}
				}
			}
			unknown, requeued := e.DetectStaleAssignmentsReplan(time.Minute)
			trace = append(trace, fmt.Sprintf("stale %v %v", unknown, requeued))
		}
		checkInvariants(t, e, seed, step, total)
	}
	return trace
}

func checkInvariants(t *testing.T, e *Engine, seed int64, step, total int) {
	t.Helper()
	jobs := e.Jobs()
	if len(jobs) != total {
		t.Fatalf("seed %d step %d: %d jobs, want %d (one was lost or duplicated)", seed, step, len(jobs), total)
	}
	seen := map[string]bool{}
	byID := map[string]Job{}
	holds := map[string][]string{}
	for _, j := range jobs {
		if seen[j.ID] {
			t.Fatalf("seed %d step %d: job %s appears twice", seed, step, j.ID)
		}
		seen[j.ID] = true
		byID[j.ID] = j
		if ((j.Status == StatusAssigned || j.Status == StatusUnknown) && j.AssignedRobot == "") || ((j.Status == StatusPending || j.Status == StatusBlocked) && j.AssignedRobot != "") {
			t.Fatalf("seed %d step %d: job %+v has a robot exactly when it should not", seed, step, j)
		}
		if j.Status == StatusAssigned {
			holds[j.AssignedRobot] = append(holds[j.AssignedRobot], j.ID)
		}
	}
	for robot, held := range holds {
		if len(held) > 1 {
			sort.Strings(held)
			t.Fatalf("seed %d step %d: robot %s holds %v at once (overlap)", seed, step, robot, held)
		}
	}
	tools := map[string]string{}
	for _, r := range e.Robots() {
		tools[r.ID] = r.Tool
	}
	for _, j := range jobs {
		if j.Status == StatusAssigned {
			for _, dep := range j.DependsOn {
				if byID[dep].Status != StatusDone {
					t.Fatalf("seed %d step %d: %s runs while %s is %s (broken dependency)", seed, step, j.ID, dep, byID[dep].Status)
				}
			}
			if j.RequiredTool != "" && tools[j.AssignedRobot] != j.RequiredTool {
				t.Fatalf("seed %d step %d: %s needs %q but runs on %s with %q", seed, step, j.ID, j.RequiredTool, j.AssignedRobot, tools[j.AssignedRobot])
			}
		}
	}
}
