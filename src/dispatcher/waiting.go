// HYDRA-UMC-JOB-DISPATCHER - why pending jobs are waiting
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package dispatcher

import (
	"sort"
	"time"
)

// Reasons a pending job is not assigned at this moment (WaitingJob.Reason).
const (
	WaitNoRobots        = "no_robots_registered"
	WaitNoRobotWithTool = "no_robot_with_required_tool"
	WaitRobotsBusy      = "matching_robots_busy"
	WaitRobotsStale     = "matching_robots_not_heard_from_recently"
	WaitForItsTurn      = "robot_available_waiting_for_dispatch"
)

// WaitingJob is a pending job and the reason the next pass would, or would not, assign it.
type WaitingJob struct {
	JobID        string `json:"jobId"`
	Priority     int    `json:"priority"`
	RequiredTool string `json:"requiredTool"`
	Reason       string `json:"reason"`
}

// WaitingJobs lists every pending job in dispatch order (priority, then arrival) with the reason it has no robot now.
// It changes nothing: it is the read-only view of what DispatchOnce would leave in the queue.
func (e *Engine) WaitingJobs() []WaitingJob {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	pending := make([]*Job, 0)
	for _, j := range e.jobs {
		if j.Status == StatusPending {
			pending = append(pending, j)
		}
	}
	sort.Slice(pending, func(i, k int) bool {
		if pending[i].Priority != pending[k].Priority {
			return pending[i].Priority > pending[k].Priority
		}
		return pending[i].seq < pending[k].seq
	})
	out := make([]WaitingJob, 0, len(pending))
	for _, j := range pending {
		out = append(out, WaitingJob{JobID: j.ID, Priority: j.Priority, RequiredTool: j.RequiredTool, Reason: e.waitReasonLocked(j, now)})
	}
	return out
}

func (e *Engine) waitReasonLocked(j *Job, now time.Time) string {
	if len(e.robots) == 0 {
		return WaitNoRobots
	}
	withTool, available := 0, 0
	for _, r := range e.robots {
		if j.RequiredTool != "" && r.Tool != j.RequiredTool {
			continue
		}
		withTool++
		if r.Available {
			available++
		}
		if e.robotEligibleLocked(r, j, now) {
			return WaitForItsTurn
		}
	}
	switch {
	case withTool == 0:
		return WaitNoRobotWithTool
	case available == 0:
		return WaitRobotsBusy
	default:
		return WaitRobotsStale
	}
}
