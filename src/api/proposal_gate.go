// HYDRA-UMC-JOB-DISPATCHER - the gate of the signed trajectory proposals on the submission of a job
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/dispatcher"
)

// SetProposalVerifier turns the check of signed proposals on (the "proposal_verify" section). Without it a job is taken as it always was and the
// fields "proposal" and "expectedRobotId" are ignored.
func (s *Server) SetProposalVerifier(v *dispatcher.ProposalVerifier) { s.proposals = v }

// checkProposal answers false (after writing the refusal) when the job must not go on: a proposal that the server does not find valid is 422, one that
// could not be checked is 503 (it fails closed), a proposal without the robot it is for is 400, and a job without a proposal is 422 only when the
// section says "require". A job that carries no proposal is untouched otherwise.
func (s *Server) checkProposal(w http.ResponseWriter, r *http.Request, proposal json.RawMessage, expectedRobotID string) bool {
	if s.proposals == nil {
		return true
	}
	trimmed := bytes.TrimSpace(proposal)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		if s.proposals.Require() {
			writeError(w, http.StatusUnprocessableEntity, errors.New("a signed trajectory proposal is required (\"proposal\" and \"expectedRobotId\")"))
			return false
		}
		return true
	}
	if expectedRobotID == "" {
		writeError(w, http.StatusBadRequest, errors.New("\"expectedRobotId\" is required with a proposal"))
		return false
	}
	verdict, err := s.proposals.Verify(r.Context(), proposal, expectedRobotID)
	if err != nil {
		log.Printf("[job-dispatcher] a signed proposal could not be checked: %v", err)
		writeError(w, http.StatusServiceUnavailable, err)
		return false
	}
	if !verdict.Valid {
		writeError(w, http.StatusUnprocessableEntity, fmt.Errorf("the signed proposal is not valid: %s", verdict.Reason))
		return false
	}
	return true
}
