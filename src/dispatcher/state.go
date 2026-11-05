// HYDRA-UMC-JOB-DISPATCHER - the state of the mission queue, as one small document
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package dispatcher

import "sort"

// StateSchema names the document of the state of the mission queue. A reader that does not know the version ignores the document.
const StateSchema = "job-dispatch-state/1"

// State is what a reader that must not fetch every job needs: how many jobs are in each status and which jobs each robot holds. It
// carries no job content (no location, no tool, no dependency).
type State struct {
	Schema string         `json:"schema"`
	Counts map[string]int `json:"counts"`
	// ByRobot lists, for each robot that holds a job, the ids of the jobs assigned to it (in submission order). A robot with none is not listed.
	ByRobot map[string][]string `json:"by_robot"`
}

// State reports the counts by status and the jobs each robot holds. It reads the queue once, under the lock.
func (e *Engine) State() State {
	e.mu.Lock()
	defer e.mu.Unlock()
	counts := map[string]int{}
	held := map[string][]*Job{}
	for _, j := range e.jobs {
		counts[string(j.Status)]++
		if j.Status == StatusAssigned && j.AssignedRobot != "" {
			held[j.AssignedRobot] = append(held[j.AssignedRobot], j)
		}
	}
	byRobot := make(map[string][]string, len(held))
	for robot, jobs := range held {
		sort.Slice(jobs, func(i, k int) bool { return jobs[i].seq < jobs[k].seq })
		ids := make([]string, 0, len(jobs))
		for _, j := range jobs {
			ids = append(ids, j.ID)
		}
		byRobot[robot] = ids
	}
	return State{Schema: StateSchema, Counts: counts, ByRobot: byRobot}
}
