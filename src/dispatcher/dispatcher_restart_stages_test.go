// HYDRA-UMC-JOB-DISPATCHER - restart at every stage of a job, and cancellation
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0 - see LICENSE
package dispatcher_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/JuanenRac/hydra-umc-job-dispatcher/src/dispatcher"
	"github.com/JuanenRac/hydra-umc-job-dispatcher/src/sqlitestore"
)

// restart closes the engine's store and opens a genuinely separate store and
// engine on the same file, as a restarted process would.
func restart(t *testing.T, dbPath string, old *sqlitestore.Store) (*dispatcher.Engine, *sqlitestore.Store) {
	t.Helper()
	if old != nil {
		if err := old.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
	}
	store, err := sqlitestore.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	engine, err := dispatcher.NewEngineWithStore(store)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	return engine, store
}

func mustJob(t *testing.T, e *dispatcher.Engine, id string) dispatcher.Job {
	t.Helper()
	j, ok := e.Job(id)
	if !ok {
		t.Fatalf("job %q missing", id)
	}
	return j
}

// A job that was already handed to a robot must not be handed out again after
// a restart: it stays Assigned and DispatchOnce does not emit it a second time.
func TestRestart_AssignedJobIsNotDispatchedAgain(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "d.db")
	e, store := restart(t, dbPath, nil)
	e.UpsertRobot(dispatcher.Robot{ID: "r1", Tool: "PnP", Available: true})
	if err := e.AddJob(dispatcher.Job{ID: "unsafe-1", RequiredTool: "PnP"}); err != nil {
		t.Fatal(err)
	}
	if got := e.DispatchOnce(); len(got) != 1 {
		t.Fatalf("expected one assignment, got %+v", got)
	}

	e, store = restart(t, dbPath, store)
	defer store.Close()
	if j := mustJob(t, e, "unsafe-1"); j.Status != dispatcher.StatusAssigned || j.AssignedRobot != "r1" {
		t.Fatalf("assignment lost across the restart: %+v", j)
	}
	e.UpsertRobot(dispatcher.Robot{ID: "r2", Tool: "PnP", Available: true})
	if got := e.DispatchOnce(); len(got) != 0 {
		t.Fatalf("an assigned job was dispatched again after a restart: %+v", got)
	}
}

// The robot that was running the job can still report the outcome after the
// dispatcher restarted, and that outcome sticks.
func TestRestart_CompletionAfterRestartIsHonouredAndDurable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "d.db")
	e, store := restart(t, dbPath, nil)
	e.UpsertRobot(dispatcher.Robot{ID: "r1", Tool: "PnP", Available: true})
	_ = e.AddJob(dispatcher.Job{ID: "a", RequiredTool: "PnP"})
	e.DispatchOnce()

	e, store = restart(t, dbPath, store)
	if err := e.CompleteJob("a", true, "r1"); err != nil {
		t.Fatalf("complete after restart: %v", err)
	}
	e, store = restart(t, dbPath, store)
	defer store.Close()
	if j := mustJob(t, e, "a"); j.Status != dispatcher.StatusDone {
		t.Fatalf("outcome not durable: %+v", j)
	}
}

// A dependency that failed keeps its dependents unreachable across a restart.
func TestRestart_FailedDependencyKeepsDependentsUnreachable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "d.db")
	e, store := restart(t, dbPath, nil)
	e.UpsertRobot(dispatcher.Robot{ID: "r1", Tool: "PnP", Available: true})
	_ = e.AddJob(dispatcher.Job{ID: "pick", RequiredTool: "PnP"})
	_ = e.AddJob(dispatcher.Job{ID: "place", RequiredTool: "PnP", DependsOn: []string{"pick"}})
	e.DispatchOnce()
	if err := e.CompleteJob("pick", false, "r1"); err != nil {
		t.Fatal(err)
	}

	e, store = restart(t, dbPath, store)
	defer store.Close()
	if j := mustJob(t, e, "place"); j.Status != dispatcher.StatusUnreachable {
		t.Fatalf("expected unreachable after restart, got %+v", j)
	}
	e.UpsertRobot(dispatcher.Robot{ID: "r2", Tool: "PnP", Available: true})
	if got := e.DispatchOnce(); len(got) != 0 {
		t.Fatalf("an unreachable job was dispatched: %+v", got)
	}
}

// A submission deduplicated by its key is still deduplicated after a restart.
func TestRestart_DedupKeySurvives(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "d.db")
	e, store := restart(t, dbPath, nil)
	if _, _, err := e.SubmitJob(dispatcher.Job{ID: "j1", DedupKey: "req-42"}); err != nil {
		t.Fatal(err)
	}

	e, store = restart(t, dbPath, store)
	defer store.Close()
	job, result, err := e.SubmitJob(dispatcher.Job{ID: "j2", DedupKey: "req-42"})
	if err != nil {
		t.Fatal(err)
	}
	if result == dispatcher.SubmitCreated || job.ID != "j1" {
		t.Fatalf("the same request created a second job after a restart: result=%q job=%+v", result, job)
	}
}

// A job whose outcome became unknown stays unknown across a restart, and a
// later genuine report from the robot still resolves it.
func TestRestart_UnknownJobStaysUnknownUntilTheRobotReports(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "d.db")
	e, store := restart(t, dbPath, nil)
	e.UpsertRobot(dispatcher.Robot{ID: "r1", Tool: "PnP", Available: true})
	_ = e.AddJob(dispatcher.Job{ID: "a", RequiredTool: "PnP"})
	e.DispatchOnce()
	time.Sleep(20 * time.Millisecond)
	if stale := e.DetectStaleAssignments(time.Millisecond); len(stale) != 1 {
		t.Fatalf("expected one stale job, got %v", stale)
	}

	e, store = restart(t, dbPath, store)
	defer store.Close()
	if j := mustJob(t, e, "a"); j.Status != dispatcher.StatusUnknown {
		t.Fatalf("unknown status lost: %+v", j)
	}
	if err := e.CompleteJob("a", true, "r1"); err != nil {
		t.Fatalf("late report refused: %v", err)
	}
}

func TestCancel_PendingJobIsNeverDispatchedAndStaysCancelledAfterRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "d.db")
	e, store := restart(t, dbPath, nil)
	_ = e.AddJob(dispatcher.Job{ID: "a", RequiredTool: "PnP"})
	if err := e.CancelJob("a"); err != nil {
		t.Fatal(err)
	}
	if err := e.CancelJob("a"); err != nil {
		t.Fatalf("cancelling twice must be safe: %v", err)
	}

	e, store = restart(t, dbPath, store)
	defer store.Close()
	e.UpsertRobot(dispatcher.Robot{ID: "r1", Tool: "PnP", Available: true})
	if got := e.DispatchOnce(); len(got) != 0 {
		t.Fatalf("a cancelled job was dispatched: %+v", got)
	}
	if j := mustJob(t, e, "a"); j.Status != dispatcher.StatusCancelled {
		t.Fatalf("cancellation lost: %+v", j)
	}
}

func TestCancel_DependentsBecomeUnreachable(t *testing.T) {
	e := dispatcher.NewEngine()
	_ = e.AddJob(dispatcher.Job{ID: "pick"})
	_ = e.AddJob(dispatcher.Job{ID: "place", DependsOn: []string{"pick"}})
	_ = e.AddJob(dispatcher.Job{ID: "weld", DependsOn: []string{"place"}})
	if err := e.CancelJob("pick"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"place", "weld"} {
		if j := mustJob(t, e, id); j.Status != dispatcher.StatusUnreachable {
			t.Fatalf("%s should be unreachable, got %+v", id, j)
		}
	}
}

func TestCancel_RefusesAJobThatMayBeRunning(t *testing.T) {
	e := dispatcher.NewEngine()
	e.UpsertRobot(dispatcher.Robot{ID: "r1", Tool: "PnP", Available: true})
	_ = e.AddJob(dispatcher.Job{ID: "a", RequiredTool: "PnP"})
	e.DispatchOnce()
	err := e.CancelJob("a")
	if !errors.Is(err, dispatcher.ErrJobInFlight) {
		t.Fatalf("expected ErrJobInFlight, got %v", err)
	}
	if j := mustJob(t, e, "a"); j.Status != dispatcher.StatusAssigned {
		t.Fatalf("a refused cancellation changed the job: %+v", j)
	}
}

func TestCancel_RefusesFinishedAndUnknownJobs(t *testing.T) {
	e := dispatcher.NewEngine()
	e.UpsertRobot(dispatcher.Robot{ID: "r1", Tool: "PnP", Available: true})
	_ = e.AddJob(dispatcher.Job{ID: "a", RequiredTool: "PnP"})
	e.DispatchOnce()
	_ = e.CompleteJob("a", true, "r1")
	if err := e.CancelJob("a"); !errors.Is(err, dispatcher.ErrJobFinished) {
		t.Fatalf("expected ErrJobFinished, got %v", err)
	}
	if err := e.CancelJob("nope"); !errors.Is(err, dispatcher.ErrUnknownJob) {
		t.Fatalf("expected ErrUnknownJob, got %v", err)
	}
}
