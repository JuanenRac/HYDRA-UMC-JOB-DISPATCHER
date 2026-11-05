// HYDRA-UMC-JOB-DISPATCHER - tests of the jobs of one robot and of the state document
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/dispatcher"
)

func stateFixture(t *testing.T) *Server {
	t.Helper()
	s := New(dispatcher.NewEngine())
	raw(t, s, http.MethodPost, "/robots", `{"id":"robot-a","available":true}`)
	raw(t, s, http.MethodPost, "/robots", `{"id":"robot-b","available":true}`)
	for _, id := range []string{"j1", "j2", "j3"} {
		raw(t, s, http.MethodPost, "/jobs", `{"id":"`+id+`"}`)
	}
	raw(t, s, http.MethodPost, "/dispatch", "")
	return s
}

func TestListJobs_RobotFilterReturnsOnlyThatRobotsJobs(t *testing.T) {
	s := stateFixture(t)
	var all []dispatcher.Job
	if err := json.Unmarshal(raw(t, s, http.MethodGet, "/jobs", "").Body.Bytes(), &all); err != nil {
		t.Fatal(err)
	}
	var robot string
	for _, j := range all {
		if j.AssignedRobot != "" {
			robot = j.AssignedRobot
		}
	}
	if robot == "" {
		t.Fatal("the fixture assigned nothing")
	}
	rec := raw(t, s, http.MethodGet, "/jobs?robot="+robot, "")
	var mine []dispatcher.Job
	if err := json.Unmarshal(rec.Body.Bytes(), &mine); err != nil || rec.Code != http.StatusOK || len(mine) == 0 {
		t.Fatalf("%d %v %+v", rec.Code, err, mine)
	}
	for _, j := range mine {
		if j.AssignedRobot != robot {
			t.Errorf("job %s belongs to %q, asked for %q", j.ID, j.AssignedRobot, robot)
		}
	}
	if rec.Header().Get("X-Total-Count") == "" {
		t.Error("the count header is missing")
	}
	rec = raw(t, s, http.MethodGet, "/jobs?robot=nobody", "")
	if strings.TrimSpace(rec.Body.String()) != "[]" || rec.Header().Get("X-Total-Count") != "0" {
		t.Errorf("an unknown robot must give an empty list: %s", rec.Body.String())
	}
	if rec := raw(t, s, http.MethodGet, "/jobs?robot="+strings.Repeat("x", 300), ""); rec.Code != http.StatusBadRequest {
		t.Errorf("a very long robot id = %d, want 400", rec.Code)
	}
}

func TestDispatchState_CountsAndByRobot(t *testing.T) {
	s := stateFixture(t)
	rec := raw(t, s, http.MethodGet, "/dispatch/state", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /dispatch/state = %d", rec.Code)
	}
	var st dispatcher.State
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Schema != dispatcher.StateSchema {
		t.Errorf("schema = %q", st.Schema)
	}
	total := 0
	for _, n := range st.Counts {
		total += n
	}
	if total != 3 {
		t.Errorf("counts add up to %d, want 3: %+v", total, st.Counts)
	}
	held := 0
	for _, ids := range st.ByRobot {
		held += len(ids)
	}
	if held != st.Counts["assigned"] {
		t.Errorf("by_robot holds %d jobs, %d are assigned", held, st.Counts["assigned"])
	}
	if rec := raw(t, s, http.MethodPost, "/dispatch/state", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, want 405", rec.Code)
	}
}
