package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain makes every test in this package hermetic by default: it sets
// BUNKER_STATE_DIR and BUNKER_CONFIG_DIR to a process-wide scratch
// directory before any test runs, so config.StateDir/config.ConfigDir
// (and anything built on them, like matrix.StateDirFor) never resolve to
// the real ~/.local/state/bunker-go or ~/.config/bunker-go -- no test in
// this package may read or write the real state/config dirs. A test that
// needs its own isolated directory (e.g. to assert on a pre-seeded
// session file) still calls t.Setenv with its own t.TempDir(), which
// overrides this default only for that test and is restored afterwards.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "bunker-cmd-test-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "TestMain: MkdirTemp:", err)
		os.Exit(1)
	}
	os.Setenv("BUNKER_STATE_DIR", filepath.Join(dir, "state"))
	os.Setenv("BUNKER_CONFIG_DIR", filepath.Join(dir, "config"))

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
