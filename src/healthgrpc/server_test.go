// HYDRA-UMC-JOB-DISPATCHER - tests of the gRPC health service
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// A real gRPC client talks to the real server over a real loopback connection; only the report is
// a stand-in where the test needs to choose the state. Nothing here touches a robot or a cell.
package healthgrpc

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/dispatcher"
	pb "github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/healthpb"
)

func fixed(state pb.HealthState) Source {
	return func() *pb.HealthReport {
		return &pb.HealthReport{Identity: &pb.NodeIdentity{Name: NodeName, Version: "9.9.9"}, State: state, Detail: "fixed"}
	}
}

func start(t *testing.T, cfg Config, source Source) (*Server, pb.HealthServiceClient, *grpc.ClientConn) {
	t.Helper()
	cfg.Listen = "127.0.0.1:0"
	server, err := Start(cfg, source)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(server.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return server, pb.NewHealthServiceClient(conn), conn
}

func ctxWith(t *testing.T, token string) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	if token != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	}
	return ctx
}

func TestCheckAnswersTheReportOverARealConnection(t *testing.T) {
	_, client, _ := start(t, Config{}, fixed(pb.HealthState_HEALTH_STATE_OK))
	report, err := client.Check(ctxWith(t, ""), &pb.Empty{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if report.GetState() != pb.HealthState_HEALTH_STATE_OK || report.GetIdentity().GetName() != NodeName || report.GetIdentity().GetVersion() != "9.9.9" {
		t.Fatalf("unexpected report: %v", report)
	}
}

func TestATokenIsRequiredWhenOneIsSet(t *testing.T) {
	_, client, _ := start(t, Config{Token: "s3cret"}, fixed(pb.HealthState_HEALTH_STATE_OK))
	for name, token := range map[string]string{"none": "", "wrong": "nope", "prefix only": "s3cre"} {
		if _, err := client.Check(ctxWith(t, token), &pb.Empty{}); status.Code(err) != codes.Unauthenticated {
			t.Errorf("Check with %s token: %v", name, err)
		}
		stream, err := client.Watch(ctxWith(t, token), &pb.Empty{})
		if err == nil {
			_, err = stream.Recv()
		}
		if status.Code(err) != codes.Unauthenticated {
			t.Errorf("Watch with %s token: %v", name, err)
		}
	}
	if _, err := client.Check(ctxWith(t, "s3cret"), &pb.Empty{}); err != nil {
		t.Errorf("Check with the right token: %v", err)
	}
	stream, err := client.Watch(ctxWith(t, "s3cret"), &pb.Empty{})
	if err != nil {
		t.Fatalf("Watch with the right token: %v", err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Errorf("Recv with the right token: %v", err)
	}
}

func TestWatchSendsAtOnceRepeatsAndFollowsAChange(t *testing.T) {
	var unhealthy atomic.Bool
	source := func() *pb.HealthReport {
		state := pb.HealthState_HEALTH_STATE_OK
		if unhealthy.Load() {
			state = pb.HealthState_HEALTH_STATE_UNHEALTHY
		}
		return &pb.HealthReport{Identity: &pb.NodeIdentity{Name: NodeName}, State: state}
	}
	_, client, _ := start(t, Config{WatchInterval: 200 * time.Millisecond}, source)
	stream, err := client.Watch(ctxWith(t, ""), &pb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	first, err := stream.Recv()
	if err != nil || first.GetState() != pb.HealthState_HEALTH_STATE_OK {
		t.Fatalf("first report: %v %v", first, err)
	}
	if time.Since(began) > 150*time.Millisecond {
		t.Errorf("the first report took %v, it must be immediate", time.Since(began))
	}
	if repeat, err := stream.Recv(); err != nil || repeat.GetState() != pb.HealthState_HEALTH_STATE_OK {
		t.Fatalf("keepalive repeat: %v %v", repeat, err)
	}
	unhealthy.Store(true)
	deadline := time.Now().Add(3 * time.Second)
	for {
		report, err := stream.Recv()
		if err != nil {
			t.Fatalf("the stream broke before the change arrived: %v", err)
		}
		if report.GetState() == pb.HealthState_HEALTH_STATE_UNHEALTHY {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the change never reached the stream")
		}
	}
}

func TestWatchStreamsAreLimitedAndTheirPlacesComeBack(t *testing.T) {
	server, client, _ := start(t, Config{MaxWatchers: 2}, fixed(pb.HealthState_HEALTH_STATE_OK))
	open := func() (grpc.ServerStreamingClient[pb.HealthReport], context.CancelFunc, error) {
		ctx, cancel := context.WithCancel(context.Background())
		stream, err := client.Watch(ctx, &pb.Empty{})
		if err == nil {
			_, err = stream.Recv()
		}
		return stream, cancel, err
	}
	_, cancelA, err := open()
	if err != nil {
		t.Fatal(err)
	}
	_, cancelB, err := open()
	if err != nil {
		t.Fatal(err)
	}
	defer cancelB()
	if _, cancel, err := open(); status.Code(err) != codes.ResourceExhausted {
		cancel()
		t.Fatalf("the third stream must be refused, got %v", err)
	} else {
		cancel()
	}
	if got := server.Watchers(); got != 2 {
		t.Fatalf("Watchers() = %d, want 2", got)
	}
	cancelA() // the client goes away: the place is free again
	waitFor(t, "a place to come back", func() bool {
		_, cancel, err := open()
		cancel()
		return err == nil
	})
}

func TestConnectionsPastTheLimitAreClosed(t *testing.T) {
	server, client, _ := start(t, Config{MaxConnections: 1}, fixed(pb.HealthState_HEALTH_STATE_OK))
	if _, err := client.Check(ctxWith(t, ""), &pb.Empty{}); err != nil {
		t.Fatal(err)
	}
	// a second client cannot get an answer while the first holds the only place
	second, err := grpc.NewClient(server.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if _, err := pb.NewHealthServiceClient(second).Check(ctx, &pb.Empty{}); err == nil {
		t.Fatal("a connection past the limit was served")
	}
	if _, err := client.Check(ctxWith(t, ""), &pb.Empty{}); err != nil {
		t.Fatalf("the first connection must not be affected: %v", err)
	}
}

func TestStopEndsTheStreamsAndLeavesNoGoroutines(t *testing.T) {
	before := runtime.NumGoroutine()
	cfg := Config{Listen: "127.0.0.1:0", WatchInterval: 100 * time.Millisecond}
	server, err := Start(cfg, fixed(pb.HealthState_HEALTH_STATE_OK))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := grpc.NewClient(server.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	client := pb.NewHealthServiceClient(conn)
	var streams []grpc.ServerStreamingClient[pb.HealthReport]
	for i := 0; i < 4; i++ {
		stream, err := client.Watch(context.Background(), &pb.Empty{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stream.Recv(); err != nil {
			t.Fatal(err)
		}
		streams = append(streams, stream)
	}
	server.Stop()
	for i, stream := range streams {
		done := make(chan error, 1)
		go func() {
			for {
				if _, err := stream.Recv(); err != nil {
					done <- err
					return
				}
			}
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatalf("stream %d did not end when the server stopped", i)
		}
	}
	_ = conn.Close()
	server.Stop() // a second Stop is harmless
	if got := server.Watchers(); got != 0 {
		t.Errorf("Watchers() = %d after Stop", got)
	}
	waitFor(t, "the goroutines to go away", func() bool { return runtime.NumGoroutine() <= before+2 })
}

func TestStartRefusesWhatCannotBeServed(t *testing.T) {
	if _, err := Start(Config{Listen: "0.0.0.0:0"}, fixed(pb.HealthState_HEALTH_STATE_OK)); err == nil {
		t.Error("an address other than loopback without a token must be refused")
	}
	taken, err := Start(Config{Listen: "127.0.0.1:0"}, fixed(pb.HealthState_HEALTH_STATE_OK))
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Stop()
	if _, err := Start(Config{Listen: taken.Addr().String()}, fixed(pb.HealthState_HEALTH_STATE_OK)); err == nil {
		t.Error("a port in use must be an error")
	}
	if got := ListenProblem("localhost:1", ""); got != "" {
		t.Errorf("localhost is loopback: %q", got)
	}
	if got := ListenProblem("[::1]:1", ""); got != "" {
		t.Errorf("::1 is loopback: %q", got)
	}
	if ListenProblem("10.0.0.5:1", "") == "" || ListenProblem("10.0.0.5:1", "t") != "" || ListenProblem("nonsense", "") == "" {
		t.Error("ListenProblem does not tell the cases apart")
	}
}

// fakeQueue lets a test choose how the queue behaves.
type fakeQueue struct {
	persistErr error
	block      chan struct{}
	calls      atomic.Int64
}

func (f *fakeQueue) LastPersistError() error { return f.persistErr }
func (f *fakeQueue) JobsPage(dispatcher.JobStatus, int, int) ([]dispatcher.Job, int) {
	f.calls.Add(1)
	if f.block != nil {
		<-f.block
	}
	return nil, 0
}

func TestQueueSourceStates(t *testing.T) {
	engine := dispatcher.NewEngine()
	if err := engine.AddJob(dispatcher.Job{ID: "j1", Priority: 1}); err != nil {
		t.Fatal(err)
	}
	report := QueueSource(engine, "1.2.3", true)()
	if report.GetState() != pb.HealthState_HEALTH_STATE_OK || report.GetMetrics()["jobs_pending"] != "1" || report.GetMetrics()["persistent"] != "true" || report.GetIdentity().GetName() != NodeName {
		t.Fatalf("a working queue: %v", report)
	}
	degraded := QueueSource(&fakeQueue{persistErr: errors.New("disk full")}, "1.2.3", true)()
	if degraded.GetState() != pb.HealthState_HEALTH_STATE_DEGRADED {
		t.Fatalf("a failing store: %v", degraded)
	}
	if degraded.GetDetail() == "" || strings.Contains(degraded.GetDetail(), "disk full") {
		t.Errorf("the detail must be there and must not carry the error text: %q", degraded.GetDetail())
	}
}

func TestQueueSourceNotAnsweringIsUnhealthyAndDoesNotPileUpProbes(t *testing.T) {
	oldTimeout, oldTTL := queueProbeTimeout, reportCacheTTL
	queueProbeTimeout, reportCacheTTL = 100*time.Millisecond, time.Millisecond
	defer func() { queueProbeTimeout, reportCacheTTL = oldTimeout, oldTTL }()
	q := &fakeQueue{block: make(chan struct{})}
	source := QueueSource(q, "1", false)
	if got := source().GetState(); got != pb.HealthState_HEALTH_STATE_UNHEALTHY {
		t.Fatalf("a queue that does not answer: %v", got)
	}
	for i := 0; i < 20; i++ {
		time.Sleep(2 * time.Millisecond)
		if got := source().GetState(); got != pb.HealthState_HEALTH_STATE_UNHEALTHY {
			t.Fatalf("still blocked: %v", got)
		}
	}
	if got := q.calls.Load(); got != 1 {
		t.Errorf("%d probes are outstanding, want exactly 1", got)
	}
	close(q.block)
	waitFor(t, "the queue to be healthy again", func() bool {
		return source().GetState() == pb.HealthState_HEALTH_STATE_OK
	})
}

func TestTheReportIsSharedByManyReaders(t *testing.T) {
	q := &fakeQueue{}
	source := QueueSource(q, "1", false)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = source()
			}
		}()
	}
	wg.Wait()
	if got := q.calls.Load(); got > 6 {
		t.Errorf("the queue was asked %d times by 800 reads in a moment, the cache must absorb them", got)
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
