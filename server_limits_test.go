// HYDRA-UMC-JOB-DISPATCHER - server_limits_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// The defaults are the limits the server always had, a silent client is dropped and the limits
// can be changed without ever being switched off.
package main

import (
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDefaultServerLimitsAreTheOnesTheServerAlwaysHad(t *testing.T) {
	s := newHTTPServer(":0", http.NewServeMux(), defaultServerLimits)
	if s.ReadHeaderTimeout != 5*time.Second || s.ReadTimeout != 15*time.Second || s.WriteTimeout != 30*time.Second || s.IdleTimeout != 60*time.Second {
		t.Fatalf("unexpected defaults: %+v", s)
	}
	if s.MaxHeaderBytes != 16*1024 {
		t.Fatalf("MaxHeaderBytes = %d, want 16384", s.MaxHeaderBytes)
	}
}

func TestZeroOrNegativeLimitsAreRefused(t *testing.T) {
	if p := defaultServerLimits.problem(64 * 1024); p != "" {
		t.Fatalf("defaults refused: %s", p)
	}
	for _, mutate := range []func(*serverLimits){
		func(l *serverLimits) { l.ReadHeaderTimeout = 0 },
		func(l *serverLimits) { l.ReadTimeout = -time.Second },
		func(l *serverLimits) { l.WriteTimeout = 0 },
		func(l *serverLimits) { l.IdleTimeout = 0 },
	} {
		l := defaultServerLimits
		mutate(&l)
		if l.problem(64*1024) == "" {
			t.Errorf("limits %+v were accepted", l)
		}
	}
	if defaultServerLimits.problem(0) == "" || defaultServerLimits.problem(-1) == "" {
		t.Error("a body limit of zero or less was accepted")
	}
}

func TestAClientThatNeverFinishesItsHeadersIsDropped(t *testing.T) {
	limits := defaultServerLimits
	limits.ReadHeaderTimeout = 150 * time.Millisecond
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := newHTTPServer("", http.NewServeMux(), limits)
	go func() { _ = server.Serve(listener) }()
	defer server.Close()

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("GET /health HTTP/1.1\r\nHost: x\r\n")); err != nil { // never ends the headers
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	// the server answers 408 and closes, or just closes: either way the read ends well before the deadline
	if err == nil && !strings.Contains(string(buf[:n]), "408") && !strings.Contains(string(buf[:n]), "400") {
		t.Fatalf("expected the connection to be dropped, got %q", buf[:n])
	}
}
