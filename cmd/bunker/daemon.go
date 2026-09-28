package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/reyer3/bunker-go/internal/config"
	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/rpc"
	"github.com/reyer3/bunker-go/internal/store"
)

func cmdDaemonMain(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fakeMode := fs.Bool("fake", false, "seed demo data via the in-memory fake channel instead of configured accounts")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runDaemon(ctx, config.StateDir(), config.AvatarCacheDir(), rpc.DefaultSocketPath(), *fakeMode, stdout, stderr); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return 0
}

// runDaemon opens the store, wires the registry (demo or configured),
// starts every adapter's Run loop feeding the store, and serves the RPC
// socket until ctx is canceled. It returns nil on a clean, ctx-triggered
// shutdown. avatarCacheDir is passed explicitly (like stateDir/
// socketPath) rather than read from config internally, so tests always
// point it at their own temp dir and never a real ~/.cache.
func runDaemon(ctx context.Context, stateDir, avatarCacheDir, socketPath string, fakeMode bool, stdout, stderr io.Writer) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("daemon: create state dir %s: %w", stateDir, err)
	}

	st, err := store.Open(filepath.Join(stateDir, "bunker.db"))
	if err != nil {
		return fmt.Errorf("daemon: open store: %w", err)
	}
	defer st.Close()

	reg, err := loadRegistry(fakeMode, stdout)
	if err != nil {
		return err
	}

	svc := core.NewService(st, reg)
	svc.SetAvatarCacheDir(avatarCacheDir)
	health := core.NewHealthTracker()
	svc.SetHealthTracker(health)
	srv := rpc.NewServer(svc)

	wg := startAdapters(ctx, reg, st, stdout, stderr, health)

	fmt.Fprintf(stdout, "bunker: daemon listening on %s\n", socketPath)
	serveErr := srv.Serve(ctx, socketPath)
	wg.Wait()

	// K3's availability lease reverts on daemon shutdown, same as an
	// explicit blur: a WhatsApp account left "available" must not stay
	// that way after the daemon (and therefore every chat view) is gone.
	svc.ShutdownPresence()

	if ctx.Err() != nil {
		return nil
	}
	return serveErr
}

// startAdapters launches every registered adapter under an
// adapterSupervisor, in its own goroutine, feeding st, and returns the
// *sync.WaitGroup the caller waits on for a clean shutdown. Before an
// adapter's Run starts, if it implements core.Retrier (currently only the
// matrix adapter), it gets one chance to re-fetch and decrypt items it
// previously stored undecryptable -- covering both "the daemon runs it at
// startup" and "after an import" (a `bunker link matrix --recovery-key`
// or `bunker import-keys` run followed by a daemon restart reaches this
// same path). A retry failure is logged, never fatal: Run still starts,
// and the next daemon restart tries again.
//
// R1: unlike a one-shot goroutine, the supervisor restarts Run for as
// long as ctx is live whenever it returns (capped exponential backoff
// with jitter, reset after a run lasting at least adapterHealthyRun), so
// a Matrix sync error or a mail reconnect loop giving up no longer
// silently drops that channel for the rest of the process's life.
// Adapter lifecycle is logged via slog to stderr with channel/account
// attributes, so journald carries every restart even though the process
// itself keeps running.
func startAdapters(ctx context.Context, reg *core.Registry, st core.Store, stdout, stderr io.Writer, health *core.HealthTracker) *sync.WaitGroup {
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	return startAdaptersWithSupervisor(ctx, reg, st, newAdapterSupervisor(logger, health))
}

// startAdaptersWithSupervisor is startAdapters' testable core: it takes
// an already-built adapterSupervisor, so tests can inject a fake clock
// and backoff to prove the restart policy deterministically.
func startAdaptersWithSupervisor(ctx context.Context, reg *core.Registry, st core.Store, sup *adapterSupervisor) *sync.WaitGroup {
	var wg sync.WaitGroup
	for _, adapter := range reg.List() {
		wg.Add(1)
		go func(a core.Adapter) {
			defer wg.Done()
			sup.supervise(ctx, a, st)
		}(adapter)
	}
	return &wg
}

// loadRegistry builds the demo registry in --fake mode, or the configured
// one otherwise. A missing config file is not an error: the daemon starts
// with no accounts so "bunker daemon" alone still serves list/counts
// against an empty store while Alice sets up ~/.config/bunker-go.
func loadRegistry(fakeMode bool, stdout io.Writer) (*core.Registry, error) {
	if fakeMode {
		return demoRegistry(), nil
	}

	cfg, err := config.LoadDefault()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintf(stdout, "bunker: no config file at %s, starting with no accounts\n", config.ConfigPath())
			return core.NewRegistry(), nil
		}
		return nil, fmt.Errorf("daemon: load config: %w", err)
	}
	return buildRegistry(cfg)
}
