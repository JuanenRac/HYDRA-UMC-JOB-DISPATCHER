// HYDRA-UMC-JOB-DISPATCHER - HTTP API
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// A minimal real JSON/HTTP surface over src/dispatcher.Engine, stdlib
// only (net/http) - no framework dependency for a handful of routes.
// Deliberately plain HTTP, matching the rest of the ecosystem's internal
// LAN traffic model (see HYDRA-UMC-SERVER's own documented CORS/mTLS
// trade-off) rather than gRPC: this is a human/ops-facing control surface
// (submit a job, register a robot, ask "what happened"), not node-to-node
// traffic - hydra.common.v1 stays reserved for that, per its own README.
package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/dispatcher"
)

// MaxBodyBytes is the largest request body any route reads. Every request here is a few hundred
// bytes of JSON; decoding without a limit let anything that could reach the port make the process
// allocate as much memory as it was sent.
const MaxBodyBytes = 64 * 1024

// Server wraps a dispatcher.Engine with HTTP handlers.
type Server struct {
	engine *dispatcher.Engine
	mux    *http.ServeMux
	token  string
}

// RequireToken makes every route except GET /health demand `Authorization: Bearer <token>`
// (compared in constant time). An empty token leaves the API open, as before - the caller
// (main.go) refuses that combination on an address other than loopback.
func (s *Server) RequireToken(token string) { s.token = token }

// New builds a Server ready to be used as an http.Handler.
func New(engine *dispatcher.Engine) *Server {
	s := &Server{engine: engine, mux: http.NewServeMux()}
	s.mux.HandleFunc("/jobs", s.handleJobs)
	s.mux.HandleFunc("/jobs/submit", s.handleSubmitJob)
	s.mux.HandleFunc("/jobs/complete", s.handleCompleteJob)
	s.mux.HandleFunc("/jobs/cancel", s.handleCancelJob)
	s.mux.HandleFunc("/jobs/requeue", s.handleRequeueJob)
	s.mux.HandleFunc("/jobs/", s.handleJobByID)
	s.mux.HandleFunc("/robots", s.handleRobots)
	s.mux.HandleFunc("/robots/", s.handleRobotByID)
	s.mux.HandleFunc("/dispatch", s.handleDispatch)
	s.mux.HandleFunc("/dispatch/waiting", s.handleWaiting)
	s.mux.HandleFunc("/jobs/detect-stale", s.handleDetectStale)
	s.mux.HandleFunc("/health", s.handleHealth)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.token != "" && r.URL.Path != "/health" && !s.authorized(r) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, errors.New("a valid Authorization: Bearer token is required"))
		return
	}
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) authorized(r *http.Request) bool {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return false
	}
	given := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	return subtle.ConstantTimeCompare([]byte(given), []byte(s.token)) == 1
}

// bodyErrorStatus is the status for a body that could not be decoded: 413 when it was over the
// limit, 400 otherwise.
func bodyErrorStatus(err error) int {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// jobRequest mirrors dispatcher.Job's caller-supplied fields only (Status/
// AssignedRobot are engine-owned, never accepted from a client).
type jobRequest struct {
	ID           string   `json:"id"`
	Priority     int      `json:"priority"`
	RequiredTool string   `json:"requiredTool"`
	Location     string   `json:"location"`
	DependsOn    []string `json:"dependsOn"`
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleListJobs(w, r)
	case http.MethodPost:
		var req jobRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, bodyErrorStatus(err), err)
			return
		}
		if req.ID == "" {
			writeError(w, http.StatusBadRequest, errors.New("\"id\" is required"))
			return
		}
		job := dispatcher.Job{
			ID:           req.ID,
			Priority:     req.Priority,
			RequiredTool: req.RequiredTool,
			Location:     req.Location,
			DependsOn:    req.DependsOn,
		}
		if err := s.engine.AddJob(job); err != nil {
			if errors.Is(err, dispatcher.ErrInvalidJob) || errors.Is(err, dispatcher.ErrUnknownDep) {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeError(w, http.StatusConflict, err)
			return
		}
		created, _ := s.engine.Job(req.ID)
		writeJSON(w, http.StatusCreated, created)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, http.StatusMethodNotAllowed, errors.New("use GET to list jobs or POST to submit one"))
	}
}

// handleListJobs answers GET /jobs. Without parameters it returns every job, as it always did.
// ?status=, ?limit= and ?offset= narrow it; X-Total-Count says how many jobs matched, so a client
// can read a long queue in pieces.
func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	status := dispatcher.JobStatus(query.Get("status"))
	if status != "" && !dispatcher.ValidStatus(status) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("unknown status %q", status))
		return
	}
	limit, offset := 0, 0
	for name, target := range map[string]*int{"limit": &limit, "offset": &offset} {
		if raw := query.Get(name); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 0 || (name == "limit" && n == 0) {
				writeError(w, http.StatusBadRequest, fmt.Errorf("%q must be a positive whole number", name))
				return
			}
			*target = n
		}
	}
	jobs, total := s.engine.JobsPage(status, offset, limit)
	w.Header().Set("X-Total-Count", strconv.Itoa(total))
	writeJSON(w, http.StatusOK, jobs)
}

// handleRobotByID answers DELETE /robots/{id}: the robot is forgotten, unless a job is still
// assigned to it (409).
func (s *Server) handleRobotByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", "DELETE")
		writeError(w, http.StatusMethodNotAllowed, errors.New("use DELETE"))
		return
	}
	id, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/robots/"))
	if err != nil || id == "" {
		writeError(w, http.StatusBadRequest, errors.New("a robot id is required"))
		return
	}
	if err := s.engine.RemoveRobot(id); err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, dispatcher.ErrUnknownRobot):
			status = http.StatusNotFound
		case errors.Is(err, dispatcher.ErrRobotBusy):
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	log.Printf("[job-dispatcher] robot %q removed", id)
	writeJSON(w, http.StatusOK, s.engine.Robots())
}

// submitRequest is jobRequest plus an optional idempotency key. A separate
// type (and route) rather than adding DedupKey to jobRequest/handleJobs
// keeps POST /jobs' existing AddJob-backed behavior and response shape
// completely unchanged for every current caller.
type submitRequest struct {
	ID           string   `json:"id"`
	Priority     int      `json:"priority"`
	RequiredTool string   `json:"requiredTool"`
	Location     string   `json:"location"`
	DependsOn    []string `json:"dependsOn"`
	DedupKey     string   `json:"dedupKey"`
}

type submitResponse struct {
	dispatcher.Job
	Result dispatcher.SubmitResult `json:"result"`
}

// handleSubmitJob is the idempotent counterpart to POST /jobs: a caller
// that sets dedupKey and retries the same logical submission (e.g. after
// a timed-out response) gets back the SAME job - created once, then
// either returned unchanged (already in flight or done) or reset to
// Pending for a genuine retry-after-failure - instead of a plain ID
// collision error or, worse, the same work running twice.
func (s *Server) handleSubmitJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, errors.New("use POST"))
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, bodyErrorStatus(err), err)
		return
	}
	req, err := submitFromBody(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, errors.New("\"id\" is required"))
		return
	}
	job := dispatcher.Job{
		ID:           req.ID,
		Priority:     req.Priority,
		RequiredTool: req.RequiredTool,
		Location:     req.Location,
		DependsOn:    req.DependsOn,
		DedupKey:     req.DedupKey,
	}
	stored, result, err := s.engine.SubmitJob(job)
	if err != nil {
		if errors.Is(err, dispatcher.ErrInvalidJob) || errors.Is(err, dispatcher.ErrUnknownDep) {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeError(w, http.StatusConflict, err)
		return
	}
	status := http.StatusOK
	if result == dispatcher.SubmitCreated {
		status = http.StatusCreated
	}
	writeJSON(w, status, submitResponse{Job: stored, Result: result})
}

type completeRequest struct {
	ID      string `json:"id"`
	Success bool   `json:"success"`
	// RobotID is this project's own real fix: without it, ANY caller naming just a
	// job ID and a success flag could report completion for a job
	// assigned to a DIFFERENT robot, silently corrupting that other
	// robot's own Load/Available bookkeeping. Required - see
	// dispatcher.Engine.CompleteJob's own doc comment.
	RobotID string `json:"robotId"`
	// Attempt is the optional Job.Attempt the report is about; a report for an older assignment
	// than the current one is a 409. Omitted or 0 means "not stated".
	Attempt int `json:"attempt"`
}

func (s *Server) handleCompleteJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, errors.New("use POST"))
		return
	}
	var req completeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, bodyErrorStatus(err), err)
		return
	}
	if req.RobotID == "" {
		writeError(w, http.StatusBadRequest, errors.New("\"robotId\" is required"))
		return
	}
	if req.Attempt < 0 {
		writeError(w, http.StatusBadRequest, errors.New("\"attempt\" must not be negative"))
		return
	}
	if err := s.engine.CompleteJobAttempt(req.ID, req.Success, req.RobotID, req.Attempt); err != nil {
		writeError(w, completeJobErrorStatus(err), err)
		return
	}
	updated, _ := s.engine.Job(req.ID)
	writeJSON(w, http.StatusOK, updated)
}

// completeJobErrorStatus maps dispatcher.Engine.CompleteJob's own distinct
// sentinel errors to a distinct, meaningful HTTP status instead of the
// generic 400 this handler used to return for every failure mode
// regardless of what actually went wrong - a caller (an actual robot or
// an operator script) can't tell "you named a job that was never
// submitted" apart from "wrong robot reported this" apart from "this job
// already finished" if they're all indistinguishable 400s with only the
// message text to go on.
func completeJobErrorStatus(err error) int {
	switch {
	case errors.Is(err, dispatcher.ErrUnknownJob), errors.Is(err, dispatcher.ErrUnknownRobot):
		// The named job (or the robot it's assigned to) doesn't exist at
		// all - a missing resource, not a malformed request.
		return http.StatusNotFound
	case errors.Is(err, dispatcher.ErrJobNotAssigned), errors.Is(err, dispatcher.ErrRobotMismatch), errors.Is(err, dispatcher.ErrAttemptMismatch):
		// The job and robot both exist, but completing it right now
		// conflicts with the job's real current state (already done/
		// failed, or genuinely not this job's assigned robot) - a real
		// conflict with server-side state, not a client input error.
		return http.StatusConflict
	default:
		// Decode/validation failures reaching this point would already
		// have been caught earlier in handleCompleteJob; an error making
		// it here that isn't one of the sentinels above is treated the
		// same way the rest of this file treats an unrecognized engine
		// error (see handleJobs/handleSubmitJob).
		return http.StatusBadRequest
	}
}

type cancelRequest struct {
	ID string `json:"id"`
}

// handleCancelJob withdraws a job no robot has been given. A job that may be
// running on a robot answers 409: only that robot's own report can end it.
func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, errors.New("use POST"))
		return
	}
	var req cancelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, bodyErrorStatus(err), err)
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, errors.New("\"id\" is required"))
		return
	}
	if err := s.engine.CancelJob(req.ID); err != nil {
		status := http.StatusBadRequest
		switch {
		case errors.Is(err, dispatcher.ErrUnknownJob):
			status = http.StatusNotFound
		case errors.Is(err, dispatcher.ErrJobInFlight), errors.Is(err, dispatcher.ErrJobFinished):
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	updated, _ := s.engine.Job(req.ID)
	writeJSON(w, http.StatusOK, updated)
}

// robotRequest is a registration or a heartbeat: a field left out keeps the value the
// registry already holds, so a minimal {"id":"arm-1"} heartbeat does not wipe the robot's tool,
// location or availability.
type robotRequest struct {
	ID        string  `json:"id"`
	Location  *string `json:"location"`
	Tool      *string `json:"tool"`
	Available *bool   `json:"available"`
}

func (s *Server) handleRobots(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.engine.Robots())
	case http.MethodPost:
		var req robotRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, bodyErrorStatus(err), err)
			return
		}
		if req.ID == "" {
			writeError(w, http.StatusBadRequest, errors.New("\"id\" is required"))
			return
		}
		s.engine.UpdateRobot(dispatcher.RobotUpdate{
			ID:        req.ID,
			Location:  req.Location,
			Tool:      req.Tool,
			Available: req.Available,
		})
		writeJSON(w, http.StatusOK, s.engine.Robots())
	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, http.StatusMethodNotAllowed, errors.New("use GET to list robots or POST to register/update one"))
	}
}

// healthResponse reports real, observable engine health - in particular
// persistenceHealthy/lastPersistError surface Engine.LastPersistError()
// (see dispatcher.go's own doc comment on Engine for exactly which
// failures land here vs. an immediate HTTP error on the call that hit
// them) so an operator or monitoring probe can see a degraded SQLite
// store without it being silently lost. status is "ok", or "degraded"
// (with HTTP 503) while the store is failing, so a probe that only looks
// at the status code notices a dispatcher that is losing transitions.
// The error text itself is not exposed here: /health needs no token.
type healthResponse struct {
	Status             string  `json:"status"`
	PersistenceHealthy bool    `json:"persistenceHealthy"`
	LastPersistError   *string `json:"lastPersistError"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeError(w, http.StatusMethodNotAllowed, errors.New("use GET"))
		return
	}
	resp := healthResponse{Status: "ok", PersistenceHealthy: true}
	status := http.StatusOK
	if err := s.engine.LastPersistError(); err != nil {
		resp.Status = "degraded"
		resp.PersistenceHealthy = false
		msg := "the persistence store reported an error (see the process log)"
		resp.LastPersistError = &msg
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, resp)
}

func (s *Server) handleDispatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, errors.New("use POST"))
		return
	}
	assignments := s.engine.DispatchOnce()
	if assignments == nil {
		assignments = []dispatcher.Assignment{}
	}
	writeJSON(w, http.StatusOK, assignments)
}

// handleWaiting answers GET /dispatch/waiting: every pending job with the reason it has no robot now. It does not
// dispatch anything, and POST /dispatch keeps answering the plain list of assignments.
func (s *Server) handleWaiting(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeError(w, http.StatusMethodNotAllowed, errors.New("use GET"))
		return
	}
	writeJSON(w, http.StatusOK, s.engine.WaitingJobs())
}

type detectStaleRequest struct {
	TimeoutSeconds float64 `json:"timeoutSeconds"`
}

func (s *Server) handleDetectStale(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, errors.New("use POST"))
		return
	}
	var req detectStaleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, bodyErrorStatus(err), err)
		return
	}
	if req.TimeoutSeconds <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("\"timeoutSeconds\" must be a positive number"))
		return
	}
	unknownJobIDs := s.engine.DetectStaleAssignments(time.Duration(req.TimeoutSeconds * float64(time.Second)))
	if unknownJobIDs == nil {
		unknownJobIDs = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"unknownJobIds": unknownJobIDs})
}

type requeueRequest struct {
	ID      string `json:"id"`
	RobotID string `json:"robotId"`
	Reason  string `json:"reason"`
}

// handleRequeueJob is the operator's way out of an assigned or unknown job whose robot will
// never report: the job goes back to pending and the robot is held unavailable until its own
// next heartbeat.
func (s *Server) handleRequeueJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, errors.New("use POST"))
		return
	}
	var req requeueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, bodyErrorStatus(err), err)
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, errors.New("\"id\" is required"))
		return
	}
	if err := s.engine.RequeueJob(req.ID, req.RobotID); err != nil {
		status := http.StatusBadRequest
		switch {
		case errors.Is(err, dispatcher.ErrUnknownJob):
			status = http.StatusNotFound
		case errors.Is(err, dispatcher.ErrJobFinished), errors.Is(err, dispatcher.ErrRobotMismatch):
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	log.Printf("[job-dispatcher] operator requeued job %q (robot %q, reason %q)", req.ID, req.RobotID, req.Reason)
	updated, _ := s.engine.Job(req.ID)
	writeJSON(w, http.StatusOK, updated)
}

// handleJobByID answers GET /jobs/{id} with one job, so a client does not have to download
// the whole list to learn the state of one.
func (s *Server) handleJobByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeError(w, http.StatusMethodNotAllowed, errors.New("use GET"))
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/jobs/")
	job, ok := s.engine.Job(id)
	if id == "" || !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("unknown job %q", id))
		return
	}
	writeJSON(w, http.StatusOK, job)
}
