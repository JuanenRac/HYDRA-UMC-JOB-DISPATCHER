// HYDRA-UMC-JOB-DISPATCHER - tests of the Operation intake of POST /jobs/submit
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// The common Operation of the SDK (what the semantic planner hands over) becomes a job, through the real handler.
package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/dispatcher"
)

func operationBody(id, status string, params string) string {
	return `{"schema_version":"1.0","operation_id":"` + id + `","correlation_id":"c","kind":"execute_plan","target":{"kind":"cell","id":"cell-2"},"status":"` + status +
		`","requested_at_utc":"2024-01-01T00:00:00Z","updated_at_utc":"2024-01-01T00:00:00Z","params":` + params + `}`
}

func TestOperationBecomesAJobWithTheSpecOfItsParams(t *testing.T) {
	s := New(dispatcher.NewEngine())
	rec := post(t, s, "/jobs/submit", json.RawMessage(operationBody("plan-abc", "received",
		`{"priority":4,"required_tool":"Laser","location":"cell-2","depends_on":[],"steps":[{"primitive":"MOVE_TO"}]}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var created submitResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID != "plan-abc" || created.Priority != 4 || created.RequiredTool != "Laser" || created.Location != "cell-2" {
		t.Fatalf("job = %+v", created.Job)
	}
	if created.Result != dispatcher.SubmitCreated {
		t.Fatalf("result = %v", created.Result)
	}
}

func TestPostingTheSameOperationAgainIsTheSameJob(t *testing.T) {
	s := New(dispatcher.NewEngine())
	body := json.RawMessage(operationBody("plan-abc", "received", `{"priority":1}`))
	if rec := post(t, s, "/jobs/submit", body); rec.Code != http.StatusCreated {
		t.Fatalf("first status = %d, body = %s", rec.Code, rec.Body.String())
	}
	rec := post(t, s, "/jobs/submit", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("second status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var jobs []dispatcher.Job
	if err := json.Unmarshal(get(t, s, "/jobs").Body.Bytes(), &jobs); err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("%d jobs, want 1", len(jobs))
	}
}

func TestAnOperationThatIsNotNewOrNotWellFormedIsRefused(t *testing.T) {
	s := New(dispatcher.NewEngine())
	cases := map[string]string{
		"state":              operationBody("plan-a", "sent", `{}`),
		"queued":             operationBody("plan-a", "queued", `{}`),
		"schema":             strings.Replace(operationBody("plan-a", "received", `{}`), `"1.0"`, `"2.0"`, 1),
		"empty id":           operationBody("", "received", `{}`),
		"no params":          `{"schema_version":"1.0","operation_id":"plan-a","status":"received"}`,
		"priority text":      operationBody("plan-a", "received", `{"priority":"high"}`),
		"priority fraction":  operationBody("plan-a", "received", `{"priority":1.5}`),
		"priority huge":      operationBody("plan-a", "received", `{"priority":99999999999}`),
		"tool number":        operationBody("plan-a", "received", `{"required_tool":3}`),
		"location number":    operationBody("plan-a", "received", `{"location":3}`),
		"depends_on text":    operationBody("plan-a", "received", `{"depends_on":"prep"}`),
		"depends_on numbers": operationBody("plan-a", "received", `{"depends_on":["a",1]}`),
		"id not a string":    `{"schema_version":"1.0","operation_id":7,"status":"received","params":{}}`,
	}
	for name, body := range cases {
		rec := post(t, s, "/jobs/submit", json.RawMessage(body))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, body = %s", name, rec.Code, rec.Body.String())
		}
	}
	var jobs []dispatcher.Job
	_ = json.Unmarshal(get(t, s, "/jobs").Body.Bytes(), &jobs)
	if len(jobs) != 0 {
		t.Fatalf("a refused operation left %d jobs", len(jobs))
	}
}

func TestAPlainJobIsReadAsBefore(t *testing.T) {
	s := New(dispatcher.NewEngine())
	rec := post(t, s, "/jobs/submit", submitRequest{ID: "job-1", Priority: 2, DedupKey: "k"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := post(t, s, "/jobs/submit", json.RawMessage(`[]`)); rec.Code != http.StatusBadRequest {
		t.Fatalf("a body that is not an object: status = %d", rec.Code)
	}
	if rec := post(t, s, "/jobs/submit", json.RawMessage(`{"priority":1}`)); rec.Code != http.StatusBadRequest {
		t.Fatalf("a job with no id: status = %d", rec.Code)
	}
}
