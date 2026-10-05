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
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	buildinfo "github.com/Bugs5382/go-buildinfo"
	"github.com/Bugs5382/go-buildinfo/health"
)

func TestNewHeadersValidatesThePrefix(t *testing.T) {
	for _, p := range []string{"Acme", "acme_corp", "-acme", "acme-", "1acme", "grpc", "grpc-x", "acme corp", strings.Repeat("a", 33)} {
		if _, err := health.NewHeaders(p); !errors.Is(err, health.ErrInvalidName) {
			t.Errorf("NewHeaders(%q) = %v; want ErrInvalidName", p, err)
		}
	}
	for _, p := range []string{"acme", "acme-corp", "x1"} {
		h, err := health.NewHeaders(p)
		if err != nil || h.Prefix() != p {
			t.Errorf("NewHeaders(%q) = %q, %v; want it accepted", p, h.Prefix(), err)
		}
	}
	h, err := health.NewHeaders("")
	if err != nil || h.Prefix() != "app" {
		t.Fatalf("NewHeaders(\"\") = %q, %v; want the app default", h.Prefix(), err)
	}
}

func TestHeadersBuild(t *testing.T) {
	h, _ := health.NewHeaders("acme")
	got := h.Build(buildinfo.Info{Version: "v1.2.3", Commit: "abc123", GoVersion: "go1.26.0"})
	want := []health.Header{{Key: "acme-version", Value: "v1.2.3"}, {Key: "acme-commit", Value: "abc123"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Build = %+v; want %+v", got, want)
	}
}

func TestHeadersBuildRedactsSecretShapedValues(t *testing.T) {
	h, _ := health.NewHeaders("acme")
	got := h.Build(buildinfo.Info{Version: "token=abc", Commit: "a\nb"})
	if len(got) != 2 {
		t.Fatalf("Build = %+v; want version and commit", got)
	}
	for _, kv := range got {
		if kv.Value != "redacted" {
			t.Fatalf("%s = %q; want redacted", kv.Key, kv.Value)
		}
	}
}

func TestHeadersDependencies(t *testing.T) {
	h, _ := health.NewHeaders("acme")
	got := h.Dependencies(health.Report{Dependencies: []health.DependencyReport{
		{Name: "postgres", State: health.StateOK, Required: true, Version: "17.2"},
		{Name: "cache", State: health.StateDegraded},
	}})
	want := []health.Header{
		{Key: "acme-dep-postgres", Value: "17.2"},
		{Key: "acme-depstate-postgres", Value: "ok"},
		{Key: "acme-depstate-cache", Value: "degraded"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Dependencies = %+v; want %+v", got, want)
	}
}

func TestHeadersParseRoundTripsAnyCase(t *testing.T) {
	h, _ := health.NewHeaders("acme")
	hdr := http.Header{}
	for _, kv := range append(h.Build(buildinfo.Info{Version: "v1.2.3", Commit: "abc123"}),
		h.Dependencies(health.Report{Dependencies: []health.DependencyReport{
			{Name: "postgres", State: health.StateDown, Version: "17.2"},
			{Name: "opa", State: health.StateOK},
		}})...) {
		hdr.Set(kv.Key, kv.Value)
	}
	hdr.Set("Other-Version", "nope")
	got := h.Parse(hdr)
	if got.Version != "v1.2.3" || got.Commit != "abc123" {
		t.Fatalf("Parse = %+v", got)
	}
	want := map[string]health.RemoteDependency{
		"postgres": {Version: "17.2", State: health.StateDown},
		"opa":      {State: health.StateOK},
	}
	if !reflect.DeepEqual(got.Dependencies, want) {
		t.Fatalf("Dependencies = %+v; want %+v", got.Dependencies, want)
	}
	if got.Headers["acme-dep-postgres"] != "17.2" || got.Headers["other-version"] != "" || len(got.Headers) != 5 {
		t.Fatalf("Headers = %v; want only the five acme- headers, lower-cased", got.Headers)
	}
}

func TestHeadersParseWithoutHeadersIsUnknown(t *testing.T) {
	h, _ := health.NewHeaders("acme")
	got := h.Parse(nil)
	if got.Version != "unknown" || got.Commit != "unknown" || len(got.Dependencies) != 0 {
		t.Fatalf("Parse(nil) = %+v; want unknown version and commit, no dependencies", got)
	}
}

func TestCheckHTTP(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
	defer srv.Close()
	check := health.CheckHTTP(srv.Client(), srv.URL+"/health")
	if err := check(context.Background()); err != nil {
		t.Fatalf("2xx check = %v; want nil", err)
	}
	status = http.StatusServiceUnavailable
	if err := check(context.Background()); err == nil {
		t.Fatal("503 check = nil; want an error")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	if err := check(ctx); err == nil {
		t.Fatal("expired context check = nil; want an error")
	}
}

func TestCheckTCP(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.Listener.Addr().String()
	if err := health.CheckTCP(addr)(context.Background()); err != nil {
		t.Fatalf("open port = %v; want nil", err)
	}
	srv.Close()
	if err := health.CheckTCP(addr)(context.Background()); err == nil {
		t.Fatal("closed port = nil; want an error")
	}
}
