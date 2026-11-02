// HYDRA-UMC-JOB-DISPATCHER - the real state behind the gRPC health service
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package healthgrpc

import (
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/dispatcher"
	pb "github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/healthpb"
)

// NodeName is the name this service answers with; the watchdog registers it under the same one.
const NodeName = "HYDRA-UMC-JOB-DISPATCHER"

// Queue is what the report reads from the engine.
type Queue interface {
	LastPersistError() error
	JobsPage(status dispatcher.JobStatus, offset, limit int) ([]dispatcher.Job, int)
}

// queueProbeTimeout and reportCacheTTL are variables only so the tests can shorten them.
var (
	queueProbeTimeout = 2 * time.Second
	reportCacheTTL    = time.Second
)

type queueCounts struct{ pending, assigned, unknown int }

// QueueSource is the report of the dispatcher: OK only when the queue answers and, if it is
// stored, the store accepts writes. A queue that does not answer within two seconds is UNHEALTHY;
// a store that failed its last write makes it DEGRADED (it still routes, but the transitions are
// being lost - the same condition the HTTP /health answers with 503). persistent says whether a
// database was asked for. The report is built at most once a second however many streams read it,
// and only one probe of the queue is ever outstanding.
func QueueSource(q Queue, version string, persistent bool) Source {
	var mu sync.Mutex
	var cached *pb.HealthReport
	var cachedAt time.Time
	var probing atomic.Bool
	identity := &pb.NodeIdentity{
		Name:    NodeName,
		Version: version,
		Role:    "Priority mission queue",
		Family:  "orchestration-swarm",
		Deploy:  pb.DeployTarget_DEPLOY_TARGET_CM5,
	}
	build := func() *pb.HealthReport {
		report := &pb.HealthReport{Identity: identity, State: pb.HealthState_HEALTH_STATE_OK, Detail: "ready", Metrics: map[string]string{"persistent": strconv.FormatBool(persistent)}}
		if !probing.CompareAndSwap(false, true) {
			report.State, report.Detail = pb.HealthState_HEALTH_STATE_UNHEALTHY, "the queue has not answered yet to an earlier check"
			return report
		}
		answer := make(chan queueCounts, 1)
		go func() {
			defer probing.Store(false)
			var c queueCounts
			_, c.pending = q.JobsPage(dispatcher.StatusPending, 0, 1)
			_, c.assigned = q.JobsPage(dispatcher.StatusAssigned, 0, 1)
			_, c.unknown = q.JobsPage(dispatcher.StatusUnknown, 0, 1)
			answer <- c
		}()
		timer := time.NewTimer(queueProbeTimeout)
		defer timer.Stop()
		select {
		case c := <-answer:
			report.Metrics["jobs_pending"] = strconv.Itoa(c.pending)
			report.Metrics["jobs_assigned"] = strconv.Itoa(c.assigned)
			report.Metrics["jobs_unknown"] = strconv.Itoa(c.unknown)
		case <-timer.C:
			report.State, report.Detail = pb.HealthState_HEALTH_STATE_UNHEALTHY, "the queue does not answer"
			return report
		}
		if err := q.LastPersistError(); err != nil {
			report.State, report.Detail = pb.HealthState_HEALTH_STATE_DEGRADED, "the persistence store reported an error (see the process log)"
		}
		return report
	}
	return func() *pb.HealthReport {
		mu.Lock()
		defer mu.Unlock()
		if cached == nil || time.Since(cachedAt) >= reportCacheTTL {
			cached, cachedAt = build(), time.Now()
		}
		return cached
	}
}
