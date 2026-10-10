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
	"fmt"
	"sync"
	"time"
)

// cell runs one probe with a timeout, caches its result for a TTL and never
// runs two copies of the probe at once. A probe that outlives its timeout is
// recorded as a timeout and is not started again until it returns, so a hung
// dependency cannot pile up goroutines.
type cell struct {
	probe   func(context.Context) (string, error)
	ttl     time.Duration
	timeout time.Duration
	now     func() time.Time
	keep    bool

	mu      sync.Mutex
	has     bool
	value   string
	err     error
	at      time.Time
	running bool
	settled chan struct{}
}

type result struct {
	value string
	err   error
	at    time.Time
}

func (c *cell) get(ctx context.Context) result {
	c.mu.Lock()
	fresh := c.has && c.now().Sub(c.at) < c.ttl
	if fresh || c.running && c.has {
		r := result{c.value, c.err, c.at}
		c.mu.Unlock()
		return r
	}
	if !c.running {
		c.start(context.WithoutCancel(ctx))
	}
	settled := c.settled
	c.mu.Unlock()
	<-settled
	c.mu.Lock()
	defer c.mu.Unlock()
	return result{c.value, c.err, c.at}
}

// peek returns the cached result without running the probe; ok is false
// until the first result is recorded.
func (c *cell) peek() (result, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return result{c.value, c.err, c.at}, c.has
}

// refresh runs the probe now, whatever the TTL, unless a run is in flight,
// and waits until it settles: it finishes or times out. A run that outlived
// its timeout has settled, so refresh returns at once while it hangs.
func (c *cell) refresh(ctx context.Context) {
	c.mu.Lock()
	if !c.running {
		c.start(context.WithoutCancel(ctx))
	}
	settled := c.settled
	c.mu.Unlock()
	<-settled
}

// start runs with c.mu held.
func (c *cell) start(parent context.Context) {
	c.running = true
	settled := make(chan struct{})
	c.settled = settled
	ctx, cancel := context.WithTimeout(parent, c.timeout)
	done := make(chan result, 1)
	go func() {
		v, err := call(ctx, c.probe)
		done <- result{value: v, err: err}
	}()
	go func() {
		defer cancel()
		select {
		case r := <-done:
			c.record(r, settled, true)
		case <-ctx.Done():
			c.record(result{err: context.DeadlineExceeded}, settled, false)
			<-done
			c.mu.Lock()
			c.running = false
			c.mu.Unlock()
		}
	}()
}

func (c *cell) record(r result, settled chan struct{}, finished bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r.err == nil {
		c.value = r.value
	} else if !c.keep {
		c.value = ""
	}
	c.err = r.err
	c.at = c.now()
	c.has = true
	if finished {
		c.running = false
	}
	close(settled)
}

func call(ctx context.Context, probe func(context.Context) (string, error)) (v string, err error) {
	defer func() {
		if p := recover(); p != nil {
			v, err = "", fmt.Errorf("%w: %v", errPanic, p)
		}
	}()
	return probe(ctx)
}
