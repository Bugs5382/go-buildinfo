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
	"net"
	"syscall"
)

// The error classes a dependency report carries in place of the raw error,
// which can hold addresses, DSNs or credentials.
const (
	ClassTimeout  = "timeout"
	ClassCanceled = "canceled"
	ClassPanic    = "panic"
	ClassRefused  = "connection-refused"
	ClassDNS      = "dns"
	ClassNetwork  = "network"
	ClassError    = "error"
	// ClassPending marks a dependency a background-refresh Checker has not
	// checked yet.
	ClassPending = "pending"
)

type classified struct {
	err   error
	class string
}

func (c *classified) Error() string { return c.err.Error() }
func (c *classified) Unwrap() error { return c.err }

// Classify tags err with an error class of the caller's choosing, such as
// "auth-failed". The class must be a valid name (see NewHeaders); an invalid
// one is reported as ClassError.
func Classify(err error, class string) error {
	if err == nil {
		return nil
	}
	return &classified{err: err, class: class}
}

var errPanic = errors.New("health: check panicked")

func classOf(err error) string {
	var c *classified
	if errors.As(err, &c) {
		if validName(c.class) {
			return c.class
		}
		return ClassError
	}
	var dnsErr *net.DNSError
	var netErr net.Error
	switch {
	case errors.Is(err, errPanic):
		return ClassPanic
	case errors.Is(err, context.DeadlineExceeded):
		return ClassTimeout
	case errors.Is(err, context.Canceled):
		return ClassCanceled
	case errors.Is(err, syscall.ECONNREFUSED):
		return ClassRefused
	case errors.As(err, &dnsErr):
		return ClassDNS
	case errors.As(err, &netErr) && netErr.Timeout():
		return ClassTimeout
	case errors.As(err, &netErr):
		return ClassNetwork
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return ClassNetwork
	}
	return ClassError
}
