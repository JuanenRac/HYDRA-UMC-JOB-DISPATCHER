// HYDRA-UMC-JOB-DISPATCHER - server_limits.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// The time and size limits of the HTTP server. http.ListenAndServe has no timeouts: a client that
// opens a connection and never finishes a request would hold it (and a goroutine) for ever. The
// defaults are the values the server always used; the flags only change them.
package main

import (
	"fmt"
	"net/http"
	"time"
)

type serverLimits struct {
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

var defaultServerLimits = serverLimits{
	ReadHeaderTimeout: 5 * time.Second,
	ReadTimeout:       15 * time.Second,
	WriteTimeout:      30 * time.Second,
	IdleTimeout:       60 * time.Second,
}

// maxHeaderBytes is the largest set of request headers the server reads.
const maxHeaderBytes = 16 * 1024

// problem names what is wrong with the limits, or returns an empty string. A timeout of zero would
// mean no timeout at all, which is what this server exists to avoid, so it is refused.
func (l serverLimits) problem(maxBody int64) string {
	for name, value := range map[string]time.Duration{
		"read-header-timeout": l.ReadHeaderTimeout, "read-timeout": l.ReadTimeout,
		"write-timeout": l.WriteTimeout, "idle-timeout": l.IdleTimeout,
	} {
		if value <= 0 {
			return fmt.Sprintf("-%s must be positive (a server without timeouts is held open for ever by a silent client)", name)
		}
	}
	if maxBody <= 0 {
		return "-max-body-bytes must be positive"
	}
	return ""
}

func newHTTPServer(addr string, handler http.Handler, l serverLimits) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: l.ReadHeaderTimeout,
		ReadTimeout:       l.ReadTimeout,
		WriteTimeout:      l.WriteTimeout,
		IdleTimeout:       l.IdleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
}
