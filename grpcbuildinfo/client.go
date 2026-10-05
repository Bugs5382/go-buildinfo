package grpcbuildinfo

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

	"github.com/Bugs5382/go-buildinfo/health"
	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
)

// Result is another service's health check answer and the build and
// dependency headers that came with it.
type Result struct {
	Status healthpb.HealthCheckResponse_ServingStatus
	health.Remote
}

type readConfig struct{ service string }

// ReadOption configures Read.
type ReadOption func(*readConfig)

// ForService checks a named service instead of the overall "" service.
func ForService(name string) ReadOption { return func(c *readConfig) { c.service = name } }

// Read calls grpc.health.v1.Health/Check on conn and parses the headers under
// prefix ("" means "app"). A NOT_SERVING answer is a Result, not an error;
// the error is the RPC's own (NotFound for an unknown service, Unavailable,
// and so on) or health.ErrInvalidName for a bad prefix.
func Read(ctx context.Context, conn grpc.ClientConnInterface, prefix string, opts ...ReadOption) (Result, error) {
	h, err := health.NewHeaders(prefix)
	if err != nil {
		return Result{}, err
	}
	var cfg readConfig
	for _, o := range opts {
		o(&cfg)
	}
	var md metadata.MD
	resp, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{Service: cfg.service}, grpc.Header(&md))
	if err != nil {
		return Result{}, err
	}
	return Result{Status: resp.GetStatus(), Remote: h.Parse(md)}, nil
}
