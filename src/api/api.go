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
	"net"
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
	// maxBody is the largest request body any route reads (MaxBodyBytes unless SetMaxBodyBytes changed it).
	maxBody int64
	// reloader runs POST /admin/reload-config; nil (no configuration file was given) leaves the route unknown.
	reloader ConfigReloader
	// maintenance holds the maintenance proposals; nil or switched off leaves their routes unknown (404).
	maintenance *dispatcher.MaintenanceBoard
	// events is the hub of job events; nil or without the stream leaves GET /events unknown (404).
	events *dispatcher.EventHub
	// version is published by GET /metrics; metrics holds the counters and metricsHandler wraps the routes with them.
	version        string
	// metricsOn opens GET /metrics (the "metrics" section of the configuration file); closed, the route answers like any unknown path.
	metricsOn      bool
	metrics        *ServiceMetrics
	metricsHandler http.Handler
}

// ConfigReloader re-reads the configuration file and installs it. It returns a summary of the values now in force or, when nothing
// was changed, the reason.
type ConfigReloader func() (map[string]any, error)

// SetConfigReloader switches POST /admin/reload-config on. Without it the route answers like any unknown path.
func (s *Server) SetConfigReloader(fn ConfigReloader) { s.reloader = fn }

// RequireToken makes every route except GET /health demand `Authorization: Bearer <token>`
// (compared in constant time). An empty token leaves the API open, as before - the caller
// (main.go) refuses that combination on an address other than loopback.
func (s *Server) RequireToken(token string) { s.token = token }

// SetMaxBodyBytes changes the largest request body any route reads (default MaxBodyBytes). A value of zero or less keeps the default.
func (s *Server) SetMaxBodyBytes(n int64) {
	if n > 0 {
		s.maxBody = n
	}
}

// New builds a Server ready to be used as an http.Handler.
func New(engine *dispatcher.Engine) *Server {
	s := &Server{engine: engine, mux: http.NewServeMux(), maxBody: MaxBodyBytes}
	s.mux.HandleFunc("/jobs", s.handleJobs)
	s.mux.HandleFunc("/jobs/submit", s.handleSubmitJob)
	s.mux.HandleFunc("/jobs/complete", s.handleCompleteJob)
	s.mux.HandleFunc("/jobs/cancel", s.handleCancelJob)
	s.mux.HandleFunc("/jobs/requeue", s.handleRequeueJob)
	s.mux.HandleFunc("/jobs/replan", s.handleReplan)
	s.mux.HandleFunc("/jobs/stop-requests", s.handleStopRequests)
	s.mux.HandleFunc("/jobs/stop-ack", s.handleStopAck)
	s.mux.HandleFunc("/jobs/", s.handleJobByID)
	s.mux.HandleFunc("/robots", s.handleRobots)
	s.mux.HandleFunc("/robots/", s.handleRobotByID)
	s.mux.HandleFunc("/dispatch", s.handleDispatch)
	s.mux.HandleFunc("/dispatch/waiting", s.handleWaiting)
	s.mux.HandleFunc("/dispatch/eta", s.handleETA)
	s.mux.HandleFunc("/jobs/detect-stale", s.handleDetectStale)
	s.mux.HandleFunc("/admin/reload-config", s.handleReloadConfig)
	s.mux.HandleFunc("/maintenance/proposals", s.handleMaintenanceProposals)
	s.mux.HandleFunc("/maintenance/proposals/approve", s.handleMaintenanceDecision)
	s.mux.HandleFunc("/maintenance/proposals/reject", s.handleMaintenanceDecision)
	s.mux.HandleFunc("/events", s.handleEvents)
	s.mux.HandleFunc("/health", s.handleHealth)
	s.metrics = NewServiceMetrics("hydra-umc-job-dispatcher")
	s.metricsHandler = s.metrics.Middleware(http.HandlerFunc(s.serveRoutes),
		func() string { return s.version },
		func(r *http.Request) bool { return s.metricsAllowed(r) },
		func() map[string]bool { return map[string]bool{"persistence": s.engine.LastPersistError() == nil} })
	return s
}

// SetVersion names the version that GET /metrics publishes.
func (s *Server) SetVersion(version string) { s.version = version }

// SetMetricsEnabled opens GET /metrics. Without it (the default) the route answers like any unknown path (404).
func (s *Server) SetMetricsEnabled(on bool) { s.metricsOn = on }

// metricsAllowed says whether the caller may read the numbers of an open GET /metrics: with a token set, the caller must present it; without
// one, only a caller on this machine may read them.
func (s *Server) metricsAllowed(r *http.Request) bool {
	if s.token != "" {
		return s.authorized(r)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	return err == nil && ip != nil && ip.IsLoopback()
}

// ServeHTTP counts every answer and serves GET /metrics when it is open (the token when one is set, loopback otherwise, 401 without it; numbers only), then
// hands over to the routes.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.metricsOn && r.Method == http.MethodGet && r.URL.Path == "/metrics" {
		// closed: the route does not exist (it is not read from the token check or the metrics either)
		s.metrics.Count(http.StatusNotFound)
		writeError(w, http.StatusNotFound, errors.New("not found"))
		return
	}
	s.metricsHandler.ServeHTTP(w, r)
}

func (s *Server) serveRoutes(w http.ResponseWriter, r *http.Request) {
	if s.token != "" && r.URL.Path != "/health" && !s.authorized(r) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, errors.New("a valid Authorization: Bearer token is required"))
		return
	}
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, s.maxBody)
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
	// StopCapable: the robot declares that it can confirm a requested stop (used only with replanning on).
	StopCapable *bool `json:"stopCapable"`
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
			ID:          req.ID,
			Location:    req.Location,
			Tool:        req.Tool,
			Available:   req.Available,
			StopCapable: req.StopCapable,
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
	s.engine.ConfirmTools() // asks the controllers which tool each robot carries; nothing when the confirmation is off
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

// handleETA answers GET /dispatch/eta: the estimated start and end of every job that has not ended and the time the
// queue is expected to be empty. While the estimates are switched off (the default) the route does not exist: the
// answer is the same 404 as for any unknown path. `?job=ID` narrows the answer to one job.
func (s *Server) handleETA(w http.ResponseWriter, r *http.Request) {
	queue, enabled := s.engine.QueueETA()
	if !enabled {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeError(w, http.StatusMethodNotAllowed, errors.New("use GET"))
		return
	}
	if id := r.URL.Query().Get("job"); id != "" {
		for _, j := range queue.Jobs {
			if j.JobID == id {
				writeJSON(w, http.StatusOK, j)
				return
			}
		}
		if _, ok := s.engine.Job(id); ok {
			writeError(w, http.StatusNotFound, errors.New("the job has ended, so it has no estimate"))
			return
		}
		writeError(w, http.StatusNotFound, errors.New("unknown job"))
		return
	}
	writeJSON(w, http.StatusOK, queue)
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
	result := s.engine.DetectStaleAssignmentsStop(time.Duration(req.TimeoutSeconds * float64(time.Second)))
	unknownJobIDs, requeued := result.Unknown, result.Requeued
	if unknownJobIDs == nil {
		unknownJobIDs = []string{}
	}
	answer := map[string]any{"unknownJobIds": unknownJobIDs}
	if s.engine.ReplanEnabled() {
		// only with replanning on: the jobs that went back to the queue instead of becoming unknown
		if requeued == nil {
			requeued = []string{}
		}
		answer["requeuedJobIds"] = requeued
		if len(result.StopRequested) > 0 {
			// robots that can confirm a stop were asked to stop; their jobs wait for the acknowledgement
			answer["stopRequestedJobIds"] = result.StopRequested
		}
	}
	writeJSON(w, http.StatusOK, answer)
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

type replanRequest struct {
	RobotID string `json:"robotId"`
	Reason  string `json:"reason"`
}

// handleReplan answers POST /jobs/replan: the robot is declared failed or gone and every job assigned to it (or whose
// outcome is unknown) goes back to the queue. While replanning is switched off (the default) the route does not exist:
// the answer is the same 404 as for any unknown path.
func (s *Server) handleReplan(w http.ResponseWriter, r *http.Request) {
	if !s.engine.ReplanEnabled() {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, errors.New("use POST"))
		return
	}
	var req replanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, bodyErrorStatus(err), err)
		return
	}
	if req.RobotID == "" {
		writeError(w, http.StatusBadRequest, errors.New("\"robotId\" is required"))
		return
	}
	ids, err := s.engine.ReplanRobot(req.RobotID)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, dispatcher.ErrUnknownRobot) {
			status = http.StatusNotFound
		}
		writeError(w, status, err)
		return
	}
	if ids == nil {
		ids = []string{}
	}
	log.Printf("[job-dispatcher] operator replanned robot %q (reason %q): jobs back in the queue: %v", req.RobotID, req.Reason, ids)
	writeJSON(w, http.StatusOK, map[string]any{"requeuedJobIds": ids})
}

// handleReloadConfig answers POST /admin/reload-config: the service re-reads its configuration file (-config) and installs the values it
// can change while it runs. Only a caller on this machine may ask (as well as the token, when one is set), and a file that is not valid
// changes nothing: the answer is 422 and the previous values stay in force. Without a configuration file the route does not exist.
func (s *Server) handleReloadConfig(w http.ResponseWriter, r *http.Request) {
	if s.reloader == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, errors.New("use POST"))
		return
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
		writeError(w, http.StatusForbidden, errors.New("the configuration is reloaded only on request from this machine"))
		return
	}
	summary, err := s.reloader()
	if err != nil {
		log.Printf("[job-dispatcher] reload of the configuration refused, the previous values stay in force: %v", err)
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error(), "kept": "the previous values"})
		return
	}
	log.Printf("[job-dispatcher] configuration reloaded: %v", summary)
	writeJSON(w, http.StatusOK, map[string]any{"reloaded": true, "applied": summary})
}

// handleStopRequests answers GET /jobs/stop-requests: the stop requests made to silent robots that declared they can
// confirm a stop, for the bridge of the robot to deliver. Off with replanning: the same 404 as for any unknown path.
func (s *Server) handleStopRequests(w http.ResponseWriter, r *http.Request) {
	if !s.engine.ReplanEnabled() {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeError(w, http.StatusMethodNotAllowed, errors.New("use GET"))
		return
	}
	writeJSON(w, http.StatusOK, s.engine.StopRequests())
}

type stopAckRequest struct {
	RobotID string `json:"robotId"`
	JobID   string `json:"jobId"`
}

// handleStopAck answers POST /jobs/stop-ack: the robot confirms that it stopped the job it was asked to stop, and the job
// goes back to the queue. Off with replanning: the same 404 as for any unknown path.
func (s *Server) handleStopAck(w http.ResponseWriter, r *http.Request) {
	if !s.engine.ReplanEnabled() {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, errors.New("use POST"))
		return
	}
	var req stopAckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, bodyErrorStatus(err), err)
		return
	}
	if req.RobotID == "" || req.JobID == "" {
		writeError(w, http.StatusBadRequest, errors.New("\"robotId\" and \"jobId\" are required"))
		return
	}
	if err := s.engine.AckStop(req.RobotID, req.JobID); err != nil {
		status := http.StatusConflict
		switch {
		case errors.Is(err, dispatcher.ErrUnknownJob):
			status = http.StatusNotFound
		case errors.Is(err, dispatcher.ErrReplanOff):
			status = http.StatusBadRequest
		}
		writeError(w, status, err)
		return
	}
	log.Printf("[job-dispatcher] robot %q confirmed the stop of job %q: back in the queue", req.RobotID, req.JobID)
	writeJSON(w, http.StatusOK, map[string]any{"requeuedJobIds": []string{req.JobID}})
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
