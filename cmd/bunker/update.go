package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/reyer3/bunker-go/internal/config"
	"github.com/reyer3/bunker-go/internal/update"
	"golang.org/x/term"
)

const (
	// updateTimeout bounds the whole "bunker update": the API call, both
	// downloads and the service restart.
	updateTimeout = 10 * time.Minute
	// updateDownloadTimeout bounds one download over the HTTP client.
	updateDownloadTimeout = 5 * time.Minute
	// serviceName is the systemd user unit deploy/systemd installs, the
	// one "make dev" restarts.
	serviceName = "bunker"
)

// commandRunner runs an external command and reports whether it
// succeeded; tests fake it so no systemctl ever runs.
type commandRunner func(ctx context.Context, name string, args ...string) error

func execRunner(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

// updateDeps is everything "bunker update" touches outside itself.
type updateDeps struct {
	build   buildInfo
	client  *http.Client
	baseURL string
	// executable is the running binary's path (os.Executable); symlinks
	// are resolved after it.
	executable func() (string, error)
	stdinIsTTY bool
	run        commandRunner
	// cachePath is the daemon's release cache, all --dry-run reads.
	cachePath    string
	goos, goarch string
}

func defaultUpdateDeps(stdin *os.File) updateDeps {
	return updateDeps{
		build:      currentBuild(),
		client:     &http.Client{Timeout: updateDownloadTimeout},
		baseURL:    update.DefaultBaseURL,
		executable: os.Executable,
		stdinIsTTY: stdin != nil && term.IsTerminal(int(stdin.Fd())),
		run:        execRunner,
		cachePath:  filepath.Join(config.StateDir(), update.CacheFile),
		goos:       runtime.GOOS,
		goarch:     runtime.GOARCH,
	}
}

// cmdUpdate replaces this binary with the latest release after
// verifying its checksum, then restarts the daemon's user service.
// --dry-run prints the plan from the daemon's cached release check and
// never touches the network.
func cmdUpdate(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, deps updateDeps) int {
	flags := newFlagSet("update", stderr)
	dryRun := flags.Bool("dry-run", false, "print the plan without downloading anything")
	yes := flags.Bool("yes", false, "do not ask for confirmation")
	jsonOut := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintln(stderr, "usage: bunker update [--dry-run] [--yes] [--json]")
		return 2
	}
	abort := func(err error) int { return fail(*jsonOut, stdout, stderr, err) }

	if !deps.build.Release {
		return abort(fmt.Errorf("update: this is a %s build (%s), built from source: update it the way it was built (git pull && make dev) or install a release", devVersion, deps.build.Version))
	}
	target, err := resolveExecutable(deps.executable)
	if err != nil {
		return abort(err)
	}
	if err := update.CheckWritable(filepath.Dir(target)); err != nil {
		return abort(fmt.Errorf("%w; bunker cannot replace %s, update it the way it was installed", err, target))
	}

	var rel update.Release
	if *dryRun {
		cache, err := update.LoadCache(deps.cachePath)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return abort(fmt.Errorf("update: --dry-run never touches the network and there is no cached release check at %s yet (the daemon writes it after its first check; is [update] check off?)", deps.cachePath))
			}
			return abort(err)
		}
		rel = cache.Release
	} else {
		rel, err = update.Latest(ctx, deps.client, deps.baseURL)
		if err != nil {
			return abort(err)
		}
	}

	newer, err := update.Newer(rel.Version, deps.build.Version)
	if err != nil {
		return abort(err)
	}
	if !newer || rel.Prerelease {
		if *jsonOut {
			writeJSON(stdout, map[string]any{"up_to_date": true, "version": deps.build.Version, "latest_version": rel.Version})
		} else {
			fmt.Fprintf(stdout, "bunker %s is up to date (latest release %s)\n", deps.build.Version, rel.Version)
		}
		return 0
	}

	plan, err := update.PlanFor(rel, deps.build.Version, deps.goos, deps.goarch, target)
	if err != nil {
		return abort(err)
	}
	if *dryRun {
		if *jsonOut {
			writeJSON(stdout, map[string]any{"dry_run": true, "plan": plan})
		} else {
			printUpdatePlan(stdout, plan)
		}
		return 0
	}

	if !*yes {
		if !deps.stdinIsTTY {
			return abort(errors.New("update: stdin is not a terminal, so there is no one to confirm; pass --yes to update without asking"))
		}
		printUpdatePlan(stderr, plan)
		fmt.Fprint(stderr, "Proceed? [y/N] ")
		if !confirmed(stdin) {
			return abort(errors.New("update: aborted, nothing changed"))
		}
	}

	if err := update.Apply(ctx, deps.client, plan); err != nil {
		return abort(err)
	}
	restarted, restartErr := restartService(ctx, deps.run)
	if *jsonOut {
		out := map[string]any{"updated": true, "plan": plan, "service_restarted": restarted}
		if restartErr != nil {
			out["error"] = restartErr.Error()
		}
		writeJSON(stdout, out)
	} else {
		fmt.Fprintf(stdout, "bunker updated %s → %s at %s (previous binary kept as %s)\n", plan.From, plan.To, plan.Target, plan.Backup)
		switch {
		case restartErr != nil:
			fmt.Fprintln(stderr, "error:", restartErr)
		case restarted:
			fmt.Fprintf(stdout, "%s.service restarted\n", serviceName)
		default:
			fmt.Fprintln(stdout, "restart your 'bunker daemon' to pick it up")
		}
	}
	if restartErr != nil {
		return 1
	}
	return 0
}

// resolveExecutable is the real file behind the running binary: a
// symlink (say ~/bin/bunker → ~/.local/bin/bunker) is followed, so the
// file that gets replaced is the one the link points to.
func resolveExecutable(executable func() (string, error)) (string, error) {
	p, err := executable()
	if err != nil {
		return "", fmt.Errorf("update: locate the running binary: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("update: resolve %s: %w", p, err)
	}
	return resolved, nil
}

func printUpdatePlan(w io.Writer, plan update.Plan) {
	fmt.Fprintf(w, "update bunker %s → %s\n", plan.From, plan.To)
	fmt.Fprintf(w, "  download  %s\n", plan.AssetURL)
	fmt.Fprintf(w, "  verify    sha256 against %s\n", plan.ChecksumsURL)
	fmt.Fprintf(w, "  replace   %s (keeping the current one as %s)\n", plan.Target, plan.Backup)
	fmt.Fprintf(w, "  restart   %s.service if it is active\n", serviceName)
}

// confirmed reads one answer line: only y or yes goes ahead.
func confirmed(stdin io.Reader) bool {
	if stdin == nil {
		return false
	}
	line, _ := bufio.NewReader(stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes", "s", "si", "sí":
		return true
	}
	return false
}

// restartService is "make dev"'s restart step in Go: when the daemon
// runs as the bunker systemd user unit, restart it so it runs the new
// binary. It reports whether it restarted; an inactive (or absent)
// unit is not an error, a failing restart of an active one is.
func restartService(ctx context.Context, run commandRunner) (bool, error) {
	if run == nil {
		return false, nil
	}
	if err := run(ctx, "systemctl", "--user", "is-active", "--quiet", serviceName); err != nil {
		return false, nil
	}
	if err := run(ctx, "systemctl", "--user", "restart", serviceName); err != nil {
		return false, fmt.Errorf("update: the new binary is installed but restarting %s.service failed: %w", serviceName, err)
	}
	return true, nil
}
