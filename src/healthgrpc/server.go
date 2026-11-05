// HYDRA-UMC-JOB-DISPATCHER - the health service of the shared contract over gRPC
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// HealthService.Check() and the streaming HealthService.Watch() of proto/hydra_common.proto, so
// HYDRA-UMC-NODE-HEALING can follow this service without polling its HTTP route. It is switched on
// by the "health_grpc" section of the configuration file and off otherwise. It reads, it never
// changes the queue.
//
// Limits: a bounded number of connections (one past the limit is closed at once), a bounded number
// of open Watch streams (past it the call is refused with RESOURCE_EXHAUSTED), small messages,
// HTTP/2 keepalive pings so a client that vanished is dropped, and every Watch goroutine ends with
// its stream, its client or the server.
package healthgrpc

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	pb "github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/healthpb"
)

// Defaults of the "health_grpc" section.
const (
	DefaultListen         = "127.0.0.1:50090"
	DefaultMaxConnections = 16
	DefaultMaxWatchers    = 8
	DefaultWatchInterval  = 5 * time.Second
	DefaultKeepalive      = 30 * time.Second
	maxMessageBytes       = 1024
	stopGrace             = 2 * time.Second
)

// Config is what the service runs with.
type Config struct {
	Listen         string        // address to listen on (default DefaultListen)
	Token          string        // bearer token every call needs; empty is only for loopback
	// Identity, when set, decides whether a bearer token is a valid signed token of HYDRA-UMC-SERVER (any role: the service only reads). It is accepted next
	// to Token, and while it is set a call with no credential is refused even on loopback.
	Identity func(token string) bool
	MaxConnections int           // most connections open at once
	MaxWatchers    int           // most Watch streams open at once
	WatchInterval  time.Duration // how often a Watch stream repeats the report when nothing changed
	Keepalive      time.Duration // HTTP/2 ping period; a client that does not answer within it is dropped
}

// withDefaults fills what was left at zero.
func (c Config) withDefaults() Config {
	if c.Listen == "" {
		c.Listen = DefaultListen
	}
	if c.MaxConnections <= 0 {
		c.MaxConnections = DefaultMaxConnections
	}
	if c.MaxWatchers <= 0 {
		c.MaxWatchers = DefaultMaxWatchers
	}
	if c.WatchInterval <= 0 {
		c.WatchInterval = DefaultWatchInterval
	}
	if c.Keepalive <= 0 {
		c.Keepalive = DefaultKeepalive
	}
	return c
}

// Source gives the current report; it is called for every Check and on every tick of a Watch.
type Source func() *pb.HealthReport

// ListenProblem is why this address may not be served without a token (empty when it may): an
// address other than loopback needs one.
func ListenProblem(listen, token string) string {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return "health_grpc.listen must be an address and a port such as 127.0.0.1:50090"
	}
	if token != "" || strings.EqualFold(host, "localhost") {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return ""
	}
	return "health_grpc.listen is reachable from the network but there is no token: name one with health_grpc.token_env (or run the API with a token), or listen on 127.0.0.1"
}

// Server is a running health service.
type Server struct {
	grpc     *grpc.Server
	listener net.Listener
	svc      *service
	stopOnce sync.Once
	served   chan struct{}
}

// Start listens and serves in the background. It returns once the port is open, or why it is not.
func Start(cfg Config, source Source) (*Server, error) {
	cfg = cfg.withDefaults()
	// an identity check stands in for the token when it is set: nothing is open then, so an address reachable from the network is served
	credential := cfg.Token
	if credential == "" && cfg.Identity != nil {
		credential = "identity"
	}
	if problem := ListenProblem(cfg.Listen, credential); problem != "" {
		return nil, errors.New(problem)
	}
	raw, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return nil, err
	}
	svc := &service{source: source, max: int64(cfg.MaxWatchers), interval: cfg.WatchInterval, done: make(chan struct{})}
	auth := authorizer{token: cfg.Token, identity: cfg.Identity}
	g := grpc.NewServer(
		grpc.UnaryInterceptor(auth.unary),
		grpc.StreamInterceptor(auth.stream),
		grpc.MaxRecvMsgSize(maxMessageBytes),
		grpc.MaxConcurrentStreams(uint32(cfg.MaxWatchers+8)),
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: cfg.Keepalive, Timeout: cfg.Keepalive}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: time.Second, PermitWithoutStream: true}),
	)
	pb.RegisterHealthServiceServer(g, svc)
	s := &Server{grpc: g, listener: raw, svc: svc, served: make(chan struct{})}
	limited := &limitedListener{Listener: raw, slots: make(chan struct{}, cfg.MaxConnections)}
	go func() {
		defer close(s.served)
		_ = g.Serve(limited)
	}()
	return s, nil
}

// Addr is the address the service listens on.
func (s *Server) Addr() net.Addr { return s.listener.Addr() }

// Watchers is how many Watch streams are open now.
func (s *Server) Watchers() int { return int(s.svc.watchers.Load()) }

// Stop ends the Watch streams, lets the calls in flight finish for a moment and closes the port.
// It can be called more than once.
func (s *Server) Stop() {
	s.stopOnce.Do(func() {
		close(s.svc.done)
		timer := time.AfterFunc(stopGrace, s.grpc.Stop)
		s.grpc.GracefulStop()
		timer.Stop()
		<-s.served
	})
}

type service struct {
	pb.UnimplementedHealthServiceServer
	source   Source
	max      int64
	interval time.Duration
	watchers atomic.Int64
	done     chan struct{}
}

func (s *service) Check(context.Context, *pb.Empty) (*pb.HealthReport, error) {
	return s.source(), nil
}

// Watch sends the report at once, again whenever the state or the detail changes, and at least
// every interval as a keepalive, until the client goes away or the server stops.
func (s *service) Watch(_ *pb.Empty, stream grpc.ServerStreamingServer[pb.HealthReport]) error {
	if s.watchers.Add(1) > s.max {
		s.watchers.Add(-1)
		return status.Error(codes.ResourceExhausted, "too many Watch streams are open")
	}
	defer s.watchers.Add(-1)
	tick := time.Second
	if s.interval < tick {
		tick = s.interval
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	ctx := stream.Context()
	var last *pb.HealthReport
	var lastSent time.Time
	for {
		report := s.source()
		if last == nil || report.GetState() != last.GetState() || report.GetDetail() != last.GetDetail() || time.Since(lastSent) >= s.interval {
			if err := stream.Send(report); err != nil {
				return err
			}
			last, lastSent = report, time.Now()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.done:
			return status.Error(codes.Unavailable, "the service is stopping")
		case <-ticker.C:
		}
	}
}

type authorizer struct {
	token    string
	identity func(token string) bool
}

func (a authorizer) check(ctx context.Context) error {
	if a.token == "" && a.identity == nil {
		return nil
	}
	md, _ := metadata.FromIncomingContext(ctx)
	for _, v := range md.Get("authorization") {
		presented, ok := strings.CutPrefix(v, "Bearer ")
		if !ok {
			continue
		}
		if a.token != "" && constantTimeEqual(presented, a.token) {
			return nil
		}
		if a.identity != nil && presented != "" && a.identity(presented) {
			return nil
		}
	}
	return status.Error(codes.Unauthenticated, "missing or wrong bearer token")
}

func (a authorizer) unary(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if err := a.check(ctx); err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

func (a authorizer) stream(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	if err := a.check(ss.Context()); err != nil {
		return err
	}
	return handler(srv, ss)
}

// constantTimeEqual compares two strings without stopping at the first difference.
func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

// limitedListener closes a connection that would pass the limit instead of queueing it.
type limitedListener struct {
	net.Listener
	slots chan struct{}
}

func (l *limitedListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.slots <- struct{}{}:
			return &limitedConn{Conn: conn, release: func() { <-l.slots }}, nil
		default:
			_ = conn.Close()
		}
	}
}

type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
