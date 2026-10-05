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
	"fmt"
	"strings"

	buildinfo "github.com/Bugs5382/go-buildinfo"
)

// DefaultPrefix is the header prefix used when none is given.
const DefaultPrefix = "app"

// Header is one response header, with a lower-case key.
type Header struct {
	Key   string
	Value string
}

// Headers names the response headers for one prefix:
//
//	<prefix>-version             the build's version
//	<prefix>-commit              the build's commit
//	<prefix>-dep-<name>          a dependency's version, when it has a Version func
//	<prefix>-depstate-<name>     a dependency's state: ok, degraded or down
//
// gRPC metadata keys are lower case as written; HTTP canonicalizes them
// (Acme-Version). Values that look like a URL, a key=value pair or anything
// outside a version's character set are sent as "redacted".
type Headers struct{ prefix string }

// NewHeaders validates prefix and returns its header set. An empty prefix
// means DefaultPrefix. A prefix uses the same rules as a dependency name and
// must not start with "grpc", which gRPC reserves.
func NewHeaders(prefix string) (Headers, error) {
	if prefix == "" {
		prefix = DefaultPrefix
	}
	if !validName(prefix) || prefix == "grpc" || strings.HasPrefix(prefix, "grpc-") {
		return Headers{}, fmt.Errorf("%w: header prefix %q", ErrInvalidName, prefix)
	}
	return Headers{prefix: prefix}, nil
}

// Prefix returns the validated prefix.
func (h Headers) Prefix() string { return h.prefix }

// Build returns the version and commit headers.
func (h Headers) Build(info buildinfo.Info) []Header {
	return []Header{
		{Key: h.prefix + "-version", Value: safeValue(info.Version)},
		{Key: h.prefix + "-commit", Value: safeValue(info.Commit)},
	}
}

// Dependencies returns each dependency's version (when it has one) and state
// headers.
func (h Headers) Dependencies(r Report) []Header {
	out := make([]Header, 0, 2*len(r.Dependencies))
	for _, d := range r.Dependencies {
		if d.Version != "" {
			out = append(out, Header{Key: h.prefix + "-dep-" + d.Name, Value: safeValue(d.Version)})
		}
		out = append(out, Header{Key: h.prefix + "-depstate-" + d.Name, Value: string(d.State)})
	}
	return out
}

// Remote is what another service reported in its headers.
type Remote struct {
	// Version and Commit are "unknown" when the headers are missing.
	Version string
	Commit  string
	// Dependencies is keyed by dependency name.
	Dependencies map[string]RemoteDependency
	// Headers holds every header under the prefix, keys in lower case.
	Headers map[string]string
}

// RemoteDependency is one dependency as another service reported it.
type RemoteDependency struct {
	Version string
	State   State
}

// Parse reads the headers under the prefix from gRPC metadata or an
// http.Header (both are a map[string][]string); keys match in any case.
func (h Headers) Parse(md map[string][]string) Remote {
	out := Remote{Version: unknown, Commit: unknown, Dependencies: map[string]RemoteDependency{}, Headers: map[string]string{}}
	p := h.prefix + "-"
	for k, vs := range md {
		k = strings.ToLower(k)
		if !strings.HasPrefix(k, p) || len(vs) == 0 {
			continue
		}
		v := vs[0]
		out.Headers[k] = v
		switch rest := k[len(p):]; {
		case rest == "version":
			out.Version = v
		case rest == "commit":
			out.Commit = v
		case strings.HasPrefix(rest, "dep-"):
			name := rest[len("dep-"):]
			d := out.Dependencies[name]
			d.Version = v
			out.Dependencies[name] = d
		case strings.HasPrefix(rest, "depstate-"):
			name := rest[len("depstate-"):]
			d := out.Dependencies[name]
			d.State = State(v)
			out.Dependencies[name] = d
		}
	}
	return out
}
