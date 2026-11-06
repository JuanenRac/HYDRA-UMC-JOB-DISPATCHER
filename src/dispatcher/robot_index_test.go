// HYDRA-UMC-JOB-DISPATCHER - src/dispatcher/robot_index_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// The robot index must give exactly the answers of a walk over every job, whatever happened to the jobs.
package dispatcher

import (
	"fmt"
	"reflect"
	"sort"
	"testing"
)

func scanHeldBy(e *Engine, robot string) []string {
	var ids []string
	for _, j := range e.jobs {
		if j.AssignedRobot == robot {
			ids = append(ids, j.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func indexedHeldBy(e *Engine, robot string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var ids []string
	for _, j := range e.jobsHeldByLocked(robot) {
		ids = append(ids, j.ID)
	}
	sort.Strings(ids)
	return ids
}

func TestRobotIndex_AgreesWithAFullScanAfterEveryKindOfChange(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "r1", Available: true})
	e.UpsertRobot(Robot{ID: "r2", Available: true})
	for i := 0; i < 6; i++ {
		if err := e.AddJob(Job{ID: fmt.Sprintf("j%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	check := func(step string) {
		t.Helper()
		for _, r := range []string{"r1", "r2", "nobody"} {
			if got, want := indexedHeldBy(e, r), scanHeldBy(e, r); !reflect.DeepEqual(got, want) {
				t.Fatalf("%s: robot %s index %v, scan %v", step, r, got, want)
			}
		}
	}
	check("queued")
	e.DispatchOnce()
	check("dispatched")
	for _, j := range e.Jobs() {
		if j.Status == StatusAssigned {
			if err := e.CancelJob(j.ID); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	check("one cancelled")
	e.UpsertRobot(Robot{ID: "r1", Available: true})
	e.UpsertRobot(Robot{ID: "r2", Available: true})
	e.DispatchOnce()
	check("dispatched again")
	for _, r := range []string{"r1", "r2"} {
		page, total := e.JobsPageFor(r, "", 0, 0)
		if total != len(scanHeldBy(e, r)) || len(page) != total {
			t.Fatalf("JobsPageFor(%s) total %d page %d, scan %d", r, total, len(page), len(scanHeldBy(e, r)))
		}
	}
	if _, total := e.JobsPageFor("", "", 0, 0); total != 6 {
		t.Fatalf("all robots: total %d, want 6", total)
	}
}
