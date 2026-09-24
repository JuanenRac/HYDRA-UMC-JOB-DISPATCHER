// HYDRA-UMC-JOB-DISPATCHER - HTTP API
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0 - see LICENSE
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
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/JuanenRac/hydra-umc-job-dispatcher/src/dispatcher"
)

// Server wraps a dispatcher.Engine with HTTP handlers.
type Server struct {
	engine *dispatcher.Engine
	mux    *http.ServeMux
}

// New builds a Server ready to be used as an http.Handler.
func New(engine *dispatcher.Engine) *Server {
	s := &Server{engine: engine, mux: http.NewServeMux()}
	s.mux.HandleFunc("/jobs", s.handleJobs)
	s.mux.HandleFunc("/jobs/submit", s.handleSubmitJob)
	s.mux.HandleFunc("/jobs/complete", s.handleCompleteJob)
	s.mux.HandleFunc("/jobs/cancel", s.handleCancelJob)
	s.mux.HandleFunc("/robots", s.handleRobots)
	s.mux.HandleFunc("/dispatch", s.handleDispatch)
	s.mux.HandleFunc("/jobs/detect-stale", s.handleDetectStale)
	s.mux.HandleFunc("/health", s.handleHealth)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
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
	DependsOn    []string `json:"dependsOn"`
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.engine.Jobs())
	case http.MethodPost:
		var req jobRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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

// submitRequest is jobRequest plus an optional idempotency key. A separate
// type (and route) rather than adding DedupKey to jobRequest/handleJobs
// keeps POST /jobs' existing AddJob-backed behavior and response shape
// completely unchanged for every current caller.
type submitRequest struct {
	ID           string   `json:"id"`
	Priority     int      `json:"priority"`
	RequiredTool string   `json:"requiredTool"`
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
	var req submitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
}

func (s *Server) handleCompleteJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, errors.New("use POST"))
		return
	}
	var req completeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.RobotID == "" {
		writeError(w, http.StatusBadRequest, errors.New("\"robotId\" is required"))
		return
	}
	if err := s.engine.CompleteJob(req.ID, req.Success, req.RobotID); err != nil {
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
	case errors.Is(err, dispatcher.ErrJobNotAssigned), errors.Is(err, dispatcher.ErrRobotMismatch):
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
		writeError(w, http.StatusBadRequest, err)
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

type robotRequest struct {
	ID        string `json:"id"`
	Location  string `json:"location"`
	Tool      string `json:"tool"`
	Available bool   `json:"available"`
}

func (s *Server) handleRobots(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.engine.Robots())
	case http.MethodPost:
		var req robotRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if req.ID == "" {
			writeError(w, http.StatusBadRequest, errors.New("\"id\" is required"))
			return
		}
		s.engine.UpsertRobot(dispatcher.Robot{
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
// store without it being silently lost. status is always "ok": this
// process being reachable enough to answer at all already proves the
// one thing a liveness probe needs to know; persistenceHealthy is the
// separate, real signal for "is the mission queue's own durability
// currently working".
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
	if err := s.engine.LastPersistError(); err != nil {
		resp.PersistenceHealthy = false
		msg := err.Error()
		resp.LastPersistError = &msg
	}
	writeJSON(w, http.StatusOK, resp)
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
		writeError(w, http.StatusBadRequest, err)
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
