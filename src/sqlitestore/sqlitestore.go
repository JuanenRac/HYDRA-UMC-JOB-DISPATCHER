// HYDRA-UMC-JOB-DISPATCHER - sqlitestore package
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0 - see LICENSE
//
// The real dispatcher.Store implementation: SQLite via modernc.org/sqlite
// (pure Go, no CGO) - this ecosystem cross-compiles this binary for the
// CM5 (arm64 Linux) from Windows/other dev machines, and a CGO-based
// driver (e.g. mattn/go-sqlite3) would need a matching C cross-toolchain
// for every target instead of `GOOS=linux GOARCH=arm64 go build` alone.
// One real file on disk (or ":memory:" for tests) is the whole
// deployment story - no separate database server/container to run
// alongside this one process.
package sqlitestore

import (
	"database/sql"
	"encoding/json"
	"fmt"

	_ "modernc.org/sqlite"

	"github.com/JuanenRac/hydra-umc-job-dispatcher/src/dispatcher"
)

// Store is a dispatcher.Store backed by one real SQLite database.
type Store struct {
	db *sql.DB
}

// Open creates (if needed) and migrates a real SQLite database at path,
// returning a ready-to-use Store. path may be ":memory:" for a real,
// private, non-persistent database - useful for tests that want the
// real SQL code path without a real file on disk.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite database %q: %w", path, err)
	}
	// This engine's own Engine already serializes every mutation behind
	// one mutex - one real DB connection is enough, and avoids SQLite's
	// own well-known "database is locked" error under concurrent writers
	// that a connection pool would otherwise invite.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating sqlite database %q: %w", path, err)
	}
	return &Store{db: db}, nil
}

// Close releases the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

const schema = `
CREATE TABLE IF NOT EXISTS jobs (
	id             TEXT PRIMARY KEY,
	priority       INTEGER NOT NULL,
	required_tool  TEXT NOT NULL,
	depends_on     TEXT NOT NULL, -- JSON array of job IDs
	status         TEXT NOT NULL,
	assigned_robot TEXT NOT NULL,
	dedup_key      TEXT NOT NULL,
	seq            INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS robots (
	id        TEXT PRIMARY KEY,
	location  TEXT NOT NULL,
	tool      TEXT NOT NULL,
	available INTEGER NOT NULL,
	load      INTEGER NOT NULL
);
`

// SaveJob implements dispatcher.Store.
func (s *Store) SaveJob(record dispatcher.JobRecord) error {
	dependsOnJSON, err := json.Marshal(record.DependsOn)
	if err != nil {
		return fmt.Errorf("encoding DependsOn for job %q: %w", record.ID, err)
	}
	_, err = s.db.Exec(
		`INSERT INTO jobs (id, priority, required_tool, depends_on, status, assigned_robot, dedup_key, seq)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
			priority = excluded.priority,
			required_tool = excluded.required_tool,
			depends_on = excluded.depends_on,
			status = excluded.status,
			assigned_robot = excluded.assigned_robot,
			dedup_key = excluded.dedup_key,
			seq = excluded.seq`,
		record.ID, record.Priority, record.RequiredTool, string(dependsOnJSON), string(record.Status), record.AssignedRobot, record.DedupKey, record.Seq,
	)
	if err != nil {
		return fmt.Errorf("saving job %q: %w", record.ID, err)
	}
	return nil
}

// SaveRobot implements dispatcher.Store.
func (s *Store) SaveRobot(r dispatcher.Robot) error {
	available := 0
	if r.Available {
		available = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO robots (id, location, tool, available, load)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
			location = excluded.location,
			tool = excluded.tool,
			available = excluded.available,
			load = excluded.load`,
		r.ID, r.Location, r.Tool, available, r.Load,
	)
	if err != nil {
		return fmt.Errorf("saving robot %q: %w", r.ID, err)
	}
	return nil
}

// LoadAll implements dispatcher.Store.
func (s *Store) LoadAll() ([]dispatcher.JobRecord, []dispatcher.Robot, error) {
	jobs, err := s.loadJobs()
	if err != nil {
		return nil, nil, err
	}
	robots, err := s.loadRobots()
	if err != nil {
		return nil, nil, err
	}
	return jobs, robots, nil
}

func (s *Store) loadJobs() ([]dispatcher.JobRecord, error) {
	rows, err := s.db.Query(`SELECT id, priority, required_tool, depends_on, status, assigned_robot, dedup_key, seq FROM jobs`)
	if err != nil {
		return nil, fmt.Errorf("loading jobs: %w", err)
	}
	defer rows.Close()

	var records []dispatcher.JobRecord
	for rows.Next() {
		var (
			record        dispatcher.JobRecord
			dependsOnJSON string
			status        string
		)
		if err := rows.Scan(&record.ID, &record.Priority, &record.RequiredTool, &dependsOnJSON, &status, &record.AssignedRobot, &record.DedupKey, &record.Seq); err != nil {
			return nil, fmt.Errorf("scanning job row: %w", err)
		}
		if err := json.Unmarshal([]byte(dependsOnJSON), &record.DependsOn); err != nil {
			return nil, fmt.Errorf("decoding DependsOn for job %q: %w", record.ID, err)
		}
		record.Status = dispatcher.JobStatus(status)
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading jobs: %w", err)
	}
	return records, nil
}

func (s *Store) loadRobots() ([]dispatcher.Robot, error) {
	rows, err := s.db.Query(`SELECT id, location, tool, available, load FROM robots`)
	if err != nil {
		return nil, fmt.Errorf("loading robots: %w", err)
	}
	defer rows.Close()

	var robots []dispatcher.Robot
	for rows.Next() {
		var (
			r         dispatcher.Robot
			available int
		)
		if err := rows.Scan(&r.ID, &r.Location, &r.Tool, &available, &r.Load); err != nil {
			return nil, fmt.Errorf("scanning robot row: %w", err)
		}
		r.Available = available != 0
		robots = append(robots, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading robots: %w", err)
	}
	return robots, nil
}

var _ dispatcher.Store = (*Store)(nil)
