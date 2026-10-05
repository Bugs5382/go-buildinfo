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
	"testing"

	"github.com/Bugs5382/go-buildinfo/grpcbuildinfo"
	"github.com/Bugs5382/go-buildinfo/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func TestReadParsesTheHealthCheckHeaders(t *testing.T) {
	c := health.New()
	if err := c.Register(
		health.Dependency{Name: "postgres", Required: true,
			Check:   func(context.Context) error { return nil },
			Version: func(context.Context) (string, error) { return "17.2", nil }},
		health.Dependency{Name: "cache", Check: func(context.Context) error { return errors.New("down") }},
	); err != nil {
		t.Fatal(err)
	}
	h := start(t, grpcbuildinfo.WithPrefix("acme"), grpcbuildinfo.WithChecker(c))
	got, err := grpcbuildinfo.Read(ctx(t), h.conn, "acme")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Status != healthpb.HealthCheckResponse_SERVING || got.Version != "v1.2.3" || got.Commit != "abc123" {
		t.Fatalf("Read = %+v", got)
	}
	if pg := got.Dependencies["postgres"]; pg.Version != "17.2" || pg.State != health.StateOK {
		t.Fatalf("postgres = %+v", pg)
	}
	if cache := got.Dependencies["cache"]; cache.State != health.StateDegraded {
		t.Fatalf("cache = %+v; want degraded", cache)
	}
}

func TestReadReportsNotServingWithoutAnError(t *testing.T) {
	c := health.New()
	if err := c.Register(health.Dependency{Name: "postgres", Required: true, Check: func(context.Context) error { return errors.New("down") }}); err != nil {
		t.Fatal(err)
	}
	h := start(t, grpcbuildinfo.WithChecker(c), grpcbuildinfo.WithServices("acme.v1.Things"))
	got, err := grpcbuildinfo.Read(ctx(t), h.conn, "", grpcbuildinfo.ForService("acme.v1.Things"))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Status != healthpb.HealthCheckResponse_NOT_SERVING || got.Version != "v1.2.3" || got.Dependencies["postgres"].State != health.StateDown {
		t.Fatalf("Read = %+v; want NOT_SERVING with build info and postgres down", got)
	}
}

func TestReadWithoutBuildInfoIsUnknown(t *testing.T) {
	h := start(t, grpcbuildinfo.WithPrefix("acme"))
	got, err := grpcbuildinfo.Read(ctx(t), h.conn, "other")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Version != "unknown" || got.Commit != "unknown" {
		t.Fatalf("Read = %+v; want unknown for a service without these headers", got)
	}
}

func TestReadErrors(t *testing.T) {
	h := start(t)
	if _, err := grpcbuildinfo.Read(ctx(t), h.conn, "Bad Prefix"); !errors.Is(err, health.ErrInvalidName) {
		t.Fatalf("Read with a bad prefix = %v; want ErrInvalidName", err)
	}
	if _, err := grpcbuildinfo.Read(ctx(t), h.conn, "", grpcbuildinfo.ForService("no.such.Service")); err == nil {
		t.Fatal("Read of an unknown service = nil; want the NotFound error")
	}
}
