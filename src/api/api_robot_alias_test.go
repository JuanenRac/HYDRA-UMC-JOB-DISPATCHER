// HYDRA-UMC-JOB-DISPATCHER - the camelCase aliases of the robot lists
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// The robot lists have always carried the field names of the Go structure; with the switch on they carry the camelCase names too, with the same
// values, and nothing that was there is removed or renamed.
package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/dispatcher"
)

func robotList(t *testing.T, s *Server, method, path, body string) []map[string]any {
	t.Helper()
	rec := raw(t, s, method, path, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s %s = %d %s", method, path, rec.Code, rec.Body.String())
	}
	var list []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("%s %s is not a list of robots: %v (%s)", method, path, err, rec.Body.String())
	}
	return list
}

func TestRobotAliases_OffByDefaultTheListIsExactlyWhatItWas(t *testing.T) {
	s := New(dispatcher.NewEngine())
	list := robotList(t, s, http.MethodPost, "/robots", `{"id":"arm-1","tool":"PnP","available":true}`)
	if len(list) != 1 {
		t.Fatalf("one robot expected, got %v", list)
	}
	for _, alias := range []string{"id", "location", "tool", "available", "load", "lastHeartbeat"} {
		if _, ok := list[0][alias]; ok {
			t.Errorf("the alias %q is there although the switch is off", alias)
		}
	}
	if list[0]["ID"] != "arm-1" || list[0]["Tool"] != "PnP" || list[0]["Available"] != true {
		t.Errorf("the original names are not as they were: %v", list[0])
	}
}

func TestRobotAliases_OnTheCamelCaseNamesCarryTheSameValuesAndNothingIsRemoved(t *testing.T) {
	s := New(dispatcher.NewEngine())
	s.SetRobotCamelAliases(true)
	for _, call := range []struct{ method, path, body string }{
		{http.MethodPost, "/robots", `{"id":"arm-1","tool":"PnP","location":"bench","available":true,"stopCapable":true}`},
		{http.MethodGet, "/robots", ""},
	} {
		list := robotList(t, s, call.method, call.path, call.body)
		if len(list) != 1 {
			t.Fatalf("%s %s: one robot expected, got %v", call.method, call.path, list)
		}
		robot := list[0]
		for original, alias := range robotAliasNames {
			value, ok := robot[original]
			if !ok {
				t.Errorf("%s %s: %q was removed", call.method, call.path, original)
				continue
			}
			if got, ok := robot[alias]; !ok || got != value {
				t.Errorf("%s %s: %q = %v, want the value of %q (%v)", call.method, call.path, alias, got, original, value)
			}
		}
		if robot["id"] != "arm-1" || robot["tool"] != "PnP" || robot["location"] != "bench" || robot["available"] != true || robot["stopCapable"] != true {
			t.Errorf("%s %s: unexpected values %v", call.method, call.path, robot)
		}
	}
}

func TestRobotAliases_AnEmptyListIsAnEmptyArrayWithTheSwitchOnOrOff(t *testing.T) {
	for _, on := range []bool{false, true} {
		s := New(dispatcher.NewEngine())
		s.SetRobotCamelAliases(on)
		rec := raw(t, s, http.MethodGet, "/robots", "")
		if strings.TrimSpace(rec.Body.String()) != "[]" {
			t.Errorf("switch %v: an empty list must be [], got %q", on, rec.Body.String())
		}
	}
}

func TestRobotAliases_TheRemovalOfARobotAnswersTheListWithTheAliasesToo(t *testing.T) {
	s := New(dispatcher.NewEngine())
	s.SetRobotCamelAliases(true)
	robotList(t, s, http.MethodPost, "/robots", `{"id":"arm-1","available":true}`)
	robotList(t, s, http.MethodPost, "/robots", `{"id":"arm-2","available":true}`)
	list := robotList(t, s, http.MethodDelete, "/robots/arm-1", "")
	if len(list) != 1 || list[0]["id"] != "arm-2" || list[0]["ID"] != "arm-2" {
		t.Fatalf("the list after the removal: %v", list)
	}
}
