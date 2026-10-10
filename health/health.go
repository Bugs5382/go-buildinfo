package health

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
	"fmt"
	"sync"
	"time"

	log "github.com/Bugs5382/go-log"
)

// State is the health of one dependency, or of the whole service.
type State string

const (
	// StateOK means the dependency answered its last check.
	StateOK State = "ok"
	// StateDegraded means an optional dependency failed its last check. The
	// service stays ready.
	StateDegraded State = "degraded"
	// StateDown means a required dependency failed its last check. The
	// service is not ready.
	StateDown State = "down"
)

// Dependency is something the service needs, registered with a Checker.
type Dependency struct {
	// Name identifies the dependency in reports and header names: lower-case
	// letters, digits and single dashes, starting with a letter, at most 32
	// characters ("postgres", "object-store").
	Name string
	// Required dependencies take readiness down when they fail; optional ones
	// only report degraded.
	Required bool
	// Check is a cheap liveness probe of the dependency, such as a ping. It
	// gets a context bounded by the checker's timeout. Nil means there is
	// nothing to check and the dependency is always ok.
	Check func(ctx context.Context) error
	// Version optionally reads the dependency's version. A failure keeps the
	// last value read; with none, the version is "unknown".
	Version func(ctx context.Context) (string, error)
}

var (
	// ErrInvalidName reports a dependency name, header prefix or service name
	// that is not allowed.
	ErrInvalidName = errors.New("health: invalid name")
	// ErrDuplicate reports a dependency name registered twice.
	ErrDuplicate = errors.New("health: dependency already registered")
	// ErrNoProbe reports a dependency with neither Check nor Version.
	ErrNoProbe = errors.New("health: dependency has neither Check nor Version")
)

const (
	defaultTTL        = 5 * time.Second
	defaultTimeout    = 2 * time.Second
	defaultVersionTTL = 5 * time.Minute
)

// Checker runs the registered dependency checks and aggregates them into a
// Report. Results are cached for the TTL, so probes hitting the service many
// times a second cost one check per dependency per TTL. A Checker is safe for
// concurrent use.
type Checker struct {
	ttl, timeout, versionTTL time.Duration
	background               bool
	logger                   log.Logger
	now                      func() time.Time

	mu   sync.RWMutex
	deps []*registered
}

type registered struct {
	dep     Dependency
	check   *cell
	version *cell

	mu   sync.Mutex
	last State
}

// Option configures a Checker.
type Option func(*Checker)

// WithTTL sets how long a check result is reused before the next probe runs
// the check again (default 5s). It is also roughly how long readiness takes
// to follow a dependency going down or coming back.
func WithTTL(d time.Duration) Option { return func(c *Checker) { c.ttl = d } }

// WithTimeout bounds each check and version read (default 2s). A slower
// dependency is reported down with the class "timeout".
func WithTimeout(d time.Duration) Option { return func(c *Checker) { c.timeout = d } }

// WithVersionTTL sets how long a version read is reused (default 5m).
func WithVersionTTL(d time.Duration) Option { return func(c *Checker) { c.versionTTL = d } }

// WithLogger logs dependency state changes (down, degraded, recovered) with
// the dependency's name and error class. The default discards them.
func WithLogger(l log.Logger) Option { return func(c *Checker) { c.logger = l } }

// WithBackgroundRefresh makes Report a pure cache read: it never runs a
// check or a version read, so a probe answers at once however slow a
// dependency is. Run does the checking and must be started; until its first
// pass settles, every dependency reports the class "pending" (down when
// required, so the service starts not ready).
func WithBackgroundRefresh() Option { return func(c *Checker) { c.background = true } }

// New returns a Checker with no dependencies.
func New(opts ...Option) *Checker {
	c := &Checker{ttl: defaultTTL, timeout: defaultTimeout, versionTTL: defaultVersionTTL, now: time.Now}
	for _, o := range opts {
		o(c)
	}
	if c.ttl <= 0 {
		c.ttl = defaultTTL
	}
	if c.timeout <= 0 {
		c.timeout = defaultTimeout
	}
	if c.versionTTL <= 0 {
		c.versionTTL = defaultVersionTTL
	}
	if c.logger == nil {
		c.logger = log.Nop()
	}
	return c
}

// Register adds dependencies. Reports list them in registration order.
func (c *Checker) Register(deps ...Dependency) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, d := range deps {
		if !validName(d.Name) {
			return fmt.Errorf("%w: dependency %q", ErrInvalidName, d.Name)
		}
		if d.Check == nil && d.Version == nil {
			return fmt.Errorf("%w: %q", ErrNoProbe, d.Name)
		}
		for _, r := range c.deps {
			if r.dep.Name == d.Name {
				return fmt.Errorf("%w: %q", ErrDuplicate, d.Name)
			}
		}
		r := &registered{dep: d, last: StateOK}
		now := func() time.Time { return c.now() }
		if d.Check != nil {
			check := d.Check
			r.check = &cell{ttl: c.ttl, timeout: c.timeout, now: now,
				probe: func(ctx context.Context) (string, error) { return "", check(ctx) }}
		}
		if d.Version != nil {
			r.version = &cell{ttl: c.versionTTL, timeout: c.timeout, now: now, keep: true, probe: d.Version}
		}
		c.deps = append(c.deps, r)
	}
	return nil
}

// Report is the aggregate health of a service's dependencies.
type Report struct {
	// Status is down when any required dependency is down, degraded when only
	// optional ones failed, and ok otherwise.
	Status State `json:"status"`
	// Ready is false exactly when Status is down.
	Ready bool `json:"ready"`
	// Dependencies lists every registered dependency in registration order.
	Dependencies []DependencyReport `json:"dependencies"`
}

// DependencyReport is one dependency's entry in a Report. It never carries
// the raw error: only its class.
type DependencyReport struct {
	Name     string `json:"name"`
	State    State  `json:"state"`
	Required bool   `json:"required"`
	// Error is the class of the last check's error (see the Class
	// constants), empty when it succeeded.
	Error string `json:"error,omitempty"`
	// CheckedAt is when the last check finished; zero for a dependency with
	// no Check.
	CheckedAt time.Time `json:"checkedAt,omitzero"`
	// Version is the dependency's version, "unknown" when it could not be
	// read, empty when it has no Version func.
	Version string `json:"version,omitempty"`
}

// Report checks every dependency whose cached result is older than the TTL,
// in parallel, and returns the aggregate. It waits at most about one check
// timeout. A canceled ctx does not cut a check short, so a probe that gives
// up never caches a false failure. With WithBackgroundRefresh it runs no
// checks and returns the results Run last recorded.
func (c *Checker) Report(ctx context.Context) Report {
	c.mu.RLock()
	deps := c.deps
	c.mu.RUnlock()

	out := Report{Status: StateOK, Ready: true, Dependencies: make([]DependencyReport, len(deps))}
	var wg sync.WaitGroup
	for i, r := range deps {
		wg.Go(func() { out.Dependencies[i] = c.reportOne(ctx, r) })
	}
	wg.Wait()
	for _, d := range out.Dependencies {
		switch d.State {
		case StateDown:
			out.Status, out.Ready = StateDown, false
		case StateDegraded:
			if out.Status == StateOK {
				out.Status = StateDegraded
			}
		}
	}
	return out
}

func (c *Checker) reportOne(ctx context.Context, r *registered) DependencyReport {
	d := DependencyReport{Name: r.dep.Name, Required: r.dep.Required, State: StateOK}
	var wg sync.WaitGroup
	if r.version != nil {
		wg.Go(func() {
			d.Version = unknown
			if v := c.read(ctx, r.version).value; v != "" {
				d.Version = safeValue(v)
			}
		})
	}
	if r.check != nil {
		res := c.read(ctx, r.check)
		if c.background && res.at.IsZero() {
			res.err = errPending
		}
		d.CheckedAt = res.at
		if res.err != nil {
			d.Error = classOf(res.err)
			d.State = StateDegraded
			if r.dep.Required {
				d.State = StateDown
			}
		}
	}
	wg.Wait()
	c.logTransition(r, d)
	return d
}

func (c *Checker) logTransition(r *registered, d DependencyReport) {
	r.mu.Lock()
	prev := r.last
	r.last = d.State
	r.mu.Unlock()
	if prev == d.State {
		return
	}
	fields := []log.Field{log.F("dependency", d.Name), log.F("required", d.Required), log.F("from", string(prev)), log.F("to", string(d.State))}
	if d.State == StateOK {
		c.logger.Info("dependency recovered", fields...)
		return
	}
	c.logger.Warn("dependency check failing", append(fields, log.F("error_class", d.Error))...)
}

var errPending = Classify(errors.New("health: not checked yet"), ClassPending)

// read is the cached result, running the probe first when it is stale,
// except with WithBackgroundRefresh, where only Run runs probes.
func (c *Checker) read(ctx context.Context, cl *cell) result {
	if c.background {
		res, _ := cl.peek()
		return res
	}
	return cl.get(ctx)
}

// Run refreshes every dependency once per TTL until ctx is done: each check
// runs whatever its age, and each version read once it is older than the
// version TTL. Checks run in parallel and a pass waits at most about one
// check timeout; a hung check is not started again until it returns. After
// each pass it logs state changes as Report does. Use it with
// WithBackgroundRefresh, so probes only read what Run recorded; without that
// option it keeps the cache warm while Report still refreshes stale results.
func (c *Checker) Run(ctx context.Context) {
	t := time.NewTicker(c.ttl)
	defer t.Stop()
	for {
		c.refreshAll(ctx)
		c.Report(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (c *Checker) refreshAll(ctx context.Context) {
	c.mu.RLock()
	deps := c.deps
	c.mu.RUnlock()
	var wg sync.WaitGroup
	for _, r := range deps {
		if r.check != nil {
			wg.Go(func() { r.check.refresh(ctx) })
		}
		if r.version != nil {
			wg.Go(func() { r.version.get(ctx) })
		}
	}
	wg.Wait()
}
