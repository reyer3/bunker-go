package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/reyer3/bunker-go/internal/config"
	"github.com/reyer3/bunker-go/internal/rpc"
)

// "bunker app" opens the TUI in a window of its own (issue #16). Outside
// tmux the TUI can draw images and videos, so the window is a terminal
// that speaks kitty graphics. The window class lets the desktop entry
// and window-manager rules find it.

// appClass is the window class (Wayland app id) of the bunker window,
// matched by deploy/desktop/bunker.desktop's StartupWMClass.
const appClass = "dev.bunker.app"

// appTerminal is a terminal "bunker app" knows how to launch.
type appTerminal struct {
	name string
	args func(exe string) []string
}

// appTerminals are tried in order. Both draw kitty graphics.
var appTerminals = []appTerminal{
	{"ghostty", func(exe string) []string {
		return []string{"--title=bunker", "--class=" + appClass, "-e", exe}
	}},
	{"kitty", func(exe string) []string {
		return []string{"--title", "bunker", "--class", appClass, exe}
	}},
}

// appCommand builds the window's command line: custom (the [app]
// command option) when set, else the first known terminal on PATH
// running exe.
func appCommand(custom []string, exe string, lookPath func(string) (string, error)) ([]string, error) {
	if len(custom) > 0 {
		if _, err := lookPath(custom[0]); err != nil {
			return nil, fmt.Errorf("app: the [app] command %q is not on PATH: %w", custom[0], err)
		}
		return append([]string(nil), custom...), nil
	}
	names := make([]string, 0, len(appTerminals))
	for _, t := range appTerminals {
		if _, err := lookPath(t.name); err == nil {
			return append([]string{t.name}, t.args(exe)...), nil
		}
		names = append(names, t.name)
	}
	return nil, fmt.Errorf("app: no supported terminal found (install %s, or set [app] command in the config)", strings.Join(names, " or "))
}

// appEnv is the window's environment: the caller's without tmux's
// variables, since "bunker app" run from a tmux pane would otherwise
// make the new window's TUI think it is inside tmux and turn images off.
func appEnv(environ []string) []string {
	env := make([]string, 0, len(environ))
	for _, kv := range environ {
		if strings.HasPrefix(kv, "TMUX=") || strings.HasPrefix(kv, "TMUX_PANE=") {
			continue
		}
		env = append(env, kv)
	}
	return env
}

// appDeps are what cmdApp needs from the system, swapped out in tests.
type appDeps struct {
	loadConfig func() (*config.Config, error)
	executable func() (string, error)
	lookPath   func(string) (string, error)
	environ    func() []string
	daemonUp   func() error
	start      func(argv, env []string) error
	// notify runs a notification command (notify-send) to completion.
	notify  func(argv []string) error
	getenv  func(string) string
	homeDir func() (string, error)
	now     func() time.Time
}

var defaultAppDeps = appDeps{
	loadConfig: config.LoadDefault,
	executable: os.Executable,
	lookPath:   exec.LookPath,
	environ:    os.Environ,
	daemonUp:   pingDaemon,
	start:      startDetached,
	notify:     runNotify,
	getenv:     os.Getenv,
	homeDir:    os.UserHomeDir,
	now:        time.Now,
}

// appNotifySummary is the desktop notification's title when "bunker
// app" fails.
const appNotifySummary = "bunker no pudo abrirse"

// runNotify runs argv and waits for it, bounded so a notification
// daemon that never answers cannot keep "bunker app" alive.
func runNotify(argv []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, argv[0], argv[1:]...).Run()
}

// appLogPath is where "bunker app" records its failures:
// $XDG_STATE_HOME/bunker/app.log, else ~/.local/state/bunker/app.log.
// A relative XDG_STATE_HOME is ignored, as the XDG spec requires.
func appLogPath(deps appDeps) (string, error) {
	if dir := deps.getenv("XDG_STATE_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, "bunker", "app.log"), nil
	}
	home, err := deps.homeDir()
	if err != nil {
		return "", fmt.Errorf("app: locate the log: %w", err)
	}
	return filepath.Join(home, ".local", "state", "bunker", "app.log"), nil
}

// appendAppLog appends one timestamped line for msg to the app log.
func appendAppLog(deps appDeps, msg string) error {
	path, err := appLogPath(deps)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("app: log dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("app: open log: %w", err)
	}
	line := deps.now().Format(time.RFC3339) + " " + strings.ReplaceAll(msg, "\n", " ") + "\n"
	if _, err := f.WriteString(line); err != nil {
		f.Close()
		return fmt.Errorf("app: write log: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("app: write log: %w", err)
	}
	return nil
}

// reportAppError makes a failure visible when "bunker app" was started
// from the desktop entry, where nobody reads stderr: a desktop
// notification when notify-send is installed, and a line in the app log.
// Reporting problems are only warnings on stderr: the launch has already
// failed and the exit code says so.
func reportAppError(deps appDeps, stderr io.Writer, msg string) {
	if _, err := deps.lookPath("notify-send"); err == nil {
		argv := []string{"notify-send", "--app-name=bunker", "--urgency=critical", appNotifySummary, msg}
		if err := deps.notify(argv); err != nil {
			fmt.Fprintln(stderr, "warning: app: notify-send:", err)
		}
	}
	if err := appendAppLog(deps, msg); err != nil {
		fmt.Fprintln(stderr, "warning:", err)
	}
}

// pingDaemon checks the daemon is reachable before opening the window:
// the TUI exits at once without it, and the window would only flash.
func pingDaemon() error {
	client, err := rpc.Dial(rpc.DefaultSocketPath())
	if err != nil {
		return err
	}
	return client.Close()
}

// startDetached starts argv in its own session, so the window outlives
// the shell (or launcher) that ran "bunker app".
func startDetached(argv, env []string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func cmdApp(args []string, stdout, stderr io.Writer, deps appDeps) int {
	flags := flag.NewFlagSet("app", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dryRun := flags.Bool("dry-run", false, "print the command line instead of opening the window")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintln(stderr, "usage: bunker app [--dry-run]")
		return 2
	}

	// fail prints msg (and any hints) and, outside --dry-run, reports it
	// on the desktop too: launched from the desktop entry, stderr is lost.
	fail := func(msg string, hints ...string) int {
		fmt.Fprintln(stderr, "error:", msg)
		for _, h := range hints {
			fmt.Fprintln(stderr, "hint:", h)
		}
		if !*dryRun {
			reportAppError(deps, stderr, msg)
		}
		return 1
	}

	var custom []string
	cfg, err := deps.loadConfig()
	switch {
	case err == nil:
		custom = cfg.App.Command
	case errors.Is(err, fs.ErrNotExist):
		// No config file: the built-in terminal choice.
	default:
		return fail(fmt.Sprintf("load config: %v", err))
	}
	exe, err := deps.executable()
	if err != nil {
		return fail(fmt.Sprintf("app: locate the bunker binary: %v", err))
	}
	argv, err := appCommand(custom, exe, deps.lookPath)
	if err != nil {
		return fail(err.Error())
	}
	if *dryRun {
		fmt.Fprintln(stdout, strings.Join(argv, " "))
		return 0
	}
	if err := deps.daemonUp(); err != nil {
		return fail(fmt.Sprintf("cannot reach bunker daemon at %s: %v", rpc.DefaultSocketPath(), err),
			"start it with 'bunker daemon' (or 'systemctl --user start bunker')")
	}
	if err := deps.start(argv, appEnv(deps.environ())); err != nil {
		return fail(fmt.Sprintf("app: start %s: %v", argv[0], err))
	}
	return 0
}
