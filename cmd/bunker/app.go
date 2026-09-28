package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"syscall"

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
}

var defaultAppDeps = appDeps{
	loadConfig: config.LoadDefault,
	executable: os.Executable,
	lookPath:   exec.LookPath,
	environ:    os.Environ,
	daemonUp:   pingDaemon,
	start:      startDetached,
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

	var custom []string
	cfg, err := deps.loadConfig()
	switch {
	case err == nil:
		custom = cfg.App.Command
	case errors.Is(err, fs.ErrNotExist):
		// No config file: the built-in terminal choice.
	default:
		fmt.Fprintln(stderr, "error: load config:", err)
		return 1
	}
	exe, err := deps.executable()
	if err != nil {
		fmt.Fprintln(stderr, "error: app: locate the bunker binary:", err)
		return 1
	}
	argv, err := appCommand(custom, exe, deps.lookPath)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	if *dryRun {
		fmt.Fprintln(stdout, strings.Join(argv, " "))
		return 0
	}
	if err := deps.daemonUp(); err != nil {
		fmt.Fprintf(stderr, "error: cannot reach bunker daemon at %s: %v\n", rpc.DefaultSocketPath(), err)
		fmt.Fprintln(stderr, "hint: start it with 'bunker daemon' (or 'systemctl --user start bunker')")
		return 1
	}
	if err := deps.start(argv, appEnv(deps.environ())); err != nil {
		fmt.Fprintf(stderr, "error: app: start %s: %v\n", argv[0], err)
		return 1
	}
	return 0
}
