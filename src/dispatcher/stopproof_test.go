// HYDRA-UMC-JOB-DISPATCHER - tests of the kept stop acknowledgements (the proof a standby reads)
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package dispatcher

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// memAckStore is a Store that also keeps acknowledgements, with a switch to make saving them fail.
type memAckStore struct {
	acks []StopAckRecord
	fail error
}

func (m *memAckStore) SaveJob(JobRecord) error                { return nil }
func (m *memAckStore) SaveRobot(Robot) error                  { return nil }
func (m *memAckStore) LoadAll() ([]JobRecord, []Robot, error) { return nil, nil, nil }
func (m *memAckStore) SaveStopAck(a StopAckRecord) error {
	if m.fail != nil {
		return m.fail
	}
	m.acks = append(m.acks, a)
	return nil
}
func (m *memAckStore) LoadStopAcks() ([]StopAckRecord, error) { return m.acks, nil }

func proofEngine(t *testing.T, persist bool, store Store) (*Engine, *fakeClock) {
	t.Helper()
	clock := &fakeClock{t: time.Unix(10_000, 0)}
	var e *Engine
	if store != nil {
		var err error
		if e, err = NewEngineWithStore(store); err != nil {
			t.Fatal(err)
		}
	} else {
		e = NewEngine()
	}
	e.now = clock.now
	e.SetReplan(ReplanConfig{Enabled: true, OnStale: true, StopAckTimeout: testAckTimeout, PersistStopAcks: persist})
	e.UpsertRobot(Robot{ID: "r1", Available: true, StopCapable: true})
	mustJobPlain(t, e, Job{ID: "j"})
	e.DispatchOnce()
	clock.t = clock.t.Add(time.Hour)
	e.DetectStaleAssignmentsStop(time.Minute)
	return e, clock
}

func TestStopProof_OffByDefaultKeepsNothing(t *testing.T) {
	store := &memAckStore{}
	e, _ := proofEngine(t, false, store)
	if err := e.AckStop("r1", "j"); err != nil {
		t.Fatal(err)
	}
	if e.StopAckProofEnabled() || len(e.StopAcks()) != 0 || len(store.acks) != 0 {
		t.Fatalf("enabled=%v acks=%+v stored=%+v, want nothing kept", e.StopAckProofEnabled(), e.StopAcks(), store.acks)
	}
}

func TestStopProof_OnKeepsTheAcknowledgementInMemoryAndInTheStore(t *testing.T) {
	store := &memAckStore{}
	e, clock := proofEngine(t, true, store)
	if !e.StopAckProofEnabled() {
		t.Fatal("proof should be on")
	}
	if err := e.AckStop("r1", "j"); err != nil {
		t.Fatal(err)
	}
	got := e.StopAcks()
	if len(got) != 1 || got[0].JobID != "j" || got[0].RobotID != "r1" || got[0].AtMs != clock.t.UnixMilli() {
		t.Fatalf("acks = %+v", got)
	}
	if !reflect.DeepEqual(store.acks, got) {
		t.Fatalf("stored = %+v, want %+v", store.acks, got)
	}
}

// The relay: a dispatcher that restarts (or the one that takes over with the same database) still has the proof.
func TestStopProof_SurvivesARestart(t *testing.T) {
	store := &memAckStore{}
	e, _ := proofEngine(t, true, store)
	if err := e.AckStop("r1", "j"); err != nil {
		t.Fatal(err)
	}
	again, err := NewEngineWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.StopAcks(); !reflect.DeepEqual(got, e.StopAcks()) || len(got) != 1 {
		t.Fatalf("after restart acks = %+v, want %+v", got, e.StopAcks())
	}
}

// If the proof cannot be kept the acknowledgement is refused and the job stays where it was, so the robot sends it again.
func TestStopProof_AFailedSaveRefusesTheAcknowledgement(t *testing.T) {
	store := &memAckStore{fail: errors.New("disk full")}
	e, _ := proofEngine(t, true, store)
	if err := e.AckStop("r1", "j"); err == nil {
		t.Fatal("want an error")
	}
	if got := jobState(t, e); got.Status != StatusAssigned || got.AssignedRobot != "r1" {
		t.Fatalf("job = %+v, want still assigned to r1", got)
	}
	if len(e.StopAcks()) != 0 {
		t.Fatalf("acks = %+v, want none", e.StopAcks())
	}
	store.fail = nil
	if err := e.AckStop("r1", "j"); err != nil {
		t.Fatalf("second try: %v", err)
	}
}

func TestStopProof_WithoutAStoreItIsKeptInMemoryOnly(t *testing.T) {
	e, _ := proofEngine(t, true, nil)
	if err := e.AckStop("r1", "j"); err != nil {
		t.Fatal(err)
	}
	if len(e.StopAcks()) != 1 {
		t.Fatalf("acks = %+v", e.StopAcks())
	}
}

func TestStopProof_NewestAttemptWins(t *testing.T) {
	e := NewEngine()
	e.mu.Lock()
	e.noteStopAckLocked(StopAckRecord{JobID: "j", RobotID: "r1", Attempt: 2})
	e.noteStopAckLocked(StopAckRecord{JobID: "j", RobotID: "r1", Attempt: 1})
	e.mu.Unlock()
	if got := e.StopAcks(); len(got) != 1 || got[0].Attempt != 2 {
		t.Fatalf("acks = %+v", got)
	}
}

func TestStopProof_ConfigurationSwitch(t *testing.T) {
	c, err := ParseConfig([]byte(`{"replan":{"enabled":true,"persist_stop_acks":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !c.ReplanSetting().PersistStopAcks {
		t.Fatal("switch should be on")
	}
	c, _ = ParseConfig([]byte(`{"replan":{"enabled":true}}`))
	if c.ReplanSetting().PersistStopAcks {
		t.Fatal("switch should default to off")
	}
}
