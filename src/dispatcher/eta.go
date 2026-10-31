// HYDRA-UMC-JOB-DISPATCHER - estimated time of arrival of jobs and of the whole queue
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package dispatcher

import (
	"sort"
	"time"
)

// ETAConfig switches on and tunes the time estimates. The zero value is "off": the engine then
// behaves exactly as it did before the estimates existed.
type ETAConfig struct {
	// Enabled turns the estimates on. Off by default.
	Enabled bool
	// DefaultDuration is the duration assumed for a job while no finished job of the same tool (or
	// of any tool) has been observed yet. Zero means "no guess": such a job gets no estimate.
	DefaultDuration time.Duration
	// Window is how many of the most recent finished jobs of one tool the average is taken over
	// (0 = DefaultETAWindow).
	Window int
}

// DefaultETAWindow is the number of recent finished jobs per tool the estimate averages.
const DefaultETAWindow = 20

// How the duration of a job was obtained (JobETA.Basis).
const (
	BasisObservedTool = "observed_tool" // average of the recent finished jobs of the same tool
	BasisObservedAll  = "observed_all"  // average of the recent finished jobs of every tool
	BasisDefault      = "default"       // the configured default duration
)

// Why a job has no estimate (JobETA.Reason).
const (
	ETANoRobotWithTool  = "no_robot_with_required_tool"
	ETANoRobotAvailable = "no_robot_available"
	ETANoDuration       = "no_duration_known"
	ETADependency       = "dependency_will_not_finish"
	ETAOutcomeUnknown   = "outcome_unknown"
)

// JobETA is the estimate for one job that has not ended. Times are unix milliseconds of the
// engine's own clock.
type JobETA struct {
	JobID  string    `json:"jobId"`
	Status JobStatus `json:"status"`
	// Estimated is false when no estimate can be given; Reason then says why and the other fields are zero.
	Estimated bool   `json:"estimated"`
	Reason    string `json:"reason,omitempty"`
	// RobotID is the robot the job runs on (assigned) or is expected to run on (waiting).
	RobotID    string `json:"robotId,omitempty"`
	StartMs    int64  `json:"startMs,omitempty"`
	EndMs      int64  `json:"endMs,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
	Basis      string `json:"basis,omitempty"`
	// Overdue is true for an assigned job that has already run longer than its expected duration:
	// its end is then reported as "now".
	Overdue bool `json:"overdue,omitempty"`
}

// ToolDuration is what the engine has observed for one tool.
type ToolDuration struct {
	Tool    string `json:"tool"`
	Samples int    `json:"samples"`
	MeanMs  int64  `json:"meanMs"`
}

// QueueETA is the estimate for the whole queue.
type QueueETA struct {
	GeneratedAtMs int64 `json:"generatedAtMs"`
	// DrainMs is when the last job that has an estimate is expected to end (0 = nothing to wait for).
	DrainMs  int64          `json:"drainMs"`
	Observed []ToolDuration `json:"observed"`
	Jobs     []JobETA       `json:"jobs"`
}

// SetETA installs the configuration of the estimates (the zero value switches them off).
func (e *Engine) SetETA(c ETAConfig) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c.Window <= 0 {
		c.Window = DefaultETAWindow
	}
	if c.DefaultDuration < 0 {
		c.DefaultDuration = 0
	}
	e.eta = c
}

// ETAEnabled reports whether the estimates are switched on.
func (e *Engine) ETAEnabled() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.eta.Enabled
}

type durationSample struct {
	tool        string
	completedAt int64
	ms          int64
}

// durationModelLocked averages, per tool, the duration of the most recent finished jobs. It
// returns the means by tool, the mean over every tool and the number of samples of each tool.
func (e *Engine) durationModelLocked() (byTool map[string]int64, all int64, samples map[string]int, observed []ToolDuration) {
	window := e.eta.Window
	if window <= 0 {
		window = DefaultETAWindow
	}
	var list []durationSample
	for _, j := range e.jobs {
		if j.Status == StatusDone && j.AssignedAtMs > 0 && j.CompletedAtMs >= j.AssignedAtMs {
			list = append(list, durationSample{j.RequiredTool, j.CompletedAtMs, j.CompletedAtMs - j.AssignedAtMs})
		}
	}
	// newest first; the id-free tie-break keeps the result stable because only sums are taken
	sort.Slice(list, func(i, k int) bool { return list[i].completedAt > list[k].completedAt })
	sum := map[string]int64{}
	count := map[string]int{}
	var allSum int64
	var allCount int
	for _, s := range list {
		if count[s.tool] < window {
			sum[s.tool] += s.ms
			count[s.tool]++
		}
		if allCount < window {
			allSum += s.ms
			allCount++
		}
	}
	byTool = map[string]int64{}
	for tool, n := range count {
		byTool[tool] = sum[tool] / int64(n)
		observed = append(observed, ToolDuration{Tool: tool, Samples: n, MeanMs: byTool[tool]})
	}
	sort.Slice(observed, func(i, k int) bool { return observed[i].Tool < observed[k].Tool })
	if allCount > 0 {
		all = allSum / int64(allCount)
	}
	return byTool, all, count, observed
}

type durationModel struct {
	byTool map[string]int64
	all    int64
	def    time.Duration
}

// forTool returns the expected duration in milliseconds and how it was obtained ("" = unknown).
func (m durationModel) forTool(tool string) (int64, string) {
	if ms, ok := m.byTool[tool]; ok {
		return ms, BasisObservedTool
	}
	if m.all > 0 {
		return m.all, BasisObservedAll
	}
	if m.def > 0 {
		return m.def.Milliseconds(), BasisDefault
	}
	return 0, ""
}

// QueueETA estimates when every job that has not ended starts and ends, and when the queue is
// empty. The estimate assumes each robot works through the jobs one after another, that a waiting
// job goes to the robot that is free the earliest among those with its tool (the location
// preference is not modelled), that jobs are given out in dispatch order (priority, then arrival)
// as soon as a robot and the dependencies allow, and that a job lasts as long as the recent
// finished jobs of its tool did. It changes nothing. The second result is false while the
// estimates are switched off.
func (e *Engine) QueueETA() (QueueETA, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.eta.Enabled {
		return QueueETA{}, false
	}
	now := e.now()
	nowMs := now.UnixMilli()
	byTool, all, _, observed := e.durationModelLocked()
	model := durationModel{byTool: byTool, all: all, def: e.eta.DefaultDuration}

	out := QueueETA{GeneratedAtMs: nowMs, Observed: observed, Jobs: []JobETA{}}
	if out.Observed == nil {
		out.Observed = []ToolDuration{}
	}
	results := map[string]*JobETA{}
	var order []string
	add := func(j *Job) *JobETA {
		r := &JobETA{JobID: j.ID, Status: j.Status}
		results[j.ID] = r
		order = append(order, j.ID)
		return r
	}

	// when each robot can take its next job
	free := map[string]int64{}
	assignedOn := map[string]bool{}
	var assigned []*Job
	for _, j := range e.jobs {
		if j.Status == StatusAssigned {
			assigned = append(assigned, j)
		}
	}
	sort.Slice(assigned, func(i, k int) bool { return assigned[i].seq < assigned[k].seq })
	for _, j := range assigned {
		r := add(j)
		ms, basis := model.forTool(j.RequiredTool)
		robot := e.robots[j.AssignedRobot]
		if basis == "" {
			r.Reason = ETANoDuration
			if robot != nil {
				assignedOn[robot.ID] = true
				free[robot.ID] = nowMs
			}
			continue
		}
		end := j.AssignedAtMs + ms
		r.Estimated, r.RobotID, r.StartMs, r.DurationMs, r.Basis = true, j.AssignedRobot, j.AssignedAtMs, ms, basis
		if end < nowMs {
			end = nowMs
			r.Overdue = true
		}
		r.EndMs = end
		if robot != nil {
			assignedOn[robot.ID] = true
			if end > free[robot.ID] {
				free[robot.ID] = end
			}
		}
	}
	for id, r := range e.robots {
		if assignedOn[id] {
			continue
		}
		if r.Available && e.robotHeardFromLocked(r, now) {
			free[id] = nowMs
		}
	}
	for _, j := range e.jobs {
		if j.Status == StatusUnknown {
			r := add(j)
			r.Reason = ETAOutcomeUnknown
		}
	}

	// the jobs still waiting, in dispatch order
	var waiting []*Job
	for _, j := range e.jobs {
		if j.Status == StatusPending || j.Status == StatusBlocked {
			waiting = append(waiting, j)
		}
	}
	sort.Slice(waiting, func(i, k int) bool {
		if waiting[i].Priority != waiting[k].Priority {
			return waiting[i].Priority > waiting[k].Priority
		}
		return waiting[i].seq < waiting[k].seq
	})
	for _, j := range waiting {
		add(j)
	}
	// depReady is when the dependencies of j end; known=false means some dependency is not planned yet,
	// dead=true means one of them will never finish.
	depReady := func(j *Job) (ready int64, known, dead bool) {
		known = true
		for _, dep := range j.DependsOn {
			d, ok := e.jobs[dep]
			if !ok || d.Status == StatusDone {
				continue
			}
			switch d.Status {
			case StatusFailed, StatusUnreachable, StatusCancelled, StatusUnknown:
				return 0, false, true
			}
			r := results[dep]
			if r == nil || (!r.Estimated && r.Reason == "") {
				known = false
				continue
			}
			if !r.Estimated {
				return 0, false, true
			}
			if r.EndMs > ready {
				ready = r.EndMs
			}
		}
		return ready, known, false
	}
	planned := map[string]bool{}
	for progress := true; progress; {
		progress = false
		for _, j := range waiting {
			if planned[j.ID] {
				continue
			}
			res := results[j.ID]
			ready, known, dead := depReady(j)
			if dead {
				res.Reason = ETADependency
				planned[j.ID], progress = true, true
				continue
			}
			if !known {
				continue
			}
			planned[j.ID], progress = true, true
			ms, basis := model.forTool(j.RequiredTool)
			if basis == "" {
				res.Reason = ETANoDuration
				continue
			}
			bestID, bestStart, found, toolExists := "", int64(0), false, false
			for _, r := range e.robots {
				if j.RequiredTool != "" && r.Tool != j.RequiredTool {
					continue
				}
				toolExists = true
				f, ok := free[r.ID]
				if !ok {
					continue
				}
				start := f
				if ready > start {
					start = ready
				}
				if !found || start < bestStart || (start == bestStart && betterTie(e.robots[r.ID], e.robots[bestID])) {
					bestID, bestStart, found = r.ID, start, true
				}
			}
			if !found {
				if toolExists {
					res.Reason = ETANoRobotAvailable
				} else {
					res.Reason = ETANoRobotWithTool
				}
				continue
			}
			res.Estimated, res.RobotID, res.StartMs, res.DurationMs, res.Basis = true, bestID, bestStart, ms, basis
			res.EndMs = bestStart + ms
			free[bestID] = res.EndMs
		}
	}

	for _, id := range order {
		r := results[id]
		if r.Estimated && r.EndMs > out.DrainMs {
			out.DrainMs = r.EndMs
		}
		out.Jobs = append(out.Jobs, *r)
	}
	return out, true
}

// betterTie is the tie-break of the dispatcher itself: the lower Load, then the lower ID.
func betterTie(r, than *Robot) bool {
	return than == nil || r.Load < than.Load || (r.Load == than.Load && r.ID < than.ID)
}

// robotHeardFromLocked is the freshness part of robotEligibleLocked: true when the check is off
// or the robot checked in recently. Caller must hold e.mu.
func (e *Engine) robotHeardFromLocked(r *Robot, now time.Time) bool {
	if e.robotFreshness <= 0 {
		return true
	}
	return !(r.LastHeartbeat.IsZero() || heartbeatFromFuture(now, r.LastHeartbeat) || now.Sub(r.LastHeartbeat) > e.robotFreshness)
}
