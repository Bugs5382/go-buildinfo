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
	"sync/atomic"
	"testing"
	"time"

	"github.com/Bugs5382/go-buildinfo/health"
)

// waitFor polls cond for up to two seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func runChecker(t *testing.T, c *health.Checker) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func TestBackgroundReportNeverRunsChecks(t *testing.T) {
	c := health.New(health.WithBackgroundRefresh())
	f := &flaky{}
	var versions atomic.Int64
	register(t, c,
		health.Dependency{Name: "postgres", Required: true, Check: f.Check, Version: func(context.Context) (string, error) {
			versions.Add(1)
			return "17.2", nil
		}},
		health.Dependency{Name: "cache", Check: f.Check},
	)
	for range 5 {
		r := c.Report(context.Background())
		if r.Ready || r.Status != health.StateDown {
			t.Fatalf("report before the first refresh = %+v; want not ready", r)
		}
		if p := dep(t, r, "postgres"); p.State != health.StateDown || p.Error != health.ClassPending || !p.CheckedAt.IsZero() || p.Version != "unknown" {
			t.Fatalf("postgres before the first refresh = %+v; want down, pending, unchecked", p)
		}
		if ch := dep(t, r, "cache"); ch.State != health.StateDegraded || ch.Error != health.ClassPending {
			t.Fatalf("optional cache before the first refresh = %+v; want degraded, pending", ch)
		}
	}
	if n, v := f.calls.Load(), versions.Load(); n != 0 || v != 0 {
		t.Fatalf("Report ran %d checks and %d version reads; want none", n, v)
	}
}

func TestBackgroundRunRefreshesAndReportFollows(t *testing.T) {
	c := health.New(health.WithBackgroundRefresh(), health.WithTTL(20*time.Millisecond))
	f := &flaky{}
	register(t, c, health.Dependency{Name: "postgres", Required: true, Check: f.Check, Version: func(context.Context) (string, error) { return "17.2", nil }})
	runChecker(t, c)

	waitFor(t, "ready", func() bool { return c.Report(context.Background()).Ready })
	if p := dep(t, c.Report(context.Background()), "postgres"); p.Version != "17.2" || p.CheckedAt.IsZero() {
		t.Fatalf("postgres after a refresh = %+v", p)
	}
	f.fail.Store(true)
	waitFor(t, "down", func() bool { return !c.Report(context.Background()).Ready })
	if p := dep(t, c.Report(context.Background()), "postgres"); p.Error != health.ClassError {
		t.Fatalf("postgres while failing = %+v; want class error", p)
	}
	f.fail.Store(false)
	waitFor(t, "recovered", func() bool { return c.Report(context.Background()).Ready })
	before := f.calls.Load()
	time.Sleep(100 * time.Millisecond)
	if after := f.calls.Load(); after <= before {
		t.Fatalf("checks stopped: %d then %d", before, after)
	}
}

func TestBackgroundReportDoesNotWaitForASlowCheck(t *testing.T) {
	c := health.New(health.WithBackgroundRefresh(), health.WithTTL(10*time.Millisecond), health.WithTimeout(time.Second))
	var slow atomic.Bool
	var calls atomic.Int64
	register(t, c, health.Dependency{Name: "postgres", Required: true, Check: func(ctx context.Context) error {
		calls.Add(1)
		if slow.Load() {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}})
	runChecker(t, c)
	waitFor(t, "ready", func() bool { return c.Report(context.Background()).Ready })
	slow.Store(true)
	n := calls.Load()
	waitFor(t, "a slow check in flight", func() bool { return calls.Load() > n })
	start := time.Now()
	for range 20 {
		c.Report(context.Background())
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("20 reports during a slow check took %v; want cache reads", d)
	}
}

func TestBackgroundHungCheckIsNotStartedAgain(t *testing.T) {
	c := health.New(health.WithBackgroundRefresh(), health.WithTTL(5*time.Millisecond), health.WithTimeout(20*time.Millisecond))
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	var calls atomic.Int64
	register(t, c, health.Dependency{Name: "broker", Required: true, Check: func(context.Context) error {
		calls.Add(1)
		<-block
		return errors.New("never")
	}})
	runChecker(t, c)
	waitFor(t, "the timeout", func() bool {
		return dep(t, c.Report(context.Background()), "broker").Error == health.ClassTimeout
	})
	time.Sleep(100 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Fatalf("hung check started %d times; want 1", n)
	}
}
