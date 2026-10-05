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
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Bugs5382/go-buildinfo/health"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func newChecker(t *testing.T, opts ...health.Option) (*health.Checker, *clock) {
	t.Helper()
	clk := &clock{now: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)}
	c := health.New(opts...)
	health.SetClock(c, clk.Now)
	return c, clk
}

func register(t *testing.T, c *health.Checker, deps ...health.Dependency) {
	t.Helper()
	if err := c.Register(deps...); err != nil {
		t.Fatalf("Register: %v", err)
	}
}

func dep(t *testing.T, r health.Report, name string) health.DependencyReport {
	t.Helper()
	for _, d := range r.Dependencies {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("report has no dependency %q: %+v", name, r)
	return health.DependencyReport{}
}

type flaky struct {
	calls atomic.Int64
	fail  atomic.Bool
}

func (f *flaky) Check(context.Context) error {
	f.calls.Add(1)
	if f.fail.Load() {
		return errors.New("connect postgres://svc:hunter2@db.example.org:5432/app: refused")
	}
	return nil
}

func TestRegisterValidates(t *testing.T) {
	ok := func(context.Context) error { return nil }
	cases := []struct {
		name string
		dep  health.Dependency
		want error
	}{
		{"empty name", health.Dependency{Check: ok}, health.ErrInvalidName},
		{"upper case", health.Dependency{Name: "Postgres", Check: ok}, health.ErrInvalidName},
		{"underscore", health.Dependency{Name: "my_db", Check: ok}, health.ErrInvalidName},
		{"trailing dash", health.Dependency{Name: "db-", Check: ok}, health.ErrInvalidName},
		{"too long", health.Dependency{Name: strings.Repeat("a", 33), Check: ok}, health.ErrInvalidName},
		{"no probe", health.Dependency{Name: "db"}, health.ErrNoProbe},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := health.New()
			if err := c.Register(tc.dep); !errors.Is(err, tc.want) {
				t.Fatalf("Register(%+v) = %v; want %v", tc.dep, err, tc.want)
			}
		})
	}
	c := health.New()
	register(t, c, health.Dependency{Name: "postgres", Check: ok})
	if err := c.Register(health.Dependency{Name: "postgres", Check: ok}); !errors.Is(err, health.ErrDuplicate) {
		t.Fatalf("second Register = %v; want ErrDuplicate", err)
	}
}

func TestReportAllHealthy(t *testing.T) {
	c, clk := newChecker(t)
	register(t, c,
		health.Dependency{Name: "postgres", Required: true,
			Check:   func(context.Context) error { return nil },
			Version: func(context.Context) (string, error) { return "17.2 (Debian 17.2-1)", nil }},
		health.Dependency{Name: "opa", Version: func(context.Context) (string, error) { return "v1.4.0", nil }},
	)
	r := c.Report(context.Background())
	if r.Status != health.StateOK || !r.Ready {
		t.Fatalf("report = %+v; want ok and ready", r)
	}
	pg := dep(t, r, "postgres")
	if pg.State != health.StateOK || !pg.Required || pg.Error != "" || pg.Version != "17.2 (Debian 17.2-1)" || !pg.CheckedAt.Equal(clk.Now()) {
		t.Fatalf("postgres = %+v", pg)
	}
	opa := dep(t, r, "opa")
	if opa.State != health.StateOK || opa.Version != "v1.4.0" || !opa.CheckedAt.IsZero() {
		t.Fatalf("version-only dependency = %+v; want ok, its version, never checked", opa)
	}
	if r.Dependencies[0].Name != "postgres" || r.Dependencies[1].Name != "opa" {
		t.Fatalf("dependencies out of registration order: %+v", r.Dependencies)
	}
}

func TestRequiredDownFailsReadinessWithoutLeakingTheError(t *testing.T) {
	c, _ := newChecker(t)
	f := &flaky{}
	f.fail.Store(true)
	register(t, c, health.Dependency{Name: "postgres", Required: true, Check: f.Check})
	r := c.Report(context.Background())
	if r.Status != health.StateDown || r.Ready {
		t.Fatalf("report = %+v; want down and not ready", r)
	}
	pg := dep(t, r, "postgres")
	if pg.State != health.StateDown || pg.Error != health.ClassError {
		t.Fatalf("postgres = %+v; want down with class %q", pg, health.ClassError)
	}
	if strings.Contains(pg.Error, "hunter2") || strings.Contains(pg.Error, "example.org") {
		t.Fatalf("error class leaks the raw error: %q", pg.Error)
	}
}

func TestOptionalDownIsDegradedAndStillReady(t *testing.T) {
	c, _ := newChecker(t)
	register(t, c,
		health.Dependency{Name: "postgres", Required: true, Check: func(context.Context) error { return nil }},
		health.Dependency{Name: "cache", Check: func(context.Context) error { return errors.New("down") }},
	)
	r := c.Report(context.Background())
	if r.Status != health.StateDegraded || !r.Ready {
		t.Fatalf("report = %+v; want degraded and ready", r)
	}
	if got := dep(t, r, "cache").State; got != health.StateDegraded {
		t.Fatalf("cache state = %q; want degraded", got)
	}
}

func TestResultsAreCachedForTheTTLThenRefreshed(t *testing.T) {
	c, clk := newChecker(t, health.WithTTL(5*time.Second))
	f := &flaky{}
	register(t, c, health.Dependency{Name: "postgres", Required: true, Check: f.Check})
	for range 5 {
		c.Report(context.Background())
	}
	if n := f.calls.Load(); n != 1 {
		t.Fatalf("checks within the TTL = %d; want 1", n)
	}
	clk.Advance(5 * time.Second)
	c.Report(context.Background())
	if n := f.calls.Load(); n != 2 {
		t.Fatalf("checks after the TTL = %d; want 2", n)
	}
}

func TestRecoversAfterTheTTL(t *testing.T) {
	c, clk := newChecker(t, health.WithTTL(time.Second))
	f := &flaky{}
	f.fail.Store(true)
	register(t, c, health.Dependency{Name: "postgres", Required: true, Check: f.Check})
	if r := c.Report(context.Background()); r.Ready {
		t.Fatal("ready while the dependency is down")
	}
	f.fail.Store(false)
	if r := c.Report(context.Background()); r.Ready {
		t.Fatal("cached failure should hold until the TTL")
	}
	clk.Advance(time.Second)
	if r := c.Report(context.Background()); !r.Ready || r.Status != health.StateOK {
		t.Fatalf("after the TTL report = %+v; want recovered", r)
	}
}

func TestConcurrentReportsShareOneCheck(t *testing.T) {
	c, _ := newChecker(t)
	var calls atomic.Int64
	release := make(chan struct{})
	register(t, c, health.Dependency{Name: "postgres", Required: true, Check: func(context.Context) error {
		calls.Add(1)
		<-release
		return nil
	}})
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() { c.Report(context.Background()) })
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("concurrent reports ran %d checks; want 1", n)
	}
}

func TestSlowCheckTimesOutWithoutBlockingTheCaller(t *testing.T) {
	c, _ := newChecker(t, health.WithTimeout(50*time.Millisecond))
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	register(t, c, health.Dependency{Name: "broker", Required: true, Check: func(context.Context) error {
		<-block
		return nil
	}})
	start := time.Now()
	r := c.Report(context.Background())
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Report took %v; want about the 50ms timeout", d)
	}
	if b := dep(t, r, "broker"); b.State != health.StateDown || b.Error != health.ClassTimeout {
		t.Fatalf("broker = %+v; want down with class timeout", b)
	}
}

func TestHungCheckIsNotStartedAgain(t *testing.T) {
	c, clk := newChecker(t, health.WithTimeout(20*time.Millisecond), health.WithTTL(time.Second))
	var calls atomic.Int64
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	register(t, c, health.Dependency{Name: "broker", Required: true, Check: func(context.Context) error {
		calls.Add(1)
		<-block
		return nil
	}})
	for range 3 {
		r := c.Report(context.Background())
		if b := dep(t, r, "broker"); b.Error != health.ClassTimeout {
			t.Fatalf("broker = %+v; want timeout", b)
		}
		clk.Advance(2 * time.Second)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("a check that never returned was started %d times; want 1", n)
	}
}

func TestCheckHonoursItsContextDeadline(t *testing.T) {
	c, _ := newChecker(t, health.WithTimeout(30*time.Millisecond))
	var sawDeadline atomic.Bool
	register(t, c, health.Dependency{Name: "db", Required: true, Check: func(ctx context.Context) error {
		_, ok := ctx.Deadline()
		sawDeadline.Store(ok)
		<-ctx.Done()
		return ctx.Err()
	}})
	if b := dep(t, c.Report(context.Background()), "db"); b.Error != health.ClassTimeout {
		t.Fatalf("db = %+v; want timeout", b)
	}
	if !sawDeadline.Load() {
		t.Fatal("check context carried no deadline")
	}
}

func TestCanceledCallerDoesNotPoisonTheCache(t *testing.T) {
	c, _ := newChecker(t)
	register(t, c, health.Dependency{Name: "db", Required: true, Check: func(ctx context.Context) error {
		return ctx.Err()
	}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Report(ctx)
	if r := c.Report(context.Background()); !r.Ready {
		t.Fatalf("report after a canceled caller = %+v; want ready", r)
	}
}

func TestPanickingCheckIsReportedNotPropagated(t *testing.T) {
	c, _ := newChecker(t)
	register(t, c,
		health.Dependency{Name: "db", Required: true, Check: func(context.Context) error { panic("boom") }},
		health.Dependency{Name: "mq", Version: func(context.Context) (string, error) { panic("boom") }},
	)
	r := c.Report(context.Background())
	if b := dep(t, r, "db"); b.State != health.StateDown || b.Error != health.ClassPanic {
		t.Fatalf("db = %+v; want down with class panic", b)
	}
	if v := dep(t, r, "mq").Version; v != "unknown" {
		t.Fatalf("panicking version = %q; want unknown", v)
	}
}

func TestErrorClasses(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().String()
	_ = ln.Close()
	cases := []struct {
		name  string
		check func(context.Context) error
		want  string
	}{
		{"custom class", func(context.Context) error { return health.Classify(errors.New("x"), "auth-failed") }, "auth-failed"},
		{"invalid custom class", func(context.Context) error { return health.Classify(errors.New("x"), "Bad Class!") }, health.ClassError},
		{"refused", health.CheckTCP(closed), health.ClassRefused},
		{"deadline", func(context.Context) error { return context.DeadlineExceeded }, health.ClassTimeout},
		{"canceled", func(context.Context) error { return context.Canceled }, health.ClassCanceled},
		{"dns", func(context.Context) error {
			return &net.DNSError{Err: "no such host", Name: "db.invalid", IsNotFound: true}
		}, health.ClassDNS},
		{"network", func(context.Context) error { return &net.OpError{Op: "read", Err: errors.New("reset")} }, health.ClassNetwork},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newChecker(t)
			register(t, c, health.Dependency{Name: "dep", Required: true, Check: tc.check})
			if got := dep(t, c.Report(context.Background()), "dep").Error; got != tc.want {
				t.Fatalf("class = %q; want %q", got, tc.want)
			}
		})
	}
}

func TestVersionIsCachedAndKeepsTheLastGoodValue(t *testing.T) {
	c, clk := newChecker(t, health.WithVersionTTL(time.Minute))
	var calls atomic.Int64
	var fail atomic.Bool
	register(t, c, health.Dependency{Name: "mq", Version: func(context.Context) (string, error) {
		calls.Add(1)
		if fail.Load() {
			return "", errors.New("closed")
		}
		return "4.0.5", nil
	}})
	c.Report(context.Background())
	c.Report(context.Background())
	if n := calls.Load(); n != 1 {
		t.Fatalf("version calls within the TTL = %d; want 1", n)
	}
	fail.Store(true)
	clk.Advance(time.Minute)
	if v := dep(t, c.Report(context.Background()), "mq").Version; v != "4.0.5" {
		t.Fatalf("version after a failed refresh = %q; want the last good 4.0.5", v)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("version calls after the TTL = %d; want 2", n)
	}
}

func TestVersionNeverReadIsUnknown(t *testing.T) {
	c, _ := newChecker(t)
	register(t, c, health.Dependency{Name: "mq", Version: func(context.Context) (string, error) { return "", errors.New("closed") }})
	if v := dep(t, c.Report(context.Background()), "mq").Version; v != "unknown" {
		t.Fatalf("version = %q; want unknown", v)
	}
}

func TestSecretShapedVersionIsRedacted(t *testing.T) {
	for _, v := range []string{
		"postgres://svc:hunter2@db.example.org/app",
		"password=hunter2",
		"v1\r\nX-Injected: 1",
		strings.Repeat("9", 200),
	} {
		c, _ := newChecker(t)
		register(t, c, health.Dependency{Name: "db", Version: func(context.Context) (string, error) { return v, nil }})
		if got := dep(t, c.Report(context.Background()), "db").Version; got != "redacted" {
			t.Fatalf("version %q reported as %q; want redacted", v, got)
		}
	}
}
