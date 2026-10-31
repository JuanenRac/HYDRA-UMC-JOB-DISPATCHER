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
	// Interval adds a range to every estimate (see IntervalConfig). Off by default.
	Interval IntervalConfig
}

// IntervalConfig adds a range next to the single value of every estimate: the spread between a
// low and a high percentile of the durations of the recent finished jobs of the same tool. The
// zero value is "off": the answer then has no range at all, exactly as before.
type IntervalConfig struct {
	// Enabled turns the ranges on. Off by default.
	Enabled bool
	// LowPercentile and HighPercentile bound the range, from 0 to 100, low below high (both 0 =
	// 25 and 75, the quartiles).
	LowPercentile  int
	HighPercentile int
	// MinSamples is how many finished jobs of the tool are needed before a range is given (0 = 3):
	// with fewer, the range would be an invention, so the estimate says why it has none.
	MinSamples int
}

// DefaultIntervalLow, DefaultIntervalHigh and DefaultIntervalMinSamples fill the zero fields of IntervalConfig.
const (
	DefaultIntervalLow        = 25
	DefaultIntervalHigh       = 75
	DefaultIntervalMinSamples = 3
)

// Valid reports whether the percentiles make a range (0 <= low < high <= 100, MinSamples not
// negative). The zero fields count as their defaults.
func (c IntervalConfig) Valid() bool {
	c = c.withDefaults()
	return c.LowPercentile >= 0 && c.LowPercentile < c.HighPercentile && c.HighPercentile <= 100 && c.MinSamples >= 1
}

func (c IntervalConfig) withDefaults() IntervalConfig {
	if c.LowPercentile == 0 && c.HighPercentile == 0 {
		c.LowPercentile, c.HighPercentile = DefaultIntervalLow, DefaultIntervalHigh
	}
	if c.MinSamples == 0 {
		c.MinSamples = DefaultIntervalMinSamples
	}
	return c
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

// Why an estimate has no range (JobETA.IntervalReason).
const (
	IntervalTooFewSamples = "too_few_samples"     // the tool has finished fewer jobs than the minimum
	IntervalNoHistory     = "no_history_for_tool" // the duration is the all-tools average or the default, not the tool's own
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
	// The range, only with the intervals on and enough history of the tool: the start, the end and
	// the duration at the low and at the high percentile (never outside the single value above).
	// IntervalReason says why an estimate has no range.
	StartLowMs     int64  `json:"startLowMs,omitempty"`
	StartHighMs    int64  `json:"startHighMs,omitempty"`
	EndLowMs       int64  `json:"endLowMs,omitempty"`
	EndHighMs      int64  `json:"endHighMs,omitempty"`
	DurationLowMs  int64  `json:"durationLowMs,omitempty"`
	DurationHighMs int64  `json:"durationHighMs,omitempty"`
	IntervalReason string `json:"intervalReason,omitempty"`
}

// ToolDuration is what the engine has observed for one tool.
type ToolDuration struct {
	Tool    string `json:"tool"`
	Samples int    `json:"samples"`
	MeanMs  int64  `json:"meanMs"`
	// LowMs and HighMs are the percentiles of the intervals, only when they are on and the tool has enough samples.
	LowMs  int64 `json:"lowMs,omitempty"`
	HighMs int64 `json:"highMs,omitempty"`
}

// QueueETA is the estimate for the whole queue.
type QueueETA struct {
	GeneratedAtMs int64 `json:"generatedAtMs"`
	// DrainMs is when the last job that has an estimate is expected to end (0 = nothing to wait for).
	DrainMs int64 `json:"drainMs"`
	// DrainLowMs and DrainHighMs are the range of DrainMs, only with the intervals on and at least one job with a range.
	DrainLowMs  int64          `json:"drainLowMs,omitempty"`
	DrainHighMs int64          `json:"drainHighMs,omitempty"`
	Observed    []ToolDuration `json:"observed"`
	Jobs        []JobETA       `json:"jobs"`
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
	if c.Interval.Enabled {
		c.Interval = c.Interval.withDefaults()
		if !c.Interval.Valid() {
			c.Interval = IntervalConfig{Enabled: true}.withDefaults()
		}
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
func (e *Engine) durationModelLocked() (byTool map[string]int64, all int64, samples map[string]int, observed []ToolDuration, window map[string][]int64) {
	size := e.eta.Window
	if size <= 0 {
		size = DefaultETAWindow
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
	window = map[string][]int64{}
	count := map[string]int{}
	var allSum int64
	var allCount int
	for _, s := range list {
		if count[s.tool] < size {
			sum[s.tool] += s.ms
			count[s.tool]++
			window[s.tool] = append(window[s.tool], s.ms)
		}
		if allCount < size {
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
	return byTool, all, count, observed, window
}

// percentile returns the p-th percentile (0..100) of the values, by linear interpolation between
// the two closest ranks, rounded down to a whole millisecond. It does not change its argument.
func percentile(values []int64, p int) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, k int) bool { return sorted[i] < sorted[k] })
	pos := float64(p) / 100 * float64(len(sorted)-1)
	lo := int(pos)
	if lo >= len(sorted)-1 {
		return sorted[len(sorted)-1]
	}
	frac := pos - float64(lo)
	return sorted[lo] + int64(frac*float64(sorted[lo+1]-sorted[lo]))
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
	byTool, all, counts, observed, window := e.durationModelLocked()
	model := durationModel{byTool: byTool, all: all, def: e.eta.DefaultDuration}

	out := QueueETA{GeneratedAtMs: nowMs, Observed: observed, Jobs: []JobETA{}}
	if out.Observed == nil {
		out.Observed = []ToolDuration{}
	}
	results, order := e.planLocked(model, now)

	var lowRes, highRes map[string]*JobETA
	iv := e.eta.Interval
	if iv.Enabled {
		// the same plan twice more, with the tools that have enough history at their low and at their high percentile
		lowModel := durationModel{byTool: map[string]int64{}, all: all, def: model.def}
		highModel := durationModel{byTool: map[string]int64{}, all: all, def: model.def}
		for tool, mean := range byTool {
			lowModel.byTool[tool], highModel.byTool[tool] = mean, mean
			if counts[tool] >= iv.MinSamples {
				lowModel.byTool[tool] = percentile(window[tool], iv.LowPercentile)
				highModel.byTool[tool] = percentile(window[tool], iv.HighPercentile)
			}
		}
		for i := range out.Observed {
			if tool := out.Observed[i].Tool; counts[tool] >= iv.MinSamples {
				out.Observed[i].LowMs, out.Observed[i].HighMs = lowModel.byTool[tool], highModel.byTool[tool]
			}
		}
		lowRes, _ = e.planLocked(lowModel, now)
		highRes, _ = e.planLocked(highModel, now)
	}

	for _, id := range order {
		r := results[id]
		if r.Estimated && r.EndMs > out.DrainMs {
			out.DrainMs = r.EndMs
		}
		if iv.Enabled && r.Estimated {
			e.attachIntervalLocked(r, lowRes[id], highRes[id], counts)
			if r.EndLowMs > 0 {
				if out.DrainLowMs == 0 || r.EndLowMs > out.DrainLowMs {
					out.DrainLowMs = r.EndLowMs
				}
				if r.EndHighMs > out.DrainHighMs {
					out.DrainHighMs = r.EndHighMs
				}
			}
		}
		out.Jobs = append(out.Jobs, *r)
	}
	return out, true
}

// attachIntervalLocked writes the range of one estimate from the same plan run with the low and
// the high durations, or says why there is none. The range never excludes the single value: where
// a different robot choice in the low or high plan would put a time outside it, the single value
// is the bound.
func (e *Engine) attachIntervalLocked(r, low, high *JobETA, counts map[string]int) {
	job := e.jobs[r.JobID]
	if job == nil {
		return
	}
	switch {
	case r.Basis != BasisObservedTool:
		r.IntervalReason = IntervalNoHistory
		return
	case counts[job.RequiredTool] < e.eta.Interval.MinSamples:
		r.IntervalReason = IntervalTooFewSamples
		return
	case low == nil || high == nil || !low.Estimated || !high.Estimated:
		r.IntervalReason = IntervalNoHistory
		return
	}
	r.StartLowMs, r.StartHighMs = minInt64(low.StartMs, r.StartMs), maxInt64(high.StartMs, r.StartMs)
	r.EndLowMs, r.EndHighMs = minInt64(low.EndMs, r.EndMs), maxInt64(high.EndMs, r.EndMs)
	r.DurationLowMs, r.DurationHighMs = minInt64(low.DurationMs, r.DurationMs), maxInt64(high.DurationMs, r.DurationMs)
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// planLocked lays the unfinished jobs out in time with the given durations: the whole of the
// estimate except the ranges. It changes nothing. Caller must hold e.mu.
func (e *Engine) planLocked(model durationModel, now time.Time) (map[string]*JobETA, []string) {
	nowMs := now.UnixMilli()
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
				if !found || start < bestStart || (start == bestStart && e.preferLocked(j, e.robots[r.ID], e.robots[bestID])) {
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

	return results, order
}

// robotHeardFromLocked is the freshness part of robotEligibleLocked: true when the check is off
// or the robot checked in recently. Caller must hold e.mu.
func (e *Engine) robotHeardFromLocked(r *Robot, now time.Time) bool {
	if e.robotFreshness <= 0 {
		return true
	}
	return !(r.LastHeartbeat.IsZero() || heartbeatFromFuture(now, r.LastHeartbeat) || now.Sub(r.LastHeartbeat) > e.robotFreshness)
}
