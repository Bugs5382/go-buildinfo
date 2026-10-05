package httpbuildinfo

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
	"encoding/json"
	"net/http"

	buildinfo "github.com/Bugs5382/go-buildinfo"
	"github.com/Bugs5382/go-buildinfo/health"
)

type config struct {
	prefix  string
	checker *health.Checker
}

// Option configures a Handler.
type Option func(*config)

// WithPrefix sets the header prefix (default "app"); HTTP canonicalizes the
// names, so "acme" gives Acme-Version and Acme-Commit.
func WithPrefix(p string) Option { return func(c *config) { c.prefix = p } }

// WithChecker reports the checker's dependencies and drives /readyz from
// them. Without one the service is always ready.
func WithChecker(ch *health.Checker) Option { return func(c *config) { c.checker = ch } }

// Handler serves the build info and dependency health over HTTP.
type Handler struct {
	cfg     config
	headers health.Headers
	info    buildinfo.Info
	build   []health.Header
}

// New validates the options. It returns health.ErrInvalidName for a bad
// prefix.
func New(opts ...Option) (*Handler, error) {
	var cfg config
	for _, o := range opts {
		o(&cfg)
	}
	h, err := health.NewHeaders(cfg.prefix)
	if err != nil {
		return nil, err
	}
	info := buildinfo.Get()
	return &Handler{cfg: cfg, headers: h, info: info, build: h.Build(info)}, nil
}

func (h *Handler) report(r *http.Request) health.Report {
	if h.cfg.checker == nil {
		return health.Report{Status: health.StateOK, Ready: true, Dependencies: []health.DependencyReport{}}
	}
	return h.cfg.checker.Report(r.Context())
}

func set(w http.ResponseWriter, hs []health.Header) {
	for _, kv := range hs {
		w.Header().Set(kv.Key, kv.Value)
	}
}

// Middleware sets the version, commit and dependency headers before calling
// next. It runs the checker (cached for its TTL) on every request, so mount
// it on a health or status endpoint rather than on all traffic.
func (h *Handler) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		set(w, h.build)
		if h.cfg.checker != nil {
			set(w, h.headers.Dependencies(h.report(r)))
		}
		next.ServeHTTP(w, r)
	})
}

type readyBody struct {
	Status       health.State              `json:"status"`
	Ready        bool                      `json:"ready"`
	Build        buildinfo.Info            `json:"build"`
	Dependencies []health.DependencyReport `json:"dependencies"`
}

// Readyz answers GET and HEAD with the readiness report as JSON: 200 when
// ready (ok or degraded), 503 while a required dependency is down.
func (h *Handler) Readyz() http.Handler {
	return probe(func(w http.ResponseWriter, r *http.Request) (int, any) {
		rep := h.report(r)
		set(w, h.build)
		set(w, h.headers.Dependencies(rep))
		code := http.StatusOK
		if !rep.Ready {
			code = http.StatusServiceUnavailable
		}
		return code, readyBody{Status: rep.Status, Ready: rep.Ready, Build: h.info, Dependencies: rep.Dependencies}
	})
}

// Livez answers GET and HEAD with 200 while the process can serve HTTP. It
// never checks dependencies, so a dependency outage cannot get the process
// restarted.
func (h *Handler) Livez() http.Handler {
	return probe(func(w http.ResponseWriter, _ *http.Request) (int, any) {
		set(w, h.build)
		return http.StatusOK, map[string]string{"status": "ok"}
	})
}

func probe(f func(http.ResponseWriter, *http.Request) (int, any)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		code, body := f(w, r)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(code)
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(body)
		}
	})
}
