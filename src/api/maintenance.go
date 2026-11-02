// HYDRA-UMC-JOB-DISPATCHER - HTTP routes of the maintenance proposals
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/dispatcher"
)

// SetMaintenance gives the server the board of maintenance proposals. The routes answer like unknown paths while the board is off.
func (s *Server) SetMaintenance(b *dispatcher.MaintenanceBoard) { s.maintenance = b }

type proposalRequest struct {
	Source string  `json:"source"`
	Reason string  `json:"reason"`
	Score  float64 `json:"score"`
	Drift  float64 `json:"drift"`
}

// handleMaintenanceProposals answers GET (list, optional ?status=) and POST (propose) on /maintenance/proposals. A proposal never
// reaches the queue: only a person's approval does. Switched off: the same 404 as for any unknown path.
func (s *Server) handleMaintenanceProposals(w http.ResponseWriter, r *http.Request) {
	if s.maintenance == nil || !s.maintenance.Enabled() {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"proposals": s.maintenance.List(r.URL.Query().Get("status"))})
	case http.MethodPost:
		var req proposalRequest
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeError(w, bodyErrorStatus(err), err)
			return
		}
		p, created, err := s.maintenance.Propose(dispatcher.ProposalInput{Source: req.Source, Reason: req.Reason, Score: req.Score, Drift: req.Drift})
		if err != nil {
			writeError(w, maintenanceErrorStatus(err), err)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeJSON(w, status, map[string]any{"proposal": p, "created": created})
	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, http.StatusMethodNotAllowed, errors.New("use GET or POST"))
	}
}

type proposalDecisionRequest struct {
	ID     string `json:"id"`
	By     string `json:"by"`
	Reason string `json:"reason"`
}

// handleMaintenanceDecision answers POST /maintenance/proposals/approve and /reject: the decision of a person.
func (s *Server) handleMaintenanceDecision(w http.ResponseWriter, r *http.Request) {
	if s.maintenance == nil || !s.maintenance.Enabled() {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, errors.New("use POST"))
		return
	}
	var req proposalDecisionRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, bodyErrorStatus(err), err)
		return
	}
	if req.ID == "" || len(req.By) > 128 || len(req.Reason) > 500 {
		writeError(w, http.StatusBadRequest, errors.New("\"id\" is required; \"by\" is at most 128 and \"reason\" at most 500 characters"))
		return
	}
	if strings.HasSuffix(r.URL.Path, "/approve") {
		p, job, err := s.maintenance.Approve(req.ID, req.By)
		if err != nil {
			writeError(w, maintenanceErrorStatus(err), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"proposal": p, "job": job})
		return
	}
	p, err := s.maintenance.Reject(req.ID, req.By, req.Reason)
	if err != nil {
		writeError(w, maintenanceErrorStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"proposal": p})
}

func maintenanceErrorStatus(err error) int {
	switch {
	case errors.Is(err, dispatcher.ErrInvalidProposal), errors.Is(err, dispatcher.ErrInvalidJob):
		return http.StatusBadRequest
	case errors.Is(err, dispatcher.ErrUnknownProposal), errors.Is(err, dispatcher.ErrMaintenanceOff):
		return http.StatusNotFound
	default:
		return http.StatusConflict
	}
}
