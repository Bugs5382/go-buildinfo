package httpbuildinfo_test

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
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	buildinfo "github.com/Bugs5382/go-buildinfo"
	"github.com/Bugs5382/go-buildinfo/health"
	"github.com/Bugs5382/go-buildinfo/httpbuildinfo"
)

func stamp(t *testing.T) {
	t.Helper()
	oldV, oldC := buildinfo.Version, buildinfo.Commit
	buildinfo.Version, buildinfo.Commit = "v1.2.3", "abc123"
	t.Cleanup(func() { buildinfo.Version, buildinfo.Commit = oldV, oldC })
}

func newHandler(t *testing.T, opts ...httpbuildinfo.Option) *httpbuildinfo.Handler {
	t.Helper()
	stamp(t)
	h, err := httpbuildinfo.New(opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

func serve(h http.Handler, method string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, "/", nil))
	return rec
}

type body struct {
	Status       health.State              `json:"status"`
	Ready        bool                      `json:"ready"`
	Build        buildinfo.Info            `json:"build"`
	Dependencies []health.DependencyReport `json:"dependencies"`
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) body {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q; want application/json", ct)
	}
	var b body
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return b
}

type counting struct {
	calls atomic.Int64
	err   error
}

func (c *counting) Check(context.Context) error {
	c.calls.Add(1)
	return c.err
}

func checker(t *testing.T, deps ...health.Dependency) *health.Checker {
	t.Helper()
	c := health.New(health.WithTTL(time.Millisecond))
	if err := c.Register(deps...); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewRejectsAnInvalidPrefix(t *testing.T) {
	if _, err := httpbuildinfo.New(httpbuildinfo.WithPrefix("Bad_Prefix")); !errors.Is(err, health.ErrInvalidName) {
		t.Fatalf("New = %v; want ErrInvalidName", err)
	}
}

func TestMiddlewareSetsTheHeaders(t *testing.T) {
	c := checker(t, health.Dependency{Name: "postgres", Required: true,
		Check:   func(context.Context) error { return nil },
		Version: func(context.Context) (string, error) { return "17.2", nil }})
	h := newHandler(t, httpbuildinfo.WithPrefix("acme"), httpbuildinfo.WithChecker(c))
	var called bool
	rec := serve(h.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
	})), http.MethodGet)
	if !called || rec.Code != http.StatusTeapot {
		t.Fatalf("wrapped handler called=%v code=%d; want it to run and keep its status", called, rec.Code)
	}
	hdr := rec.Header()
	if hdr.Get("Acme-Version") != "v1.2.3" || hdr.Get("Acme-Commit") != "abc123" ||
		hdr.Get("Acme-Dep-Postgres") != "17.2" || hdr.Get("Acme-Depstate-Postgres") != "ok" {
		t.Fatalf("headers = %v", hdr)
	}
}

func TestMiddlewareWithoutCheckerSetsBuildInfoOnly(t *testing.T) {
	h := newHandler(t)
	rec := serve(h.Middleware(http.NotFoundHandler()), http.MethodGet)
	if rec.Header().Get("App-Version") != "v1.2.3" || rec.Header().Get("App-Commit") != "abc123" {
		t.Fatalf("headers = %v; want the app default prefix", rec.Header())
	}
}

func TestReadyzHealthy(t *testing.T) {
	c := checker(t, health.Dependency{Name: "postgres", Required: true,
		Check:   func(context.Context) error { return nil },
		Version: func(context.Context) (string, error) { return "17.2", nil }})
	h := newHandler(t, httpbuildinfo.WithPrefix("acme"), httpbuildinfo.WithChecker(c))
	rec := serve(h.Readyz(), http.MethodGet)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d; want 200", rec.Code)
	}
	b := decode(t, rec)
	if b.Status != health.StateOK || !b.Ready || b.Build.Version != "v1.2.3" || b.Build.Commit != "abc123" || b.Build.GoVersion == "" {
		t.Fatalf("body = %+v", b)
	}
	if len(b.Dependencies) != 1 || b.Dependencies[0].Name != "postgres" || b.Dependencies[0].Version != "17.2" ||
		!b.Dependencies[0].Required || b.Dependencies[0].CheckedAt.IsZero() {
		t.Fatalf("dependencies = %+v", b.Dependencies)
	}
	if rec.Header().Get("Acme-Depstate-Postgres") != "ok" {
		t.Fatalf("headers = %v; want the dependency headers too", rec.Header())
	}
}

func TestReadyzRequiredDownIs503WithoutSecrets(t *testing.T) {
	dsn := "postgres://svc:hunter2@db.example.org:5432/app"
	c := checker(t, health.Dependency{Name: "postgres", Required: true,
		Check: func(context.Context) error { return errors.New("connect " + dsn + ": refused") }})
	h := newHandler(t, httpbuildinfo.WithChecker(c))
	rec := serve(h.Readyz(), http.MethodGet)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d; want 503", rec.Code)
	}
	b := decode(t, rec)
	if b.Status != health.StateDown || b.Ready || b.Dependencies[0].State != health.StateDown || b.Dependencies[0].Error != health.ClassError {
		t.Fatalf("body = %+v", b)
	}
	if strings.Contains(rec.Body.String(), "hunter2") || strings.Contains(rec.Body.String(), "example.org") {
		t.Fatalf("body leaks the raw error: %s", rec.Body.String())
	}
}

func TestReadyzOptionalDownIsDegraded200(t *testing.T) {
	c := checker(t, health.Dependency{Name: "cache", Check: func(context.Context) error { return errors.New("down") }})
	h := newHandler(t, httpbuildinfo.WithChecker(c))
	rec := serve(h.Readyz(), http.MethodGet)
	if rec.Code != http.StatusOK || decode(t, rec).Status != health.StateDegraded {
		t.Fatalf("code %d body %s; want 200 degraded", rec.Code, rec.Body.String())
	}
}

func TestReadyzWithoutCheckerIsReady(t *testing.T) {
	h := newHandler(t)
	rec := serve(h.Readyz(), http.MethodGet)
	if b := decode(t, rec); rec.Code != http.StatusOK || !b.Ready || b.Dependencies == nil {
		t.Fatalf("code %d body %s; want 200 ready with an empty dependency list", rec.Code, rec.Body.String())
	}
}

func TestLivezNeverChecksDependencies(t *testing.T) {
	d := &counting{err: errors.New("down")}
	c := checker(t, health.Dependency{Name: "postgres", Required: true, Check: d.Check})
	h := newHandler(t, httpbuildinfo.WithChecker(c))
	rec := serve(h.Livez(), http.MethodGet)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d; want 200 while a required dependency is down", rec.Code)
	}
	if rec.Header().Get("App-Version") != "v1.2.3" || rec.Header().Get("App-Depstate-Postgres") != "" {
		t.Fatalf("headers = %v; want build info only", rec.Header())
	}
	var b struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil || b.Status != "ok" {
		t.Fatalf("body %q: want {\"status\":\"ok\"}", rec.Body.String())
	}
	if n := d.calls.Load(); n != 0 {
		t.Fatalf("livez ran %d dependency checks; want 0", n)
	}
}

func TestProbesAnswerHeadWithoutABody(t *testing.T) {
	h := newHandler(t)
	for name, p := range map[string]http.Handler{"readyz": h.Readyz(), "livez": h.Livez()} {
		rec := serve(p, http.MethodHead)
		if rec.Code != http.StatusOK || rec.Body.Len() != 0 || rec.Header().Get("App-Version") != "v1.2.3" {
			t.Fatalf("HEAD %s: code %d body %q headers %v", name, rec.Code, rec.Body.String(), rec.Header())
		}
	}
}

func TestProbesRejectOtherMethods(t *testing.T) {
	h := newHandler(t)
	for name, p := range map[string]http.Handler{"readyz": h.Readyz(), "livez": h.Livez()} {
		rec := serve(p, http.MethodPost)
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
			t.Fatalf("POST %s: code %d Allow %q; want 405 with GET, HEAD", name, rec.Code, rec.Header().Get("Allow"))
		}
	}
}
