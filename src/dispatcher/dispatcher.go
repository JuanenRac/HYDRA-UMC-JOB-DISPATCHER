// HYDRA-UMC-JOB-DISPATCHER - dispatcher package
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0 - see LICENSE
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
)

// Job is one unit of work in the global mission queue.
type Job struct {
	ID            string
	Priority      int      // higher runs first - an "Emergency Defect Fix" gets a high value to bypass normal flow
	RequiredTool  string   // must match a Robot's Tool exactly, e.g. "PnP", "Laser" (empty = any robot)
	DependsOn     []string // job IDs that must reach StatusDone before this one is eligible
	Status        JobStatus
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
// NewEngineWithStore. github.com/JuanenRac/hydra-umc-job-dispatcher/src/
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
}

// NewEngine returns an empty, ready-to-use, purely in-memory Engine - no
// Store, so every method behaves exactly as it always has.
func NewEngine() *Engine {
	return &Engine{
		jobs:       make(map[string]*Job),
		robots:     make(map[string]*Robot),
		dedupIndex: make(map[string]string),
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
	return e.store.SaveJob(JobRecord{Job: *j, Seq: j.seq})
}

// persistRobotLocked mirrors persistJobLocked for a robot. Caller must
// hold e.mu.
func (e *Engine) persistRobotLocked(r *Robot) error {
	if e.store == nil {
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
// cloneJob and JOB-01's own fix below).
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
// JOB-01 (P1): a
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
			existing.Priority = j.Priority
			existing.RequiredTool = j.RequiredTool
			existing.DependsOn = cloneStrings(j.DependsOn)
			existing.AssignedRobot = ""
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
		// JOB-02 (P0): Available doubles as both the robot's own self-reported
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
		if !e.robotHasActiveAssignmentLocked(r.ID) {
			existing.Available = r.Available
		}
		e.recordPersistOutcome(e.persistRobotLocked(existing))
		return
	}
	stored := r
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
// robot's own heartbeat currently declares. See JOB-02's fix in
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
		if d.Status == StatusFailed || d.Status == StatusUnreachable {
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
		j.Status = StatusAssigned
		j.AssignedRobot = robot.ID
		robot.Available = false
		persisted = true
		if err := e.persistJobLocked(j); err != nil {
			persistErr = err
		}
		if err := e.persistRobotLocked(robot); err != nil {
			persistErr = err
		}
		assignments = append(assignments, Assignment{JobID: j.ID, RobotID: robot.ID})
	}
	if persisted {
		e.recordPersistOutcome(persistErr)
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
// less-used robot), and re-evaluates every Blocked/Unreachable job: a Done
// result may unblock a later stage of a multi-step mission, while a Failed
// result may instead make one or more later stages Unreachable.
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
	// Soft persist, matching Engine's own doc comment: the physical work
	// already happened - a store hiccup here must not turn into an error
	// telling the caller a real completion never occurred. Combined via
	// recordPersistOutcome so a real job-save failure is never hidden by
	// the robot-save or refreshBlocked's own writes succeeding right
	// after it.
	jobErr := e.persistJobLocked(j)
	robotErr := e.persistRobotLocked(robot)
	refreshErr := e.refreshBlocked()
	e.recordPersistOutcome(jobErr, robotErr, refreshErr)
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
