//go:build integration

package health_test

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
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bugs5382/go-buildinfo/grpcbuildinfo"
	"github.com/Bugs5382/go-buildinfo/health"
	"github.com/Bugs5382/go-buildinfo/httpbuildinfo"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	grpchealth "google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/test/bufconn"
)

type container struct {
	t    *testing.T
	name string
	addr string
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return fmt.Sprint(ln.Addr().(*net.TCPAddr).Port)
}

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// run starts image with a fixed host port, so the address survives a stop
// and start.
func run(t *testing.T, image, port string, args ...string) *container {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH")
	}
	host := freePort(t)
	name := fmt.Sprintf("go-buildinfo-it-%s-%d", strings.NewReplacer("/", "-", ":", "-").Replace(image), time.Now().UnixNano())
	docker(t, append([]string{"run", "-d", "--name", name, "-p", "127.0.0.1:" + host + ":" + port}, append(args, image)...)...)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	return &container{t: t, name: name, addr: "127.0.0.1:" + host}
}

func (c *container) stop()  { docker(c.t, "stop", "-t", "2", c.name) }
func (c *container) start() { docker(c.t, "start", c.name) }

// checkPostgres sends an SSLRequest and expects the one-byte answer, so it
// proves a PostgreSQL server is speaking, not only that the port is open.
func checkPostgres(addr string) func(context.Context) error {
	return func(ctx context.Context) error {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close() }()
		if dl, ok := ctx.Deadline(); ok {
			_ = conn.SetDeadline(dl)
		}
		if _, err := conn.Write([]byte{0, 0, 0, 8, 0x04, 0xd2, 0x16, 0x2f}); err != nil {
			return err
		}
		b := make([]byte, 1)
		if _, err := conn.Read(b); err != nil {
			return err
		}
		if b[0] != 'N' && b[0] != 'S' {
			return errors.New("not a postgres server")
		}
		return nil
	}
}

// checkAMQP sends the AMQP 0-9-1 protocol header and expects a frame back.
func checkAMQP(addr string) func(context.Context) error {
	return func(ctx context.Context) error {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close() }()
		if dl, ok := ctx.Deadline(); ok {
			_ = conn.SetDeadline(dl)
		}
		if _, err := conn.Write([]byte("AMQP\x00\x00\x09\x01")); err != nil {
			return err
		}
		b := make([]byte, 1)
		if _, err := conn.Read(b); err != nil {
			return err
		}
		if b[0] != 1 {
			return errors.New("not an AMQP server")
		}
		return nil
	}
}

func valkeyCmd(ctx context.Context, addr, cmd string) (string, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if _, err := conn.Write([]byte(cmd + "\r\n")); err != nil {
		return "", err
	}
	r := bufio.NewReader(conn)
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "$")))
	if !strings.HasPrefix(line, "$") || err != nil || n < 0 {
		return strings.TrimSpace(line), nil
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return "", err
	}
	return string(body), nil
}

func checkValkey(addr string) func(context.Context) error {
	return func(ctx context.Context) error {
		resp, err := valkeyCmd(ctx, addr, "PING")
		if err != nil {
			return err
		}
		if resp != "+PONG" {
			return errors.New("unexpected PING answer")
		}
		return nil
	}
}

func valkeyVersion(addr string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		info, err := valkeyCmd(ctx, addr, "INFO server")
		if err != nil {
			return "", err
		}
		for _, l := range strings.Split(info, "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(l), "valkey_version:"); ok {
				return v, nil
			}
		}
		return "", errors.New("no version in INFO")
	}
}

type probes struct {
	readyz http.Handler
	conn   *grpc.ClientConn
}

func (p probes) state(t *testing.T) (int, healthpb.HealthCheckResponse_ServingStatus, health.Remote) {
	t.Helper()
	rec := httptest.NewRecorder()
	p.readyz.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := grpcbuildinfo.Read(ctx, p.conn, "acme")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return rec.Code, res.Status, res.Remote
}

func waitFor(t *testing.T, p probes, what string, cond func(int, healthpb.HealthCheckResponse_ServingStatus, health.Remote) bool) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		code, st, remote := p.state(t)
		if cond(code, st, remote) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s: http %d, grpc %v, deps %+v", what, code, st, remote.Dependencies)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func TestIntegrationDependencyOutagesAndRecovery(t *testing.T) {
	var pg, mq, vk *container
	var wg sync.WaitGroup
	wg.Go(func() { pg = run(t, "postgres:17-alpine", "5432", "-e", "POSTGRES_PASSWORD=integration-only") })
	wg.Go(func() { mq = run(t, "rabbitmq:4-management-alpine", "5672") })
	wg.Go(func() { vk = run(t, "valkey/valkey:8", "6379") })
	wg.Wait()
	if t.Failed() || t.Skipped() {
		return
	}

	c := health.New(health.WithTTL(200*time.Millisecond), health.WithTimeout(time.Second), health.WithVersionTTL(time.Second))
	if err := c.Register(
		health.Dependency{Name: "postgres", Required: true, Check: checkPostgres(pg.addr)},
		health.Dependency{Name: "rabbitmq", Required: true, Check: checkAMQP(mq.addr)},
		health.Dependency{Name: "valkey", Check: checkValkey(vk.addr), Version: valkeyVersion(vk.addr)},
	); err != nil {
		t.Fatal(err)
	}

	hh, err := httpbuildinfo.New(httpbuildinfo.WithPrefix("acme"), httpbuildinfo.WithChecker(c))
	if err != nil {
		t.Fatal(err)
	}
	hs := grpchealth.NewServer()
	bi, err := grpcbuildinfo.New(grpcbuildinfo.WithPrefix("acme"), grpcbuildinfo.WithChecker(c), grpcbuildinfo.WithHealthServer(hs))
	if err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(grpc.ChainUnaryInterceptor(bi.UnaryServerInterceptor()))
	healthpb.RegisterHealthServer(srv, hs)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	p := probes{readyz: hh.Readyz(), conn: conn}

	allOK := func(code int, st healthpb.HealthCheckResponse_ServingStatus, r health.Remote) bool {
		for _, n := range []string{"postgres", "rabbitmq", "valkey"} {
			if r.Dependencies[n].State != health.StateOK {
				return false
			}
		}
		return code == http.StatusOK && st == healthpb.HealthCheckResponse_SERVING
	}
	waitFor(t, p, "all dependencies up", allOK)
	if _, _, r := p.state(t); !strings.HasPrefix(r.Dependencies["valkey"].Version, "8.") {
		t.Fatalf("valkey version = %q; want 8.x", r.Dependencies["valkey"].Version)
	}

	vk.stop()
	waitFor(t, p, "valkey degraded, still ready", func(code int, st healthpb.HealthCheckResponse_ServingStatus, r health.Remote) bool {
		return code == http.StatusOK && st == healthpb.HealthCheckResponse_SERVING &&
			r.Dependencies["valkey"].State == health.StateDegraded && strings.HasPrefix(r.Dependencies["valkey"].Version, "8.")
	})
	vk.start()
	waitFor(t, p, "valkey recovered", allOK)

	for _, dep := range []struct {
		name string
		c    *container
	}{{"postgres", pg}, {"rabbitmq", mq}} {
		dep.c.stop()
		waitFor(t, p, dep.name+" down, not ready", func(code int, st healthpb.HealthCheckResponse_ServingStatus, r health.Remote) bool {
			return code == http.StatusServiceUnavailable && st == healthpb.HealthCheckResponse_NOT_SERVING &&
				r.Dependencies[dep.name].State == health.StateDown
		})
		dep.c.start()
		waitFor(t, p, dep.name+" recovered", allOK)
	}
}
