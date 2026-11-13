// HYDRA-UMC-JOB-DISPATCHER - src/dispatcher/simulation_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// Simulation bench: the real engine against a simulated fleet with injected faults (robots that
// fail a job, robots that fall silent, a controlled clock) and a fixed seed. No hardware.
package dispatcher

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"
	"time"
)

type simOutcome struct {
	counts  map[string]int
	done    []string
	assigns int
}

// runFleetSimulation drives one deterministic scenario and checks the invariants of the queue at every tick.
func runFleetSimulation(t *testing.T, seed int64) simOutcome {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	e := NewEngine()
	clock := &fakeClock{t: time.Unix(1_000_000, 0)}
	e.now = clock.now
	robots := []string{"sim-r1", "sim-r2", "sim-r3", "sim-r4"}
	tools := []string{"", "PnP", "Laser"}
	for i, r := range robots {
		e.UpsertRobot(Robot{ID: r, Tool: tools[i%len(tools)], Available: true})
	}
	const jobs = 40
	for i := 0; i < jobs; i++ {
		j := Job{ID: fmt.Sprintf("sim-j%02d", i), Priority: rng.Intn(5)}
		if i > 3 && rng.Intn(3) == 0 {
			j.DependsOn = []string{fmt.Sprintf("sim-j%02d", rng.Intn(i))}
		}
		if rng.Intn(4) == 0 {
			j.RequiredTool = tools[1+rng.Intn(2)]
		}
		if err := e.AddJob(j); err != nil {
			t.Fatalf("seed %d: AddJob %s: %v", seed, j.ID, err)
		}
	}
	silent := map[string]bool{}
	assigns := 0
	for tick := 0; tick < 200; tick++ {
		clock.t = clock.t.Add(10 * time.Second)
		for _, r := range robots {
			if silent[r] {
				if rng.Intn(6) == 0 { // the robot comes back with a fresh heartbeat
					silent[r] = false
				} else {
					continue
				}
			}
			if rng.Intn(25) == 0 { // injected fault: the robot stops answering
				silent[r] = true
				continue
			}
			e.UpdateRobot(RobotUpdate{ID: r})
		}
		for _, a := range e.DispatchOnce() {
			assigns++
			job, _ := e.Job(a.JobID)
			for _, dep := range job.DependsOn {
				if d, _ := e.Job(dep); d.Status != StatusDone {
					t.Fatalf("seed %d: %s was assigned while its dependency %s is %q", seed, a.JobID, dep, d.Status)
				}
			}
			if job.RequiredTool != "" {
				robot := a.RobotID
				var have string
				for _, r := range e.Robots() {
					if r.ID == robot {
						have = r.Tool
					}
				}
				if have != job.RequiredTool {
					t.Fatalf("seed %d: %s needs %q but went to %s with %q", seed, a.JobID, job.RequiredTool, robot, have)
				}
			}
		}
		// every assigned job is reported by its robot, unless that robot is injected as silent
		for _, j := range e.Jobs() {
			if j.Status == StatusAssigned && !silent[j.AssignedRobot] && rng.Intn(3) == 0 {
				_ = e.CompleteJob(j.ID, rng.Intn(8) != 0, j.AssignedRobot)
			}
		}
		e.DetectStaleAssignments(45 * time.Second)

		st := e.State()
		total := 0
		for _, n := range st.Counts {
			total += n
		}
		if total != jobs {
			t.Fatalf("seed %d tick %d: counts %v add up to %d, want %d", seed, tick, st.Counts, total, jobs)
		}
		for robot, ids := range st.ByRobot {
			if len(ids) > 1 {
				t.Fatalf("seed %d tick %d: robot %s holds %d jobs at once: %v", seed, tick, robot, len(ids), ids)
			}
		}
	}
	out := simOutcome{counts: e.State().Counts, assigns: assigns}
	for _, j := range e.Jobs() {
		if j.Status == StatusDone {
			out.done = append(out.done, j.ID)
		}
	}
	sort.Strings(out.done)
	return out
}

func TestSimulation_FleetWithInjectedFaultsKeepsTheQueueInvariants(t *testing.T) {
	for _, seed := range []int64{1, 7, 42, 2026} {
		out := runFleetSimulation(t, seed)
		if len(out.done) == 0 {
			t.Fatalf("seed %d: nothing finished in 200 ticks: %v", seed, out.counts)
		}
		if out.assigns < len(out.done) {
			t.Fatalf("seed %d: %d jobs done from only %d assignments", seed, len(out.done), out.assigns)
		}
	}
}

func TestSimulation_SameSeedGivesTheSameOutcome(t *testing.T) {
	a := runFleetSimulation(t, 99)
	b := runFleetSimulation(t, 99)
	if fmt.Sprint(a.done) != fmt.Sprint(b.done) || a.assigns != b.assigns {
		t.Fatalf("the same seed gave different outcomes: %v/%d and %v/%d", a.done, a.assigns, b.done, b.assigns)
	}
}
