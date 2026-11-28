package api

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/dispatcher"
)

// The routes of the hold against the contract shared with HYDRA-UMC-ORCHESTRATOR: testdata/job-dispatcher-hold.json is a byte copy of
// conformance/fixtures/v1/job-dispatcher-hold.json of HYDRA-UMC-SDK.

type holdCall struct {
	Method   string          `json:"method"`
	Path     string          `json:"path"`
	Status   int             `json:"status"`
	Response map[string]bool `json:"response"`
}

func TestHold_AnswersAsTheSharedContractSays(t *testing.T) {
	raw, err := os.ReadFile("testdata/job-dispatcher-hold.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Hold              holdCall `json:"hold"`
		State             holdCall `json:"state"`
		Release           holdCall `json:"release"`
		DispatchWhileHeld holdCall `json:"dispatchWhileHeld"`
		Refusals          struct {
			SwitchedOff int `json:"switchedOff"`
		} `json:"refusals"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	s := New(heldEngine(t))
	// the order of the contract that makes its answers true: nothing held yet, then held, then released
	for _, step := range []holdCall{contract.State, contract.Hold, contract.DispatchWhileHeld, contract.Release} {
		rec := withToken(t, s, step.Method, step.Path, "", nil)
		if rec.Code != step.Status {
			t.Fatalf("%s %s = %d, want %d", step.Method, step.Path, rec.Code, step.Status)
		}
		if step.Response == nil {
			continue
		}
		var got map[string]bool
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got["held"] != step.Response["held"] {
			t.Errorf("%s %s answered %s, want %v", step.Method, step.Path, rec.Body.String(), step.Response)
		}
	}
	off := New(dispatcher.NewEngine())
	if rec := withToken(t, off, http.MethodPost, contract.Hold.Path, "", nil); rec.Code != contract.Refusals.SwitchedOff {
		t.Errorf("switched off = %d, want %d", rec.Code, contract.Refusals.SwitchedOff)
	}
}
