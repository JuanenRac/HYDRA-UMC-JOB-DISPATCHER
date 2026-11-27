// =============================================================================
// HYDRA-UMC-JOB-DISPATCHER - src/api/hold_and_robot_tokens.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
// =============================================================================
//
// Two options of the API, both off unless the configuration file asks for them: the global hold of assignments (POST /dispatch/hold and
// /dispatch/release) and a token of its own for a robot, accepted only where that robot speaks for itself.

package api

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/dispatcher"
)

// RobotTokens resolves the "robot_tokens" section (robot id to the name of an environment variable) into robot id to token. It fails
// closed: a variable that is empty, a token shared by two robots or one equal to the token of the API is an error.
func RobotTokens(section map[string]string, apiToken string, getenv func(string) string) (map[string]string, error) {
	robots := make([]string, 0, len(section))
	for robot := range section {
		robots = append(robots, robot)
	}
	sort.Strings(robots)
	tokens := make(map[string]string, len(section))
	owner := map[string]string{}
	for _, robot := range robots {
		name := section[robot]
		value := strings.TrimSpace(getenv(name))
		switch {
		case robot == "" || name == "":
			return nil, errors.New("robot_tokens: every entry needs a robot id and the name of an environment variable")
		case value == "":
			return nil, fmt.Errorf("robot_tokens: the environment variable of robot %q is empty", robot)
		case value == apiToken:
			return nil, fmt.Errorf("robot_tokens: robot %q has the token of the API", robot)
		case owner[value] != "":
			return nil, fmt.Errorf("robot_tokens: robots %q and %q share a token", owner[value], robot)
		}
		owner[value] = robot
		tokens[robot] = value
	}
	return tokens, nil
}

// SetRobotTokens gives robots a token of their own (nil or empty: as before).
func (s *Server) SetRobotTokens(tokens map[string]string) { s.robotTokens = tokens }

func bearerOf(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
}

// robotOfToken names the robot whose token this is, or "".
func (s *Server) robotOfToken(given string) string {
	found := ""
	for robot, token := range s.robotTokens {
		if subtle.ConstantTimeCompare([]byte(given), []byte(token)) == 1 {
			found = robot
		}
	}
	return found
}

// robotCaller says whether the request carries the token of a robot on a route such a token opens: its reports (a completion, a stop
// acknowledgement, its heartbeat), and reading the stop requests it has to answer. The handler then checks which robot it speaks for.
func (s *Server) robotCaller(r *http.Request) bool {
	if len(s.robotTokens) == 0 || s.robotOfToken(bearerOf(r)) == "" {
		return false
	}
	switch r.URL.Path {
	case "/jobs/complete", "/jobs/stop-ack", "/robots":
		return r.Method == http.MethodPost
	case "/jobs/stop-requests":
		return r.Method == http.MethodGet
	}
	return false
}

// robotMaySpeak refuses (403) a report in the name of a robot the caller may not speak for. A token of a robot speaks for that robot
// only; a robot that has a token of its own is spoken for by that token or by the token of the API; a robot without one is as before.
func (s *Server) robotMaySpeak(w http.ResponseWriter, r *http.Request, robotID string) bool {
	if len(s.robotTokens) == 0 {
		return true
	}
	given := bearerOf(r)
	allowed := false
	if owner := s.robotOfToken(given); owner != "" {
		allowed = owner == robotID
	} else if _, own := s.robotTokens[robotID]; !own {
		allowed = true
	} else {
		allowed = s.token != "" && subtle.ConstantTimeCompare([]byte(given), []byte(s.token)) == 1
	}
	if !allowed {
		writeError(w, http.StatusForbidden, errors.New("these credentials do not speak for this robot"))
	}
	return allowed
}

// mayRelease: releasing the hold takes the token of the API or an admin identity of HYDRA-UMC-SERVER. With neither configured the API is
// open, as every other route is then.
func (s *Server) mayRelease(r *http.Request) bool {
	given := bearerOf(r)
	if s.token != "" && subtle.ConstantTimeCompare([]byte(given), []byte(s.token)) == 1 {
		return true
	}
	if s.identityKey != nil {
		identity, err := dispatcher.VerifyServerToken(given, s.identityKey, time.Now())
		return err == nil && identity.Role == "admin"
	}
	return s.token == ""
}

// handleHold answers GET /dispatch/hold (is it held) and POST /dispatch/hold (hold). Without the hold switched on the routes do not exist.
func (s *Server) handleHold(w http.ResponseWriter, r *http.Request) {
	if !s.engine.HoldEnabled() {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]bool{"held": s.engine.Held()})
	case http.MethodPost:
		err := s.engine.SetHeld(true)
		log.Printf("[job-dispatcher] assignments are held")
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, errors.New("held, but the hold could not be saved: it would not survive a restart"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"held": true})
	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, http.StatusMethodNotAllowed, errors.New("use GET or POST"))
	}
}

// handleRelease answers POST /dispatch/release.
func (s *Server) handleRelease(w http.ResponseWriter, r *http.Request) {
	if !s.engine.HoldEnabled() {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, errors.New("use POST"))
		return
	}
	if !s.mayRelease(r) {
		writeError(w, http.StatusForbidden, errors.New("releasing the hold takes the token of the API or an admin identity"))
		return
	}
	if err := s.engine.SetHeld(false); err != nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("the release could not be saved: assignments are still held"))
		return
	}
	log.Printf("[job-dispatcher] assignments are released")
	writeJSON(w, http.StatusOK, map[string]bool{"held": false})
}
