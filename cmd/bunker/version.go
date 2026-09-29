package main

import (
	"fmt"
	"io"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
)

// version, commit and date are set by goreleaser through -ldflags -X
// (see .goreleaser.yaml). A source build (make dev, go build, go
// install) leaves them at their defaults, and currentBuild falls back to
// the build info the Go toolchain embeds.
var (
	version = devVersion
	commit  = ""
	date    = ""
)

// devVersion is what a build with no release version reports.
const devVersion = "dev"

// buildInfo is this binary's identity, as "bunker version" prints it and
// the update check compares it.
type buildInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	Date      string `json:"date,omitempty"`
	GoVersion string `json:"go"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	// Release is false for a dev or pseudo-version build: those never
	// look for updates and "bunker update" refuses to replace them,
	// since they were built from a checkout on purpose.
	Release bool `json:"release"`
}

// pseudoVersion matches the tail the Go toolchain stamps on an untagged
// commit (0.0.0-20260101120000-abcdef123456, or
// 1.2.4-0.20260101120000-abcdef123456 after a tag).
var pseudoVersion = regexp.MustCompile(`(^|[.-])\d{14}-[0-9a-f]{12}$`)

// currentBuild resolves the running binary's buildInfo.
func currentBuild() buildInfo {
	info, _ := debug.ReadBuildInfo()
	return resolveBuild(version, commit, date, info)
}

// resolveBuild prefers the ldflags values and fills what they leave
// unset from the toolchain's build info: the main module version (a tag
// for "go install ...@vX", a pseudo-version for a VCS checkout) and the
// vcs.revision/vcs.time settings.
func resolveBuild(ver, com, dat string, info *debug.BuildInfo) buildInfo {
	b := buildInfo{
		Version:   strings.TrimPrefix(ver, "v"),
		Commit:    com,
		Date:      dat,
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
	if b.Version == "" {
		b.Version = devVersion
	}
	if info != nil {
		if b.Version == devVersion {
			if mv := info.Main.Version; mv != "" && mv != "(devel)" {
				b.Version = strings.TrimPrefix(mv, "v")
			}
		}
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				if b.Commit == "" {
					b.Commit = s.Value
				}
			case "vcs.time":
				if b.Date == "" {
					b.Date = s.Value
				}
			}
		}
	}
	if len(b.Commit) > 12 {
		b.Commit = b.Commit[:12]
	}
	b.Release = isReleaseVersion(b.Version)
	return b
}

// isReleaseVersion reports whether v names a published release: not
// "dev", not a pseudo-version, not a dirty checkout.
func isReleaseVersion(v string) bool {
	if v == "" || v == devVersion || strings.Contains(v, "+dirty") {
		return false
	}
	base, _, _ := strings.Cut(v, "+")
	return !pseudoVersion.MatchString(base)
}

// String is the one-line human form, e.g.
// "bunker 0.12.0 (abc1234, 2026-09-29T00:00:00Z, go1.26.8, linux/amd64)".
func (b buildInfo) String() string {
	var extra []string
	if b.Commit != "" {
		extra = append(extra, b.Commit)
	}
	if b.Date != "" {
		extra = append(extra, b.Date)
	}
	extra = append(extra, b.GoVersion, b.OS+"/"+b.Arch)
	return "bunker " + b.Version + " (" + strings.Join(extra, ", ") + ")"
}

// cmdVersion prints the binary's version. It needs no daemon: it
// describes this binary, which may differ from the one the daemon runs.
func cmdVersion(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("version", stderr)
	jsonOut := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	b := currentBuild()
	if *jsonOut {
		writeJSON(stdout, b)
		return 0
	}
	fmt.Fprintln(stdout, b.String())
	return 0
}
