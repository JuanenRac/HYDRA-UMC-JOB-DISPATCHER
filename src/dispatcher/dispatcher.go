// HYDRA-UMC-JOB-DISPATCHER - dispatcher package
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// The real priority mission queue described in the README's "DISPATCHER
// FLOW" diagram: a global job queue, tool-aware routing against a robot
// registry, and multi-stage mission dependencies (a job only becomes
// eligible once every job it DependsOn is Done). In-memory by default
// (NewEngine); NewEngineWithStore adds real SQLite-backed persistence -
// see the package doc on Store/Engine for how the two compose.
package dispatcher

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// JobStatus is the lifecycle state of a Job.
type JobStatus string

const (
	StatusPending     JobStatus = "pending"  // submitted, not yet dispatchable or not yet assigned
	StatusBlocked     JobStatus = "blocked"  // pending, but waiting on an unfinished dependency
	StatusAssigned    JobStatus = "assigned" // dispatched to a robot, work in progress
	StatusDone        JobStatus = "done"
	StatusFailed      JobStatus = "failed"
	StatusUnreachable JobStatus = "unreachable" // blocked on a dependency that itself ended Failed (or is Unreachable) - can never become eligible on its own, unlike Blocked which just needs more time. Not the same as Failed: this job itself was never dispatched. Resolves back to Pending/Blocked if that dependency is later retried (see SubmitJob) and succeeds.
	// StatusUnknown means this job was Assigned to a robot whose
	// heartbeat then went stale (see DetectStaleAssignments) with no
	// completion ever reported - the dispatcher genuinely does not know
	// whether it succeeded, is still running, or was lost with the
	// robot. Never set automatically to Done/Failed by anything but a
	// real, later CompleteJob call for this same job (a robot that
	// reconnects and reports what actually happened) - silence alone
	// must never be read as either outcome.
	StatusUnknown JobStatus = "unknown"
	// StatusCancelled means an operator withdrew this job before any robot
	// was given it (see CancelJob). It is terminal and never dispatched.
	// A job that depends on a cancelled job can never become eligible, so it
	// surfaces as Unreachable, the same as when a dependency failed.
	StatusCancelled JobStatus = "cancelled"
)

// Job is one unit of work in the global mission queue.
type Job struct {
	ID            string
	Priority      int      // higher runs first - an "Emergency Defect Fix" gets a high value to bypass normal flow
	RequiredTool  string   // must match a Robot's Tool exactly, e.g. "PnP", "Laser" (empty = any robot)
	Location      string   // preferred robot Location (empty = anywhere): robots in this location are preferred, never required - another one still takes the job when none there is available
	DependsOn     []string // job IDs that must reach StatusDone before this one is eligible
	Status        JobStatus
	AssignedRobot string

	// DedupKey identifies the logical unit of work behind this submission
	// (e.g. a client-generated request ID), independent of Job.ID. Empty
	// means "no deduplication requested" - AddJob's plain ID-collision
	// check is the only guard, unchanged. See SubmitJob.
	DedupKey string

	// SubmittedAtMs, AssignedAtMs and CompletedAtMs are unix milliseconds of the engine's own clock
	// (0 = it has not happened): when the job was submitted (or retried), when it was last given to
	// a robot and when it ended. They let a client compute queue time, cycle time and SLA.
	SubmittedAtMs int64
	AssignedAtMs  int64
	CompletedAtMs int64
	// Attempt counts how many times the job has been given to a robot (0 = never). A robot (or
	// ORCHESTRATOR) that sends it back with its completion report is refused when a newer
	// assignment exists, so a late report of an abandoned attempt cannot close the new one.
	Attempt int

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
	// set when a heartbeat reports Available=false WHILE this robot
	// has an active assignment - this project's own guard in UpsertRobot already
	// refuses to let that heartbeat touch Available directly (the
	// scheduler's reservation must win over a racing heartbeat), but
	// silently dropping it entirely lost the real signal outright: a robot
	// that faults mid-task used to come back fully Available the instant
	// CompleteJob ran, with no memory that anything was ever wrong.
	// Package-private and never persisted - see CompleteJob/UpsertRobot's
	// own comments for exactly how it is set and cleared.
	selfReportedUnavailable bool
	// LastHeartbeat is when UpsertRobot last recorded a real heartbeat/registration for this
	// robot - the engine's own clock (e.now()), never a value a caller passed (UpsertRobot
	// overwrites it). The zero time means "never checked in". It is persisted with the robot
	// (every heartbeat already writes the robot), so a restart does not wipe it:
	// DetectStaleAssignments still sees a robot that went silent before the restart, and a robot
	// loaded from a database without a recorded heartbeat is not offered new work until it
	// checks in once.
	LastHeartbeat time.Time
}

// Assignment is the result of matching one eligible Job to one available Robot.
type Assignment struct {
	JobID   string
	RobotID string
}

// JobRecord is exactly what a Store persists for one job: Job's own
// public fields plus its internal sequence number (Seq), needed to
// restore the same stable FIFO/listing order across a restart - Job's
// own seq field is deliberately unexported (see Job's own comment), so a
// Store implementation in another package can only see it through this
// type, never by reaching into Job directly.
type JobRecord struct {
	Job
	Seq int64
}

// Store is the real, minimal persistence boundary Engine writes through
// - found while auditing the code: the README's
// own "Persistence: fault-tolerant mission state using local Redis/
// Database storage" was real future work, not forgotten, and Engine's
// state was deliberately kept behind exported methods only so a real
// backing store could be added later without changing every caller. See
// NewEngineWithStore. github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/
// sqlitestore is the real, shipped implementation (pure-Go, no CGO -
// cross-compiles the same way this binary already does for the CM5).
type Store interface {
	// SaveJob durably upserts one job's complete current state, keyed by
	// JobRecord.ID - called after every real state transition (created,
	// retried, assigned, done, failed, blocked/unreachable re-evaluation).
	SaveJob(record JobRecord) error
	// SaveRobot durably upserts one robot's complete current state, keyed
	// by Robot.ID - called after every real registration/availability/
	// load change.
	SaveRobot(r Robot) error
	// LoadAll returns every previously-persisted job and robot, in no
	// particular order - NewEngineWithStore sorts by JobRecord.Seq itself
	// to restore FIFO order.
	LoadAll() ([]JobRecord, []Robot, error)
}

// BatchStore is implemented by a Store that can write several records in ONE transaction. The
// Engine uses it for the operations that change a job, its robot and the jobs that depend on it
// together (a completion, a cancellation, a requeue, the stale-heartbeat sweep), so a crash in
// the middle can never leave the database with a job done and its robot still reserved.
type BatchStore interface {
	SaveBatch(jobs []JobRecord, robots []Robot) error
}

// RobotDeleter is implemented by a Store that can forget a robot (see Engine.RemoveRobot).
type RobotDeleter interface {
	DeleteRobot(id string) error
}

// Engine holds all queue/registry state and implements the scheduling
// algorithm. Safe for concurrent use.
//
// Persistence is opt-in via a Store (NewEngineWithStore) - NewEngine's
// plain in-memory behavior is unchanged and remains the right choice for
// short-lived tests/local development that don't need a mission queue to
// survive a restart. When a Store is configured, every real mutation
// (AddJob/SubmitJob/UpsertRobot/DispatchOnce/CompleteJob, and every
// status flip refreshBlocked makes as a side effect of one of those)
// writes through to it - not as a best-effort background task, but
// synchronously, before the in-memory map is even updated to reflect a
// NEW job's insertion (addJobLocked) or a NEW robot's registration
// (UpsertRobot), so a write failure there is a real, returned error
// instead of an update memory can't actually back up. Failures on an
// EXISTING job/robot's state transition (assignment, completion,
// refreshBlocked's own cascades) are recorded via lastPersistErr/
// LastPersistError() instead of aborting an already-decided, already-
// user-visible transition (e.g. a robot that just finished a real job
// must not be told "sorry, undone" because a database write hiccuped
// after the physical work already happened) - an operator can see and
// act on a degraded store through LastPersistError() without this
// process's own in-memory state (which remains correct and authoritative
// for its own uptime either way) losing a single real event.
type Engine struct {
	mu             sync.Mutex
	jobs           map[string]*Job
	robots         map[string]*Robot
	nextSeq        int64
	dedupIndex     map[string]string // DedupKey -> JobID, only for jobs submitted with a non-empty DedupKey
	store          Store
	lastPersistErr error
	// robotFreshness is how long a robot may stay silent and still be offered new work (see
	// SetRobotFreshness). Zero disables the check: every Available robot is eligible, as before.
	robotFreshness time.Duration
	// tools, when set, is the catalog of known tool names; it only logs a warning for an unknown one (see tools.go).
	tools *ToolCatalog
	// eta is the configuration of the time estimates (see eta.go); the zero value is "off".
	eta ETAConfig
	// productionSink receives the ProductionEvent of every job that ends; nil (the default) means the events are off (see production_event.go).
	productionSink ProductionEventSink
	// now is the engine's own clock, injectable so a test can control it
	// exactly (see DetectStaleAssignments) - real callers always get
	// time.Now via the constructors below.
	now func() time.Time
	// pending collects the writes of one operation so they are committed together (see
	// beginBatchLocked); nil outside such an operation.
	pending *pendingWrites
}

type pendingWrites struct {
	jobs   map[string]*Job
	robots map[string]*Robot
}

// maxHeartbeatFutureSkew is how far ahead of the engine's clock a recorded heartbeat may be before
// it is not trusted. A heartbeat loaded from the database after the clock stepped backwards (a CM5
// without a battery clock that booted in 1970, a manual correction) would otherwise look fresh for
// as long as the clock needs to catch up.
const maxHeartbeatFutureSkew = time.Minute

func heartbeatFromFuture(now, heartbeat time.Time) bool {
	return heartbeat.Sub(now) > maxHeartbeatFutureSkew
}

// beginBatchLocked starts collecting persist calls instead of writing each one at once. Caller must
// hold e.mu and call commitBatchLocked before releasing it.
func (e *Engine) beginBatchLocked() {
	if e.store == nil {
		return
	}
	e.pending = &pendingWrites{jobs: map[string]*Job{}, robots: map[string]*Robot{}}
}

// commitBatchLocked writes everything collected since beginBatchLocked: in one transaction when
// the store supports it (BatchStore), one record at a time otherwise. It returns the first error.
func (e *Engine) commitBatchLocked() error {
	p := e.pending
	e.pending = nil
	if p == nil || (len(p.jobs) == 0 && len(p.robots) == 0) {
		return nil
	}
	jobs := make([]JobRecord, 0, len(p.jobs))
	for _, j := range p.jobs {
		jobs = append(jobs, JobRecord{Job: *j, Seq: j.seq})
	}
	sort.Slice(jobs, func(i, k int) bool { return jobs[i].Seq < jobs[k].Seq })
	robots := make([]Robot, 0, len(p.robots))
	for _, r := range p.robots {
		robots = append(robots, *r)
	}
	sort.Slice(robots, func(i, k int) bool { return robots[i].ID < robots[k].ID })
	if bs, ok := e.store.(BatchStore); ok {
		return bs.SaveBatch(jobs, robots)
	}
	var first error
	for _, r := range robots {
		if err := e.store.SaveRobot(r); err != nil && first == nil {
			first = err
		}
	}
	for _, j := range jobs {
		if err := e.store.SaveJob(j); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// NewEngine returns an empty, ready-to-use, purely in-memory Engine - no
// Store, so every method behaves exactly as it always has.
func NewEngine() *Engine {
	return &Engine{
		jobs:       make(map[string]*Job),
		robots:     make(map[string]*Robot),
		dedupIndex: make(map[string]string),
		now:        time.Now,
	}
}

// NewEngineWithStore returns an Engine backed by a real Store: every
// previously-persisted job/robot is loaded back into memory first (jobs
// restored in their original submission order via JobRecord.Seq, so
// DispatchOnce's own FIFO tie-breaking is unaffected by a restart), then
// every subsequent mutation writes through to store - see Engine's own
// doc comment for exactly which failures are returned here vs. recorded
// for LastPersistError(). A real error loading existing state is fatal
// here (unlike a later persist failure during operation): starting an
// engine that silently discarded an unreadable/corrupt mission queue
// would be worse than refusing to start at all.
func NewEngineWithStore(store Store) (*Engine, error) {
	records, robots, err := store.LoadAll()
	if err != nil {
		return nil, fmt.Errorf("loading persisted state: %w", err)
	}

	e := &Engine{
		jobs:       make(map[string]*Job, len(records)),
		robots:     make(map[string]*Robot, len(robots)),
		dedupIndex: make(map[string]string),
		store:      store,
		now:        time.Now,
	}
	for i := range records {
		r := records[i]
		j := r.Job
		j.seq = r.Seq
		e.jobs[j.ID] = &j
		if j.DedupKey != "" {
			e.dedupIndex[j.DedupKey] = j.ID
		}
		if j.seq >= e.nextSeq {
			e.nextSeq = j.seq + 1
		}
	}
	for i := range robots {
		r := robots[i]
		e.robots[r.ID] = &r
	}
	return e, nil
}

// persistJobLocked writes j through to e.store, if configured, and
// returns the real error (nil if store is nil or the write succeeded).
// It never touches lastPersistErr itself - a caller making a single
// "hard" write (see addJobLocked) propagates this directly; a caller
// making one or more "soft" writes (every other mutator) combines every
// attempt's own result with recordPersistOutcome below, so a real
// failure from one write in a multi-write operation (e.g. CompleteJob's
// job save AND robot save) is never silently hidden by a later,
// unrelated write in that SAME operation succeeding. Caller must hold
// e.mu.
func (e *Engine) persistJobLocked(j *Job) error {
	if e.store == nil {
		return nil
	}
	if e.pending != nil {
		e.pending.jobs[j.ID] = j
		return nil
	}
	return e.store.SaveJob(JobRecord{Job: *j, Seq: j.seq})
}

// persistRobotLocked mirrors persistJobLocked for a robot. Caller must
// hold e.mu.
func (e *Engine) persistRobotLocked(r *Robot) error {
	if e.store == nil {
		return nil
	}
	if e.pending != nil {
		e.pending.robots[r.ID] = r
		return nil
	}
	return e.store.SaveRobot(*r)
}

// recordPersistOutcome commits lastPersistErr as the combined result of
// every soft persist attempt made during one public mutating call: the
// first real error among errs, or nil only when every one of them
// succeeded. Call this exactly once, after every soft persist attempt an
// operation makes (see CompleteJob/UpsertRobot/SubmitJob's retry path for
// the pattern) - calling it once per individual write, like an earlier
// version of this code did, let a later successful write within the same
// operation silently erase an earlier real failure from that identical
// operation. Caller must hold e.mu.
func (e *Engine) recordPersistOutcome(errs ...error) {
	for _, err := range errs {
		if err != nil {
			e.lastPersistErr = err
			return
		}
	}
	e.lastPersistErr = nil
}

// LastPersistError reports the most recent Store write failure recorded
// for a non-hard persist (see persistJobLocked/persistRobotLocked) - nil
// once the store is healthy again on a later successful write. Real
// observability for the "fault-tolerant mission state" the README
// promises: a caller (see src/api's own /health) can surface this
// instead of a degraded store failing silently.
func (e *Engine) LastPersistError() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastPersistErr
}

var (
	ErrJobExists      = errors.New("job ID already exists")
	ErrUnknownDep     = errors.New("dependency job ID does not exist")
	ErrUnknownJob     = errors.New("job ID does not exist")
	ErrUnknownRobot   = errors.New("robot ID does not exist")
	ErrJobNotAssigned = errors.New("job is not in the assigned state")
	// ErrJobInFlight is returned by CancelJob for a job a robot may be
	// executing right now (Assigned, or Unknown after a stale heartbeat).
	// The dispatcher cannot stop physical work, so it never pretends to.
	ErrJobInFlight = errors.New("job may be running on a robot; it can only be ended by that robot's own report")
	// ErrJobFinished is returned by CancelJob for a job that already ended.
	ErrJobFinished = errors.New("job already finished")
	ErrRobotMismatch  = errors.New("robot ID does not match the job's assigned robot")
	// ErrRobotBusy is returned by RemoveRobot for a robot that still has a job assigned to it.
	ErrRobotBusy = errors.New("robot still has a job assigned to it")
	// ErrAttemptMismatch is returned by CompleteJobAttempt when the report belongs to an older
	// assignment of the job than the current one.
	ErrAttemptMismatch = errors.New("attempt does not match the job's current assignment")
	ErrInvalidJob     = errors.New("invalid job")
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
	e.tools.Check(j.RequiredTool, fmt.Sprintf("RequiredTool of job %q", j.ID))
	if strings.TrimSpace(j.ID) == "" {
		return nil, fmt.Errorf("%w: job ID must not be empty", ErrInvalidJob)
	}
	if err := validateDependencyShape(j.ID, j.DependsOn); err != nil {
		return nil, err
	}
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
	stored.DependsOn = cloneStrings(j.DependsOn)
	stored.seq = e.nextSeq
	stored.SubmittedAtMs = e.now().UnixMilli()
	stored.AssignedAtMs, stored.CompletedAtMs, stored.Attempt = 0, 0, 0
	stored.Status = e.computeStatus(&stored)
	// Persisted BEFORE this brand-new job enters e.jobs - a failure here
	// leaves Engine's state exactly as it was before this call (safe for
	// the caller to retry) rather than an in-memory job the store never
	// actually has a record of.
	if err := e.persistJobLocked(&stored); err != nil {
		return nil, fmt.Errorf("persisting new job %q: %w", stored.ID, err)
	}
	e.jobs[j.ID] = &stored
	if stored.DedupKey != "" {
		e.dedupIndex[stored.DedupKey] = stored.ID
	}
	return &stored, nil
}

// cloneStrings returns a fresh copy of s, sharing no backing array with
// it - nil in, nil out. Used at every boundary where a []string crosses
// between caller-owned memory and Engine's own internal state (see
// cloneJob and this project's own fix below).
func cloneStrings(s []string) []string {
	if s == nil {
		return nil
	}
	out := make([]string, len(s))
	copy(out, s)
	return out
}

// cloneJob returns a deep copy of *j safe to hand to a caller (or store
// as a caller's own input) without sharing DependsOn's backing array.
//
// a
// Job's DependsOn slice was copied only at the struct level (a plain
// `*j` dereference, or `existing.DependsOn = j.DependsOn`) - a shallow
// copy of a struct containing a slice copies the slice HEADER only, so
// the copy's DependsOn still points at the exact same backing array as
// Engine's own internal Job. A caller mutating a slice it got back from
// Job()/Jobs()/SubmitJob (e.g. `j.DependsOn[0] = "x"`), or later
// mutating a []string it originally passed into AddJob/SubmitJob,
// silently rewrote Engine's real dependency graph - bypassing e.mu
// entirely and never going through persistJobLocked. Every read/write
// boundary below now goes through this real, allocated copy instead.
func cloneJob(j *Job) Job {
	c := *j
	c.DependsOn = cloneStrings(j.DependsOn)
	return c
}

func validateDependencyShape(jobID string, dependencies []string) error {
	seenDependencies := make(map[string]struct{}, len(dependencies))
	for _, dep := range dependencies {
		if strings.TrimSpace(dep) == "" {
			return fmt.Errorf("%w: job %q has an empty dependency", ErrInvalidJob, jobID)
		}
		if dep == jobID {
			return fmt.Errorf("%w: job %q cannot depend on itself", ErrInvalidJob, jobID)
		}
		if _, duplicate := seenDependencies[dep]; duplicate {
			return fmt.Errorf("%w: job %q repeats dependency %q", ErrInvalidJob, jobID, dep)
		}
		seenDependencies[dep] = struct{}{}
	}
	return nil
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
//   - An existing job with the same DedupKey that is Failed or Cancelled is reset to
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
			if existing.Status != StatusFailed && existing.Status != StatusCancelled {
				return cloneJob(existing), SubmitDuplicate, nil
			}
			if err := validateDependencyShape(existing.ID, j.DependsOn); err != nil {
				return Job{}, "", err
			}
			for _, dep := range j.DependsOn {
				if _, ok := e.jobs[dep]; !ok {
					return Job{}, "", fmt.Errorf("%w: job %q depends on %q", ErrUnknownDep, existing.ID, dep)
				}
			}
			if e.dependsTransitivelyLocked(j.DependsOn, existing.ID) {
				return Job{}, "", fmt.Errorf("%w: job %q would depend on itself through %v (dependency cycle)", ErrInvalidJob, existing.ID, j.DependsOn)
			}
			existing.Priority = j.Priority
			existing.RequiredTool = j.RequiredTool
			e.tools.Check(j.RequiredTool, fmt.Sprintf("RequiredTool of job %q", existing.ID))
			existing.Location = j.Location
			existing.DependsOn = cloneStrings(j.DependsOn)
			existing.AssignedRobot = ""
			existing.SubmittedAtMs = e.now().UnixMilli()
			existing.AssignedAtMs, existing.CompletedAtMs = 0, 0
			existing.Status = e.computeStatus(existing)
			existingErr := e.persistJobLocked(existing)
			// BUG: a retried job going back to Pending/Blocked
			// only ever updated ITS OWN Status above - any other job that had
			// already been marked Unreachable because it (transitively)
			// DependsOn this one stayed stuck as Unreachable forever, since
			// nothing re-evaluated it: the only other caller of
			// refreshBlocked() is CompleteJob, which this retry path never
			// goes through. That directly contradicts Job.Status's own
			// documented contract for StatusUnreachable ("Resolves back to
			// Pending/Blocked if that dependency is later retried"). Verified
			// with a real repro (pick fails -> place becomes Unreachable;
			// pick is retried via SubmitJob -> place stayed Unreachable
			// instead of returning to Blocked) before this fix, and confirmed
			// fixed after it. refreshBlocked() re-evaluates every
			// Pending/Blocked/Unreachable job to a fixed point, so this both
			// unsticks direct dependents and propagates through a multi-step
			// chain in one call, the same way CompleteJob's own call already
			// does for the Done/Failed transitions.
			refreshErr := e.refreshBlocked()
			e.recordPersistOutcome(existingErr, refreshErr)
			return cloneJob(existing), SubmitRetried, nil
		}
	}

	stored, err := e.addJobLocked(j)
	if err != nil {
		return Job{}, "", err
	}
	return cloneJob(stored), SubmitCreated, nil
}

// dependsTransitivelyLocked reports whether any job in roots is, or (through its own
// dependencies) depends on, the job named target. Caller must hold e.mu.
func (e *Engine) dependsTransitivelyLocked(roots []string, target string) bool {
	seen := make(map[string]struct{})
	stack := append([]string(nil), roots...)
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if id == target {
			return true
		}
		if _, done := seen[id]; done {
			continue
		}
		seen[id] = struct{}{}
		if d, ok := e.jobs[id]; ok {
			stack = append(stack, d.DependsOn...)
		}
	}
	return false
}

// RobotUpdate is a partial robot registration/heartbeat: a nil field keeps the value the
// registry already holds (a brand new robot starts from the zero value).
type RobotUpdate struct {
	ID        string
	Location  *string
	Tool      *string
	Available *bool
}

// UpdateRobot applies a partial heartbeat. A lightweight heartbeat that only names the robot
// no longer wipes its tool, location or availability.
func (e *Engine) UpdateRobot(u RobotUpdate) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r := Robot{ID: u.ID}
	if existing, ok := e.robots[u.ID]; ok {
		r.Location, r.Tool = existing.Location, existing.Tool
	}
	if u.Location != nil {
		r.Location = *u.Location
	}
	if u.Tool != nil {
		r.Tool = *u.Tool
	}
	if u.Available != nil {
		r.Available = *u.Available
	}
	e.upsertRobotLocked(r, u.Available != nil)
}

// UpsertRobot registers a new robot or updates an existing one's fields
// (location/tool/availability). Load is only ever changed internally by
// CompleteJob, never overwritten by a caller-supplied Robot value, so an
// operator correcting a robot's tool head mid-shift can't accidentally
// reset its fairness counter.
func (e *Engine) UpsertRobot(r Robot) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.upsertRobotLocked(r, true)
}

// upsertRobotLocked is UpsertRobot's body. availabilityGiven false means the caller did not
// state availability (a partial heartbeat): an existing robot keeps it untouched.
func (e *Engine) upsertRobotLocked(r Robot, availabilityGiven bool) {
	e.tools.Check(r.Tool, fmt.Sprintf("Tool of robot %q", r.ID))
	if existing, ok := e.robots[r.ID]; ok {
		existing.Location = r.Location
		existing.Tool = r.Tool
		// This call arriving at all is itself a real heartbeat, whether or
		// not this project's own guard below lets it change Available - recorded
		// unconditionally so DetectStaleAssignments sees this robot as
		// genuinely alive right now.
		existing.LastHeartbeat = e.now()
		// Available doubles as both the robot's own self-reported
		// readiness AND the scheduler's real reservation flag (DispatchOnce
		// sets it false the instant it assigns a job). A heartbeat/
		// re-registration declaring Available=true must never override an
		// active reservation - without this guard, a heartbeat that raced
		// an assignment (arriving right after DispatchOnce marked this
		// robot busy) could flip it back to available, letting a second
		// DispatchOnce call assign a different job to the same
		// physically-busy robot. The scheduler's own reservation
		// (robotHasActiveAssignmentLocked, derived from real job state, not
		// a separate flag that could itself drift) always wins over
		// whatever a robot's own heartbeat currently declares.
		if !availabilityGiven {
			// nothing to change about availability
		} else if !e.robotHasActiveAssignmentLocked(r.ID) {
			existing.Available = r.Available
			// a valid heartbeat received while genuinely idle is the
			// only thing that may clear a prior mid-task fault signal - see
			// CompleteJob's own comment for why finishing the job itself
			// must not do this. A fresh Available=false here re-arms it
			// too (redundant with the mid-task case below, but harmless
			// and correct: still not available).
			existing.selfReportedUnavailable = !r.Available
		} else if !r.Available {
			// this project's own guard above correctly refuses to let this
			// heartbeat flip Available while the scheduler's reservation
			// owns it - but the negative signal itself is real and must
			// not just vanish. Recorded here so CompleteJob can refuse to
			// silently clear it later.
			existing.selfReportedUnavailable = true
		}
		e.recordPersistOutcome(e.persistRobotLocked(existing))
		return
	}
	stored := r
	stored.LastHeartbeat = e.now() // a first-ever registration counts as this robot's first heartbeat
	e.robots[r.ID] = &stored
	// Soft, not hard, unlike a job's own first-ever insert: a robot that
	// fails to persist here re-registers on its very next heartbeat/
	// reconnect (every real client already does this - see the README's
	// own fleet-registration flow), so refusing THIS call outright would
	// only make an operational fleet appear to reject a robot it actually
	// accepted, for no real benefit.
	e.recordPersistOutcome(e.persistRobotLocked(&stored))
}

// robotHasActiveAssignmentLocked reports whether robotID is the
// AssignedRobot of a job still in StatusAssigned - the scheduler's own
// real, current reservation of that robot, independent of whatever the
// robot's own heartbeat currently declares. See 's fix in
// UpsertRobot. Caller must hold e.mu.
func (e *Engine) robotHasActiveAssignmentLocked(robotID string) bool {
	for _, j := range e.jobs {
		if j.Status == StatusAssigned && j.AssignedRobot == robotID {
			return true
		}
	}
	return false
}

// computeStatus derives Pending vs Blocked vs Unreachable from current
// dependency state. A dependency that itself ended Failed - or is already
// Unreachable, so it can never end anything but Failed either - means this
// job can never become eligible on its own: it is Unreachable, not merely
// Blocked (which implies "will unblock once the dependency finishes", a
// promise a failed dependency can't keep without an operator retrying it).
// Caller must hold e.mu.
func (e *Engine) computeStatus(j *Job) JobStatus {
	blocked := false
	for _, dep := range j.DependsOn {
		d, ok := e.jobs[dep]
		if !ok {
			continue
		}
		if d.Status == StatusFailed || d.Status == StatusUnreachable || d.Status == StatusCancelled {
			return StatusUnreachable
		}
		if d.Status != StatusDone {
			blocked = true
		}
	}
	if blocked {
		return StatusBlocked
	}
	return StatusPending
}

// refreshBlocked re-evaluates every Pending/Blocked/Unreachable job's
// status. Called after any job transitions to Done or Failed, since either
// may change others' eligibility - Done can unblock a dependent, Failed can
// make one Unreachable. Loops to a fixed point (instead of one pass) so an
// Unreachable verdict propagates through an entire multi-step dependency
// chain in one call - e.g. weld depends on place depends on pick: if pick
// fails, place and weld must both surface as Unreachable right away, not
// only after some future completion event on place that will now never
// happen. Caller must hold e.mu.
// Returns the last real persist failure hit while writing through any
// job this pass actually changed, or nil if every one of those writes
// succeeded (or none were needed) - the caller combines this with its
// own other persist attempts via recordPersistOutcome, exactly like
// every other soft persist site.
func (e *Engine) refreshBlocked() error {
	var persistErr error
	for changed := true; changed; {
		changed = false
		for _, j := range e.jobs {
			if j.Status != StatusPending && j.Status != StatusBlocked && j.Status != StatusUnreachable {
				continue
			}
			if next := e.computeStatus(j); next != j.Status {
				j.Status = next
				changed = true
				if err := e.persistJobLocked(j); err != nil {
					persistErr = err
				}
			}
		}
	}
	return persistErr
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
	// persisted tracks whether this pass actually attempted any write at
	// all - a pass that assigns nothing (nothing eligible, or nothing
	// eligible has a matching idle robot) must not touch lastPersistErr,
	// since doing so would either silently clear a real, still-relevant
	// failure from an earlier call or, less harmfully but still
	// misleadingly, report success for an operation that made no writes.
	var persisted bool
	var persistErr error
	for _, j := range eligible {
		robot := e.bestRobotFor(j)
		if robot == nil {
			continue // no matching idle robot this pass - stays Pending
		}
		prevRobotAvailable := robot.Available
		robot.Available = false
		persisted = true
		// Store's own two SaveJob/SaveRobot calls are not a joint
		// transaction, so this used to emit the assignment (and keep the
		// in-memory Assigned/Available state) even when either write
		// failed, only combining the failure into persistErr for the
		// caller to notice later, out of band - the physical dispatch
		// this return value drives had no such caller. If the process
		// restarted before a later retry happened to persist the job,
		// that exact job would still read Pending from disk and get
		// handed to DispatchOnce again next cycle - a second, invisible
		// dispatch of work already sent out once.
		//
		// The robot is saved FIRST, deliberately: a robot's own
		// availability self-heals from its next real heartbeat regardless
		// of what this call does (see UpsertRobot), but nothing else in
		// this system ever re-announces a JOB's own status - so the one
		// write that must never durably say "Assigned" unless a matching
		// robot reservation is durable too is the job's. Only once the
		// robot save succeeds does the job even get marked Assigned in
		// memory, and only a job whose OWN save also succeeds is emitted;
		// any failure rolls every change on this iteration back to its
		// pre-attempt state, so a restart at any point during this
		// sequence can never observe a job durably Assigned with no
		// durable robot reservation behind it.
		if err := e.persistRobotLocked(robot); err != nil {
			robot.Available = prevRobotAvailable
			persistErr = err
			continue
		}
		prevAssignedAt := j.AssignedAtMs
		j.Status = StatusAssigned
		j.AssignedRobot = robot.ID
		j.Attempt++
		j.AssignedAtMs = e.now().UnixMilli()
		if err := e.persistJobLocked(j); err != nil {
			j.Status = StatusPending
			j.AssignedRobot = ""
			j.Attempt--
			j.AssignedAtMs = prevAssignedAt
			robot.Available = prevRobotAvailable
			persistErr = err
			// Best-effort compensating write - if this also fails, the
			// robot's own next heartbeat still recovers it (see above);
			// this second failure is folded into the same persistErr
			// report either way, never silently dropped.
			if compErr := e.persistRobotLocked(robot); compErr != nil {
				persistErr = compErr
			}
			continue
		}
		assignments = append(assignments, Assignment{JobID: j.ID, RobotID: robot.ID})
	}
	if persisted {
		e.recordPersistOutcome(persistErr)
	}
	return assignments
}

// SetToolCatalog installs the catalog of known tool names (nil removes it). Unknown tool names are only warned about.
func (e *Engine) SetToolCatalog(c *ToolCatalog) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.tools = c
}

// SetRobotFreshness makes a robot that has not checked in for longer than d ineligible for new
// work: a robot that stopped answering keeps Available=true in the registry, and without this it
// went on receiving jobs. Zero (the default) disables the check. A robot with no recorded
// heartbeat at all (never seen since the registry was loaded) is also ineligible while the check is on.
func (e *Engine) SetRobotFreshness(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.robotFreshness = d
}

// robotEligibleLocked reports whether r may be offered j: available, the required tool attached,
// and (when the freshness check is on) heard from recently. Caller must hold e.mu.
func (e *Engine) robotEligibleLocked(r *Robot, j *Job, now time.Time) bool {
	if !r.Available {
		return false
	}
	if j.RequiredTool != "" && r.Tool != j.RequiredTool {
		return false
	}
	if e.robotFreshness > 0 && (r.LastHeartbeat.IsZero() || heartbeatFromFuture(now, r.LastHeartbeat) || now.Sub(r.LastHeartbeat) > e.robotFreshness) {
		return false
	}
	return true
}

// bestRobotFor returns the eligible robot with the lowest Load, preferring one in the job's own
// Location when the job names one, or nil if none qualifies. Caller must hold e.mu.
func (e *Engine) bestRobotFor(j *Job) *Robot {
	now := e.now()
	var best, bestInLocation *Robot
	better := func(r, than *Robot) bool {
		return than == nil || r.Load < than.Load || (r.Load == than.Load && r.ID < than.ID)
	}
	for _, r := range e.robots {
		if !e.robotEligibleLocked(r, j, now) {
			continue
		}
		if better(r, best) {
			best = r
		}
		if j.Location != "" && r.Location == j.Location && better(r, bestInLocation) {
			bestInLocation = r
		}
	}
	if bestInLocation != nil {
		return bestInLocation
	}
	return best
}

// CompleteJob marks an Assigned (or Unknown - see DetectStaleAssignments)
// job Done or Failed, frees its robot (Available again, Load incremented
// so future ties favour a less-used robot) if that robot
// isn't already busy with something else, and re-evaluates every
// Blocked/Unreachable job: a Done result may unblock a later stage of a
// multi-step mission, while a Failed result may instead make one or more
// later stages Unreachable.
//
// `robotID` must match the job's own real `AssignedRobot` - the
// HTTP handler had no way at all to tell a genuine completion report
// from the robot the dispatcher actually assigned this job to apart
// from ANY caller simply naming a job ID and a success flag, so a wrong
// or malicious report for job X could silently steal or corrupt a
// DIFFERENT robot's own real Load/Available bookkeeping. This form states no
// attempt number, so it only checks which ROBOT is reporting; the report of a
// client that knows the attempt of its assignment goes through
// CompleteJobAttempt below, which also refuses the late report of an abandoned
// assignment.
func (e *Engine) CompleteJob(jobID string, success bool, robotID string) error {
	return e.CompleteJobAttempt(jobID, success, robotID, 0)
}

// CompleteJobAttempt is CompleteJob with the attempt number of the assignment the report is
// about (Job.Attempt as the robot received it). A report for an older attempt than the current
// one is refused with ErrAttemptMismatch, so the late report of an abandoned assignment cannot
// close the one that replaced it. attempt 0 means "not stated" and is not checked.
func (e *Engine) CompleteJobAttempt(jobID string, success bool, robotID string, attempt int) error {
	// The production event is handed over after the lock has been released (defers run last in, first out), so a sink never holds up the engine.
	var sink ProductionEventSink
	var event *ProductionEvent
	defer func() {
		if sink != nil && event != nil {
			deliver(sink, *event)
		}
	}()
	e.mu.Lock()
	defer e.mu.Unlock()

	j, ok := e.jobs[jobID]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownJob, jobID)
	}
	// A job DetectStaleAssignments gave up tracking can still be resolved
	// for real by a late, genuine report from the robot that was actually
	// running it - silence being wrong (the robot was fine all along)
	// must not permanently lock that job out of ever completing honestly.
	if j.Status != StatusAssigned && j.Status != StatusUnknown {
		return fmt.Errorf("%w: job %q is %q", ErrJobNotAssigned, jobID, j.Status)
	}
	if robotID != j.AssignedRobot {
		return fmt.Errorf("%w: job %q is assigned to %q, not %q", ErrRobotMismatch, jobID, j.AssignedRobot, robotID)
	}
	if attempt != 0 && attempt != j.Attempt {
		return fmt.Errorf("%w: job %q is on attempt %d, the report is for attempt %d", ErrAttemptMismatch, jobID, j.Attempt, attempt)
	}
	robot, ok := e.robots[j.AssignedRobot]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownRobot, j.AssignedRobot)
	}

	// The job, its robot and every dependent whose status changes are written in one transaction.
	e.beginBatchLocked()
	// Load counts every finished assignment, failed ones included: a robot that
	// fails everything it is given must not stay the least-loaded one and keep
	// attracting the whole queue.
	robot.Load++
	j.CompletedAtMs = e.now().UnixMilli()
	if success {
		j.Status = StatusDone
	} else {
		j.Status = StatusFailed
	}
	sink, event = e.productionEventLocked(j)
	// finishing THIS job only ever ends the scheduler's own
	// reservation on the robot - it must never be read as "the robot
	// itself is healthy again". A heartbeat reporting Available=false
	// while this job was active (dropped by UpsertRobot's guard,
	// but recorded via selfReportedUnavailable) means the robot itself
	// already said it isn't ready; unconditionally flipping Available
	// back to true here used to silently discard that real signal the
	// instant the job ended, regardless of success or failure. Only a
	// robot that never self-reported unavailable mid-task comes back
	// available on completion - one that did stays unavailable until its
	// own next heartbeat reports true (see UpsertRobot).
	//
	// robotHasActiveAssignmentLocked is checked too (j.Status is already
	// updated away from Assigned/Unknown above, so this only ever sees a
	// DIFFERENT job): DetectStaleAssignments can let a robot pick up a
	// brand new job B while its stale job A is still sitting Unknown -
	// if A's own late completion report then arrives, the robot may
	// genuinely be busy with B right now, and this must never mark it
	// Available out from under that real, current assignment.
	if !e.robotHasActiveAssignmentLocked(robot.ID) && !robot.selfReportedUnavailable {
		robot.Available = true
	}
	// Soft persist, matching Engine's own doc comment: the physical work
	// already happened - a store hiccup here must not turn into an error
	// telling the caller a real completion never occurred. Combined via
	// recordPersistOutcome so a real job-save failure is never hidden by
	// the robot-save or refreshBlocked's own writes succeeding right
	// after it.
	jobErr := e.persistJobLocked(j)
	robotErr := e.persistRobotLocked(robot)
	refreshErr := e.refreshBlocked()
	e.recordPersistOutcome(jobErr, robotErr, refreshErr, e.commitBatchLocked())
	return nil
}

// CancelJob withdraws a job that no robot has been given: Pending, Blocked or
// Unreachable becomes Cancelled and is never dispatched, also not after a
// restart (the status is persisted). Cancelling an already cancelled job is a
// no-op, so a retried request is safe. A job that is Assigned (or Unknown
// after a stale heartbeat) may be running on a robot right now; the
// dispatcher cannot stop physical work, so it refuses with ErrJobInFlight and
// leaves the outcome to that robot's own CompleteJob report. Jobs that depend
// on the cancelled one become Unreachable.
func (e *Engine) CancelJob(jobID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	j, ok := e.jobs[jobID]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownJob, jobID)
	}
	switch j.Status {
	case StatusCancelled:
		return nil
	case StatusAssigned, StatusUnknown:
		return fmt.Errorf("%w: job %q is %q", ErrJobInFlight, jobID, j.Status)
	case StatusDone, StatusFailed:
		return fmt.Errorf("%w: job %q is %q", ErrJobFinished, jobID, j.Status)
	}
	e.beginBatchLocked()
	j.Status = StatusCancelled
	j.AssignedRobot = ""
	j.CompletedAtMs = e.now().UnixMilli()
	jobErr := e.persistJobLocked(j)
	refreshErr := e.refreshBlocked()
	e.recordPersistOutcome(jobErr, refreshErr, e.commitBatchLocked())
	return nil
}

// RequeueJob is the operator's way out of a job nobody will ever report on: an Assigned or
// Unknown job goes back to Pending (no robot, ready for the next dispatch pass) and the robot
// it was given to is marked unavailable until its own next heartbeat says otherwise, so the
// same dead robot is not handed the job again. `robotID`, when not empty, must be the robot
// the job is assigned to (a stale caller cannot requeue a job that has moved on). A job that
// is already Pending, Blocked or Unreachable is returned as it is.
func (e *Engine) RequeueJob(jobID, robotID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	j, ok := e.jobs[jobID]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownJob, jobID)
	}
	switch j.Status {
	case StatusPending, StatusBlocked, StatusUnreachable:
		return nil
	case StatusDone, StatusFailed, StatusCancelled:
		return fmt.Errorf("%w: job %q is %q", ErrJobFinished, jobID, j.Status)
	}
	if robotID != "" && robotID != j.AssignedRobot {
		return fmt.Errorf("%w: job %q is assigned to %q, not %q", ErrRobotMismatch, jobID, j.AssignedRobot, robotID)
	}
	e.beginBatchLocked()
	var robotErr error
	if robot, ok := e.robots[j.AssignedRobot]; ok {
		robot.Available = false
		robot.selfReportedUnavailable = true
		robotErr = e.persistRobotLocked(robot)
	}
	j.AssignedRobot = ""
	j.AssignedAtMs = 0
	j.Status = e.computeStatus(j)
	jobErr := e.persistJobLocked(j)
	e.recordPersistOutcome(robotErr, jobErr, e.commitBatchLocked())
	return nil
}

// DetectStaleAssignments marks every StatusAssigned job whose robot has
// not sent a real heartbeat (UpsertRobot call) within `timeout` as
// StatusUnknown - the dispatcher genuinely cannot tell whether that job
// succeeded, is still running, or was lost along with its robot. A
// caller should invoke this on the same cadence it already polls
// DispatchOnce on; it does not run on any timer of its own.
//
// A robot that has never sent a single heartbeat is never flagged stale
// by this check on its own account - see LastHeartbeat's own comment
// on why a zero value can't be trusted to mean "known-stale" (it may
// simply mean this process restarted since that robot last checked in).
//
// Marking a job Unknown deliberately does NOT touch its robot's
// Available field: robotHasActiveAssignmentLocked only ever counts a
// StatusAssigned job as an active reservation, so a robot whose only job
// just became Unknown is immediately eligible to accept new work on its
// own next heartbeat reporting Available=true - refusing to let a real,
// reconnected, idle robot pick up new work just because one old job's
// outcome is still a mystery would strand it for no operational benefit.
// See CompleteJob's own comment for how a later, genuine report for the
// stale job itself is still honoured even after that happens.
//
// Returns the IDs of every job just marked Unknown, so a caller can
// alert on them.
func (e *Engine) DetectStaleAssignments(timeout time.Duration) []string {
	e.mu.Lock()
	defer e.mu.Unlock()

	now := e.now()
	var newlyUnknown []string
	var persistErr error
	e.beginBatchLocked()
	for _, j := range e.jobs {
		if j.Status != StatusAssigned {
			continue
		}
		robot, ok := e.robots[j.AssignedRobot]
		if !ok {
			continue // the robot itself was removed entirely - a separate concern from a merely-quiet one
		}
		// a heartbeat from the future (the clock stepped backwards since) says nothing about
		// the robot being alive now, so it is treated as silence
		if robot.LastHeartbeat.IsZero() || (now.Sub(robot.LastHeartbeat) < timeout && !heartbeatFromFuture(now, robot.LastHeartbeat)) {
			continue
		}
		j.Status = StatusUnknown
		newlyUnknown = append(newlyUnknown, j.ID)
		if err := e.persistJobLocked(j); err != nil {
			persistErr = err
		}
	}
	commitErr := e.commitBatchLocked()
	if len(newlyUnknown) > 0 {
		e.recordPersistOutcome(persistErr, commitErr)
	}
	return newlyUnknown
}

// RemoveRobot forgets a robot (it was decommissioned or registered by mistake). A robot that
// still has a job Assigned or Unknown is refused with ErrRobotBusy: the job's outcome would have
// nobody to be reported by. When the store can forget robots (RobotDeleter) the record is deleted
// first, so a failed delete leaves everything as it was and is returned to the caller.
func (e *Engine) RemoveRobot(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.robots[id]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownRobot, id)
	}
	for _, j := range e.jobs {
		if (j.Status == StatusAssigned || j.Status == StatusUnknown) && j.AssignedRobot == id {
			return fmt.Errorf("%w: job %q is %q on robot %q", ErrRobotBusy, j.ID, j.Status, id)
		}
	}
	if deleter, ok := e.store.(RobotDeleter); ok && e.store != nil {
		if err := deleter.DeleteRobot(id); err != nil {
			return fmt.Errorf("deleting robot %q: %w", id, err)
		}
	}
	delete(e.robots, id)
	return nil
}

// JobsPage returns the jobs in submission order, optionally only those in `status` (empty = all),
// skipping `offset` and returning at most `limit` (0 = no limit), and how many jobs matched in
// total. It lets a client read a long queue in pieces instead of downloading it all.
func (e *Engine) JobsPage(status JobStatus, offset, limit int) ([]Job, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	matching := make([]*Job, 0, len(e.jobs))
	for _, j := range e.jobs {
		if status == "" || j.Status == status {
			matching = append(matching, j)
		}
	}
	sort.Slice(matching, func(i, k int) bool { return matching[i].seq < matching[k].seq })
	total := len(matching)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	matching = matching[offset:]
	if limit > 0 && limit < len(matching) {
		matching = matching[:limit]
	}
	out := make([]Job, 0, len(matching))
	for _, j := range matching {
		out = append(out, cloneJob(j))
	}
	return out, total
}

// ValidStatus reports whether s names a job status.
func ValidStatus(s JobStatus) bool {
	switch s {
	case StatusPending, StatusBlocked, StatusAssigned, StatusDone, StatusFailed, StatusUnreachable, StatusUnknown, StatusCancelled:
		return true
	}
	return false
}

// Job returns a copy of one job's current state.
func (e *Engine) Job(id string) (Job, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	j, ok := e.jobs[id]
	if !ok {
		return Job{}, false
	}
	return cloneJob(j), true
}

// Jobs returns a copy of every job's current state, for listing/inspection.
func (e *Engine) Jobs() []Job {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Job, 0, len(e.jobs))
	for _, j := range e.jobs {
		out = append(out, cloneJob(j))
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
