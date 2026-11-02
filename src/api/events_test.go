// HYDRA-UMC-JOB-DISPATCHER - HTTP tests of the stream of job events
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package api

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/dispatcher"
)

func streamHub(t *testing.T, sse bool) *dispatcher.EventHub {
	t.Helper()
	cfg := dispatcher.DefaultEventsConfig()
	cfg.Enabled, cfg.SSE = true, sse
	hub, err := dispatcher.NewEventHub(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hub.Close)
	return hub
}

func TestEvents_RouteIsAnUnknownPathWhileOff(t *testing.T) {
	s := New(dispatcher.NewEngine())
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/events", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
	s.SetEvents(streamHub(t, false))
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/events", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("with the stream off: status %d, want 404", rec.Code)
	}
}

func TestEvents_StreamNeedsTheTokenWhenOneIsSet(t *testing.T) {
	s := New(dispatcher.NewEngine())
	s.RequireToken("tok")
	s.SetEvents(streamHub(t, true))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/events", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rec.Code)
	}
}

func TestEvents_StreamReplaysFiltersAndFollows(t *testing.T) {
	e := dispatcher.NewEngine()
	hub := streamHub(t, true)
	e.SetEvents(hub)
	s := New(e)
	s.SetEvents(hub)
	e.UpsertRobot(dispatcher.Robot{ID: "r1", Available: true})
	_ = e.AddJob(dispatcher.Job{ID: "a"})
	e.DispatchOnce() // event 1: assigned

	srv := httptest.NewServer(s)
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/events?types=done,assigned", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	lines := make(chan string, 32)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	wait := func(prefix string) string {
		deadline := time.After(3 * time.Second)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("stream closed while waiting for %q", prefix)
				}
				if strings.HasPrefix(l, prefix) {
					return l
				}
			case <-deadline:
				t.Fatalf("no %q line", prefix)
			}
		}
	}
	if l := wait("event:"); l != "event: assigned" {
		t.Fatalf("first event %q (the replay)", l)
	}
	if err := e.CompleteJob("a", true, "r1"); err != nil {
		t.Fatal(err)
	}
	if l := wait("event:"); l != "event: done" {
		t.Fatalf("followed event %q", l)
	}
	if l := wait("data:"); !strings.Contains(l, `"jobId":"a"`) {
		t.Fatalf("data %q", l)
	}
}

func TestEvents_UnknownTypeIsABadRequestAndPostIsRefused(t *testing.T) {
	s := New(dispatcher.NewEngine())
	s.SetEvents(streamHub(t, true))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/events?types=exploded", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/events", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status %d, want 405", rec.Code)
	}
}
