package buildinfo

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
	"runtime"
	"runtime/debug"
)

// Version and Commit are the link-time stamps. Set them with
//
//	-ldflags "-X github.com/Bugs5382/go-buildinfo.Version=v1.2.3 -X github.com/Bugs5382/go-buildinfo.Commit=<sha>"
//
// Read them through Get, which applies the fallbacks.
var (
	Version = "dev"
	Commit  = ""
)

// Info describes the running binary.
type Info struct {
	// Version is the stamped version, or "dev" for an unstamped build.
	Version string `json:"version"`
	// Commit is the stamped commit, else the VCS revision Go recorded at build
	// time, else "unknown".
	Commit string `json:"commit"`
	// GoVersion is the toolchain that built the binary.
	GoVersion string `json:"goVersion"`
	// Modified reports that the build came from a checkout with uncommitted
	// changes (Go's vcs.modified setting).
	Modified bool `json:"modified"`
}

var readBuildInfo = debug.ReadBuildInfo

// Get returns the build's version, commit, Go version and modified flag.
func Get() Info {
	info := Info{Version: Version, Commit: Commit, GoVersion: runtime.Version()}
	if info.Version == "" {
		info.Version = "dev"
	}
	var revision string
	if bi, ok := readBuildInfo(); ok && bi != nil {
		if bi.GoVersion != "" {
			info.GoVersion = bi.GoVersion
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				revision = s.Value
			case "vcs.modified":
				info.Modified = s.Value == "true"
			}
		}
	}
	if info.Commit == "" {
		info.Commit = revision
	}
	if info.Commit == "" {
		info.Commit = "unknown"
	}
	return info
}
