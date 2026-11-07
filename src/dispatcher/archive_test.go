// HYDRA-UMC-JOB-DISPATCHER - src/dispatcher/archive_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// A job that ended long ago leaves the live queue and stays in the archive of the store, without changing the verdict of the jobs that wait for it.
package dispatcher

import (
	"errors"
	"sort"
	"testing"
	"time"
)

// archivingStore is a recordingStore that also keeps an archive.
type archivingStore struct {
	*recordingStore
	archived   map[string]int64
	archiveErr error
}

func newArchivingStore() *archivingStore {
	return &archivingStore{recordingStore: newRecordingStore(), archived: map[string]int64{}}
}

func (s *archivingStore) ArchiveJobs(ids []string, atMs int64) error {
	if s.archiveErr != nil {
		return s.archiveErr
	}
	for _, id := range ids {
		s.archived[id] = atMs
	}
	return nil
}

func (s *archivingStore) LoadArchived(offset, limit int) ([]JobRecord, int, error) {
	var out []JobRecord
	for id := range s.archived {
		out = append(out, s.jobs[id])
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Seq < out[k].Seq })
	total := len(out)
	if offset > total {
		offset = total
	}
	out = out[offset:]
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out, total, nil
}

const dayDuration = 24 * time.Hour

func archiveEngine(t *testing.T, store Store) (*Engine, *fakeClock) {
	t.Helper()
	e, err := NewEngineWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	e.now = clock.now
	e.UpsertRobot(Robot{ID: "r1", Available: true})
	return e, clock
}

// finishJob runs one job to the end with the given outcome.
func finishJob(t *testing.T, e *Engine, id string, success bool) {
	t.Helper()
	e.DispatchOnce()
	j, ok := e.Job(id)
	if !ok || j.Status != StatusAssigned {
		t.Fatalf("job %s is %+v, want assigned", id, j)
	}
	if err := e.CompleteJob(id, success, j.AssignedRobot); err != nil {
		t.Fatal(err)
	}
	e.UpsertRobot(Robot{ID: "r1", Available: true})
}

func jobIDs(jobs []Job) []string {
	var out []string
	for _, j := range jobs {
		out = append(out, j.ID)
	}
	sort.Strings(out)
	return out
}

func TestArchive_ThirtyDaysByDefaultAndOnlyFinishedJobs(t *testing.T) {
	store := newArchivingStore()
	e, clock := archiveEngine(t, store)
	if e.archiveAfter != 30*dayDuration {
		t.Fatalf("a store-backed engine archives after %s, want 30 days", e.archiveAfter)
	}
	for _, id := range []string{"done-old", "failed-old", "done-recent", "pending"} {
		if err := e.AddJob(Job{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	finishJob(t, e, "done-old", true)
	finishJob(t, e, "failed-old", false)
	clock.t = clock.t.Add(20 * dayDuration)
	finishJob(t, e, "done-recent", true)
	clock.t = clock.t.Add(15 * dayDuration) // done-old and failed-old ended 35 days ago, done-recent 15

	archived, err := e.ArchiveFinished()
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(archived)
	if len(archived) != 2 || archived[0] != "done-old" || archived[1] != "failed-old" {
		t.Fatalf("archived %v, want done-old and failed-old", archived)
	}
	if got := jobIDs(e.Jobs()); len(got) != 2 || got[0] != "done-recent" || got[1] != "pending" {
		t.Fatalf("the live queue is %v, want done-recent and pending", got)
	}
	if _, ok := e.Job("done-old"); ok {
		t.Fatal("an archived job is still in the live queue")
	}
	if _, in := store.archived["done-old"]; !in {
		t.Fatal("the store was not told to archive done-old")
	}
	page, total, err := e.ArchivedPage(0, 0)
	if err != nil || total != 2 || len(page) != 2 {
		t.Fatalf("ArchivedPage = %v, %d, %v; want the 2 archived jobs", jobIDs(page), total, err)
	}
	page, total, err = e.ArchivedPage(1, 1)
	if err != nil || total != 2 || len(page) != 1 {
		t.Fatalf("ArchivedPage(1,1) = %v, %d, %v", jobIDs(page), total, err)
	}
	// nothing more to do on the next look
	if again, err := e.ArchiveFinished(); err != nil || len(again) != 0 {
		t.Fatalf("a second look archived %v, %v", again, err)
	}
}

func TestArchive_NeverUnreachableUnknownOrAssignedJobsAndNeverWhenOff(t *testing.T) {
	store := newArchivingStore()
	e, clock := archiveEngine(t, store)
	if err := e.AddJob(Job{ID: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := e.AddJob(Job{ID: "b", DependsOn: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	finishJob(t, e, "a", false) // b becomes unreachable
	clock.t = clock.t.Add(400 * dayDuration)
	archived, err := e.ArchiveFinished()
	if err != nil || len(archived) != 0 {
		t.Fatalf("archived %v, %v: a failed job that an unreachable job waits for must stay", archived, err)
	}
	if j, _ := e.Job("b"); j.Status != StatusUnreachable {
		t.Fatalf("b is %s, want unreachable", j.Status)
	}
	e.SetArchiveAfter(0)
	if archived, err := e.ArchiveFinished(); err != nil || len(archived) != 0 {
		t.Fatalf("with the archive off %v, %v were archived", archived, err)
	}
}

func TestArchive_AChainThatEndedIsArchivedTogetherAndADependentThatLivesKeepsItsDependency(t *testing.T) {
	store := newArchivingStore()
	e, clock := archiveEngine(t, store)
	for _, j := range []Job{{ID: "first"}, {ID: "second", DependsOn: []string{"first"}}, {ID: "third", DependsOn: []string{"second"}}} {
		if err := e.AddJob(j); err != nil {
			t.Fatal(err)
		}
	}
	finishJob(t, e, "first", true)
	finishJob(t, e, "second", true)
	clock.t = clock.t.Add(40 * dayDuration)
	// third still waits to be dispatched: second must stay (third depends on it), and so must first (second depends on it)
	if archived, err := e.ArchiveFinished(); err != nil || len(archived) != 0 {
		t.Fatalf("archived %v, %v while third depends on second", archived, err)
	}
	finishJob(t, e, "third", true)
	clock.t = clock.t.Add(31 * dayDuration)
	archived, err := e.ArchiveFinished()
	if err != nil || len(archived) != 3 {
		t.Fatalf("archived %v, %v; want the whole chain", archived, err)
	}
	if len(e.Jobs()) != 0 {
		t.Fatalf("the queue still holds %v", jobIDs(e.Jobs()))
	}
}

func TestArchive_ADedupKeyOfAnArchivedJobIsFree(t *testing.T) {
	store := newArchivingStore()
	e, clock := archiveEngine(t, store)
	if _, _, err := e.SubmitJob(Job{ID: "order-1", DedupKey: "order-key"}); err != nil {
		t.Fatal(err)
	}
	finishJob(t, e, "order-1", true)
	clock.t = clock.t.Add(31 * dayDuration)
	if archived, err := e.ArchiveFinished(); err != nil || len(archived) != 1 {
		t.Fatalf("archived %v, %v", archived, err)
	}
	if _, in := e.dedupIndex["order-key"]; in {
		t.Fatal("the dedup index still points at an archived job")
	}
}

func TestArchive_AFailedStoreKeepsEverythingInTheQueue(t *testing.T) {
	store := newArchivingStore()
	e, clock := archiveEngine(t, store)
	if err := e.AddJob(Job{ID: "old"}); err != nil {
		t.Fatal(err)
	}
	finishJob(t, e, "old", true)
	clock.t = clock.t.Add(31 * dayDuration)
	store.archiveErr = errors.New("disk full")
	if archived, err := e.ArchiveFinished(); err == nil || len(archived) != 0 {
		t.Fatalf("archived %v, %v; want an error and nothing archived", archived, err)
	}
	if _, ok := e.Job("old"); !ok {
		t.Fatal("the job left the queue although the store refused to archive it")
	}
	store.archiveErr = nil
	if archived, err := e.ArchiveFinished(); err != nil || len(archived) != 1 {
		t.Fatalf("after the store recovered: %v, %v", archived, err)
	}
}

func TestArchive_WithoutADatabaseThereIsNoArchive(t *testing.T) {
	e := NewEngine()
	e.SetArchiveAfter(time.Hour)
	if archived, err := e.ArchiveFinished(); err != nil || archived != nil {
		t.Fatalf("an in-memory engine archived %v, %v", archived, err)
	}
	if _, _, err := e.ArchivedPage(0, 0); !errors.Is(err, ErrNoArchive) {
		t.Fatalf("ArchivedPage = %v, want ErrNoArchive", err)
	}
	// a store that cannot archive behaves the same
	plain, err := NewEngineWithStore(newRecordingStore())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plain.ArchivedPage(0, 0); !errors.Is(err, ErrNoArchive) {
		t.Fatalf("ArchivedPage on a store without an archive = %v, want ErrNoArchive", err)
	}
}
