// HYDRA-UMC-JOB-DISPATCHER - tests of the stop acknowledgements in the database
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package sqlitestore

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/dispatcher"
)

func TestStopAcks_RoundTripAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	a1 := dispatcher.StopAckRecord{JobID: "b", RobotID: "r1", Attempt: 1, AtMs: 5}
	a2 := dispatcher.StopAckRecord{JobID: "a", RobotID: "r2", Attempt: 2, AtMs: 9}
	for _, a := range []dispatcher.StopAckRecord{a1, a2, {JobID: "a", RobotID: "r3", Attempt: 2, AtMs: 11}} {
		if err := s.SaveStopAck(a); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.LoadStopAcks()
	if err != nil {
		t.Fatal(err)
	}
	want := []dispatcher.StopAckRecord{{JobID: "a", RobotID: "r3", Attempt: 2, AtMs: 11}, a1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestStopAcks_EmptyAndClosed(t *testing.T) {
	s := openTestStore(t)
	if got, err := s.LoadStopAcks(); err != nil || len(got) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
	s.Close()
	if err := s.SaveStopAck(dispatcher.StopAckRecord{JobID: "x"}); err == nil {
		t.Fatal("want an error on a closed database")
	}
	if _, err := s.LoadStopAcks(); err == nil {
		t.Fatal("want an error on a closed database")
	}
}
