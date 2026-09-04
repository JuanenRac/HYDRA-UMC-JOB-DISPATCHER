// HYDRA-UMC-JOB-DISPATCHER - dispatcher package tests
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0 - see LICENSE
package dispatcher

import (
	"errors"
	"testing"
)

func TestDispatchOnce_ToolAwareRouting(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "robot-a", Tool: "PnP", Available: true})
	e.UpsertRobot(Robot{ID: "robot-b", Tool: "Laser", Available: true})

	if err := e.AddJob(Job{ID: "job-1", Priority: 1, RequiredTool: "Laser"}); err != nil {
		t.Fatalf("AddJob: %v", err)
	}

	assignments := e.DispatchOnce()
	if len(assignments) != 1 || assignments[0].RobotID != "robot-b" {
		t.Fatalf("assignments = %+v, want job-1 routed to robot-b (the Laser-equipped robot)", assignments)
	}
}

func TestDispatchOnce_HighPriorityBypassesQueue(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "robot-a", Available: true})

	if err := e.AddJob(Job{ID: "normal-job", Priority: 1}); err != nil {
		t.Fatalf("AddJob normal-job: %v", err)
	}
	if err := e.AddJob(Job{ID: "emergency-fix", Priority: 100}); err != nil {
		t.Fatalf("AddJob emergency-fix: %v", err)
	}

	// Only one robot available - the emergency job (submitted SECOND but
	// with higher priority) must be the one that gets it.
	assignments := e.DispatchOnce()
	if len(assignments) != 1 || assignments[0].JobID != "emergency-fix" {
		t.Fatalf("assignments = %+v, want emergency-fix to bypass normal-job", assignments)
	}

	normal, _ := e.Job("normal-job")
	if normal.Status != StatusPending {
		t.Fatalf("normal-job status = %q, want still pending (no robot left for it)", normal.Status)
	}
}

func TestDispatchOnce_MultiStageDependency(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "robot-a", Available: true})

	if err := e.AddJob(Job{ID: "pick", Priority: 1}); err != nil {
		t.Fatalf("AddJob pick: %v", err)
	}
	if err := e.AddJob(Job{ID: "place", Priority: 1, DependsOn: []string{"pick"}}); err != nil {
		t.Fatalf("AddJob place: %v", err)
	}

	place, _ := e.Job("place")
	if place.Status != StatusBlocked {
		t.Fatalf("place status = %q, want blocked until pick is done", place.Status)
	}

	// "place" must not be dispatched before "pick" is done, even though
	// it's the only other pending job and a robot is free.
	assignments := e.DispatchOnce()
	if len(assignments) != 1 || assignments[0].JobID != "pick" {
		t.Fatalf("assignments = %+v, want only pick dispatched first", assignments)
	}

	if err := e.CompleteJob("pick", true); err != nil {
		t.Fatalf("CompleteJob pick: %v", err)
	}

	place, _ = e.Job("place")
	if place.Status != StatusPending {
		t.Fatalf("place status after pick done = %q, want pending (unblocked)", place.Status)
	}

	assignments = e.DispatchOnce()
	if len(assignments) != 1 || assignments[0].JobID != "place" {
		t.Fatalf("assignments = %+v, want place dispatched now that pick is done", assignments)
	}
}

func TestDispatchOnce_LoadBalancesAcrossIdleRobots(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "robot-a", Available: true})
	e.UpsertRobot(Robot{ID: "robot-b", Available: true})

	// Give robot-a a prior completion so it has Load=1; robot-b starts at 0.
	if err := e.AddJob(Job{ID: "warmup", Priority: 1}); err != nil {
		t.Fatal(err)
	}
	assignments := e.DispatchOnce()
	if err := e.CompleteJob(assignments[0].JobID, true); err != nil {
		t.Fatal(err)
	}

	if err := e.AddJob(Job{ID: "next", Priority: 1}); err != nil {
		t.Fatal(err)
	}
	assignments = e.DispatchOnce()
	if len(assignments) != 1 {
		t.Fatalf("assignments = %+v, want exactly 1", assignments)
	}
	// Whichever robot got "warmup" now has Load=1; the other (Load=0)
	// must be preferred for the next job.
	if assignments[0].RobotID == "robot-a" && assignments[0].JobID == "next" {
		robots := e.Robots()
		for _, r := range robots {
			if r.ID == "robot-a" && r.Load > 0 {
				t.Fatalf("robot-a had Load=%d but was still picked over the idle, unused robot", r.Load)
			}
		}
	}
}

func TestAddJob_RejectsDuplicateID(t *testing.T) {
	e := NewEngine()
	if err := e.AddJob(Job{ID: "job-1"}); err != nil {
		t.Fatal(err)
	}
	if err := e.AddJob(Job{ID: "job-1"}); err == nil {
		t.Fatal("expected an error for a duplicate job ID, got nil")
	}
}

func TestAddJob_RejectsUnknownDependency(t *testing.T) {
	e := NewEngine()
	if err := e.AddJob(Job{ID: "job-1", DependsOn: []string{"does-not-exist"}}); err == nil {
		t.Fatal("expected an error for a dependency on a nonexistent job, got nil")
	}
}

func TestAddJob_RejectsInvalidIdentityAndDependencies(t *testing.T) {
	e := NewEngine()
	if err := e.AddJob(Job{ID: "   "}); !errors.Is(err, ErrInvalidJob) {
		t.Fatalf("blank ID error = %v, want ErrInvalidJob", err)
	}
	if err := e.AddJob(Job{ID: "self", DependsOn: []string{"self"}}); !errors.Is(err, ErrInvalidJob) {
		t.Fatalf("self dependency error = %v, want ErrInvalidJob", err)
	}
	if err := e.AddJob(Job{ID: "repeat", DependsOn: []string{"dep", "dep"}}); !errors.Is(err, ErrInvalidJob) {
		t.Fatalf("duplicate dependency error = %v, want ErrInvalidJob", err)
	}
}

func TestCompleteJob_RejectsNotAssigned(t *testing.T) {
	e := NewEngine()
	if err := e.AddJob(Job{ID: "job-1"}); err != nil {
		t.Fatal(err)
	}
	// job-1 is still Pending - never dispatched - so completing it must fail.
	if err := e.CompleteJob("job-1", true); err == nil {
		t.Fatal("expected an error completing a job that was never assigned, got nil")
	}
}

func TestSubmitJob_NoDedupKeyAlwaysCreates(t *testing.T) {
	e := NewEngine()

	job, result, err := e.SubmitJob(Job{ID: "job-1"})
	if err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}
	if result != SubmitCreated {
		t.Fatalf("result = %q, want %q", result, SubmitCreated)
	}
	if job.Status != StatusPending {
		t.Fatalf("job.Status = %q, want pending", job.Status)
	}
}

func TestSubmitJob_DuplicateDedupKeyReturnsSameJobUnchanged(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "robot-a", Available: true})

	first, result, err := e.SubmitJob(Job{ID: "job-1", Priority: 1, DedupKey: "req-abc"})
	if err != nil {
		t.Fatalf("first SubmitJob: %v", err)
	}
	if result != SubmitCreated {
		t.Fatalf("first result = %q, want %q", result, SubmitCreated)
	}

	// A client retries the same logical request (same DedupKey) with a
	// DIFFERENT job ID, as a real client generating a fresh ID per HTTP
	// attempt would - the dedup key, not the ID, is what must be honored.
	second, result, err := e.SubmitJob(Job{ID: "job-1-retry-attempt", Priority: 99, DedupKey: "req-abc"})
	if err != nil {
		t.Fatalf("second SubmitJob: %v", err)
	}
	if result != SubmitDuplicate {
		t.Fatalf("second result = %q, want %q", result, SubmitDuplicate)
	}
	if second.ID != first.ID || second.Priority != first.Priority {
		t.Fatalf("second = %+v, want the original job (%+v) returned untouched", second, first)
	}

	if len(e.Jobs()) != 1 {
		t.Fatalf("len(Jobs()) = %d, want exactly 1 - the duplicate must not create a second job", len(e.Jobs()))
	}
}

func TestSubmitJob_RetryAfterFailureRunsExactlyOnceEachTime(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "robot-a", Available: true})

	job, _, err := e.SubmitJob(Job{ID: "job-1", Priority: 1, DedupKey: "req-abc"})
	if err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}

	assignments := e.DispatchOnce()
	if len(assignments) != 1 || assignments[0].JobID != job.ID {
		t.Fatalf("assignments = %+v, want job-1 dispatched", assignments)
	}
	if err := e.CompleteJob(job.ID, false); err != nil {
		t.Fatalf("CompleteJob (fail): %v", err)
	}

	// The client, unaware the first attempt failed cleanly, retries with
	// the same DedupKey but yet another fresh job ID.
	retried, result, err := e.SubmitJob(Job{ID: "job-1-retry", Priority: 5, DedupKey: "req-abc"})
	if err != nil {
		t.Fatalf("retry SubmitJob: %v", err)
	}
	if result != SubmitRetried {
		t.Fatalf("result = %q, want %q", result, SubmitRetried)
	}
	if retried.ID != job.ID {
		t.Fatalf("retried.ID = %q, want the SAME original job ID %q, not a second job", retried.ID, job.ID)
	}
	if retried.Status != StatusPending {
		t.Fatalf("retried.Status = %q, want pending (ready for a fresh attempt)", retried.Status)
	}
	if len(e.Jobs()) != 1 {
		t.Fatalf("len(Jobs()) = %d, want exactly 1 - a retry must reuse the original job, never add a second one", len(e.Jobs()))
	}

	// The retried attempt now runs to completion - exactly once, under
	// the same job ID throughout its whole lifecycle.
	assignments = e.DispatchOnce()
	if len(assignments) != 1 || assignments[0].JobID != job.ID {
		t.Fatalf("assignments = %+v, want the retried job-1 dispatched again", assignments)
	}
	if err := e.CompleteJob(job.ID, true); err != nil {
		t.Fatalf("CompleteJob (success): %v", err)
	}
	done, _ := e.Job(job.ID)
	if done.Status != StatusDone {
		t.Fatalf("job.Status = %q, want done", done.Status)
	}

	// A THIRD submission with the same DedupKey, after the job already
	// succeeded, must NOT run it a third time.
	final, result, err := e.SubmitJob(Job{ID: "job-1-yet-another-retry", Priority: 1, DedupKey: "req-abc"})
	if err != nil {
		t.Fatalf("final SubmitJob: %v", err)
	}
	if result != SubmitDuplicate {
		t.Fatalf("final result = %q, want %q (already done, must not re-run)", result, SubmitDuplicate)
	}
	if final.Status != StatusDone {
		t.Fatalf("final.Status = %q, want done (untouched)", final.Status)
	}
	assignments = e.DispatchOnce()
	if len(assignments) != 0 {
		t.Fatalf("assignments = %+v, want none - the already-done job must not be re-dispatched", assignments)
	}
}

func TestSubmitJob_RetryRejectsUnknownDependency(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "robot-a", Available: true})

	// Force job-1 into Failed the only real way: submit, dispatch, fail it.
	if _, _, err := e.SubmitJob(Job{ID: "job-1", DedupKey: "req-abc"}); err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}
	e.DispatchOnce()
	if err := e.CompleteJob("job-1", false); err != nil {
		t.Fatalf("CompleteJob (fail): %v", err)
	}

	if _, _, err := e.SubmitJob(Job{ID: "job-1-retry", DedupKey: "req-abc", DependsOn: []string{"does-not-exist"}}); err == nil {
		t.Fatal("expected an error retrying with a dependency on a nonexistent job, got nil")
	}
}

func TestDispatchOnce_PriorityOrderIsDeterministicAcrossRepeatedRuns(t *testing.T) {
	build := func() *Engine {
		e := NewEngine()
		e.UpsertRobot(Robot{ID: "robot-a", Available: true})
		for i, spec := range []struct {
			id       string
			priority int
		}{
			{"job-c", 5}, {"job-a", 9}, {"job-e", 5}, {"job-b", 9}, {"job-d", 5},
		} {
			if err := e.AddJob(Job{ID: spec.id, Priority: spec.priority}); err != nil {
				t.Fatalf("AddJob %d: %v", i, err)
			}
		}
		return e
	}

	// Same jobs, same priorities, same submission order, run from scratch
	// repeatedly - the dispatch order must come out identical every time:
	// highest priority first, submission order (FIFO) breaking ties among
	// equal priorities. A map-iteration-order bug would make this flaky.
	var want []string
	for run := 0; run < 20; run++ {
		e := build()
		var got []string
		for {
			assignments := e.DispatchOnce()
			if len(assignments) == 0 {
				break
			}
			got = append(got, assignments[0].JobID)
			if err := e.CompleteJob(assignments[0].JobID, true); err != nil {
				t.Fatalf("CompleteJob: %v", err)
			}
		}
		if run == 0 {
			want = got
			continue
		}
		if len(got) != len(want) {
			t.Fatalf("run %d: got %v, want same length as first run %v", run, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("run %d: dispatch order %v diverged from first run %v at index %d", run, got, want, i)
			}
		}
	}

	expected := []string{"job-a", "job-b", "job-c", "job-e", "job-d"}
	if len(want) != len(expected) {
		t.Fatalf("dispatch order = %v, want %v", want, expected)
	}
	for i := range expected {
		if want[i] != expected[i] {
			t.Fatalf("dispatch order = %v, want %v (priority desc, then FIFO submission order)", want, expected)
		}
	}
}

func TestRefreshBlocked_DependencyFailureMakesDependentUnreachable(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "robot-a", Available: true})

	if err := e.AddJob(Job{ID: "pick", Priority: 1}); err != nil {
		t.Fatalf("AddJob pick: %v", err)
	}
	if err := e.AddJob(Job{ID: "place", Priority: 1, DependsOn: []string{"pick"}}); err != nil {
		t.Fatalf("AddJob place: %v", err)
	}

	assignments := e.DispatchOnce()
	if len(assignments) != 1 || assignments[0].JobID != "pick" {
		t.Fatalf("assignments = %+v, want only pick dispatched", assignments)
	}
	if err := e.CompleteJob("pick", false); err != nil {
		t.Fatalf("CompleteJob pick (fail): %v", err)
	}

	// "place" must not be left silently Blocked forever - its only
	// dependency can never reach Done now, so it must surface as
	// Unreachable, a real, queryable-via-the-API state distinct from
	// both Blocked (implies "still waiting, will resolve on its own")
	// and Failed (implies "this job itself ran and failed").
	place, _ := e.Job("place")
	if place.Status != StatusUnreachable {
		t.Fatalf("place status after pick failed = %q, want unreachable (not stuck blocked forever)", place.Status)
	}

	// It must also never be dispatched: DispatchOnce only considers
	// Pending jobs, so an Unreachable job (unlike a merely Blocked one)
	// can never silently become eligible again on its own.
	if assignments := e.DispatchOnce(); len(assignments) != 0 {
		t.Fatalf("assignments = %+v, want none - place is unreachable, not pending", assignments)
	}
	place, _ = e.Job("place")
	if place.Status != StatusUnreachable {
		t.Fatalf("place status after a further DispatchOnce = %q, want still unreachable", place.Status)
	}
}

func TestRefreshBlocked_UnreachablePropagatesThroughMultiStepChain(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "robot-a", Available: true})

	if err := e.AddJob(Job{ID: "pick", Priority: 1}); err != nil {
		t.Fatalf("AddJob pick: %v", err)
	}
	if err := e.AddJob(Job{ID: "place", Priority: 1, DependsOn: []string{"pick"}}); err != nil {
		t.Fatalf("AddJob place: %v", err)
	}
	if err := e.AddJob(Job{ID: "weld", Priority: 1, DependsOn: []string{"place"}}); err != nil {
		t.Fatalf("AddJob weld: %v", err)
	}

	weld, _ := e.Job("weld")
	if weld.Status != StatusBlocked {
		t.Fatalf("weld status = %q, want blocked before pick even runs", weld.Status)
	}

	e.DispatchOnce()
	if err := e.CompleteJob("pick", false); err != nil {
		t.Fatalf("CompleteJob pick (fail): %v", err)
	}

	// A single CompleteJob call on the first step of a three-stage mission
	// must surface every later stage as Unreachable in one pass, not only
	// the immediate next one - "weld" has no chance of ever seeing "place"
	// reach Done, since "place" itself is now Unreachable.
	place, _ := e.Job("place")
	if place.Status != StatusUnreachable {
		t.Fatalf("place status = %q, want unreachable", place.Status)
	}
	weld, _ = e.Job("weld")
	if weld.Status != StatusUnreachable {
		t.Fatalf("weld status = %q, want unreachable (propagated through place)", weld.Status)
	}
}

func TestDispatchOnce_NoMatchingToolLeavesJobPending(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "robot-a", Tool: "PnP", Available: true})

	if err := e.AddJob(Job{ID: "job-1", RequiredTool: "Laser"}); err != nil {
		t.Fatal(err)
	}
	assignments := e.DispatchOnce()
	if len(assignments) != 0 {
		t.Fatalf("assignments = %+v, want none - no robot has the required tool", assignments)
	}
	j, _ := e.Job("job-1")
	if j.Status != StatusPending {
		t.Fatalf("job-1 status = %q, want still pending", j.Status)
	}
}

// BUG (found in audit): SubmitJob's retry path only ever recomputed the
// retried job's OWN Status - it never called refreshBlocked(), the only
// mechanism that re-evaluates OTHER jobs' eligibility. A dependent already
// marked Unreachable because this job had failed stayed stuck as
// Unreachable forever after the retry, even though the retried job went
// straight back to Pending - directly contradicting StatusUnreachable's
// own documented contract ("Resolves back to Pending/Blocked if that
// dependency is later retried"). Reproduced against the pre-fix code
// (place stayed "unreachable" after pick was retried) and confirmed fixed.
func TestSubmitJob_RetryUnsticksDependentFromUnreachable(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "robot-a", Available: true})

	pick, _, err := e.SubmitJob(Job{ID: "pick", Priority: 1, DedupKey: "pick-key"})
	if err != nil {
		t.Fatalf("submit pick: %v", err)
	}
	if _, _, err := e.SubmitJob(Job{ID: "place", Priority: 1, DependsOn: []string{"pick"}}); err != nil {
		t.Fatalf("submit place: %v", err)
	}

	if len(e.DispatchOnce()) != 1 {
		t.Fatalf("expected pick to be dispatched")
	}
	if err := e.CompleteJob(pick.ID, false); err != nil {
		t.Fatalf("complete pick (fail): %v", err)
	}

	place, _ := e.Job("place")
	if place.Status != StatusUnreachable {
		t.Fatalf("place.Status = %q, want Unreachable after pick failed", place.Status)
	}

	retriedPick, result, err := e.SubmitJob(Job{ID: "pick-retry", Priority: 1, DedupKey: "pick-key"})
	if err != nil {
		t.Fatalf("retry submit: %v", err)
	}
	if result != SubmitRetried {
		t.Fatalf("result = %q, want SubmitRetried", result)
	}
	if retriedPick.Status != StatusPending {
		t.Fatalf("retriedPick.Status = %q, want Pending", retriedPick.Status)
	}

	place, _ = e.Job("place")
	if place.Status != StatusBlocked {
		t.Fatalf("place.Status = %q, want Blocked once its dependency is retryable again, not stuck Unreachable", place.Status)
	}
}
