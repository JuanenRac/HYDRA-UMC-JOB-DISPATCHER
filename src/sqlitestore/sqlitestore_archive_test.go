// HYDRA-UMC-JOB-DISPATCHER - src/sqlitestore/sqlitestore_archive_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// Archived jobs stay in the database, are not loaded back into the queue and are read apart.
package sqlitestore

import (
	"path/filepath"
	"testing"

	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/dispatcher"
)

func finished(id string, seq, completed int64) dispatcher.JobRecord {
	return dispatcher.JobRecord{Job: dispatcher.Job{ID: id, Status: dispatcher.StatusDone, DependsOn: []string{}, CompletedAtMs: completed}, Seq: seq}
}

func TestArchive_ArchivedJobsAreNotLoadedBackAndAreReadApartOldestFirst(t *testing.T) {
	store := openTestStore(t)
	for _, r := range []dispatcher.JobRecord{finished("c", 3, 300), finished("a", 1, 100), finished("b", 2, 200), finished("live", 4, 400)} {
		if err := store.SaveJob(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ArchiveJobs([]string{"a", "b", "c", "missing"}, 5000); err != nil {
		t.Fatal(err)
	}
	jobs, _, err := store.LoadAll()
	if err != nil || len(jobs) != 1 || jobs[0].ID != "live" {
		t.Fatalf("LoadAll = %+v, %v; want only the live job", jobs, err)
	}
	all, total, err := store.LoadArchived(0, 0)
	if err != nil || total != 3 || len(all) != 3 || all[0].ID != "a" || all[1].ID != "b" || all[2].ID != "c" {
		t.Fatalf("LoadArchived = %+v, %d, %v", all, total, err)
	}
	page, total, err := store.LoadArchived(1, 1)
	if err != nil || total != 3 || len(page) != 1 || page[0].ID != "b" {
		t.Fatalf("LoadArchived(1,1) = %+v, %d, %v", page, total, err)
	}
	if err := store.ArchiveJobs(nil, 1); err != nil {
		t.Fatalf("archiving nothing: %v", err)
	}
	if err := store.ArchiveJobs([]string{"a"}, 0); err == nil {
		t.Fatal("a time of zero must be refused")
	}
}

func TestArchive_SavingAnArchivedIDAgainPutsItBackInTheQueue(t *testing.T) {
	store := openTestStore(t)
	if err := store.SaveJob(finished("x", 1, 100)); err != nil {
		t.Fatal(err)
	}
	if err := store.ArchiveJobs([]string{"x"}, 5000); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveJob(finished("x", 9, 200)); err != nil {
		t.Fatal(err)
	}
	jobs, _, _ := store.LoadAll()
	_, total, _ := store.LoadArchived(0, 0)
	if len(jobs) != 1 || total != 0 {
		t.Fatalf("a job saved again must live in the queue: %d live, %d archived", len(jobs), total)
	}
}

func TestArchive_ADatabaseOfAnOlderVersionGainsTheColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("ALTER TABLE jobs DROP COLUMN archived_at_ms"); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveJob(finished("old", 1, 100)); err != nil {
		t.Fatal(err)
	}
	store.Close()
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopening a database without the column: %v", err)
	}
	defer reopened.Close()
	jobs, _, err := reopened.LoadAll()
	if err != nil || len(jobs) != 1 {
		t.Fatalf("LoadAll = %+v, %v; the old job must still be live", jobs, err)
	}
}
