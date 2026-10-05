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
	"fmt"
	"time"

	buildinfo "github.com/Bugs5382/go-buildinfo"
	"github.com/Bugs5382/go-buildinfo/health"
	"google.golang.org/grpc"
	grpchealth "google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
)

const (
	checkMethod = "/grpc.health.v1.Health/Check"
	watchMethod = "/grpc.health.v1.Health/Watch"

	defaultInterval = 5 * time.Second
)

type config struct {
	prefix   string
	checker  *health.Checker
	hs       *grpchealth.Server
	services []string
	liveness string
	allRPCs  bool
	interval time.Duration
}

// Option configures a Server.
type Option func(*config)

// WithPrefix sets the header prefix (default "app"), usually the product
// name: "acme" gives acme-version and acme-commit.
func WithPrefix(p string) Option { return func(c *config) { c.prefix = p } }

// WithChecker reports the checker's dependencies on the health check and
// drives readiness from them. Without one the service is always ready.
func WithChecker(ch *health.Checker) Option { return func(c *config) { c.checker = ch } }

// WithHealthServer sets the grpc-go health server whose serving status the
// Server drives. Register the same server with healthpb.RegisterHealthServer.
func WithHealthServer(hs *grpchealth.Server) Option { return func(c *config) { c.hs = hs } }

// WithServices names services whose serving status follows readiness, in
// addition to the overall "" service.
func WithServices(names ...string) Option {
	return func(c *config) { c.services = append(c.services, names...) }
}

// WithLivenessService names a service that is always SERVING while the
// process is up and never runs dependency checks, for a gRPC liveness probe.
func WithLivenessService(name string) Option { return func(c *config) { c.liveness = name } }

// WithAllRPCs adds the version and commit headers to every RPC, not only the
// health check.
func WithAllRPCs() Option { return func(c *config) { c.allRPCs = true } }

// WithInterval sets how often Run refreshes the serving status (default 5s).
func WithInterval(d time.Duration) Option { return func(c *config) { c.interval = d } }

// Server adds build info and dependency headers to gRPC responses and keeps
// the health server's serving status in step with readiness.
type Server struct {
	cfg     config
	headers health.Headers
	build   metadata.MD
}

// New validates the options. It returns health.ErrInvalidName for a bad
// prefix.
func New(opts ...Option) (*Server, error) {
	cfg := config{interval: defaultInterval}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.interval <= 0 {
		cfg.interval = defaultInterval
	}
	h, err := health.NewHeaders(cfg.prefix)
	if err != nil {
		return nil, err
	}
	if cfg.liveness != "" {
		for _, s := range append([]string{""}, cfg.services...) {
			if s == cfg.liveness {
				return nil, fmt.Errorf("%w: liveness service %q is also a readiness service", health.ErrInvalidName, s)
			}
		}
	}
	s := &Server{cfg: cfg, headers: h, build: toMD(h.Build(buildinfo.Get()))}
	if cfg.hs != nil {
		for _, svc := range append([]string{""}, cfg.services...) {
			cfg.hs.SetServingStatus(svc, healthpb.HealthCheckResponse_SERVING)
		}
		if cfg.liveness != "" {
			cfg.hs.SetServingStatus(cfg.liveness, healthpb.HealthCheckResponse_SERVING)
		}
	}
	return s, nil
}

func toMD(hs []health.Header) metadata.MD {
	md := make(metadata.MD, len(hs))
	for _, h := range hs {
		md.Append(h.Key, h.Value)
	}
	return md
}

func (s *Server) isLiveness(req any) bool {
	r, ok := req.(*healthpb.HealthCheckRequest)
	return ok && s.cfg.liveness != "" && r.GetService() == s.cfg.liveness
}

// UnaryServerInterceptor sets the headers on Health/Check (and on every
// unary RPC with WithAllRPCs). On a readiness check it first refreshes the
// serving status, so the answer reflects the checker's current report.
func (s *Server) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		switch {
		case info.FullMethod == checkMethod && !s.isLiveness(req):
			md := s.build.Copy()
			if s.cfg.checker != nil {
				md = metadata.Join(md, toMD(s.headers.Dependencies(s.Update(ctx))))
			}
			_ = grpc.SetHeader(ctx, md)
		case info.FullMethod == checkMethod, s.cfg.allRPCs:
			_ = grpc.SetHeader(ctx, s.build)
		}
		return handler(ctx, req)
	}
}

// StreamServerInterceptor sets the version and commit headers on
// Health/Watch (and on every streaming RPC with WithAllRPCs).
func (s *Server) StreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if s.cfg.allRPCs || info.FullMethod == watchMethod {
			_ = ss.SetHeader(s.build)
		}
		return handler(srv, ss)
	}
}

// Update runs the checker and sets the serving status of "" and every
// WithServices name: NOT_SERVING while a required dependency is down,
// SERVING otherwise. It returns the report.
func (s *Server) Update(ctx context.Context) health.Report {
	if s.cfg.checker == nil {
		return health.Report{Status: health.StateOK, Ready: true, Dependencies: []health.DependencyReport{}}
	}
	r := s.cfg.checker.Report(ctx)
	if s.cfg.hs != nil {
		st := healthpb.HealthCheckResponse_SERVING
		if !r.Ready {
			st = healthpb.HealthCheckResponse_NOT_SERVING
		}
		for _, svc := range append([]string{""}, s.cfg.services...) {
			s.cfg.hs.SetServingStatus(svc, st)
		}
	}
	return r
}

// Run calls Update every interval until ctx is done, so Health/Watch
// subscribers see readiness change without anyone polling Check. It does not
// call Shutdown on the health server; do that when the gRPC server stops.
func (s *Server) Run(ctx context.Context) {
	t := time.NewTicker(s.cfg.interval)
	defer t.Stop()
	for {
		s.Update(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
