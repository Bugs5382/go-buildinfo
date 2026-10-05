package grpcbuildinfo_test

/*
MIT License

Copyright (c) 2026 Shane

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
*/

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	buildinfo "github.com/Bugs5382/go-buildinfo"
	"github.com/Bugs5382/go-buildinfo/grpcbuildinfo"
	"github.com/Bugs5382/go-buildinfo/health"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	grpchealth "google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
)

const pingMethod = "/test.Echo/Ping"

var echoDesc = grpc.ServiceDesc{
	ServiceName: "test.Echo",
	HandlerType: (*any)(nil),
	Methods: []grpc.MethodDesc{{
		MethodName: "Ping",
		Handler: func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
			in := new(emptypb.Empty)
			if err := dec(in); err != nil {
				return nil, err
			}
			h := func(context.Context, any) (any, error) { return &emptypb.Empty{}, nil }
			if interceptor == nil {
				return h(ctx, in)
			}
			return interceptor(ctx, in, &grpc.UnaryServerInfo{Server: srv, FullMethod: pingMethod}, h)
		},
	}},
}

func stamp(t *testing.T) {
	t.Helper()
	oldV, oldC := buildinfo.Version, buildinfo.Commit
	buildinfo.Version, buildinfo.Commit = "v1.2.3", "abc123"
	t.Cleanup(func() { buildinfo.Version, buildinfo.Commit = oldV, oldC })
}

type harness struct {
	conn *grpc.ClientConn
	hs   *grpchealth.Server
	bi   *grpcbuildinfo.Server
}

func start(t *testing.T, opts ...grpcbuildinfo.Option) harness {
	t.Helper()
	stamp(t)
	hs := grpchealth.NewServer()
	bi, err := grpcbuildinfo.New(append([]grpcbuildinfo.Option{grpcbuildinfo.WithHealthServer(hs)}, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(bi.UnaryServerInterceptor()),
		grpc.ChainStreamInterceptor(bi.StreamServerInterceptor()),
	)
	healthpb.RegisterHealthServer(srv, hs)
	srv.RegisterService(&echoDesc, struct{}{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return harness{conn: conn, hs: hs, bi: bi}
}

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

func check(t *testing.T, conn *grpc.ClientConn, service string) (healthpb.HealthCheckResponse_ServingStatus, metadata.MD) {
	t.Helper()
	var md metadata.MD
	resp, err := healthpb.NewHealthClient(conn).Check(ctx(t), &healthpb.HealthCheckRequest{Service: service}, grpc.Header(&md))
	if err != nil {
		t.Fatalf("Check(%q): %v", service, err)
	}
	return resp.GetStatus(), md
}

func ping(t *testing.T, conn *grpc.ClientConn) metadata.MD {
	t.Helper()
	var md metadata.MD
	if err := conn.Invoke(ctx(t), pingMethod, &emptypb.Empty{}, &emptypb.Empty{}, grpc.Header(&md)); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	return md
}

func one(md metadata.MD, key string) string {
	if v := md.Get(key); len(v) == 1 {
		return v[0]
	}
	return ""
}

type toggle struct {
	calls atomic.Int64
	down  atomic.Bool
}

func (d *toggle) Check(context.Context) error {
	d.calls.Add(1)
	if d.down.Load() {
		return errors.New("dial tcp: connection refused")
	}
	return nil
}

func eventually(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestNewRejectsAnInvalidPrefix(t *testing.T) {
	for _, p := range []string{"Acme", "grpc", "acme_x", "a b"} {
		if _, err := grpcbuildinfo.New(grpcbuildinfo.WithPrefix(p)); !errors.Is(err, health.ErrInvalidName) {
			t.Errorf("New(WithPrefix(%q)) = %v; want ErrInvalidName", p, err)
		}
	}
}

func TestHealthCheckCarriesVersionAndCommit(t *testing.T) {
	h := start(t, grpcbuildinfo.WithPrefix("acme"))
	status, md := check(t, h.conn, "")
	if status != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("status = %v; want SERVING", status)
	}
	if one(md, "acme-version") != "v1.2.3" || one(md, "acme-commit") != "abc123" {
		t.Fatalf("headers = %v; want acme-version and acme-commit", md)
	}
}

func TestDefaultPrefixIsApp(t *testing.T) {
	h := start(t)
	_, md := check(t, h.conn, "")
	if one(md, "app-version") != "v1.2.3" || one(md, "app-commit") != "abc123" {
		t.Fatalf("headers = %v; want app-version and app-commit", md)
	}
}

func TestOtherCallsAreUntouchedByDefault(t *testing.T) {
	h := start(t, grpcbuildinfo.WithPrefix("acme"))
	if md := ping(t, h.conn); one(md, "acme-version") != "" || one(md, "acme-commit") != "" {
		t.Fatalf("Ping headers = %v; want no build info", md)
	}
}

func TestAllRPCsOption(t *testing.T) {
	h := start(t, grpcbuildinfo.WithPrefix("acme"), grpcbuildinfo.WithAllRPCs())
	if md := ping(t, h.conn); one(md, "acme-version") != "v1.2.3" || one(md, "acme-commit") != "abc123" {
		t.Fatalf("unary headers = %v; want build info on every RPC", md)
	}
	stream, err := healthpb.NewHealthClient(h.conn).Watch(ctx(t), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	md, err := stream.Header()
	if err != nil {
		t.Fatalf("Watch header: %v", err)
	}
	if one(md, "acme-version") != "v1.2.3" {
		t.Fatalf("stream headers = %v; want build info on every RPC", md)
	}
}

func TestDependencyHeadersOnHealthCheckOnly(t *testing.T) {
	c := health.New()
	if err := c.Register(health.Dependency{Name: "postgres", Required: true,
		Check:   func(context.Context) error { return nil },
		Version: func(context.Context) (string, error) { return "17.2", nil }}); err != nil {
		t.Fatal(err)
	}
	h := start(t, grpcbuildinfo.WithPrefix("acme"), grpcbuildinfo.WithChecker(c), grpcbuildinfo.WithAllRPCs())
	_, md := check(t, h.conn, "")
	if one(md, "acme-dep-postgres") != "17.2" || one(md, "acme-depstate-postgres") != "ok" {
		t.Fatalf("Check headers = %v; want the postgres version and state", md)
	}
	if md := ping(t, h.conn); one(md, "acme-dep-postgres") != "" {
		t.Fatalf("Ping headers = %v; want dependency headers on the health check only", md)
	}
}

func TestReadinessFollowsRequiredDependencies(t *testing.T) {
	d := &toggle{}
	c := health.New(health.WithTTL(20 * time.Millisecond))
	if err := c.Register(health.Dependency{Name: "postgres", Required: true, Check: d.Check}); err != nil {
		t.Fatal(err)
	}
	h := start(t, grpcbuildinfo.WithChecker(c), grpcbuildinfo.WithServices("acme.v1.Things"))
	for _, svc := range []string{"", "acme.v1.Things"} {
		if st, _ := check(t, h.conn, svc); st != healthpb.HealthCheckResponse_SERVING {
			t.Fatalf("%q status = %v; want SERVING", svc, st)
		}
	}
	d.down.Store(true)
	for _, svc := range []string{"", "acme.v1.Things"} {
		eventually(t, func() bool {
			st, md := check(t, h.conn, svc)
			return st == healthpb.HealthCheckResponse_NOT_SERVING && one(md, "app-depstate-postgres") == "down"
		}, "NOT_SERVING for "+svc)
	}
	d.down.Store(false)
	for _, svc := range []string{"", "acme.v1.Things"} {
		eventually(t, func() bool {
			st, _ := check(t, h.conn, svc)
			return st == healthpb.HealthCheckResponse_SERVING
		}, "recovery for "+svc)
	}
}

func TestOptionalDependencyKeepsServing(t *testing.T) {
	c := health.New()
	if err := c.Register(health.Dependency{Name: "cache", Check: func(context.Context) error { return errors.New("down") }}); err != nil {
		t.Fatal(err)
	}
	h := start(t, grpcbuildinfo.WithChecker(c))
	st, md := check(t, h.conn, "")
	if st != healthpb.HealthCheckResponse_SERVING || one(md, "app-depstate-cache") != "degraded" {
		t.Fatalf("status %v headers %v; want SERVING with cache degraded", st, md)
	}
}

func TestLivenessIgnoresDependencies(t *testing.T) {
	d := &toggle{}
	d.down.Store(true)
	c := health.New()
	if err := c.Register(health.Dependency{Name: "postgres", Required: true, Check: d.Check}); err != nil {
		t.Fatal(err)
	}
	h := start(t, grpcbuildinfo.WithChecker(c), grpcbuildinfo.WithLivenessService("liveness"))
	st, md := check(t, h.conn, "liveness")
	if st != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("liveness = %v; want SERVING while a required dependency is down", st)
	}
	if one(md, "app-version") != "v1.2.3" || one(md, "app-depstate-postgres") != "" {
		t.Fatalf("liveness headers = %v; want build info only", md)
	}
	if n := d.calls.Load(); n != 0 {
		t.Fatalf("liveness ran %d dependency checks; want 0", n)
	}
}

func TestSlowOrPanickingDependencyNeverFailsTheCall(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	c := health.New(health.WithTimeout(50 * time.Millisecond))
	if err := c.Register(
		health.Dependency{Name: "slow", Required: true, Check: func(context.Context) error { <-block; return nil }},
		health.Dependency{Name: "boom", Version: func(context.Context) (string, error) { panic("boom") }},
	); err != nil {
		t.Fatal(err)
	}
	h := start(t, grpcbuildinfo.WithPrefix("acme"), grpcbuildinfo.WithChecker(c))
	begin := time.Now()
	st, md := check(t, h.conn, "")
	if d := time.Since(begin); d > 2*time.Second {
		t.Fatalf("Check took %v; want about the 50ms check timeout", d)
	}
	if st != healthpb.HealthCheckResponse_NOT_SERVING || one(md, "acme-depstate-slow") != "down" || one(md, "acme-dep-boom") != "unknown" {
		t.Fatalf("status %v headers %v; want NOT_SERVING, slow down, boom unknown", st, md)
	}
	if one(md, "acme-version") != "v1.2.3" {
		t.Fatalf("headers = %v; want build info despite the failing dependencies", md)
	}
}

func TestRunDrivesTheServingStatus(t *testing.T) {
	d := &toggle{}
	c := health.New(health.WithTTL(10 * time.Millisecond))
	if err := c.Register(health.Dependency{Name: "postgres", Required: true, Check: d.Check}); err != nil {
		t.Fatal(err)
	}
	h := start(t, grpcbuildinfo.WithChecker(c), grpcbuildinfo.WithInterval(10*time.Millisecond))
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.bi.Run(runCtx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	watch, err := healthpb.NewHealthClient(h.conn).Watch(ctx(t), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	next := func(want healthpb.HealthCheckResponse_ServingStatus) {
		t.Helper()
		for {
			resp, err := watch.Recv()
			if err != nil {
				t.Fatalf("Watch.Recv: %v", err)
			}
			if resp.GetStatus() == want {
				return
			}
		}
	}
	next(healthpb.HealthCheckResponse_SERVING)
	d.down.Store(true)
	next(healthpb.HealthCheckResponse_NOT_SERVING)
	d.down.Store(false)
	next(healthpb.HealthCheckResponse_SERVING)
}
