// Package health checks a service's dependencies for readiness probes.
//
// Register each dependency with a cheap Check (a ping) and, optionally, a
// Version func. Report runs the checks in parallel, each bounded by a
// timeout, reuses a result for the TTL, and never runs a second copy of a
// check that has not returned, so probes cannot pile up goroutines on a hung
// dependency. A failing, slow or panicking check marks its dependency down,
// never the caller. With WithBackgroundRefresh, Report only reads the cache
// and Run does the checking once per TTL, so a probe never waits on a check.
//
// A required dependency that is down makes the service not ready; an
// optional one only makes it degraded. Readiness follows the dependencies
// both ways on its own: nothing restarts the process. Liveness should never
// consult a Checker.
//
// Reports carry an error class ("timeout", "connection-refused", ...) instead
// of the raw error, and version strings that look secret-shaped are replaced
// with "redacted", so a report is safe to serve to a probe or another
// service.
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
