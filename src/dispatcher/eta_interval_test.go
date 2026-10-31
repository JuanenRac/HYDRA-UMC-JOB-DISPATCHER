// HYDRA-UMC-JOB-DISPATCHER - tests of the ranges of the estimates
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package dispatcher

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

var historySeq int

// history finishes one job per duration on the robot, one after another, with the injected clock.
func history(t *testing.T, e *Engine, clock *etaClock, tool string, robot string, secs ...int) {
	t.Helper()
	for _, s := range secs {
		historySeq++
		id := fmt.Sprintf("h-%d", historySeq)
		mustJob(t, e, Job{ID: id, RequiredTool: tool})
		e.DispatchOnce()
		finish(t, e, clock, id, assignedRobot(t, e, id), time.Duration(s)*time.Second)
	}
}

func TestPercentile(t *testing.T) {
	v := []int64{50, 10, 40, 20, 30}
	for p, want := range map[int]int64{0: 10, 25: 20, 50: 30, 75: 40, 100: 50} {
		if got := percentile(v, p); got != want {
			t.Errorf("p%d = %d, want %d", p, got, want)
		}
	}
	if got := percentile([]int64{10, 20}, 25); got != 12 {
		t.Errorf("interpolated p25 of [10 20] = %d, want 12 (rounded down)", got)
	}
	if got := percentile(nil, 50); got != 0 {
		t.Errorf("empty = %d", got)
	}
	if v[0] != 50 {
		t.Error("percentile changed its argument")
	}
}

func TestInterval_RangeFromTheQuartilesOfTheSameTool(t *testing.T) {
	e, clock := etaEngine(ETAConfig{Enabled: true, DefaultDuration: time.Minute, Interval: IntervalConfig{Enabled: true}})
	e.UpsertRobot(Robot{ID: "r1", Tool: "PnP", Available: true})
	history(t, e, clock, "PnP", "r1", 10, 20, 30, 40, 50) // mean 30 s, p25 20 s, p75 40 s
	mustJob(t, e, Job{ID: "x", RequiredTool: "PnP"})
	mustJob(t, e, Job{ID: "y", RequiredTool: "PnP"})
	now := clock.t.UnixMilli()
	q, _ := e.QueueETA()

	x, y := etaByID(t, q, "x"), etaByID(t, q, "y")
	if x.DurationMs != 30000 || x.DurationLowMs != 20000 || x.DurationHighMs != 40000 {
		t.Fatalf("x durations = %+v", x)
	}
	if x.EndMs != now+30000 || x.EndLowMs != now+20000 || x.EndHighMs != now+40000 {
		t.Fatalf("x ends = %+v", x)
	}
	// the second job waits for the first: the uncertainty adds up
	if y.StartMs != now+30000 || y.StartLowMs != now+20000 || y.StartHighMs != now+40000 {
		t.Fatalf("y starts = %+v", y)
	}
	if y.EndMs != now+60000 || y.EndLowMs != now+40000 || y.EndHighMs != now+80000 {
		t.Fatalf("y ends = %+v", y)
	}
	if q.DrainMs != now+60000 || q.DrainLowMs != now+40000 || q.DrainHighMs != now+80000 {
		t.Fatalf("drain = %d [%d %d]", q.DrainMs, q.DrainLowMs, q.DrainHighMs)
	}
	if len(q.Observed) != 1 || q.Observed[0].LowMs != 20000 || q.Observed[0].HighMs != 40000 {
		t.Fatalf("observed = %+v", q.Observed)
	}
	if x.IntervalReason != "" {
		t.Fatalf("a job with a range has no reason, got %q", x.IntervalReason)
	}
}

func TestInterval_WithoutEnoughHistoryItSaysWhy(t *testing.T) {
	e, clock := etaEngine(ETAConfig{Enabled: true, DefaultDuration: time.Minute, Interval: IntervalConfig{Enabled: true}})
	e.UpsertRobot(Robot{ID: "r1", Tool: "PnP", Available: true})
	e.UpsertRobot(Robot{ID: "r2", Tool: "Laser", Available: true})
	history(t, e, clock, "PnP", "r1", 10, 20) // two samples: fewer than the minimum of three
	mustJob(t, e, Job{ID: "few", RequiredTool: "PnP"})
	mustJob(t, e, Job{ID: "none", RequiredTool: "Laser"})
	q, _ := e.QueueETA()
	few, none := etaByID(t, q, "few"), etaByID(t, q, "none")
	if few.IntervalReason != IntervalTooFewSamples || few.EndLowMs != 0 || few.EndHighMs != 0 || !few.Estimated {
		t.Fatalf("few = %+v", few)
	}
	if none.IntervalReason != IntervalNoHistory || none.EndLowMs != 0 {
		t.Fatalf("none = %+v", none)
	}
	if q.DrainLowMs != 0 || q.DrainHighMs != 0 {
		t.Fatalf("no job has a range, so the queue has none: %d %d", q.DrainLowMs, q.DrainHighMs)
	}
}

func TestInterval_AJobThatCannotBeEstimatedKeepsItsOwnReason(t *testing.T) {
	e, _ := etaEngine(ETAConfig{Enabled: true, DefaultDuration: time.Minute, Interval: IntervalConfig{Enabled: true}})
	mustJob(t, e, Job{ID: "j", RequiredTool: "PnP"})
	q, _ := e.QueueETA()
	j := etaByID(t, q, "j")
	if j.Estimated || j.Reason != ETANoRobotWithTool || j.IntervalReason != "" || j.EndLowMs != 0 {
		t.Fatalf("j = %+v", j)
	}
}

func TestInterval_CustomPercentilesAndMinimum(t *testing.T) {
	e, clock := etaEngine(ETAConfig{Enabled: true, DefaultDuration: time.Minute,
		Interval: IntervalConfig{Enabled: true, LowPercentile: 10, HighPercentile: 90, MinSamples: 5}})
	e.UpsertRobot(Robot{ID: "r1", Tool: "PnP", Available: true})
	history(t, e, clock, "PnP", "r1", 10, 20, 30, 40, 50)
	mustJob(t, e, Job{ID: "x", RequiredTool: "PnP"})
	q, _ := e.QueueETA()
	x := etaByID(t, q, "x")
	if x.DurationLowMs != 14000 || x.DurationHighMs != 46000 {
		t.Fatalf("x = %+v, want 14 s and 46 s", x)
	}
}

func TestInterval_InvalidPercentilesFallBackToTheQuartiles(t *testing.T) {
	e, _ := etaEngine(ETAConfig{Enabled: true, Interval: IntervalConfig{Enabled: true, LowPercentile: 80, HighPercentile: 20}})
	if iv := e.eta.Interval; iv.LowPercentile != 25 || iv.HighPercentile != 75 || iv.MinSamples != 3 {
		t.Fatalf("interval = %+v", iv)
	}
}

func TestInterval_AnOverdueJobHasARangeThatIncludesTheSingleValue(t *testing.T) {
	e, clock := etaEngine(ETAConfig{Enabled: true, Interval: IntervalConfig{Enabled: true}})
	e.UpsertRobot(Robot{ID: "r1", Tool: "PnP", Available: true})
	history(t, e, clock, "PnP", "r1", 10, 20, 30)
	mustJob(t, e, Job{ID: "x", RequiredTool: "PnP"})
	e.DispatchOnce()
	clock.advance(10 * time.Minute)
	q, _ := e.QueueETA()
	x := etaByID(t, q, "x")
	if !x.Overdue || x.EndLowMs > x.EndMs || x.EndHighMs < x.EndMs || x.EndLowMs == 0 {
		t.Fatalf("x = %+v", x)
	}
}

// Property: for random queues the range always contains the single value, and the answer does not
// depend on the interval switch (every single value is the same with it on or off).
func TestInterval_PropertyTheRangeContainsTheSingleValueAndChangesNothingElse(t *testing.T) {
	for seed := int64(1); seed <= 30; seed++ {
		build := func(on bool) (QueueETA, *etaClock) {
			r := rand.New(rand.NewSource(seed))
			e, clock := etaEngine(ETAConfig{Enabled: true, DefaultDuration: time.Minute, Interval: IntervalConfig{Enabled: on}})
			for i := 0; i < 1+r.Intn(3); i++ {
				e.UpsertRobot(Robot{ID: fmt.Sprintf("r%d", i), Tool: []string{"PnP", "Laser"}[r.Intn(2)], Available: true})
			}
			for _, rb := range e.Robots() {
				for k := 0; k < r.Intn(7); k++ {
					history(t, e, clock, rb.Tool, rb.ID, 5+r.Intn(60))
				}
			}
			for i := 0; i < 2+r.Intn(8); i++ {
				var deps []string
				if i > 0 && r.Intn(3) == 0 {
					deps = []string{fmt.Sprintf("q%d", r.Intn(i))}
				}
				mustJob(t, e, Job{ID: fmt.Sprintf("q%d", i), Priority: r.Intn(3), RequiredTool: []string{"PnP", "Laser", ""}[r.Intn(3)], DependsOn: deps})
			}
			if r.Intn(2) == 0 {
				e.DispatchOnce()
			}
			q, _ := e.QueueETA()
			return q, clock
		}
		off, _ := build(false)
		on, _ := build(true)
		if len(off.Jobs) != len(on.Jobs) || off.DrainMs != on.DrainMs {
			t.Fatalf("seed %d: the switch changed the single values", seed)
		}
		for i, j := range on.Jobs {
			o := off.Jobs[i]
			o2 := j
			o2.StartLowMs, o2.StartHighMs, o2.EndLowMs, o2.EndHighMs, o2.DurationLowMs, o2.DurationHighMs, o2.IntervalReason = 0, 0, 0, 0, 0, 0, ""
			if o != o2 {
				t.Fatalf("seed %d: single value of %s changed: %+v vs %+v", seed, j.JobID, o, o2)
			}
			if j.EndLowMs != 0 {
				if !(j.StartLowMs <= j.StartMs && j.StartMs <= j.StartHighMs && j.EndLowMs <= j.EndMs && j.EndMs <= j.EndHighMs && j.DurationLowMs <= j.DurationMs && j.DurationMs <= j.DurationHighMs) {
					t.Fatalf("seed %d: range of %s does not contain the single value: %+v", seed, j.JobID, j)
				}
			}
			if j.Estimated && j.EndLowMs == 0 && j.IntervalReason == "" {
				t.Fatalf("seed %d: %s has neither a range nor a reason: %+v", seed, j.JobID, j)
			}
		}
	}
}
