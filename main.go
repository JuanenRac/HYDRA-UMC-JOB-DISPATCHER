// HYDRA-UMC-JOB-DISPATCHER - entry point
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0 - see LICENSE
//
// Real priority mission queue, no longer just an identity print:
// src/dispatcher implements the scheduling algorithm (tool-aware
// routing, multi-stage dependencies, priority bypass), src/api
// exposes it over plain HTTP/JSON (POST /jobs, POST /robots, POST
// /dispatch, POST /jobs/complete, GET /jobs, GET /robots).
//
// Why persistence (Redis/DB, per the README) isn't wired in yet: see the
// doc comment on dispatcher.Engine - the scheduling algorithm needed to
// be proven correct on its own first.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/JuanenRac/hydra-umc-job-dispatcher/src/api"
	"github.com/JuanenRac/hydra-umc-job-dispatcher/src/dispatcher"
)

func main() {
	addr := flag.String("addr", ":8090", "address to listen on for the HTTP API")
	flag.Parse()

	fmt.Printf("HYDRA-UMC-JOB-DISPATCHER v%s\n", Version)
	fmt.Println("Priority-based mission queue: routes jobs to the best-available robot in the fleet based on location, tool and current load.")

	engine := dispatcher.NewEngine()
	server := api.New(engine)

	fmt.Printf("[job-dispatcher] HTTP API listening on %s\n", *addr)
	fmt.Println("[job-dispatcher] POST /robots, POST /jobs, POST /dispatch, POST /jobs/complete, GET /jobs, GET /robots")
	log.Fatal(http.ListenAndServe(*addr, server))
}
