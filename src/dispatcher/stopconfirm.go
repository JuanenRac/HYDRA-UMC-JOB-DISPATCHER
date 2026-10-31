// HYDRA-UMC-JOB-DISPATCHER - confirmed stop before a job of a silent robot is given to another one
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package dispatcher

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// DefaultStopAckTimeout is how long a stop request waits for its acknowledgement when the
// configuration does not say (replan.stop_ack_timeout_seconds).
const DefaultStopAckTimeout = 30 * time.Second

// A robot that is silent is not necessarily stopped: it may still be executing the job. When
// replanning for silent robots is on and the robot declared that it can confirm a stop
// (Robot.StopCapable), the dispatcher does not hand its job to another robot at once. It records a
// stop request (listed by StopRequests, for the bridge of the robot to deliver) and waits for the
// robot's acknowledgement (AckStop) for at most the configured time:
//
//   - acknowledged: the job goes back to the queue (a confirmed stop is safe at any moment, so a
//     late acknowledgement counts too);
//   - not acknowledged in time and AllowUnconfirmed off (the default): the job becomes "unknown",
//     as when replanning is off, and stays that way until an operator or a late acknowledgement
//     resolves it;
//   - not acknowledged in time and AllowUnconfirmed on: the job goes back to the queue anyway.
//
// A silent robot that did not declare the capability cannot be asked and keeps the plain replanning
// behaviour: its job goes back to the queue.

// ErrNoStopRequest is returned by AckStop when no stop request is open for the job.
var ErrNoStopRequest = errors.New("no stop request is open for that job")

// StopRequest is one request to a silent robot to stop the job it was given.
type StopRequest struct {
	JobID         string `json:"jobId"`
	RobotID       string `json:"robotId"`
	RequestedAtMs int64  `json:"requestedAtMs"`
	// Expired is true once the acknowledgement did not arrive in time (the job is then unknown, or was
	// sent back to the queue under AllowUnconfirmed).
	Expired bool `json:"expired"`
}

type stopRequest struct {
	robotID   string
	attempt   int
	at        time.Time
	timeoutAt time.Time
	expired   bool
}

// StaleResult is the answer of DetectStaleAssignmentsStop.
type StaleResult struct {
	// Unknown: jobs just marked "unknown".
	Unknown []string
	// Requeued: jobs that went back to the queue.
	Requeued []string
	// StopRequested: jobs for which a stop request was just made and whose acknowledgement is awaited.
	StopRequested []string
}

type stopDecision int

const (
	stopRequeue stopDecision = iota
	stopRequested
	stopWaiting
	stopUnknown
)

func (e *Engine) stopAckTimeoutLocked() time.Duration {
	if e.replan.StopAckTimeout > 0 {
		return e.replan.StopAckTimeout
	}
	return DefaultStopAckTimeout
}

// stopDecisionLocked decides what the sweep does with the assigned job of a silent robot while
// replanning for silent robots is on. Caller must hold e.mu.
func (e *Engine) stopDecisionLocked(j *Job, now time.Time) stopDecision {
	robot := e.robots[j.AssignedRobot]
	if robot == nil || !robot.StopCapable {
		return stopRequeue
	}
	req := e.stopReq[j.ID]
	if req != nil && (req.robotID != j.AssignedRobot || req.attempt != j.Attempt) {
		req = nil // a request of an earlier assignment of this job
	}
	if req == nil {
		if e.stopReq == nil {
			e.stopReq = map[string]*stopRequest{}
		}
		e.stopReq[j.ID] = &stopRequest{robotID: j.AssignedRobot, attempt: j.Attempt, at: now, timeoutAt: now.Add(e.stopAckTimeoutLocked())}
		return stopRequested
	}
	if now.Before(req.timeoutAt) {
		return stopWaiting
	}
	req.expired = true
	if e.replan.AllowUnconfirmed {
		return stopRequeue
	}
	return stopUnknown
}

// StopRequests lists the open stop requests (including those that expired without acknowledgement
// and whose job is still unknown), in job order.
func (e *Engine) StopRequests() []StopRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := []StopRequest{}
	for id, r := range e.stopReq {
		j, ok := e.jobs[id]
		if !ok || (j.Status != StatusAssigned && j.Status != StatusUnknown) || j.AssignedRobot != r.robotID {
			continue
		}
		out = append(out, StopRequest{JobID: id, RobotID: r.robotID, RequestedAtMs: r.at.UnixMilli(), Expired: r.expired})
	}
	sort.Slice(out, func(i, k int) bool { return out[i].JobID < out[k].JobID })
	return out
}

// AckStop records that the robot confirmed it stopped the job: the job goes back to the queue, and
// the robot stays held unavailable until its own next heartbeat. It works for a job still assigned
// and for one that already became "unknown" because the acknowledgement was late. Only while
// replanning is on (ErrReplanOff), for a known job (ErrUnknownJob), given to that robot
// (ErrRobotMismatch) and with an open request (ErrNoStopRequest).
func (e *Engine) AckStop(robotID, jobID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.replan.Enabled {
		return ErrReplanOff
	}
	j, ok := e.jobs[jobID]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownJob, jobID)
	}
	if j.AssignedRobot != "" && j.AssignedRobot != robotID {
		return fmt.Errorf("%w: job %q is assigned to %q, not %q", ErrRobotMismatch, jobID, j.AssignedRobot, robotID)
	}
	req := e.stopReq[jobID]
	if (j.Status != StatusAssigned && j.Status != StatusUnknown) || req == nil || req.robotID != robotID || req.attempt != j.Attempt {
		return fmt.Errorf("%w: %q", ErrNoStopRequest, jobID)
	}
	e.requeueAllLocked([]*Job{j})
	return nil
}
