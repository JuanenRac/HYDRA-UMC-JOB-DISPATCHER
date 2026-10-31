// HYDRA-UMC-JOB-DISPATCHER - tests of the confirmed stop before a job of a silent robot is reassigned
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package dispatcher

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

const testAckTimeout = 10 * time.Second

// stopEngine builds an engine with replanning for silent robots on and one job given to a silent
// robot r1, on a simulated clock: the first sweep happens an hour after the last heartbeat.
func stopEngine(t *testing.T, capable, allowUnconfirmed bool) (*Engine, *fakeClock) {
	t.Helper()
	e, clock := replanEngine(ReplanConfig{Enabled: true, OnStale: true, StopAckTimeout: testAckTimeout, AllowUnconfirmed: allowUnconfirmed})
	e.UpsertRobot(Robot{ID: "r1", Available: true, StopCapable: capable})
	mustJobPlain(t, e, Job{ID: "j"})
	e.DispatchOnce()
	clock.t = clock.t.Add(time.Hour)
	return e, clock
}

func jobState(t *testing.T, e *Engine) Job {
	t.Helper()
	j, ok := e.Job("j")
	if !ok {
		t.Fatal("job j is gone")
	}
	return j
}

// A silent robot that did not declare the capability cannot be asked: the job goes back at once,
// whatever the switch says (the behaviour of replanning before the stop request existed).
func TestStopConfirm_RobotWithoutCapabilityKeepsPlainReplanning(t *testing.T) {
	for _, allow := range []bool{false, true} {
		e, _ := stopEngine(t, false, allow)
		res := e.DetectStaleAssignmentsStop(time.Minute)
		if !reflect.DeepEqual(res.Requeued, []string{"j"}) || len(res.Unknown) != 0 || len(res.StopRequested) != 0 {
			t.Fatalf("allow=%v: result = %+v, want j requeued", allow, res)
		}
		if got := jobState(t, e); got.Status != StatusPending || got.AssignedRobot != "" {
			t.Fatalf("allow=%v: job = %+v, want pending without a robot", allow, got)
		}
		if reqs := e.StopRequests(); len(reqs) != 0 {
			t.Fatalf("allow=%v: stop requests = %+v, want none", allow, reqs)
		}
	}
}

// With the capability the first sweep only asks: the job stays assigned and nothing is reassigned.
func TestStopConfirm_FirstSweepAsksAndKeepsTheJobAssigned(t *testing.T) {
	for _, allow := range []bool{false, true} {
		e, clock := stopEngine(t, true, allow)
		res := e.DetectStaleAssignmentsStop(time.Minute)
		if !reflect.DeepEqual(res.StopRequested, []string{"j"}) || len(res.Requeued) != 0 || len(res.Unknown) != 0 {
			t.Fatalf("allow=%v: result = %+v, want only a stop request for j", allow, res)
		}
		if got := jobState(t, e); got.Status != StatusAssigned || got.AssignedRobot != "r1" {
			t.Fatalf("allow=%v: job = %+v, want still assigned to r1", allow, got)
		}
		reqs := e.StopRequests()
		if len(reqs) != 1 || reqs[0].JobID != "j" || reqs[0].RobotID != "r1" || reqs[0].Expired || reqs[0].RequestedAtMs != clock.t.UnixMilli() {
			t.Fatalf("allow=%v: stop requests = %+v", allow, reqs)
		}
		// before the time is up another sweep neither asks again nor decides
		clock.t = clock.t.Add(testAckTimeout - time.Second)
		res = e.DetectStaleAssignmentsStop(time.Minute)
		if len(res.StopRequested)+len(res.Requeued)+len(res.Unknown) != 0 {
			t.Fatalf("allow=%v: sweep before the timeout = %+v, want nothing", allow, res)
		}
		if got := jobState(t, e); got.Status != StatusAssigned {
			t.Fatalf("allow=%v: job = %+v, want still assigned", allow, got)
		}
	}
}

// Acknowledged in time: the job goes back to the queue, whatever the switch says.
func TestStopConfirm_AcknowledgedInTimeRequeues(t *testing.T) {
	for _, allow := range []bool{false, true} {
		e, clock := stopEngine(t, true, allow)
		e.DetectStaleAssignmentsStop(time.Minute)
		clock.t = clock.t.Add(2 * time.Second)
		if err := e.AckStop("r1", "j"); err != nil {
			t.Fatalf("allow=%v: AckStop: %v", allow, err)
		}
		if got := jobState(t, e); got.Status != StatusPending || got.AssignedRobot != "" || got.Attempt != 1 {
			t.Fatalf("allow=%v: job = %+v, want pending, no robot, attempt kept", allow, got)
		}
		if reqs := e.StopRequests(); len(reqs) != 0 {
			t.Fatalf("allow=%v: stop requests = %+v, want none", allow, reqs)
		}
		// the silent robot is held back and the job is not duplicated
		if got := e.DispatchOnce(); len(got) != 0 {
			t.Fatalf("allow=%v: dispatched %v, want nothing: the only robot is held unavailable", allow, got)
		}
		clock.t = clock.t.Add(time.Hour)
		res := e.DetectStaleAssignmentsStop(time.Minute)
		if len(res.StopRequested)+len(res.Requeued)+len(res.Unknown) != 0 {
			t.Fatalf("allow=%v: later sweep = %+v, want nothing", allow, res)
		}
	}
}

// Not acknowledged and the switch off (the default): the job is marked unknown, never reassigned.
func TestStopConfirm_NoAcknowledgementWithSwitchOffLeavesTheJobUnknown(t *testing.T) {
	e, clock := stopEngine(t, true, false)
	e.DetectStaleAssignmentsStop(time.Minute)
	clock.t = clock.t.Add(testAckTimeout)
	res := e.DetectStaleAssignmentsStop(time.Minute)
	if !reflect.DeepEqual(res.Unknown, []string{"j"}) || len(res.Requeued) != 0 {
		t.Fatalf("result = %+v, want j unknown", res)
	}
	if got := jobState(t, e); got.Status != StatusUnknown || got.AssignedRobot != "r1" {
		t.Fatalf("job = %+v, want unknown on r1", got)
	}
	if reqs := e.StopRequests(); len(reqs) != 1 || !reqs[0].Expired {
		t.Fatalf("stop requests = %+v, want one expired request", reqs)
	}
	if got := e.DispatchOnce(); len(got) != 0 {
		t.Fatalf("an unknown job must not be dispatched again, got %v", got)
	}
	// nothing more happens on its own
	clock.t = clock.t.Add(24 * time.Hour)
	if res := e.DetectStaleAssignmentsStop(time.Minute); len(res.Unknown)+len(res.Requeued)+len(res.StopRequested) != 0 {
		t.Fatalf("later sweep = %+v, want nothing", res)
	}
}

// Late acknowledgement after the job became unknown: it proves the robot stopped, so it goes back.
func TestStopConfirm_LateAcknowledgementResolvesAnUnknownJob(t *testing.T) {
	e, clock := stopEngine(t, true, false)
	e.DetectStaleAssignmentsStop(time.Minute)
	clock.t = clock.t.Add(testAckTimeout + time.Second)
	e.DetectStaleAssignmentsStop(time.Minute)
	if jobState(t, e).Status != StatusUnknown {
		t.Fatal("job should be unknown before the late acknowledgement")
	}
	clock.t = clock.t.Add(time.Minute)
	if err := e.AckStop("r1", "j"); err != nil {
		t.Fatalf("late AckStop: %v", err)
	}
	if got := jobState(t, e); got.Status != StatusPending || got.AssignedRobot != "" {
		t.Fatalf("job = %+v, want pending without a robot", got)
	}
	if err := e.AckStop("r1", "j"); !errors.Is(err, ErrNoStopRequest) {
		t.Fatalf("second ack err = %v, want ErrNoStopRequest", err)
	}
}

// Not acknowledged and the switch on: the job goes back anyway; a later acknowledgement changes nothing.
func TestStopConfirm_NoAcknowledgementWithSwitchOnRequeues(t *testing.T) {
	e, clock := stopEngine(t, true, true)
	e.DetectStaleAssignmentsStop(time.Minute)
	clock.t = clock.t.Add(testAckTimeout)
	res := e.DetectStaleAssignmentsStop(time.Minute)
	if !reflect.DeepEqual(res.Requeued, []string{"j"}) || len(res.Unknown) != 0 {
		t.Fatalf("result = %+v, want j requeued", res)
	}
	if got := jobState(t, e); got.Status != StatusPending || got.AssignedRobot != "" {
		t.Fatalf("job = %+v, want pending without a robot", got)
	}
	if err := e.AckStop("r1", "j"); !errors.Is(err, ErrNoStopRequest) {
		t.Fatalf("late ack after the requeue err = %v, want ErrNoStopRequest", err)
	}
	if got := jobState(t, e); got.Status != StatusPending {
		t.Fatalf("job = %+v, a late acknowledgement must not touch a requeued job", got)
	}
}

func TestStopConfirm_AcknowledgementErrors(t *testing.T) {
	e, _ := stopEngine(t, true, false)
	if err := e.AckStop("r1", "j"); !errors.Is(err, ErrNoStopRequest) {
		t.Fatalf("ack before any request: %v, want ErrNoStopRequest", err)
	}
	e.DetectStaleAssignmentsStop(time.Minute)
	if err := e.AckStop("r2", "j"); !errors.Is(err, ErrRobotMismatch) {
		t.Fatalf("ack by another robot: %v, want ErrRobotMismatch", err)
	}
	if err := e.AckStop("r1", "nope"); !errors.Is(err, ErrUnknownJob) {
		t.Fatalf("ack of an unknown job: %v, want ErrUnknownJob", err)
	}
	if got := jobState(t, e); got.Status != StatusAssigned {
		t.Fatalf("job = %+v, failed acknowledgements must change nothing", got)
	}
	off := NewEngine()
	if err := off.AckStop("r1", "j"); !errors.Is(err, ErrReplanOff) {
		t.Fatalf("ack with replanning off: %v, want ErrReplanOff", err)
	}
}

// The robot is heard again before it acknowledged: the request is withdrawn and the job carries on.
func TestStopConfirm_ARobotThatIsBackWithdrawsTheRequest(t *testing.T) {
	e, clock := stopEngine(t, true, true)
	e.DetectStaleAssignmentsStop(time.Minute)
	e.UpsertRobot(Robot{ID: "r1", Available: true, StopCapable: true})
	e.DetectStaleAssignmentsStop(time.Minute)
	if reqs := e.StopRequests(); len(reqs) != 0 {
		t.Fatalf("stop requests = %+v, want none: the robot is back", reqs)
	}
	clock.t = clock.t.Add(time.Hour)
	res := e.DetectStaleAssignmentsStop(time.Minute)
	if !reflect.DeepEqual(res.StopRequested, []string{"j"}) {
		t.Fatalf("a new silence must start a new request, got %+v", res)
	}
	if reqs := e.StopRequests(); len(reqs) != 1 || reqs[0].RequestedAtMs != clock.t.UnixMilli() {
		t.Fatalf("stop requests = %+v, want a fresh one", reqs)
	}
}

// Without the stale replanning (on_stale off) the capability changes nothing: unknown, as always.
func TestStopConfirm_NothingChangesWhileStaleReplanningIsOff(t *testing.T) {
	for _, cfg := range []ReplanConfig{{}, {Enabled: true, OnStale: false, OnFault: true}} {
		e, clock := replanEngine(cfg)
		e.UpsertRobot(Robot{ID: "r1", Available: true, StopCapable: true})
		mustJobPlain(t, e, Job{ID: "j"})
		e.DispatchOnce()
		clock.t = clock.t.Add(time.Hour)
		res := e.DetectStaleAssignmentsStop(time.Minute)
		if !reflect.DeepEqual(res.Unknown, []string{"j"}) || len(res.StopRequested)+len(res.Requeued) != 0 {
			t.Fatalf("cfg %+v: result = %+v, want j unknown", cfg, res)
		}
		if reqs := e.StopRequests(); len(reqs) != 0 {
			t.Fatalf("cfg %+v: stop requests = %+v, want none", cfg, reqs)
		}
	}
}

// The timeout comes from the configuration; zero means the documented default.
func TestStopConfirm_TimeoutFromConfigurationAndDefault(t *testing.T) {
	e, clock := replanEngine(ReplanConfig{Enabled: true, OnStale: true}) // no timeout: the default
	e.UpsertRobot(Robot{ID: "r1", Available: true, StopCapable: true})
	mustJobPlain(t, e, Job{ID: "j"})
	e.DispatchOnce()
	clock.t = clock.t.Add(time.Hour)
	e.DetectStaleAssignmentsStop(time.Minute)
	clock.t = clock.t.Add(DefaultStopAckTimeout - time.Second)
	if res := e.DetectStaleAssignmentsStop(time.Minute); len(res.Unknown) != 0 {
		t.Fatalf("decided before the default timeout: %+v", res)
	}
	clock.t = clock.t.Add(time.Second)
	if res := e.DetectStaleAssignmentsStop(time.Minute); !reflect.DeepEqual(res.Unknown, []string{"j"}) {
		t.Fatalf("not decided at the default timeout: %+v", res)
	}
}

// A partial heartbeat keeps the declared capability; a full one states it again.
func TestStopConfirm_CapabilityIsKeptByPartialHeartbeats(t *testing.T) {
	e := NewEngine()
	yes := true
	e.UpdateRobot(RobotUpdate{ID: "r1", StopCapable: &yes})
	e.UpdateRobot(RobotUpdate{ID: "r1"})
	if rs := e.Robots(); len(rs) != 1 || !rs[0].StopCapable {
		t.Fatalf("robots = %+v, want StopCapable kept", rs)
	}
	no := false
	e.UpdateRobot(RobotUpdate{ID: "r1", StopCapable: &no})
	if rs := e.Robots(); rs[0].StopCapable {
		t.Fatalf("robots = %+v, want StopCapable withdrawn", rs)
	}
}

func TestStopConfirm_ConfigurationFile(t *testing.T) {
	c, err := ParseConfig([]byte(`{"replan":{"enabled":true,"stop_ack_timeout_seconds":2.5,"allow_unconfirmed":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	if r := c.ReplanSetting(); r.StopAckTimeout != 2500*time.Millisecond || !r.AllowUnconfirmed || !r.OnStale {
		t.Fatalf("setting = %+v", r)
	}
	c, err = ParseConfig([]byte(`{"replan":{"enabled":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	if r := c.ReplanSetting(); r.StopAckTimeout != 0 || r.AllowUnconfirmed {
		t.Fatalf("defaults = %+v, want zero timeout (the default applies) and the switch off", r)
	}
	for _, bad := range []string{`{"replan":{"enabled":true,"stop_ack_timeout_seconds":0}}`, `{"replan":{"enabled":true,"stop_ack_timeout_seconds":-1}}`, `{"replan":{"enabled":true,"stop_ack_timeout_seconds":100000}}`} {
		if _, err := ParseConfig([]byte(bad)); err == nil {
			t.Errorf("%s accepted, want an error", bad)
		}
	}
}
