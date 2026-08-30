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
	s.mux.HandleFunc("/robots", s.handleRobots)
	s.mux.HandleFunc("/dispatch", s.handleDispatch)
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
	if err := s.engine.CompleteJob(req.ID, req.Success); err != nil {
		writeError(w, http.StatusBadRequest, err)
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
