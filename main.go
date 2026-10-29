// HYDRA-UMC-JOB-DISPATCHER - entry point
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// Real priority mission queue, no longer just an identity print:
// src/dispatcher implements the scheduling algorithm (tool-aware
// routing, multi-stage dependencies, priority bypass, deterministic
// priority ordering, and DedupKey-based idempotent submission), src/api
// exposes it over plain HTTP/JSON (POST /jobs, POST /jobs/submit, POST
// /robots, POST /dispatch, POST /jobs/complete, GET /jobs, GET /robots,
// GET /health).
//
// Real SQLite-backed persistence (src/sqlitestore, per the README's own
// "Persistence: fault-tolerant mission state" - see dispatcher.Engine's
// own doc comment for the full design) is opt-in via -db: unset, this
// runs exactly as it always has (dispatcher.NewEngine, purely in-memory,
// fine for local development/testing); set to a real file path, the
// mission queue survives this process restarting.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/api"
	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/dispatcher"
	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/sqlitestore"
)

// isLoopbackAddr reports whether an address such as "127.0.0.1:8090" or "localhost:8090" only
// listens on this machine. ":8090" (every interface) and any other host are not loopback.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// startupProblem is why this process must not start (empty when it may): the API can register
// robots, submit and cancel jobs and report completions, so an address other than loopback
// without a token would give all of that to anybody on the network.
func startupProblem(addr, token string, allowAnonymous bool) string {
	if token == "" && !allowAnonymous && !isLoopbackAddr(addr) {
		return fmt.Sprintf("-addr %q is reachable from the network but no token is set: set JOB_DISPATCHER_TOKEN (or -token), listen on 127.0.0.1, or pass -allow-anonymous for an open lab dispatcher", addr)
	}
	return ""
}

// buildToolCatalog joins the tools of a JSON file and of a comma-separated list. It returns nil when neither is given.
func buildToolCatalog(file, list string) (*dispatcher.ToolCatalog, error) {
	if file == "" && strings.TrimSpace(list) == "" {
		return nil, nil
	}
	var names []string
	if file != "" {
		fromFile, err := dispatcher.LoadToolCatalogFile(file)
		if err != nil {
			return nil, err
		}
		names = append(names, fromFile.Names()...)
	}
	names = append(names, strings.Split(list, ",")...)
	return dispatcher.NewToolCatalog(names), nil
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8090", "address to listen on for the HTTP API (loopback by default: another address needs a token)")
	token := flag.String("token", os.Getenv("JOB_DISPATCHER_TOKEN"), "bearer token every route except GET /health requires (default: the JOB_DISPATCHER_TOKEN environment variable)")
	allowAnonymous := flag.Bool("allow-anonymous", false, "allow an address other than loopback without a token (an open lab dispatcher)")
	robotTimeout := flag.Duration("robot-timeout", 2*time.Minute, "a robot silent for longer than this is not offered new work (0 disables the check)")
	dbPath := flag.String("db", "", "path to a real SQLite database file for fault-tolerant mission-queue persistence (unset: purely in-memory, cleared on restart)")
	toolsFile := flag.String("tools-file", os.Getenv("JOB_DISPATCHER_TOOLS_FILE"), "JSON file listing the known tool names (the TOOL_* profiles of the tool heads); a job or robot naming another tool only gets a warning in the log (default: the JOB_DISPATCHER_TOOLS_FILE environment variable)")
	toolsList := flag.String("tools", os.Getenv("JOB_DISPATCHER_TOOLS"), "comma-separated known tool names, added to those of -tools-file (default: the JOB_DISPATCHER_TOOLS environment variable)")
	flag.Parse()

	if problem := startupProblem(*addr, *token, *allowAnonymous); problem != "" {
		log.Fatalf("[job-dispatcher] refusing to start: %s", problem)
	}

	fmt.Printf("HYDRA-UMC-JOB-DISPATCHER v%s\n", Version)
	fmt.Println("Priority-based mission queue: routes jobs to the best-available robot in the fleet based on location, tool and current load.")

	var engine *dispatcher.Engine
	if *dbPath == "" {
		fmt.Println("[job-dispatcher] -db not set: purely in-memory, the mission queue does NOT survive a restart")
		engine = dispatcher.NewEngine()
	} else {
		store, err := sqlitestore.Open(*dbPath)
		if err != nil {
			log.Fatalf("[job-dispatcher] opening SQLite database %q: %v", *dbPath, err)
		}
		engine, err = dispatcher.NewEngineWithStore(store)
		if err != nil {
			log.Fatalf("[job-dispatcher] loading persisted mission queue from %q: %v", *dbPath, err)
		}
		fmt.Printf("[job-dispatcher] persisting the mission queue to %s\n", *dbPath)
	}
	engine.SetRobotFreshness(*robotTimeout)
	catalog, err := buildToolCatalog(*toolsFile, *toolsList)
	if err != nil {
		log.Fatalf("[job-dispatcher] %v", err)
	}
	if catalog != nil {
		engine.SetToolCatalog(catalog)
		fmt.Printf("[job-dispatcher] tool catalog: %d known tool(s); an unknown tool name is only warned about\n", len(catalog.Names()))
	}
	server := api.New(engine)
	if *token != "" {
		server.RequireToken(*token)
		fmt.Println("[job-dispatcher] every route except GET /health requires a bearer token")
	} else if !isLoopbackAddr(*addr) {
		fmt.Println("[job-dispatcher] WARNING: -allow-anonymous: this API is open to the network")
	}

	fmt.Printf("[job-dispatcher] HTTP API listening on %s\n", *addr)
	fmt.Println("[job-dispatcher] POST /robots, POST /jobs, POST /jobs/submit, POST /dispatch, POST /jobs/complete, POST /jobs/cancel, POST /jobs/requeue, POST /jobs/detect-stale, GET /jobs, GET /jobs/{id}, GET /robots, GET /health")
	if *robotTimeout > 0 {
		// Nothing else calls detect-stale: without this a job whose robot went silent stayed
		// "assigned" (and the robot reserved) for ever.
		go func() {
			ticker := time.NewTicker(*robotTimeout / 2)
			defer ticker.Stop()
			for range ticker.C {
				if ids := engine.DetectStaleAssignments(*robotTimeout); len(ids) > 0 {
					log.Printf("[job-dispatcher] robot silent for over %s: jobs now unknown: %v", *robotTimeout, ids)
				}
			}
		}()
	}
	// http.ListenAndServe has no timeouts: a client that opens a connection and never finishes a
	// request would hold it (and a goroutine) for ever.
	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           server,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 * 1024,
	}
	log.Fatal(httpServer.ListenAndServe())
}
