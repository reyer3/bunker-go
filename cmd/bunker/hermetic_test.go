package main

import (
	"os"
	"strings"
	"testing"

	"github.com/reyer3/bunker-go/internal/config"
)

// TestStateDirNeverResolvesUnderRealHomeDuringTests is the guard for
// T15(b): several cmd/bunker tests used to call cmdLink with testConfig()
// and no BUNKER_STATE_DIR set, so matrix.StateDirFor (which builds on
// config.StateDir) resolved to the REAL ~/.local/state/bunker-go and read
// whatever session/crypto state actually lives there. TestMain (see
// testmain_test.go) now sets BUNKER_STATE_DIR to a process-wide temp
// directory before any test runs; this test fails loudly if that ever
// regresses, without itself reading anything under the real state dir
// (config.StateDir is a pure path computation, no I/O).
func TestStateDirNeverResolvesUnderRealHomeDuringTests(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory available to guard against")
	}
	dir := config.StateDir()
	if strings.HasPrefix(dir, home) {
		t.Fatalf("config.StateDir() = %q resolves under the real home %q; every cmd/bunker test must run with BUNKER_STATE_DIR set (see TestMain)", dir, home)
	}
}
