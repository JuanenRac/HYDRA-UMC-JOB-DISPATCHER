// HYDRA-UMC-JOB-DISPATCHER - tests of the time estimates
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// Every test drives the engine with its own injectable clock, so the estimates are exact.
package dispatcher

import (
	"math/rand"
	"reflect"
	"testing"
	"time"
)

type etaClock struct{ t time.Time }

func (c *etaClock) get() time.Time          { return c.t }
func (c *etaClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func etaEngine(cfg ETAConfig) (*Engine, *etaClock) {
	clock := &etaClock{t: time.Unix(1_000_000, 0)}
	e := NewEngine()
	e.now = clock.get
	e.SetETA(cfg)
	return e, clock
}

func etaByID(t *testing.T, q QueueETA, id string) JobETA {
	t.Helper()
	for _, j := range q.Jobs {
		if j.JobID == id {
			return j
		}
	}
	t.Fatalf("no estimate row for job %q in %+v", id, q.Jobs)
	return JobETA{}
}

func mustJob(t *testing.T, e *Engine, j Job) {
	t.Helper()
	if err := e.AddJob(j); err != nil {
		t.Fatalf("AddJob(%q): %v", j.ID, err)
	}
}

func finish(t *testing.T, e *Engine, clock *etaClock, id, robot string, took time.Duration) {
	t.Helper()
	clock.advance(took)
	if err := e.CompleteJob(id, true, robot); err != nil {
		t.Fatalf("CompleteJob(%q): %v", id, err)
	}
}

// Characterization: with the estimates off (the default) there is nothing to read, and asking
// changes no state; with them on, asking still changes no state.
func TestETA_OffByDefaultAndReadOnly(t *testing.T) {
	e := NewEngine()
	if e.ETAEnabled() {
		t.Fatal("the estimates must be off by default")
	}
	if _, on := e.QueueETA(); on {
		t.Fatal("QueueETA must report that the estimates are off")
	}

	e, _ = etaEngine(ETAConfig{Enabled: true, DefaultDuration: time.Minute})
	e.UpsertRobot(Robot{ID: "r1", Tool: "PnP", Available: true})
	mustJob(t, e, Job{ID: "a", Priority: 1, RequiredTool: "PnP"})
	mustJob(t, e, Job{ID: "b", Priority: 5, RequiredTool: "PnP", DependsOn: []string{"a"}})
	jobsBefore, robotsBefore := e.Jobs(), e.Robots()
	if _, on := e.QueueETA(); !on {
		t.Fatal("QueueETA must answer when the estimates are on")
	}
	if !reflect.DeepEqual(jobsBefore, e.Jobs()) || !reflect.DeepEqual(robotsBefore, e.Robots()) {
		t.Fatal("asking for the estimates changed the state of the engine")
	}
}

// Characterization: switching the estimates on does not change which job a pass gives to which robot.
func TestETA_DoesNotChangeDispatch(t *testing.T) {
	run := func(on bool) []Assignment {
		e, _ := etaEngine(ETAConfig{Enabled: on, DefaultDuration: time.Minute})
		e.UpsertRobot(Robot{ID: "r1", Tool: "PnP", Available: true})
		e.UpsertRobot(Robot{ID: "r2", Tool: "PnP", Available: true})
		mustJob(t, e, Job{ID: "low", Priority: 1, RequiredTool: "PnP"})
		mustJob(t, e, Job{ID: "high", Priority: 9, RequiredTool: "PnP"})
		mustJob(t, e, Job{ID: "third", Priority: 5, RequiredTool: "PnP"})
		e.QueueETA()
		return e.DispatchOnce()
	}
	if off, on := run(false), run(true); !reflect.DeepEqual(off, on) {
		t.Fatalf("dispatch differs: off=%v on=%v", off, on)
	}
}

func TestETA_QueueOnOneRobotRunsOneAfterAnother(t *testing.T) {
	e, clock := etaEngine(ETAConfig{Enabled: true, DefaultDuration: time.Minute})
	e.UpsertRobot(Robot{ID: "r1", Tool: "PnP", Available: true})
	mustJob(t, e, Job{ID: "first", Priority: 1, RequiredTool: "PnP"})
	mustJob(t, e, Job{ID: "urgent", Priority: 9, RequiredTool: "PnP"})
	mustJob(t, e, Job{ID: "second", Priority: 1, RequiredTool: "PnP"})
	now := clock.t.UnixMilli()
	q, _ := e.QueueETA()

	want := map[string][2]int64{
		"urgent": {now, now + 60_000},
		"first":  {now + 60_000, now + 120_000},
		"second": {now + 120_000, now + 180_000},
	}
	for id, w := range want {
		got := etaByID(t, q, id)
		if !got.Estimated || got.StartMs != w[0] || got.EndMs != w[1] || got.RobotID != "r1" || got.Basis != BasisDefault {
			t.Errorf("%s: got %+v, want start %d end %d on r1 by default", id, got, w[0], w[1])
		}
	}
	if q.DrainMs != now+180_000 {
		t.Errorf("DrainMs = %d, want %d", q.DrainMs, now+180_000)
	}
}

func TestETA_TwoRobotsWorkInParallel(t *testing.T) {
	e, clock := etaEngine(ETAConfig{Enabled: true, DefaultDuration: 10 * time.Second})
	e.UpsertRobot(Robot{ID: "r1", Tool: "PnP", Available: true})
	e.UpsertRobot(Robot{ID: "r2", Tool: "PnP", Available: true})
	for _, id := range []string{"a", "b", "c"} {
		mustJob(t, e, Job{ID: id, RequiredTool: "PnP"})
	}
	now := clock.t.UnixMilli()
	q, _ := e.QueueETA()
	starts := map[string]int64{}
	for _, id := range []string{"a", "b", "c"} {
		starts[id] = etaByID(t, q, id).StartMs
	}
	if starts["a"] != now || starts["b"] != now || starts["c"] != now+10_000 {
		t.Fatalf("starts = %v, want a and b now and c ten seconds later", starts)
	}
	if q.DrainMs != now+20_000 {
		t.Errorf("DrainMs = %d, want %d", q.DrainMs, now+20_000)
	}
}

func TestETA_LearnsFromFinishedJobsOfTheSameTool(t *testing.T) {
	e, clock := etaEngine(ETAConfig{Enabled: true, DefaultDuration: time.Hour})
	e.UpsertRobot(Robot{ID: "r1", Tool: "PnP", Available: true})
	e.UpsertRobot(Robot{ID: "r2", Tool: "Laser", Available: true})
	mustJob(t, e, Job{ID: "done-pnp", RequiredTool: "PnP"})
	e.DispatchOnce()
	finish(t, e, clock, "done-pnp", "r1", 30*time.Second)

	mustJob(t, e, Job{ID: "next-pnp", RequiredTool: "PnP"})
	mustJob(t, e, Job{ID: "next-laser", RequiredTool: "Laser"})
	q, _ := e.QueueETA()
	pnp := etaByID(t, q, "next-pnp")
	if pnp.DurationMs != 30_000 || pnp.Basis != BasisObservedTool {
		t.Errorf("PnP job: %+v, want 30 s observed for the tool", pnp)
	}
	laser := etaByID(t, q, "next-laser")
	if laser.DurationMs != 30_000 || laser.Basis != BasisObservedAll {
		t.Errorf("Laser job: %+v, want the 30 s of every tool", laser)
	}
	if len(q.Observed) != 1 || q.Observed[0].Tool != "PnP" || q.Observed[0].Samples != 1 || q.Observed[0].MeanMs != 30_000 {
		t.Errorf("Observed = %+v", q.Observed)
	}
}

func TestETA_AveragesOnlyTheWindowOfRecentJobs(t *testing.T) {
	e, clock := etaEngine(ETAConfig{Enabled: true, Window: 2})
	e.UpsertRobot(Robot{ID: "r1", Tool: "PnP", Available: true})
	for i, d := range []time.Duration{100 * time.Second, 10 * time.Second, 20 * time.Second} {
		id := string(rune('a' + i))
		mustJob(t, e, Job{ID: id, RequiredTool: "PnP"})
		e.DispatchOnce()
		finish(t, e, clock, id, "r1", d)
	}
	mustJob(t, e, Job{ID: "z", RequiredTool: "PnP"})
	q, _ := e.QueueETA()
	if got := etaByID(t, q, "z").DurationMs; got != 15_000 {
		t.Fatalf("duration = %d ms, want the mean of the last two (15000)", got)
	}
}

func TestETA_AssignedJobEndsWhenItsDurationIsOverAndIsOverdueAfter(t *testing.T) {
	e, clock := etaEngine(ETAConfig{Enabled: true, DefaultDuration: time.Minute})
	e.UpsertRobot(Robot{ID: "r1", Tool: "PnP", Available: true})
	mustJob(t, e, Job{ID: "running", RequiredTool: "PnP"})
	mustJob(t, e, Job{ID: "queued", RequiredTool: "PnP"})
	e.DispatchOnce()
	assignedAt := clock.t.UnixMilli()

	clock.advance(20 * time.Second)
	q, _ := e.QueueETA()
	running := etaByID(t, q, "running")
	if running.EndMs != assignedAt+60_000 || running.Overdue || running.StartMs != assignedAt {
		t.Errorf("running = %+v", running)
	}
	queued := etaByID(t, q, "queued")
	if queued.StartMs != assignedAt+60_000 || queued.EndMs != assignedAt+120_000 {
		t.Errorf("queued = %+v, want it to start when the running job ends", queued)
	}

	clock.advance(100 * time.Second) // 120 s after the start: over the expected 60 s
	now := clock.t.UnixMilli()
	q, _ = e.QueueETA()
	running = etaByID(t, q, "running")
	if !running.Overdue || running.EndMs != now {
		t.Errorf("overdue running = %+v, want its end to be now", running)
	}
	if got := etaByID(t, q, "queued"); got.StartMs != now {
		t.Errorf("queued start = %d, want %d", got.StartMs, now)
	}
}

func TestETA_DependencyDelaysTheStartEvenWithAFreeRobot(t *testing.T) {
	e, clock := etaEngine(ETAConfig{Enabled: true, DefaultDuration: 30 * time.Second})
	e.UpsertRobot(Robot{ID: "r1", Tool: "PnP", Available: true})
	e.UpsertRobot(Robot{ID: "r2", Tool: "PnP", Available: true})
	mustJob(t, e, Job{ID: "pick", RequiredTool: "PnP"})
	mustJob(t, e, Job{ID: "place", RequiredTool: "PnP", DependsOn: []string{"pick"}})
	now := clock.t.UnixMilli()
	q, _ := e.QueueETA()
	place := etaByID(t, q, "place")
	if place.StartMs != now+30_000 || place.EndMs != now+60_000 {
		t.Fatalf("place = %+v, want it to start when pick ends", place)
	}
}

func TestETA_JobsThatCannotBeEstimatedSayWhy(t *testing.T) {
	e, _ := etaEngine(ETAConfig{Enabled: true}) // no default duration, nothing observed
	e.UpsertRobot(Robot{ID: "r1", Tool: "PnP", Available: true})
	e.UpsertRobot(Robot{ID: "busy", Tool: "Laser", Available: false})
	mustJob(t, e, Job{ID: "no-duration", RequiredTool: "PnP"})
	q, _ := e.QueueETA()
	if got := etaByID(t, q, "no-duration"); got.Estimated || got.Reason != ETANoDuration {
		t.Errorf("no-duration = %+v", got)
	}

	e.SetETA(ETAConfig{Enabled: true, DefaultDuration: time.Minute})
	mustJob(t, e, Job{ID: "no-tool", RequiredTool: "Drill"})
	mustJob(t, e, Job{ID: "no-free", RequiredTool: "Laser"})
	mustJob(t, e, Job{ID: "after-no-tool", RequiredTool: "PnP", DependsOn: []string{"no-tool"}})
	q, _ = e.QueueETA()
	if got := etaByID(t, q, "no-tool"); got.Estimated || got.Reason != ETANoRobotWithTool {
		t.Errorf("no-tool = %+v", got)
	}
	if got := etaByID(t, q, "no-free"); got.Estimated || got.Reason != ETANoRobotAvailable {
		t.Errorf("no-free = %+v", got)
	}
	if got := etaByID(t, q, "after-no-tool"); got.Estimated || got.Reason != ETADependency {
		t.Errorf("after-no-tool = %+v", got)
	}
}

func TestETA_FailedDependencyAndUnknownOutcome(t *testing.T) {
	e, clock := etaEngine(ETAConfig{Enabled: true, DefaultDuration: time.Minute})
	e.UpsertRobot(Robot{ID: "r1", Tool: "PnP", Available: true})
	mustJob(t, e, Job{ID: "pick", RequiredTool: "PnP"})
	mustJob(t, e, Job{ID: "place", RequiredTool: "PnP", DependsOn: []string{"pick"}})
	e.DispatchOnce()
	clock.advance(time.Second)
	if err := e.CompleteJob("pick", false, "r1"); err != nil {
		t.Fatal(err)
	}
	q, _ := e.QueueETA()
	for _, j := range q.Jobs {
		if j.JobID == "place" || j.JobID == "pick" {
			t.Errorf("%+v: a job that can never run (or has ended) is not part of the estimate", j)
		}
	}

	e2, clock2 := etaEngine(ETAConfig{Enabled: true, DefaultDuration: time.Minute})
	e2.SetRobotFreshness(time.Minute)
	e2.UpsertRobot(Robot{ID: "r1", Tool: "PnP", Available: true})
	mustJob(t, e2, Job{ID: "lost", RequiredTool: "PnP"})
	e2.DispatchOnce()
	clock2.advance(5 * time.Minute)
	if ids := e2.DetectStaleAssignments(time.Minute); len(ids) != 1 {
		t.Fatalf("stale = %v", ids)
	}
	q, _ = e2.QueueETA()
	if got := etaByID(t, q, "lost"); got.Estimated || got.Reason != ETAOutcomeUnknown {
		t.Errorf("lost = %+v", got)
	}
}

func TestETA_SilentRobotIsNotCountedAsFree(t *testing.T) {
	e, clock := etaEngine(ETAConfig{Enabled: true, DefaultDuration: time.Minute})
	e.SetRobotFreshness(time.Minute)
	e.UpsertRobot(Robot{ID: "r1", Tool: "PnP", Available: true})
	mustJob(t, e, Job{ID: "a", RequiredTool: "PnP"})
	clock.advance(10 * time.Minute) // r1 has been silent for far longer than the freshness window
	q, _ := e.QueueETA()
	if got := etaByID(t, q, "a"); got.Estimated || got.Reason != ETANoRobotAvailable {
		t.Fatalf("a = %+v, want no estimate while the only robot is silent", got)
	}
	e.UpsertRobot(Robot{ID: "r1", Tool: "PnP", Available: true}) // it checks in again
	q, _ = e.QueueETA()
	if got := etaByID(t, q, "a"); !got.Estimated {
		t.Fatalf("a = %+v, want an estimate once the robot is back", got)
	}
}

// Property: whatever the queue looks like, no two estimated jobs overlap on one robot, every job
// starts after the dependencies it names end, and every estimated end is start plus duration.
func TestETA_PlanNeverOverlapsAndRespectsDependencies(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	tools := []string{"PnP", "Laser", ""}
	for round := 0; round < 40; round++ {
		e, _ := etaEngine(ETAConfig{Enabled: true, DefaultDuration: time.Duration(1+rng.Intn(50)) * time.Second})
		for r := 0; r < 1+rng.Intn(3); r++ {
			e.UpsertRobot(Robot{ID: string(rune('p' + r)), Tool: tools[rng.Intn(2)], Available: true})
		}
		var ids []string
		for n := 0; n < 3+rng.Intn(8); n++ {
			id := string(rune('A' + n))
			var deps []string
			if len(ids) > 0 && rng.Intn(3) == 0 {
				deps = []string{ids[rng.Intn(len(ids))]}
			}
			mustJob(t, e, Job{ID: id, Priority: rng.Intn(4), RequiredTool: tools[rng.Intn(3)], DependsOn: deps})
			ids = append(ids, id)
		}
		q, _ := e.QueueETA()
		byRobot := map[string][]JobETA{}
		for _, j := range q.Jobs {
			if !j.Estimated {
				continue
			}
			if j.EndMs != j.StartMs+j.DurationMs {
				t.Fatalf("round %d: %+v end != start + duration", round, j)
			}
			byRobot[j.RobotID] = append(byRobot[j.RobotID], j)
			if j.EndMs > q.DrainMs {
				t.Fatalf("round %d: %+v ends after the drain time %d", round, j, q.DrainMs)
			}
		}
		for robot, list := range byRobot {
			for i := range list {
				for k := i + 1; k < len(list); k++ {
					a, b := list[i], list[k]
					if a.StartMs < b.EndMs && b.StartMs < a.EndMs {
						t.Fatalf("round %d: robot %s runs %+v and %+v at the same time", round, robot, a, b)
					}
				}
			}
		}
		for _, job := range e.Jobs() {
			me := etaByID(t, q, job.ID)
			if !me.Estimated {
				continue
			}
			for _, dep := range job.DependsOn {
				d := etaByID(t, q, dep)
				if !d.Estimated || me.StartMs < d.EndMs {
					t.Fatalf("round %d: %+v starts before its dependency %+v ends", round, me, d)
				}
			}
		}
	}
}
