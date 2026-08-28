// HYDRA-UMC-JOB-DISPATCHER - dispatcher package
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0 - see LICENSE
//
// The real priority mission queue described in the README's "DISPATCHER
// FLOW" diagram: a global job queue, tool-aware routing against a robot
// registry, and multi-stage mission dependencies (a job only becomes
// eligible once every job it DependsOn is Done). Deliberately in-memory
// only (no Redis/DB persistence yet) - see the package doc on Engine for
// why that's an honest v0 rather than a missing feature.
package dispatcher

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// JobStatus is the lifecycle state of a Job.
type JobStatus string

const (
	StatusPending  JobStatus = "pending"  // submitted, not yet dispatchable or not yet assigned
	StatusBlocked  JobStatus = "blocked"  // pending, but waiting on an unfinished dependency
	StatusAssigned JobStatus = "assigned" // dispatched to a robot, work in progress
	StatusDone     JobStatus = "done"
	StatusFailed   JobStatus = "failed"
)

// Job is one unit of work in the global mission queue.
type Job struct {
	ID           string
	Priority     int      // higher runs first - an "Emergency Defect Fix" gets a high value to bypass normal flow
	RequiredTool string   // must match a Robot's Tool exactly, e.g. "PnP", "Laser" (empty = any robot)
	DependsOn    []string // job IDs that must reach StatusDone before this one is eligible
	Status       JobStatus
	AssignedRobot string

	// DedupKey identifies the logical unit of work behind this submission
	// (e.g. a client-generated request ID), independent of Job.ID. Empty
	// means "no deduplication requested" - AddJob's plain ID-collision
	// check is the only guard, unchanged. See SubmitJob.
	DedupKey string

	seq int64 // internal submission order, used only as a stable tie-breaker
}

// SubmitResult reports what SubmitJob actually did with a submission.
type SubmitResult string

const (
	SubmitCreated   SubmitResult = "created"   // no matching DedupKey on record - a brand new job was added
	SubmitDuplicate SubmitResult = "duplicate" // an in-flight or already-done job with this DedupKey exists - returned untouched
	SubmitRetried   SubmitResult = "retried"   // a previously Failed job with this DedupKey was reset to Pending for a fresh attempt
)

// Robot is one entry in the fleet registry this engine dispatches against.
type Robot struct {
	ID        string
	Location  string
	Tool      string // currently attached URTC tool head, e.g. "PnP", "Laser", "" if none
	Available bool   // false while it is executing an assigned job
	Load      int    // completed-job counter this session, used to balance ties (lower = preferred)
}

// Assignment is the result of matching one eligible Job to one available Robot.
type Assignment struct {
	JobID   string
	RobotID string
}

// Engine holds all queue/registry state and implements the scheduling
// algorithm. Safe for concurrent use.
//
// Why in-memory only, not Redis/a real database yet: the README's
// "Persistence: fault-tolerant mission state using local Redis/Database
// storage" is real future work, not forgotten - but wiring a specific
// store before the scheduling algorithm itself was proven correct would
// mean debugging both at once. Engine's own state is deliberately kept
// behind exported methods only (no exported map field) so a persistent
// backing store can be added later by changing what's behind those
// methods, not by changing every caller.
type Engine struct {
	mu         sync.Mutex
	jobs       map[string]*Job
	robots     map[string]*Robot
	nextSeq    int64
	dedupIndex map[string]string // DedupKey -> JobID, only for jobs submitted with a non-empty DedupKey
}

// NewEngine returns an empty, ready-to-use Engine.
func NewEngine() *Engine {
	return &Engine{
		jobs:       make(map[string]*Job),
		robots:     make(map[string]*Robot),
		dedupIndex: make(map[string]string),
	}
}

var (
	ErrJobExists       = errors.New("job ID already exists")
	ErrUnknownDep      = errors.New("dependency job ID does not exist")
	ErrUnknownJob      = errors.New("job ID does not exist")
	ErrUnknownRobot    = errors.New("robot ID does not exist")
	ErrJobNotAssigned  = errors.New("job is not in the assigned state")
)

// AddJob submits a new job to the queue. DependsOn entries must already
// exist (submitted earlier) - this catches a typo'd dependency ID at
// submission time instead of the job silently never becoming eligible.
//
// AddJob always inserts: two calls with the same ID are a real error
// (ErrJobExists), never silently merged. For a caller that may retry a
// submission and needs the SAME logical job returned instead of a
// collision error, see SubmitJob.
func (e *Engine) AddJob(j Job) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, err := e.addJobLocked(j)
	return err
}

// addJobLocked is the shared insert path for AddJob and a first-time
// SubmitJob call. Caller must hold e.mu.
func (e *Engine) addJobLocked(j Job) (*Job, error) {
	if _, exists := e.jobs[j.ID]; exists {
		return nil, fmt.Errorf("%w: %q", ErrJobExists, j.ID)
	}
	for _, dep := range j.DependsOn {
		if _, exists := e.jobs[dep]; !exists {
			return nil, fmt.Errorf("%w: job %q depends on %q", ErrUnknownDep, j.ID, dep)
		}
	}

	e.nextSeq++
	stored := j
	stored.seq = e.nextSeq
	stored.Status = e.computeStatus(&stored)
	e.jobs[j.ID] = &stored
	if stored.DedupKey != "" {
		e.dedupIndex[stored.DedupKey] = stored.ID
	}
	return &stored, nil
}

// SubmitJob is the idempotent entry point for submitting work: unlike
// AddJob (which always inserts and errors on an ID collision), SubmitJob
// treats a repeated Job.DedupKey as the same logical unit of work -
// this is what makes a retried submission never execute the same work
// twice.
//
//   - DedupKey == "": always creates a new job, identical to AddJob.
//   - An existing job with the same DedupKey that is Pending, Blocked,
//     Assigned, or Done is returned UNCHANGED (SubmitDuplicate). A caller
//     that resubmits after a timed-out response, unsure whether the first
//     attempt was received, gets back the original job instead of a
//     second one racing it for a robot or a robot running it twice.
//   - An existing job with the same DedupKey that is Failed is reset to
//     Pending (SubmitRetried) under its ORIGINAL job ID, refreshed with
//     the retry's own Priority/RequiredTool/DependsOn (e.g. a bumped
//     Priority on a retried defect fix) - a genuine retry-after-failure
//     reuses the same job identity rather than minting a new one, so its
//     history stays under one ID.
func (e *Engine) SubmitJob(j Job) (Job, SubmitResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if j.DedupKey != "" {
		if existingID, ok := e.dedupIndex[j.DedupKey]; ok {
			existing := e.jobs[existingID]
			if existing.Status != StatusFailed {
				return *existing, SubmitDuplicate, nil
			}
			for _, dep := range j.DependsOn {
				if dep == existing.ID {
					return Job{}, "", fmt.Errorf("%w: job %q cannot depend on itself", ErrUnknownDep, existing.ID)
				}
				if _, ok := e.jobs[dep]; !ok {
					return Job{}, "", fmt.Errorf("%w: job %q depends on %q", ErrUnknownDep, existing.ID, dep)
				}
			}
			existing.Priority = j.Priority
			existing.RequiredTool = j.RequiredTool
			existing.DependsOn = j.DependsOn
			existing.AssignedRobot = ""
			existing.Status = e.computeStatus(existing)
			return *existing, SubmitRetried, nil
		}
	}

	stored, err := e.addJobLocked(j)
	if err != nil {
		return Job{}, "", err
	}
	return *stored, SubmitCreated, nil
}

// UpsertRobot registers a new robot or updates an existing one's fields
// (location/tool/availability). Load is only ever changed internally by
// CompleteJob, never overwritten by a caller-supplied Robot value, so an
// operator correcting a robot's tool head mid-shift can't accidentally
// reset its fairness counter.
func (e *Engine) UpsertRobot(r Robot) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if existing, ok := e.robots[r.ID]; ok {
		existing.Location = r.Location
		existing.Tool = r.Tool
		existing.Available = r.Available
		return
	}
	stored := r
	e.robots[r.ID] = &stored
}

// computeStatus derives Pending vs Blocked from current dependency state.
// Caller must hold e.mu.
func (e *Engine) computeStatus(j *Job) JobStatus {
	for _, dep := range j.DependsOn {
		if d, ok := e.jobs[dep]; ok && d.Status != StatusDone {
			return StatusBlocked
		}
	}
	return StatusPending
}

// refreshBlocked re-evaluates every Blocked/Pending job's status. Called
// after any job transitions to Done, since that may unblock others.
// Caller must hold e.mu.
func (e *Engine) refreshBlocked() {
	for _, j := range e.jobs {
		if j.Status == StatusPending || j.Status == StatusBlocked {
			j.Status = e.computeStatus(j)
		}
	}
}

// DispatchOnce runs one scheduling pass: every eligible job (Pending,
// highest Priority first, oldest submission breaking ties) is matched to
// the best available robot with a matching tool and the lowest Load
// (fewest completed jobs this session, so work spreads across the fleet
// instead of piling onto whichever robot happens to sort first).
//
// A job with RequiredTool == "" matches any available robot regardless
// of its Tool. Returns every assignment made this pass; an eligible job
// with no matching robot right now is left Pending and reconsidered on
// the next call.
func (e *Engine) DispatchOnce() []Assignment {
	e.mu.Lock()
	defer e.mu.Unlock()

	eligible := make([]*Job, 0, len(e.jobs))
	for _, j := range e.jobs {
		if j.Status == StatusPending {
			eligible = append(eligible, j)
		}
	}
	sort.Slice(eligible, func(i, k int) bool {
		if eligible[i].Priority != eligible[k].Priority {
			return eligible[i].Priority > eligible[k].Priority // higher priority first
		}
		return eligible[i].seq < eligible[k].seq // FIFO among equal priority
	})

	var assignments []Assignment
	for _, j := range eligible {
		robot := e.bestRobotFor(j)
		if robot == nil {
			continue // no matching idle robot this pass - stays Pending
		}
		j.Status = StatusAssigned
		j.AssignedRobot = robot.ID
		robot.Available = false
		assignments = append(assignments, Assignment{JobID: j.ID, RobotID: robot.ID})
	}
	return assignments
}

// bestRobotFor returns the available robot with a matching tool and the
// lowest Load, or nil if none qualifies. Caller must hold e.mu.
func (e *Engine) bestRobotFor(j *Job) *Robot {
	var best *Robot
	for _, r := range e.robots {
		if !r.Available {
			continue
		}
		if j.RequiredTool != "" && r.Tool != j.RequiredTool {
			continue
		}
		if best == nil || r.Load < best.Load || (r.Load == best.Load && r.ID < best.ID) {
			best = r
		}
	}
	return best
}

// CompleteJob marks an Assigned job Done or Failed, frees its robot
// (Available again, Load incremented on success so future ties favour a
// less-used robot), and re-evaluates every Blocked job in case this
// completion just unblocked a later stage of a multi-step mission.
func (e *Engine) CompleteJob(jobID string, success bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	j, ok := e.jobs[jobID]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownJob, jobID)
	}
	if j.Status != StatusAssigned {
		return fmt.Errorf("%w: job %q is %q", ErrJobNotAssigned, jobID, j.Status)
	}
	robot, ok := e.robots[j.AssignedRobot]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownRobot, j.AssignedRobot)
	}

	if success {
		j.Status = StatusDone
		robot.Load++
	} else {
		j.Status = StatusFailed
	}
	robot.Available = true
	e.refreshBlocked()
	return nil
}

// Job returns a copy of one job's current state.
func (e *Engine) Job(id string) (Job, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	j, ok := e.jobs[id]
	if !ok {
		return Job{}, false
	}
	return *j, true
}

// Jobs returns a copy of every job's current state, for listing/inspection.
func (e *Engine) Jobs() []Job {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Job, 0, len(e.jobs))
	for _, j := range e.jobs {
		out = append(out, *j)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].seq < out[k].seq })
	return out
}

// Robots returns a copy of every robot's current state.
func (e *Engine) Robots() []Robot {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Robot, 0, len(e.robots))
	for _, r := range e.robots {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].ID < out[k].ID })
	return out
}
