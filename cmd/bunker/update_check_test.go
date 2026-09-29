package main

import (
	"io"
	"testing"

	"github.com/reyer3/bunker-go/internal/config"
)

func TestUpdateCheckEnabled(t *testing.T) {
	release := buildInfo{Version: "0.12.0", Release: true}
	dev := buildInfo{Version: devVersion}
	on := &config.Config{Update: config.Update{Check: true}}
	off := &config.Config{Update: config.Update{Check: false}}
	for _, tc := range []struct {
		name  string
		build buildInfo
		cfg   *config.Config
		fake  bool
		want  bool
	}{
		{"release, default config", release, on, false, true},
		{"release, no config file", release, nil, false, true},
		{"release, check = false", release, off, false, false},
		{"dev build", dev, on, false, false},
		{"fake daemon", release, on, true, false},
	} {
		if got := updateCheckEnabled(tc.build, tc.cfg, tc.fake); got != tc.want {
			t.Errorf("%s: updateCheckEnabled = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestNewUpdateCheckerOnDevBuildReportsNothing(t *testing.T) {
	c := newUpdateChecker(buildInfo{Version: devVersion}, nil, false, t.TempDir(), io.Discard)
	if c.Enabled {
		t.Fatal("a dev build enabled the update check")
	}
	if st := c.Status(); st.Available {
		t.Fatalf("status = %+v", st)
	}
}
