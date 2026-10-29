// HYDRA-UMC-JOB-DISPATCHER - HTTP API tests
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// Real HTTP round-trips via httptest - actual JSON encoding/decoding
// through actual handler functions, not calls straight into the engine.
package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"net/http/httptest"
	"testing"

	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/dispatcher"
)

func post(t *testing.T, h http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestFullFlow_RegisterSubmitDispatchComplete(t *testing.T) {
	s := New(dispatcher.NewEngine())

	rec := post(t, s, "/robots", robotBody{ID: "robot-a", Tool: "PnP", Available: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /robots status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = post(t, s, "/jobs", jobRequest{ID: "job-1", Priority: 5, RequiredTool: "PnP"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /jobs status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = post(t, s, "/dispatch", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /dispatch status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var assignments []dispatcher.Assignment
	if err := json.Unmarshal(rec.Body.Bytes(), &assignments); err != nil {
		t.Fatalf("decoding /dispatch response: %v", err)
	}
	if len(assignments) != 1 || assignments[0].JobID != "job-1" || assignments[0].RobotID != "robot-a" {
		t.Fatalf("assignments = %+v, want job-1 assigned to robot-a", assignments)
	}

	rec = post(t, s, "/jobs/complete", completeRequest{ID: "job-1", Success: true, RobotID: "robot-a"})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /jobs/complete status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var updated dispatcher.Job
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decoding /jobs/complete response: %v", err)
	}
	if updated.Status != dispatcher.StatusDone {
		t.Fatalf("job-1 status after complete = %q, want done", updated.Status)
	}

	rec = get(t, s, "/robots")
	var robots []dispatcher.Robot
	if err := json.Unmarshal(rec.Body.Bytes(), &robots); err != nil {
		t.Fatalf("decoding /robots response: %v", err)
	}
	if len(robots) != 1 || !robots[0].Available {
		t.Fatalf("robots = %+v, want robot-a available again after job completion", robots)
	}
}

// the HTTP handler itself must require a real robotId, not just
// forward whatever the engine happens to accept.
func TestHandleCompleteJob_RejectsMissingRobotID(t *testing.T) {
	s := New(dispatcher.NewEngine())
	post(t, s, "/robots", robotBody{ID: "robot-a", Available: true})
	post(t, s, "/jobs", jobRequest{ID: "job-1"})
	post(t, s, "/dispatch", nil)

	rec := post(t, s, "/jobs/complete", completeRequest{ID: "job-1", Success: true})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 when robotId is missing, body = %s", rec.Code, rec.Body.String())
	}
}

// Same real check, exercised through the actual HTTP round-trip rather
// than calling the engine directly.
func TestHandleCompleteJob_RejectsAMismatchedRobotID(t *testing.T) {
	s := New(dispatcher.NewEngine())
	post(t, s, "/robots", robotBody{ID: "robot-a", Available: true})
	post(t, s, "/robots", robotBody{ID: "robot-b", Available: true})
	post(t, s, "/jobs", jobRequest{ID: "job-1", RequiredTool: ""})
	rec := post(t, s, "/dispatch", nil)
	var assignments []dispatcher.Assignment
	if err := json.Unmarshal(rec.Body.Bytes(), &assignments); err != nil {
		t.Fatalf("decoding /dispatch response: %v", err)
	}
	if len(assignments) != 1 {
		t.Fatalf("assignments = %+v, want exactly 1", assignments)
	}
	wrongRobot := "robot-b"
	if assignments[0].RobotID == wrongRobot {
		wrongRobot = "robot-a"
	}

	rec = post(t, s, "/jobs/complete", completeRequest{ID: "job-1", Success: true, RobotID: wrongRobot})
	// A mismatched robotId is a real conflict with the job's actual
	// assigned-robot state (both the job and the named robot genuinely
	// exist), not a malformed request - see completeJobErrorStatus.
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for a mismatched robotId, body = %s", rec.Code, rec.Body.String())
	}
}

// A job ID that was never submitted is a missing resource, not a
// malformed request - distinct from every other completeJobErrorStatus
// case below.
func TestHandleCompleteJob_UnknownJobIDReturns404(t *testing.T) {
	s := New(dispatcher.NewEngine())
	post(t, s, "/robots", robotBody{ID: "robot-a", Available: true})

	rec := post(t, s, "/jobs/complete", completeRequest{ID: "no-such-job", Success: true, RobotID: "robot-a"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown job ID, body = %s", rec.Code, rec.Body.String())
	}
}

// Completing a job that's already Done/Failed (i.e. was never Assigned/
// Unknown to begin with) is a real state conflict, not a 400 - the
// request itself is well-formed and names a real job.
func TestHandleCompleteJob_AlreadyCompletedJobReturns409(t *testing.T) {
	s := New(dispatcher.NewEngine())
	post(t, s, "/robots", robotBody{ID: "robot-a", Available: true})
	post(t, s, "/jobs", jobRequest{ID: "job-1"})
	post(t, s, "/dispatch", nil)

	rec := post(t, s, "/jobs/complete", completeRequest{ID: "job-1", Success: true, RobotID: "robot-a"})
	if rec.Code != http.StatusOK {
		t.Fatalf("first POST /jobs/complete status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = post(t, s, "/jobs/complete", completeRequest{ID: "job-1", Success: true, RobotID: "robot-a"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for completing an already-done job, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleJobs_RejectsMissingID(t *testing.T) {
	s := New(dispatcher.NewEngine())
	rec := post(t, s, "/jobs", jobRequest{Priority: 1})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a job with no id", rec.Code)
	}
}

func TestHandleJobs_RejectsWhitespaceIDAndSelfDependency(t *testing.T) {
	s := New(dispatcher.NewEngine())
	for _, request := range []jobRequest{
		{ID: "   "},
		{ID: "self", DependsOn: []string{"self"}},
	} {
		rec := post(t, s, "/jobs", request)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("POST /jobs invalid request status = %d, want 400, body = %s", rec.Code, rec.Body.String())
		}
	}
}

func TestHandleJobs_RejectsDuplicateID(t *testing.T) {
	s := New(dispatcher.NewEngine())
	post(t, s, "/jobs", jobRequest{ID: "job-1"})
	rec := post(t, s, "/jobs", jobRequest{ID: "job-1"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for a duplicate job id", rec.Code)
	}
}

func TestHandleSubmitJob_DuplicateDedupKeyReturns200Unchanged(t *testing.T) {
	s := New(dispatcher.NewEngine())

	rec := post(t, s, "/jobs/submit", submitRequest{ID: "job-1", Priority: 1, DedupKey: "req-abc"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("first POST /jobs/submit status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var first submitResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
		t.Fatalf("decoding first response: %v", err)
	}
	if first.Result != dispatcher.SubmitCreated {
		t.Fatalf("first Result = %q, want %q", first.Result, dispatcher.SubmitCreated)
	}

	// Same DedupKey, different client-generated ID - the real shape of a
	// retried HTTP request.
	rec = post(t, s, "/jobs/submit", submitRequest{ID: "job-1-retry", Priority: 1, DedupKey: "req-abc"})
	if rec.Code != http.StatusOK {
		t.Fatalf("second POST /jobs/submit status = %d, want 200 (duplicate, not created), body = %s", rec.Code, rec.Body.String())
	}
	var second submitResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &second); err != nil {
		t.Fatalf("decoding second response: %v", err)
	}
	if second.Result != dispatcher.SubmitDuplicate || second.ID != first.ID {
		t.Fatalf("second = %+v, want the original job-1 returned as a duplicate", second)
	}

	rec = get(t, s, "/jobs")
	var jobs []dispatcher.Job
	if err := json.Unmarshal(rec.Body.Bytes(), &jobs); err != nil {
		t.Fatalf("decoding /jobs response: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("GET /jobs returned %d job(s), want exactly 1 - the retry must not create a second job", len(jobs))
	}
}

func TestHandleSubmitJob_RetryAfterFailureRunsExactlyOnce(t *testing.T) {
	s := New(dispatcher.NewEngine())
	post(t, s, "/robots", robotBody{ID: "robot-a", Available: true})

	rec := post(t, s, "/jobs/submit", submitRequest{ID: "job-1", DedupKey: "req-abc"})
	var created submitResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	post(t, s, "/dispatch", nil)
	rec = post(t, s, "/jobs/complete", completeRequest{ID: created.ID, Success: false, RobotID: "robot-a"})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /jobs/complete (fail) status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = post(t, s, "/jobs/submit", submitRequest{ID: "job-1-retry", DedupKey: "req-abc"})
	if rec.Code != http.StatusOK {
		t.Fatalf("retry POST /jobs/submit status = %d, want 200 (retried, not created), body = %s", rec.Code, rec.Body.String())
	}
	var retried submitResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &retried); err != nil {
		t.Fatalf("decoding retry response: %v", err)
	}
	if retried.Result != dispatcher.SubmitRetried || retried.ID != created.ID || retried.Status != dispatcher.StatusPending {
		t.Fatalf("retried = %+v, want the same job reset to pending", retried)
	}

	rec = post(t, s, "/dispatch", nil)
	var assignments []dispatcher.Assignment
	_ = json.Unmarshal(rec.Body.Bytes(), &assignments)
	if len(assignments) != 1 || assignments[0].JobID != created.ID {
		t.Fatalf("assignments = %+v, want the retried job dispatched exactly once", assignments)
	}
}

func TestHandleSubmitJob_RejectsMissingID(t *testing.T) {
	s := New(dispatcher.NewEngine())
	rec := post(t, s, "/jobs/submit", submitRequest{Priority: 1})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a job with no id", rec.Code)
	}
}

func TestHandleSubmitJob_MethodNotAllowed(t *testing.T) {
	s := New(dispatcher.NewEngine())
	req := httptest.NewRequest(http.MethodGet, "/jobs/submit", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405 for GET /jobs/submit", rec.Code)
	}
}

func TestHandleJobs_MethodNotAllowed(t *testing.T) {
	s := New(dispatcher.NewEngine())
	req := httptest.NewRequest(http.MethodDelete, "/jobs", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405 for DELETE /jobs", rec.Code)
	}
}

func TestHandleDetectStale_MethodNotAllowed(t *testing.T) {
	s := New(dispatcher.NewEngine())
	req := httptest.NewRequest(http.MethodGet, "/jobs/detect-stale", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405 for GET /jobs/detect-stale", rec.Code)
	}
}

func TestHandleDetectStale_RejectsNonPositiveTimeout(t *testing.T) {
	s := New(dispatcher.NewEngine())
	for _, req := range []detectStaleRequest{{TimeoutSeconds: 0}, {TimeoutSeconds: -5}} {
		rec := post(t, s, "/jobs/detect-stale", req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("timeoutSeconds=%v: status = %d, want 400, body = %s", req.TimeoutSeconds, rec.Code, rec.Body.String())
		}
	}
}

func TestHandleDetectStale_ReportsNoneStaleRightAfterDispatch(t *testing.T) {
	// A real end-to-end round trip through the actual handler: no fake
	// clock available at this layer (dispatcher.Engine's own clock is
	// package-private, see the dispatcher package's own tests for the
	// timeout logic itself), but a real assignment made moments ago must
	// never be reported stale against any real, sane timeout.
	s := New(dispatcher.NewEngine())
	post(t, s, "/robots", robotBody{ID: "robot-a", Tool: "PnP", Available: true})
	post(t, s, "/jobs", jobRequest{ID: "job-1", RequiredTool: "PnP"})
	post(t, s, "/dispatch", nil)

	rec := post(t, s, "/jobs/detect-stale", detectStaleRequest{TimeoutSeconds: 300})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /jobs/detect-stale status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		UnknownJobIds []string `json:"unknownJobIds"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding /jobs/detect-stale response: %v", err)
	}
	if len(resp.UnknownJobIds) != 0 {
		t.Fatalf("unknownJobIds = %v, want none - job-1 was just assigned", resp.UnknownJobIds)
	}
}

func TestHandleHealth_ReportsHealthyWithNoStoreConfigured(t *testing.T) {
	s := New(dispatcher.NewEngine())
	rec := get(t, s, "/health")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Status != "ok" || !resp.PersistenceHealthy || resp.LastPersistError != nil {
		t.Fatalf("unexpected health response: %+v", resp)
	}
}

// brokenStore is a real dispatcher.Store whose robot writes always fail -
// used to prove GET /health surfaces a real degraded store, not just the
// happy path.
type brokenStore struct{}

func (brokenStore) SaveJob(dispatcher.JobRecord) error { return nil }
func (brokenStore) SaveRobot(dispatcher.Robot) error   { return errors.New("simulated disk-full error") }
func (brokenStore) LoadAll() ([]dispatcher.JobRecord, []dispatcher.Robot, error) {
	return nil, nil, nil
}

func TestHandleHealth_ReportsADegradedStore(t *testing.T) {
	engine, err := dispatcher.NewEngineWithStore(brokenStore{})
	if err != nil {
		t.Fatalf("NewEngineWithStore: %v", err)
	}
	s := New(engine)

	post(t, s, "/robots", robotBody{ID: "robot-a", Available: true})

	rec := get(t, s, "/health")
	var resp healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.PersistenceHealthy {
		t.Fatal("expected persistenceHealthy=false once the store starts failing")
	}
	if resp.LastPersistError == nil || *resp.LastPersistError == "" {
		t.Fatal("expected a real lastPersistError message")
	}
}

func TestHandleCancelJob_CancelsUnknownsAndRefusesInFlight(t *testing.T) {
	s := New(dispatcher.NewEngine())
	post(t, s, "/robots", robotBody{ID: "robot-a", Available: true})
	post(t, s, "/jobs", map[string]any{"id": "queued"})

	if rec := post(t, s, "/jobs/cancel", cancelRequest{ID: "queued"}); rec.Code != http.StatusOK {
		t.Fatalf("cancelling a queued job: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := post(t, s, "/jobs/cancel", cancelRequest{ID: "no-such-job"}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown job: status = %d, want 404", rec.Code)
	}
	if rec := post(t, s, "/jobs/cancel", cancelRequest{}); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing id: status = %d, want 400", rec.Code)
	}

	post(t, s, "/jobs", map[string]any{"id": "running"})
	post(t, s, "/dispatch", map[string]any{})
	if rec := post(t, s, "/jobs/cancel", cancelRequest{ID: "running"}); rec.Code != http.StatusConflict {
		t.Fatalf("an assigned job: status = %d, want 409, body = %s", rec.Code, rec.Body.String())
	}
}

// robotBody is what tests post to /robots: plain values, every field always sent.
type robotBody struct {
	ID        string `json:"id"`
	Location  string `json:"location"`
	Tool      string `json:"tool"`
	Available bool   `json:"available"`
}

func TestHandleHealth_Is503WhileTheStoreIsFailing(t *testing.T) {
	engine, err := dispatcher.NewEngineWithStore(brokenStore{})
	if err != nil {
		t.Fatalf("NewEngineWithStore: %v", err)
	}
	s := New(engine)
	if rec := get(t, s, "/health"); rec.Code != http.StatusOK {
		t.Fatalf("healthy /health = %d, want 200", rec.Code)
	}
	post(t, s, "/robots", robotBody{ID: "robot-a", Available: true})
	rec := get(t, s, "/health")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("degraded /health = %d, want 503", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "disk-full") {
		t.Fatalf("/health needs no token and must not echo the store's error text: %s", rec.Body.String())
	}
}

func TestHandleJobByIDAndRequeue(t *testing.T) {
	s := New(dispatcher.NewEngine())
	post(t, s, "/robots", map[string]any{"id": "robot-a", "available": true})
	post(t, s, "/jobs", map[string]any{"id": "j1"})
	post(t, s, "/dispatch", nil)
	if rec := get(t, s, "/jobs/j1"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "robot-a") {
		t.Fatalf("GET /jobs/j1 = %d %s", rec.Code, rec.Body.String())
	}
	if rec := get(t, s, "/jobs/nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("GET /jobs/nope = %d, want 404", rec.Code)
	}
	if rec := post(t, s, "/jobs/requeue", requeueRequest{ID: "j1", RobotID: "other"}); rec.Code != http.StatusConflict {
		t.Fatalf("requeue naming another robot = %d, want 409", rec.Code)
	}
	if rec := post(t, s, "/jobs/requeue", requeueRequest{ID: "j1", RobotID: "robot-a", Reason: "node lost"}); rec.Code != http.StatusOK {
		t.Fatalf("requeue = %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(t, s, "/jobs/requeue", requeueRequest{ID: "zzz"}); rec.Code != http.StatusNotFound {
		t.Fatalf("requeue of an unknown job = %d, want 404", rec.Code)
	}
	if rec := post(t, s, "/jobs/requeue", requeueRequest{}); rec.Code != http.StatusBadRequest {
		t.Fatalf("requeue without id = %d, want 400", rec.Code)
	}
}

func TestHandleRobots_ABareHeartbeatKeepsTheRobot(t *testing.T) {
	s := New(dispatcher.NewEngine())
	post(t, s, "/robots", map[string]any{"id": "arm-1", "tool": "PnP", "location": "cell-1", "available": true})
	rec := post(t, s, "/robots", map[string]any{"id": "arm-1"})
	var robots []dispatcher.Robot
	if err := json.Unmarshal(rec.Body.Bytes(), &robots); err != nil || len(robots) != 1 {
		t.Fatalf("decoding: %v %s", err, rec.Body.String())
	}
	if robots[0].Tool != "PnP" || robots[0].Location != "cell-1" || !robots[0].Available {
		t.Fatalf("robot after a bare heartbeat = %+v", robots[0])
	}
}

func TestDispatchWaiting_ExplainsWhyAJobHasNoRobotAndDispatchIsUnchanged(t *testing.T) {
	s := New(dispatcher.NewEngine())
	waiting := func() []dispatcher.WaitingJob {
		rec := get(t, s, "/dispatch/waiting")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /dispatch/waiting status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var out []dispatcher.WaitingJob
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decoding: %v", err)
		}
		return out
	}
	post(t, s, "/jobs", jobRequest{ID: "j1", Priority: 1, RequiredTool: "Laser"})
	if w := waiting(); len(w) != 1 || w[0].Reason != dispatcher.WaitNoRobots {
		t.Fatalf("no robots: %+v", w)
	}
	post(t, s, "/robots", robotBody{ID: "r1", Tool: "PnP", Available: true})
	if w := waiting(); w[0].Reason != dispatcher.WaitNoRobotWithTool {
		t.Fatalf("no robot with the tool: %+v", w)
	}
	post(t, s, "/robots", robotBody{ID: "r2", Tool: "Laser", Available: false})
	if w := waiting(); w[0].Reason != dispatcher.WaitRobotsBusy {
		t.Fatalf("busy: %+v", w)
	}
	post(t, s, "/robots", robotBody{ID: "r2", Tool: "Laser", Available: true})
	if w := waiting(); w[0].Reason != dispatcher.WaitForItsTurn {
		t.Fatalf("free robot: %+v", w)
	}
	rec := post(t, s, "/dispatch", nil)
	var assignments []dispatcher.Assignment
	if err := json.Unmarshal(rec.Body.Bytes(), &assignments); err != nil || len(assignments) != 1 {
		t.Fatalf("POST /dispatch must still answer a plain list of assignments: %v %s", err, rec.Body.String())
	}
	if w := waiting(); len(w) != 0 {
		t.Fatalf("an assigned job is no longer waiting: %+v", w)
	}
	if rec := post(t, s, "/dispatch/waiting", nil); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /dispatch/waiting = %d, want 405", rec.Code)
	}
}
