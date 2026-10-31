// HYDRA-UMC-JOB-DISPATCHER - replanning: the jobs of a robot that failed go back to the queue
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package dispatcher

import (
	"errors"
	"fmt"
	"sort"
)

// ReplanConfig says what happens to the jobs of a robot that fails or goes silent. The zero value
// is "off": such a job then stays assigned (a fault) or becomes "unknown" (silence) until an
// operator or the robot itself resolves it, exactly as before.
type ReplanConfig struct {
	// Enabled turns replanning on. Off by default. Nothing below does anything while it is off.
	Enabled bool
	// OnStale sends the jobs of a robot that went silent (DetectStaleAssignments) back to the queue
	// instead of marking them "unknown".
	OnStale bool
	// OnFault sends the assigned job of a robot that reports itself unavailable in the middle of
	// the job back to the queue.
	OnFault bool
}

// ErrReplanOff is returned by ReplanRobot while replanning is switched off.
var ErrReplanOff = errors.New("replanning is switched off")

// SetReplan installs the replanning configuration (the zero value switches it off).
func (e *Engine) SetReplan(c ReplanConfig) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.replan = c
}

// ReplanEnabled reports whether replanning is switched on.
func (e *Engine) ReplanEnabled() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.replan.Enabled
}

// requeueJobLocked is the part RequeueJob and the replanning share: the job goes back to
// pending/blocked without a robot and the robot it was given to is held unavailable until its own
// next heartbeat says otherwise, so the same dead robot is not handed the job again. The job keeps
// its submission order (so the queue order is the one it had) and its Attempt count (so the report
// of the abandoned attempt is recognisable). Caller must hold e.mu.
func (e *Engine) requeueJobLocked(j *Job) (robotErr, jobErr error) {
	if robot, ok := e.robots[j.AssignedRobot]; ok {
		robot.Available = false
		robot.selfReportedUnavailable = true
		robotErr = e.persistRobotLocked(robot)
	}
	j.AssignedRobot = ""
	j.AssignedAtMs = 0
	j.Status = e.computeStatus(j)
	jobErr = e.persistJobLocked(j)
	return robotErr, jobErr
}

// jobsOfRobotLocked returns the jobs assigned to a robot (and, with includeUnknown, those whose
// outcome is unknown), in submission order, so the result never depends on map order.
func (e *Engine) jobsOfRobotLocked(robotID string, includeUnknown bool) []*Job {
	var out []*Job
	for _, j := range e.jobs {
		if j.AssignedRobot != robotID {
			continue
		}
		if j.Status == StatusAssigned || (includeUnknown && j.Status == StatusUnknown) {
			out = append(out, j)
		}
	}
	sort.Slice(out, func(i, k int) bool { return out[i].seq < out[k].seq })
	return out
}

// requeueAllLocked sends every given job back to the queue in one transaction and returns their
// IDs in the order given. Caller must hold e.mu and must not be inside a batch already.
func (e *Engine) requeueAllLocked(jobs []*Job, extra ...*Robot) []string {
	ids := make([]string, 0, len(jobs))
	var errs []error
	e.beginBatchLocked()
	for _, j := range jobs {
		ids = append(ids, j.ID)
		robotErr, jobErr := e.requeueJobLocked(j)
		errs = append(errs, robotErr, jobErr)
	}
	for _, r := range extra {
		errs = append(errs, e.persistRobotLocked(r))
	}
	errs = append(errs, e.commitBatchLocked())
	e.recordPersistOutcome(errs...)
	return ids
}

// ReplanRobot declares a robot failed or gone: it is held unavailable until its own next
// heartbeat says otherwise and every job assigned to it (or whose outcome is unknown) goes back
// to the queue, in submission order. The returned IDs are those jobs; calling it twice returns
// nothing the second time, and no job is ever duplicated or dropped. It only works while
// replanning is on (ErrReplanOff otherwise) and for a known robot (ErrUnknownRobot).
func (e *Engine) ReplanRobot(robotID string) ([]string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.replan.Enabled {
		return nil, ErrReplanOff
	}
	robot, ok := e.robots[robotID]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownRobot, robotID)
	}
	robot.Available = false
	robot.selfReportedUnavailable = true
	return e.requeueAllLocked(e.jobsOfRobotLocked(robotID, true), robot), nil
}
