package main

import (
	"bytes"
	"encoding/json"
	"runtime/debug"
	"strings"
	"testing"
)

func TestResolveBuildPrefersLdflags(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "v0.0.0-20260101120000-abcdef123456"}}
	b := resolveBuild("0.12.0", "abc1234", "2026-09-29T00:00:00Z", info)
	if b.Version != "0.12.0" || b.Commit != "abc1234" || b.Date != "2026-09-29T00:00:00Z" || !b.Release {
		t.Fatalf("build = %+v", b)
	}
}

func TestResolveBuildFallsBackToBuildInfo(t *testing.T) {
	cases := []struct {
		name        string
		mainVersion string
		wantVersion string
		wantRelease bool
	}{
		{"devel checkout", "(devel)", "dev", false},
		{"go install at a tag", "v0.12.0", "0.12.0", true},
		{"pseudo-version", "v0.0.0-20260101120000-abcdef123456", "0.0.0-20260101120000-abcdef123456", false},
		{"pseudo-version after a tag", "v0.11.1-0.20260101120000-abcdef123456", "0.11.1-0.20260101120000-abcdef123456", false},
		{"dirty", "v0.11.1-0.20260101120000-abcdef123456+dirty", "0.11.1-0.20260101120000-abcdef123456+dirty", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := &debug.BuildInfo{
				Main: debug.Module{Version: tc.mainVersion},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "0123456789abcdef0123"},
					{Key: "vcs.time", Value: "2026-01-01T12:00:00Z"},
				},
			}
			b := resolveBuild("dev", "", "", info)
			if b.Version != tc.wantVersion || b.Release != tc.wantRelease {
				t.Fatalf("build = %+v, want version %q release %v", b, tc.wantVersion, tc.wantRelease)
			}
			if b.Commit != "0123456789ab" || b.Date != "2026-01-01T12:00:00Z" {
				t.Fatalf("vcs fallback = %q %q", b.Commit, b.Date)
			}
		})
	}
}

func TestResolveBuildWithoutInfo(t *testing.T) {
	b := resolveBuild("", "", "", nil)
	if b.Version != devVersion || b.Release {
		t.Fatalf("build = %+v", b)
	}
}

func TestVersionCommand(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--version"}} {
		var stdout, stderr bytes.Buffer
		if code := runCommandLine(args, nil, &stdout, &stderr); code != 0 {
			t.Fatalf("%v: exit %d, stderr %s", args, code, stderr.String())
		}
		if !strings.HasPrefix(stdout.String(), "bunker ") {
			t.Fatalf("%v: stdout = %q", args, stdout.String())
		}
	}

	var stdout, stderr bytes.Buffer
	if code := runCommandLine([]string{"version", "--json"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr %s", code, stderr.String())
	}
	var got buildInfo
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("json: %v (%s)", err, stdout.String())
	}
	if got.Version == "" || got.OS == "" || got.Arch == "" || got.GoVersion == "" {
		t.Fatalf("version json = %+v", got)
	}
}
