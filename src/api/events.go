// HYDRA-UMC-JOB-DISPATCHER - HTTP route of the stream of job events
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/dispatcher"
)

// SetEvents gives the server the hub of job events. GET /events answers like an unknown path while the hub is missing or its stream is off.
func (s *Server) SetEvents(h *dispatcher.EventHub) { s.events = h }

// handleEvents answers GET /events as server-sent events: one "event:" per job event, with the id of the event so a client that
// reconnects with Last-Event-ID (or ?since=) gets what it missed while the hub still keeps it. ?types=done,failed narrows it.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if !s.events.SSEEnabled() {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeError(w, http.StatusMethodNotAllowed, errors.New("use GET"))
		return
	}
	// the controller reaches the real writer through the wrappers that count the answers
	rc := http.NewResponseController(w)
	wanted := map[string]bool{}
	if raw := r.URL.Query().Get("types"); raw != "" {
		for _, t := range strings.Split(raw, ",") {
			t = strings.TrimSpace(t)
			if !dispatcher.ValidEventType(t) {
				writeError(w, http.StatusBadRequest, fmt.Errorf("unknown event type %q", t))
				return
			}
			wanted[t] = true
		}
	}
	var since int64
	if raw := r.Header.Get("Last-Event-ID"); raw != "" {
		since, _ = strconv.ParseInt(raw, 10, 64)
	} else if raw := r.URL.Query().Get("since"); raw != "" {
		since, _ = strconv.ParseInt(raw, 10, 64)
	}
	// the write timeout of the server must not cut a stream that is meant to stay open
	_ = rc.SetWriteDeadline(time.Time{})
	replay, ch, cancel := s.events.Subscribe(since)
	defer cancel()
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	write := func(ev dispatcher.Event) bool {
		if len(wanted) > 0 && !wanted[ev.Type] {
			return true
		}
		data, _ := json.Marshal(ev)
		if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.ID, ev.Type, data); err != nil {
			return false
		}
		_ = rc.Flush()
		return true
	}
	for _, ev := range replay {
		if !write(ev) {
			return
		}
	}
	_ = rc.Flush()
	keep := time.NewTicker(s.events.Config().KeepAlive)
	defer keep.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keep.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			_ = rc.Flush()
		case ev, open := <-ch:
			if !open {
				return // fell behind: the client reconnects with Last-Event-ID
			}
			if !write(ev) {
				return
			}
		}
	}
}
