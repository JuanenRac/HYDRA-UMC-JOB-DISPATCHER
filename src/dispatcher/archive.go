// HYDRA-UMC-JOB-DISPATCHER - the archive of finished jobs
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package dispatcher

import (
	"errors"
	"fmt"
	"time"
)

// DefaultArchiveAfter is how long a finished job stays in the live queue before it is archived.
const DefaultArchiveAfter = 30 * 24 * time.Hour

// DefaultArchiveEvery is how often the service looks for finished jobs to archive.
const DefaultArchiveEvery = time.Hour

// ErrNoArchive is returned when the archive is asked for and the engine has no database that keeps one.
var ErrNoArchive = errors.New("no archive: the service keeps its queue in memory only")

// ArchiveStore is implemented by a Store that can keep finished jobs apart from the live queue. An archived job stays in the
// database, is not loaded back into the queue at the next start, and is read with LoadArchived.
type ArchiveStore interface {
	// ArchiveJobs marks the jobs as archived at atMs (unix milliseconds), in one transaction.
	ArchiveJobs(ids []string, atMs int64) error
	// LoadArchived returns the archived jobs, oldest finished first, skipping offset and returning at most limit (0 = all),
	// and how many are archived in total.
	LoadArchived(offset, limit int) ([]JobRecord, int, error)
}

// ArchiveConfig is the setting of the archive of finished jobs: a zero After turns the archive off.
type ArchiveConfig struct {
	After time.Duration
	Every time.Duration
}

// archivable reports whether a job is finished for good: done, failed or cancelled. An Unreachable job is not: it goes back to
// Pending the day the dependency it waits for is retried and succeeds.
func archivable(s JobStatus) bool {
	return s == StatusDone || s == StatusFailed || s == StatusCancelled
}

// SetArchiveAfter sets how long a finished job stays in the live queue before ArchiveFinished moves it to the archive (0 = never).
// It needs a Store that implements ArchiveStore; without one nothing is ever archived.
func (e *Engine) SetArchiveAfter(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.archiveAfter = d
}

// ArchiveFinished moves the jobs that ended more than the configured time ago out of the live queue and into the archive of the
// store, and returns their ids. The store is written first: if that fails nothing leaves the queue and the error is returned. A job
// that another job still in the queue depends on stays, so the verdict ("unreachable", "blocked") of the jobs that wait for it
// never changes because a record was archived; a chain whose every member ended is archived together.
func (e *Engine) ArchiveFinished() ([]string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	archive, ok := e.store.(ArchiveStore)
	if !ok || e.archiveAfter <= 0 {
		return nil, nil
	}
	nowMs := e.now().UnixMilli()
	cutoff := nowMs - e.archiveAfter.Milliseconds()
	candidates := map[string]struct{}{}
	for id, j := range e.jobs {
		if archivable(j.Status) && j.CompletedAtMs > 0 && j.CompletedAtMs <= cutoff {
			candidates[id] = struct{}{}
		}
	}
	// A candidate that a job outside the candidates depends on stays; leaving one candidate can free others that depended on it, so repeat.
	for changed := true; changed && len(candidates) > 0; {
		changed = false
		for _, j := range e.jobs {
			if _, in := candidates[j.ID]; in {
				continue
			}
			for _, dep := range j.DependsOn {
				if _, held := candidates[dep]; held {
					delete(candidates, dep)
					changed = true
				}
			}
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(candidates))
	for id := range candidates {
		ids = append(ids, id)
	}
	if err := archive.ArchiveJobs(ids, nowMs); err != nil {
		e.lastPersistErr = err
		return nil, fmt.Errorf("archiving %d finished jobs: %w", len(ids), err)
	}
	for _, id := range ids {
		j := e.jobs[id]
		if j.DedupKey != "" && e.dedupIndex[j.DedupKey] == id {
			delete(e.dedupIndex, j.DedupKey)
		}
		delete(e.stopReq, id)
		delete(e.jobs, id)
	}
	return ids, nil
}

// ArchivedPage returns the archived jobs, oldest finished first, skipping offset and returning at most limit (0 = all), and how many
// are archived in total. ErrNoArchive when the engine has no database that keeps one.
func (e *Engine) ArchivedPage(offset, limit int) ([]Job, int, error) {
	e.mu.Lock()
	store := e.store
	e.mu.Unlock()
	archive, ok := store.(ArchiveStore)
	if !ok {
		return nil, 0, ErrNoArchive
	}
	records, total, err := archive.LoadArchived(offset, limit)
	if err != nil {
		return nil, 0, err
	}
	out := make([]Job, 0, len(records))
	for _, r := range records {
		j := r.Job
		j.seq = r.Seq
		out = append(out, cloneJob(&j))
	}
	return out, total, nil
}
