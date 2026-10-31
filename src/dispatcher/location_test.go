// HYDRA-UMC-JOB-DISPATCHER - tests of the location costs
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package dispatcher

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
	"time"
)

func costTable() LocationConfig {
	return LocationConfig{Enabled: true, Costs: map[string]map[string]float64{
		"A": {"B": 5, "C": 1},
		"B": {"C": 2},
	}}
}

func TestLocationCosts_TheClosestLocationWinsOverTheLowestLoad(t *testing.T) {
	e := NewEngine()
	if err := e.SetLocationCosts(costTable()); err != nil {
		t.Fatal(err)
	}
	e.UpsertRobot(Robot{ID: "inB", Location: "B", Available: true})
	e.UpsertRobot(Robot{ID: "inC", Location: "C", Available: true})
	e.robots["inC"].Load = 9 // the busier robot is closer to A
	mustJobPlain(t, e, Job{ID: "j", Location: "A"})
	e.DispatchOnce()
	if got := assignedRobot(t, e, "j"); got != "inC" {
		t.Fatalf("job went to %q, want inC (cost 1 against 5)", got)
	}
}

func TestLocationCosts_TheTableIsReadBothWays(t *testing.T) {
	c := costTable()
	if v, ok := c.cost("C", "A"); !ok || v != 1 {
		t.Fatalf("cost(C,A) = %v %v, want 1 true", v, ok)
	}
	if v, ok := c.cost("A", "A"); !ok || v != 0 {
		t.Fatalf("a location is at cost 0 from itself, got %v %v", v, ok)
	}
	if _, ok := c.cost("A", "Z"); ok {
		t.Fatal("an undeclared pair must have an unknown cost")
	}
}

func TestLocationCosts_AnUnknownCostRanksAfterEveryKnownOne(t *testing.T) {
	e := NewEngine()
	_ = e.SetLocationCosts(costTable())
	e.UpsertRobot(Robot{ID: "unknown", Location: "Z", Available: true})
	e.UpsertRobot(Robot{ID: "known", Location: "B", Available: true})
	e.robots["known"].Load = 50
	mustJobPlain(t, e, Job{ID: "j", Location: "A"})
	e.DispatchOnce()
	if got := assignedRobot(t, e, "j"); got != "known" {
		t.Fatalf("job went to %q, want known", got)
	}
}

func TestLocationCosts_TiesAreDecidedByLoadThenID(t *testing.T) {
	e := NewEngine()
	_ = e.SetLocationCosts(costTable())
	e.UpsertRobot(Robot{ID: "b2", Location: "B", Available: true})
	e.UpsertRobot(Robot{ID: "b1", Location: "B", Available: true})
	e.UpsertRobot(Robot{ID: "b0", Location: "B", Available: true})
	e.robots["b0"].Load = 3
	mustJobPlain(t, e, Job{ID: "j", Location: "A"})
	e.DispatchOnce()
	if got := assignedRobot(t, e, "j"); got != "b1" {
		t.Fatalf("job went to %q, want b1 (lowest load, then lowest ID)", got)
	}
}

func TestLocationCosts_ToolAndAvailabilityStillRule(t *testing.T) {
	e := NewEngine()
	_ = e.SetLocationCosts(costTable())
	e.UpsertRobot(Robot{ID: "near-wrong-tool", Location: "C", Tool: "Laser", Available: true})
	e.UpsertRobot(Robot{ID: "near-busy", Location: "C", Tool: "PnP", Available: false})
	e.UpsertRobot(Robot{ID: "far", Location: "B", Tool: "PnP", Available: true})
	mustJobPlain(t, e, Job{ID: "j", Location: "A", RequiredTool: "PnP"})
	e.DispatchOnce()
	if got := assignedRobot(t, e, "j"); got != "far" {
		t.Fatalf("job went to %q, want far (the only eligible robot)", got)
	}
}

// Off, or without a location in the job, the choice is the old one.
func TestLocationCosts_OffOrWithoutALocationKeepsTheOldChoice(t *testing.T) {
	for _, tc := range []struct {
		name string
		on   bool
		loc  string
	}{{"off", false, "A"}, {"on but no location", true, ""}} {
		e := NewEngine()
		_ = e.SetLocationCosts(LocationConfig{Enabled: tc.on, Costs: costTable().Costs})
		e.UpsertRobot(Robot{ID: "close", Location: "C", Available: true})
		e.UpsertRobot(Robot{ID: "idle", Location: "B", Available: true})
		e.robots["close"].Load = 4
		mustJobPlain(t, e, Job{ID: "j", Location: tc.loc})
		e.DispatchOnce()
		if got := assignedRobot(t, e, "j"); got != "idle" {
			t.Fatalf("%s: job went to %q, want idle (lowest load)", tc.name, got)
		}
	}
}

func TestLocationCosts_InvalidTableIsRefusedAndKeepsThePreviousOne(t *testing.T) {
	e := NewEngine()
	_ = e.SetLocationCosts(costTable())
	for _, v := range []float64{-1, math.NaN(), math.Inf(1)} {
		if err := e.SetLocationCosts(LocationConfig{Enabled: true, Costs: map[string]map[string]float64{"A": {"B": v}}}); err == nil {
			t.Fatalf("cost %v must be refused", v)
		}
	}
	if !e.location.Enabled || e.location.Costs["A"]["C"] != 1 {
		t.Fatal("a refused table must leave the previous one in place")
	}
}

func TestLocationCosts_TheEngineKeepsItsOwnCopy(t *testing.T) {
	e := NewEngine()
	c := costTable()
	_ = e.SetLocationCosts(c)
	c.Costs["A"]["C"] = 100
	if e.location.Costs["A"]["C"] != 1 {
		t.Fatal("changing the caller's map changed the engine")
	}
}

// The estimate now follows the preference too: among the robots free at the same time it picks the closest.
func TestLocationCosts_TheEstimateModelsThePreference(t *testing.T) {
	build := func(on bool) JobETA {
		e, _ := etaEngine(ETAConfig{Enabled: true, DefaultDuration: time.Minute})
		_ = e.SetLocationCosts(LocationConfig{Enabled: on, Costs: costTable().Costs})
		e.UpsertRobot(Robot{ID: "inB", Location: "B", Tool: "PnP", Available: true})
		e.UpsertRobot(Robot{ID: "inC", Location: "C", Tool: "PnP", Available: true})
		e.robots["inC"].Load = 7
		mustJob(t, e, Job{ID: "j", RequiredTool: "PnP", Location: "A"})
		q, _ := e.QueueETA()
		return etaByID(t, q, "j")
	}
	if got := build(false).RobotID; got != "inB" {
		t.Fatalf("off: %q, want inB (the old tie-break)", got)
	}
	if got := build(true).RobotID; got != "inC" {
		t.Fatalf("on: %q, want inC (cost 1 against 5)", got)
	}
}

// Property: with a random table the robot chosen has the minimum (known cost, load, ID) among the eligible ones.
func TestLocationCosts_PropertyTheChosenRobotIsTheMinimum(t *testing.T) {
	for seed := int64(1); seed <= 40; seed++ {
		rng := rand.New(rand.NewSource(seed))
		locs := []string{"A", "B", "C", "D", "E"}
		cfg := LocationConfig{Enabled: true, Costs: map[string]map[string]float64{}}
		for _, a := range locs {
			cfg.Costs[a] = map[string]float64{}
			for _, b := range locs {
				if a < b && rng.Intn(4) != 0 {
					cfg.Costs[a][b] = float64(rng.Intn(20))
				}
			}
		}
		e := NewEngine()
		if err := e.SetLocationCosts(cfg); err != nil {
			t.Fatal(err)
		}
		n := 3 + rng.Intn(6)
		for i := 0; i < n; i++ {
			e.UpsertRobot(Robot{ID: fmt.Sprintf("r%d", i), Location: locs[rng.Intn(len(locs))], Available: rng.Intn(4) != 0})
			e.robots[fmt.Sprintf("r%d", i)].Load = rng.Intn(4)
		}
		jobLoc := locs[rng.Intn(len(locs))]
		mustJobPlain(t, e, Job{ID: "j", Location: jobLoc})

		var want *Robot
		for _, r := range e.robots {
			if !r.Available {
				continue
			}
			if want == nil || less(cfg, jobLoc, r, want) {
				want = r
			}
		}
		e.DispatchOnce()
		got := assignedRobot(t, e, "j")
		if want == nil {
			if got != "" {
				t.Fatalf("seed %d: job assigned to %q with nobody available", seed, got)
			}
			continue
		}
		if got != want.ID {
			t.Fatalf("seed %d: job went to %q, want %q", seed, got, want.ID)
		}
	}
}

// less is an independent statement of the ordering (known cost first, then load, then ID).
func less(cfg LocationConfig, loc string, a, b *Robot) bool {
	ca, oka := cfg.cost(loc, a.Location)
	cb, okb := cfg.cost(loc, b.Location)
	if oka != okb {
		return oka
	}
	if oka && ca != cb {
		return ca < cb
	}
	if a.Load != b.Load {
		return a.Load < b.Load
	}
	return a.ID < b.ID
}
