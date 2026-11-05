// HYDRA-UMC-JOB-DISPATCHER - entry point
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// Real priority mission queue, no longer just an identity print:
// src/dispatcher implements the scheduling algorithm (tool-aware
// routing, multi-stage dependencies, priority bypass, deterministic
// priority ordering, and DedupKey-based idempotent submission), src/api
// exposes it over plain HTTP/JSON (POST /jobs, POST /jobs/submit, POST
// /robots, POST /dispatch, GET /dispatch/waiting, POST /jobs/complete, GET /jobs, GET /robots,
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
	"os"
	"strings"
	"time"

	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/api"
	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/dispatcher"
	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/healthgrpc"
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

// productionEventsProblem is why the production events cannot be switched on (empty when they can, or are off): the switch needs somewhere to send them.
func productionEventsProblem(on bool, file, url string) string {
	if !on {
		return ""
	}
	if file == "" && url == "" {
		return "-production-events needs -production-events-file and/or -production-events-url"
	}
	return ""
}

// buildProductionSink joins the sinks that were asked for; the HTTP one runs on its own goroutine so a slow receiver never holds up a completion.
func buildProductionSink(file, url, token string, identityKey []byte) dispatcher.ProductionEventSink {
	var sinks []dispatcher.ProductionEventSink
	if file != "" {
		sinks = append(sinks, dispatcher.JSONLinesSink(file))
	}
	if url != "" {
		post := dispatcher.HTTPSink(url, token, 5*time.Second)
		if identityKey != nil {
			post = dispatcher.SignedHTTPSink(url, identityKey, 5*time.Second) // a token of the SERVER format, signed for every request
		}
		async, _ := dispatcher.AsyncSink(post, 256)
		sinks = append(sinks, async)
	}
	return func(event dispatcher.ProductionEvent) error {
		var first error
		for _, sink := range sinks {
			if err := sink(event); err != nil && first == nil {
				first = err
			}
		}
		return first
	}
}

// configProblem is why the configuration file cannot be used with the switches given (empty when it can): a range for the estimates needs the estimates.
func configProblem(cfg dispatcher.FileConfig, etaOn bool) string {
	if cfg.IntervalSetting().Enabled && !etaOn {
		return "eta_interval is enabled in the configuration file but the estimates are off: add -eta"
	}
	return ""
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8090", "address to listen on for the HTTP API (loopback by default: another address needs a token)")
	token := flag.String("token", os.Getenv("JOB_DISPATCHER_TOKEN"), "bearer token every route except GET /health requires (default: the JOB_DISPATCHER_TOKEN environment variable)")
	serverIdentityKeyFile := flag.String("identity-key-file", os.Getenv("JOB_DISPATCHER_IDENTITY_KEY_FILE"), "file with the signing key of HYDRA-UMC-SERVER (its JWT_SECRET): a valid token of the SERVER is then accepted next to -token, every route (GET /health and GET /metrics too) demands an identity and a viewer may read but not change; off by default (default: the JOB_DISPATCHER_IDENTITY_KEY_FILE environment variable)")
	allowAnonymous := flag.Bool("allow-anonymous", false, "allow an address other than loopback without a token (an open lab dispatcher)")
	robotTimeout := flag.Duration("robot-timeout", 2*time.Minute, "a robot silent for longer than this is not offered new work (0 disables the check)")
	dbPath := flag.String("db", "", "path to a real SQLite database file for fault-tolerant mission-queue persistence (unset: purely in-memory, cleared on restart)")
	toolsFile := flag.String("tools-file", os.Getenv("JOB_DISPATCHER_TOOLS_FILE"), "JSON file listing the known tool names (the TOOL_* profiles of the tool heads); a job or robot naming another tool only gets a warning in the log (default: the JOB_DISPATCHER_TOOLS_FILE environment variable)")
	toolsList := flag.String("tools", os.Getenv("JOB_DISPATCHER_TOOLS"), "comma-separated known tool names, added to those of -tools-file (default: the JOB_DISPATCHER_TOOLS environment variable)")
	maxBody := flag.Int64("max-body-bytes", api.MaxBodyBytes, "largest request body, in bytes, that any route reads (a larger one is refused with 413)")
	readHeaderTimeout := flag.Duration("read-header-timeout", defaultServerLimits.ReadHeaderTimeout, "how long a client may take to send the request headers")
	readTimeout := flag.Duration("read-timeout", defaultServerLimits.ReadTimeout, "how long a client may take to send a whole request")
	writeTimeout := flag.Duration("write-timeout", defaultServerLimits.WriteTimeout, "how long the answer to a request may take to be written")
	idleTimeout := flag.Duration("idle-timeout", defaultServerLimits.IdleTimeout, "how long an idle keep-alive connection is kept open")
	etaOn := flag.Bool("eta", os.Getenv("JOB_DISPATCHER_ETA") == "1", "switch on GET /dispatch/eta: the estimated start and end of every unfinished job and of the whole queue (off by default; the environment variable JOB_DISPATCHER_ETA=1 does the same)")
	etaDefault := flag.Duration("eta-default-duration", time.Minute, "duration assumed for a job until a finished job has been observed (0 = no guess); only with -eta")
	etaWindow := flag.Int("eta-window", dispatcher.DefaultETAWindow, "how many recent finished jobs per tool the estimate averages; only with -eta")
	productionOn := flag.Bool("production-events", os.Getenv("JOB_DISPATCHER_PRODUCTION_EVENTS") == "1", "hand over a ProductionEvent of the shared contract (when the cycle ended, how long it took, whether it went well) every time a robot reports that a job ended; needs -production-events-file and/or -production-events-url (off by default; JOB_DISPATCHER_PRODUCTION_EVENTS=1 does the same)")
	productionFile := flag.String("production-events-file", os.Getenv("JOB_DISPATCHER_PRODUCTION_EVENTS_FILE"), "append every production event as one JSON line to this file; only with -production-events")
	productionURL := flag.String("production-events-url", os.Getenv("JOB_DISPATCHER_PRODUCTION_EVENTS_URL"), "POST every production event as JSON to this URL; only with -production-events")
	productionKeyFile := flag.String("production-events-identity-key-file", os.Getenv("JOB_DISPATCHER_PRODUCTION_EVENTS_IDENTITY_KEY_FILE"), "file with the signing key of HYDRA-UMC-SERVER (its JWT_SECRET): every event sent with -production-events-url carries a short-lived token signed with it (role operator), instead of -production-events-token")
	productionToken := flag.String("production-events-token", os.Getenv("JOB_DISPATCHER_PRODUCTION_EVENTS_TOKEN"), "bearer token sent with -production-events-url (default: the JOB_DISPATCHER_PRODUCTION_EVENTS_TOKEN environment variable)")
	configPath := flag.String("config", os.Getenv("JOB_DISPATCHER_CONFIG"), "JSON file with the tuned values of the optional behaviours: location_costs (the closest robot by a declared table of costs), replan (jobs of a robot that failed or went silent go back to the queue) and eta_interval (a range next to every estimate, needs -eta); every one is off unless the file turns it on (default: the JOB_DISPATCHER_CONFIG environment variable)")
	flag.Parse()
	fileConfig := dispatcher.FileConfig{}
	if *configPath != "" {
		loaded, err := dispatcher.LoadConfigFile(*configPath)
		if err != nil {
			log.Fatalf("[job-dispatcher] refusing to start: %v", err)
		}
		fileConfig = loaded
		if problem := configProblem(fileConfig, *etaOn); problem != "" {
			log.Fatalf("[job-dispatcher] refusing to start: %s", problem)
		}
	}
	if problem := productionEventsProblem(*productionOn, *productionFile, *productionURL); problem != "" {
		log.Fatalf("[job-dispatcher] refusing to start: %s", problem)
	}
	limits := serverLimits{ReadHeaderTimeout: *readHeaderTimeout, ReadTimeout: *readTimeout, WriteTimeout: *writeTimeout, IdleTimeout: *idleTimeout}
	if problem := limits.problem(*maxBody); problem != "" {
		log.Fatalf("[job-dispatcher] refusing to start: %s", problem)
	}

	var serverIdentityKey []byte
	if *serverIdentityKeyFile != "" {
		key, err := dispatcher.LoadIdentityKey(*serverIdentityKeyFile)
		if err != nil {
			log.Fatalf("[job-dispatcher] refusing to start: -identity-key-file: %v", err)
		}
		serverIdentityKey = key
	}
	if problem := startupProblem(*addr, *token, *allowAnonymous || serverIdentityKey != nil); problem != "" {
		log.Fatalf("[job-dispatcher] refusing to start: %s", problem)
	}
	healthGRPC, err := healthGRPCSettings(fileConfig.HealthGRPC, *token, os.Getenv, serverIdentityKey)
	if err != nil {
		log.Fatalf("[job-dispatcher] refusing to start: %v", err)
	}

	fmt.Printf("HYDRA-UMC-JOB-DISPATCHER v%s\n", Version)
	fmt.Println("Priority-based mission queue: routes jobs to the best-available robot in the fleet based on location, tool and current load.")

	var engine *dispatcher.Engine
	var eventsHub *dispatcher.EventHub
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
	if *etaOn {
		engine.SetETA(dispatcher.ETAConfig{Enabled: true, DefaultDuration: *etaDefault, Window: *etaWindow, Interval: fileConfig.IntervalSetting()})
		fmt.Println("[job-dispatcher] GET /dispatch/eta is on: estimates of when each job and the whole queue finish")
	}
	if err := engine.SetLocationCosts(fileConfig.LocationSetting()); err != nil {
		log.Fatalf("[job-dispatcher] refusing to start: %v", err)
	}
	if fileConfig.LocationSetting().Enabled {
		fmt.Println("[job-dispatcher] location costs are on: a job that names a location goes to the robot with the lowest declared cost")
	}
	engine.SetReplan(fileConfig.ReplanSetting())
	if fileConfig.ReplanSetting().Enabled {
		fmt.Println("[job-dispatcher] replanning is on: the jobs of a robot that failed or went silent go back to the queue (POST /jobs/replan)")
	}
	if *productionOn {
		var identityKey []byte
		if *productionKeyFile != "" {
			key, err := dispatcher.LoadIdentityKey(*productionKeyFile)
			if err != nil {
				log.Fatalf("[job-dispatcher] refusing to start: -production-events-identity-key-file: %v", err)
			}
			identityKey = key
		}
		engine.SetProductionEvents(buildProductionSink(*productionFile, *productionURL, *productionToken, identityKey))
		fmt.Println("[job-dispatcher] production events are on: a ProductionEvent is handed over for every job that ends")
	}
	catalog, err := buildToolCatalog(*toolsFile, *toolsList)
	if err != nil {
		log.Fatalf("[job-dispatcher] %v", err)
	}
	if catalog != nil {
		engine.SetToolCatalog(catalog)
		fmt.Printf("[job-dispatcher] tool catalog: %d known tool(s); an unknown tool name is only warned about\n", len(catalog.Names()))
	}
	board := dispatcher.NewMaintenanceBoard(engine)
	board.SetConfig(fileConfig.MaintenanceSetting())
	if fileConfig.MaintenanceSetting().Enabled {
		fmt.Println("[job-dispatcher] maintenance proposals are on: a drift can only PROPOSE an order; a person approves or rejects it (POST /maintenance/proposals/approve)")
	}
	if events := fileConfig.EventsSetting(); events.Enabled {
		hub, err := dispatcher.NewEventHub(events)
		if err != nil {
			log.Fatalf("[job-dispatcher] refusing to start: %v", err)
		}
		engine.SetEvents(hub)
		eventsHub = hub
		fmt.Printf("[job-dispatcher] job events are on: stream %v, %d signed webhook(s) (a change of this section needs a restart)\n", events.SSE, len(events.Webhooks))
	}
	if ms := fileConfig.MissionStateSetting(os.Getenv); ms.Enabled {
		publisher, err := dispatcher.NewStatePublisher(engine, ms, log.Printf)
		if err != nil {
			log.Fatalf("[job-dispatcher] refusing to start: %v", err)
		}
		publisher.Start()
		defer publisher.Stop()
		fmt.Printf("[job-dispatcher] the state of the queue is published, retained and read-only, on %s of %s (a change of this section needs a restart)\n", ms.Topic, ms.URL)
	}
	if tc := fileConfig.ToolConfirmSetting(); tc.Enabled {
		token := ""
		if env := fileConfig.ToolConfirm.TokenEnv; env != "" {
			token = os.Getenv(env)
		}
		timeout := 5 * time.Second
		if fileConfig.ToolConfirm.TimeoutSeconds != nil {
			timeout = time.Duration(*fileConfig.ToolConfirm.TimeoutSeconds * float64(time.Second))
		}
		if err := engine.SetToolConfirmation(tc, dispatcher.HTTPToolFetcher(fileConfig.ToolConfirm.URLTemplate, token, timeout)); err != nil {
			log.Fatalf("[job-dispatcher] refusing to start: %v", err)
		}
		fmt.Printf("[job-dispatcher] tool confirmation is on: a job that needs a tool only goes to a robot whose controller confirms it (unconfirmed: %s)\n", tc.OnUnconfirmed)
	}
	server := api.New(engine)
	if pv := fileConfig.ProposalVerifySetting(); pv.Enabled {
		verifier, err := dispatcher.NewProposalVerifier(pv)
		if err != nil {
			log.Fatalf("[job-dispatcher] refusing to start: %v", err)
		}
		server.SetProposalVerifier(verifier)
		fmt.Printf("[job-dispatcher] signed proposals are checked: a job that carries one is queued only when HYDRA-UMC-SERVER finds it valid (a job without one is refused: %v; a change of this section needs a restart)\n", pv.Require)
	}
	server.SetEvents(eventsHub)
	server.SetMaintenance(board)
	server.SetMaxBodyBytes(*maxBody)
	server.SetVersion(Version)
	if fileConfig.Metrics != nil && fileConfig.Metrics.Enabled {
		server.SetMetricsEnabled(true)
		fmt.Println("[job-dispatcher] GET /metrics is open (this machine, or the bearer token when one is set); numbers only")
	}
	if *configPath != "" {
		// the service can re-read its configuration file while it runs (POST /admin/reload-config, from this machine only)
		server.SetConfigReloader(newConfigReloader(*configPath, engine, *etaOn, *etaDefault, *etaWindow).withBoard(board).Reload)
		fmt.Println("[job-dispatcher] POST /admin/reload-config re-reads the configuration file (from this machine only; an invalid file changes nothing)")
	}
	if serverIdentityKey != nil {
		server.SetIdentityKey(serverIdentityKey)
		fmt.Println("[job-dispatcher] tokens of HYDRA-UMC-SERVER are accepted, and nothing is open: every route needs an identity, a viewer may read but not change")
	}
	if *token != "" {
		server.RequireToken(*token)
		fmt.Println("[job-dispatcher] every route except GET /health requires a bearer token")
	} else if serverIdentityKey == nil && !isLoopbackAddr(*addr) {
		fmt.Println("[job-dispatcher] WARNING: -allow-anonymous: this API is open to the network")
	}

	if healthGRPC != nil {
		health, err := healthgrpc.Start(*healthGRPC, healthgrpc.QueueSource(engine, Version, *dbPath != ""))
		if err != nil {
			log.Fatalf("[job-dispatcher] the gRPC health service could not start: %v", err)
		}
		fmt.Printf("[job-dispatcher] gRPC health service (Check, Watch) listening on %s (a change of its section needs a restart)\n", health.Addr())
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
				result := engine.DetectStaleAssignmentsStop(*robotTimeout)
				unknown, requeued := result.Unknown, result.Requeued
				if len(result.StopRequested) > 0 {
					log.Printf("[job-dispatcher] robot silent for over %s: stop requested for jobs: %v (GET /jobs/stop-requests)", *robotTimeout, result.StopRequested)
				}
				if len(unknown) > 0 {
					log.Printf("[job-dispatcher] robot silent for over %s: jobs now unknown: %v", *robotTimeout, unknown)
				}
				if len(requeued) > 0 {
					log.Printf("[job-dispatcher] robot silent for over %s: jobs back in the queue: %v", *robotTimeout, requeued)
				}
			}
		}()
	}
	// http.ListenAndServe has no timeouts: a client that opens a connection and never finishes a
	// request would hold it (and a goroutine) for ever.
	httpServer := newHTTPServer(*addr, server, limits)
	log.Fatal(httpServer.ListenAndServe())
}
