package buildinfo_test

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
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"

	buildinfo "github.com/Bugs5382/go-buildinfo"
)

func stamp(t *testing.T, version, commit string) {
	t.Helper()
	oldV, oldC := buildinfo.Version, buildinfo.Commit
	buildinfo.Version, buildinfo.Commit = version, commit
	t.Cleanup(func() { buildinfo.Version, buildinfo.Commit = oldV, oldC })
}

func fakeBuild(t *testing.T, settings ...debug.BuildSetting) {
	t.Helper()
	t.Cleanup(buildinfo.SetReadBuildInfo(func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{GoVersion: "go1.99.0", Settings: settings}, true
	}))
}

func TestGetStampedValuesWin(t *testing.T) {
	stamp(t, "v1.2.3", "abc123")
	fakeBuild(t, debug.BuildSetting{Key: "vcs.revision", Value: "fromvcs"})
	got := buildinfo.Get()
	if got.Version != "v1.2.3" || got.Commit != "abc123" {
		t.Fatalf("Get() = %+v; want v1.2.3 and abc123", got)
	}
}

func TestGetUnstampedVersionIsDev(t *testing.T) {
	stamp(t, "", "abc123")
	fakeBuild(t)
	if got := buildinfo.Get().Version; got != "dev" {
		t.Fatalf("Version = %q; want dev", got)
	}
}

func TestGetCommitFallsBackToVCSRevision(t *testing.T) {
	stamp(t, "v1.0.0", "")
	fakeBuild(t,
		debug.BuildSetting{Key: "vcs.revision", Value: "0123456789abcdef"},
		debug.BuildSetting{Key: "vcs.modified", Value: "true"},
	)
	got := buildinfo.Get()
	if got.Commit != "0123456789abcdef" {
		t.Fatalf("Commit = %q; want the vcs.revision", got.Commit)
	}
	if !got.Modified {
		t.Fatal("Modified = false; want true from vcs.modified")
	}
	if got.GoVersion != "go1.99.0" {
		t.Fatalf("GoVersion = %q; want the build's Go version", got.GoVersion)
	}
}

func TestGetCommitUnknownWithoutVCS(t *testing.T) {
	stamp(t, "v1.0.0", "")
	fakeBuild(t)
	if got := buildinfo.Get().Commit; got != "unknown" {
		t.Fatalf("Commit = %q; want unknown", got)
	}
}

func TestGetWithoutBuildInfo(t *testing.T) {
	stamp(t, "", "")
	t.Cleanup(buildinfo.SetReadBuildInfo(func() (*debug.BuildInfo, bool) { return nil, false }))
	got := buildinfo.Get()
	if got.Version != "dev" || got.Commit != "unknown" || got.GoVersion != runtime.Version() || got.Modified {
		t.Fatalf("Get() = %+v; want dev, unknown, the runtime's Go version, unmodified", got)
	}
}

func buildStamp(t *testing.T, args ...string) buildinfo.Info {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}
	bin := filepath.Join(t.TempDir(), "stamp")
	cmd := exec.Command(goBin, append(append([]string{"build", "-o", bin}, args...), "./testdata/stamp")...)
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	out, err := exec.Command(bin).Output()
	if err != nil {
		t.Fatalf("run stamped binary: %v", err)
	}
	var info buildinfo.Info
	if err := json.Unmarshal(out, &info); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	return info
}

func TestLdflagsStampTheBinary(t *testing.T) {
	info := buildStamp(t, "-buildvcs=false", "-ldflags",
		"-X github.com/Bugs5382/go-buildinfo.Version=v9.8.7 -X github.com/Bugs5382/go-buildinfo.Commit=deadbeef")
	if info.Version != "v9.8.7" || info.Commit != "deadbeef" {
		t.Fatalf("stamped binary reports %+v; want v9.8.7 and deadbeef", info)
	}
	if !strings.HasPrefix(info.GoVersion, "go") {
		t.Fatalf("GoVersion = %q; want the toolchain version", info.GoVersion)
	}
}

func TestUnstampedBinaryWithoutVCS(t *testing.T) {
	info := buildStamp(t, "-buildvcs=false")
	if info.Version != "dev" || info.Commit != "unknown" {
		t.Fatalf("unstamped binary reports %+v; want dev and unknown", info)
	}
}

func TestUnstampedBinaryFallsBackToGitRevision(t *testing.T) {
	head, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Skip("not a git checkout")
	}
	top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skip("not a git checkout")
	}
	// The go command only stamps VCS info when .git is a directory, so a
	// linked worktree builds without it.
	if fi, err := os.Stat(filepath.Join(strings.TrimSpace(string(top)), ".git")); err != nil || !fi.IsDir() {
		t.Skip("linked git worktree: the go command does not stamp VCS info")
	}
	info := buildStamp(t, "-buildvcs=true")
	if want := strings.TrimSpace(string(head)); info.Commit != want {
		t.Fatalf("Commit = %q; want the checkout's HEAD %q", info.Commit, want)
	}
}
