// HYDRA-UMC-JOB-DISPATCHER - entry point
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0 - see LICENSE
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
	"net/http"

	"github.com/JuanenRac/hydra-umc-job-dispatcher/src/api"
	"github.com/JuanenRac/hydra-umc-job-dispatcher/src/dispatcher"
	"github.com/JuanenRac/hydra-umc-job-dispatcher/src/sqlitestore"
)

func main() {
	addr := flag.String("addr", ":8090", "address to listen on for the HTTP API")
	dbPath := flag.String("db", "", "path to a real SQLite database file for fault-tolerant mission-queue persistence (unset: purely in-memory, cleared on restart)")
	flag.Parse()

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
	server := api.New(engine)

	fmt.Printf("[job-dispatcher] HTTP API listening on %s\n", *addr)
	fmt.Println("[job-dispatcher] POST /robots, POST /jobs, POST /jobs/submit, POST /dispatch, POST /jobs/complete, GET /jobs, GET /robots, GET /health")
	log.Fatal(http.ListenAndServe(*addr, server))
}
