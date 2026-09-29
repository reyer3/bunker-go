package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/config"
)

// onPath fakes exec.LookPath with the given installed programs.
func onPath(installed ...string) func(string) (string, error) {
	return func(name string) (string, error) {
		for _, n := range installed {
			if n == name {
				return "/usr/bin/" + name, nil
			}
		}
		return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
	}
}

func TestAppCommand(t *testing.T) {
	cases := []struct {
		name      string
		custom    []string
		installed []string
		want      string
		wantErr   string
	}{
		{"ghostty first", nil, []string{"kitty", "ghostty"}, "ghostty --title=bunker --class=dev.bunker.app -e /opt/bunker", ""},
		{"kitty fallback", nil, []string{"kitty"}, "kitty --title bunker --class dev.bunker.app /opt/bunker", ""},
		{"no terminal", nil, nil, "", "no supported terminal found (install ghostty or kitty"},
		{"custom", []string{"wezterm", "start", "--", "bunker"}, []string{"wezterm"}, "wezterm start -- bunker", ""},
		{"custom missing", []string{"wezterm", "start"}, []string{"ghostty"}, "", `[app] command "wezterm" is not on PATH`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			argv, err := appCommand(c.custom, "/opt/bunker", onPath(c.installed...))
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want it to mention %q", err, c.wantErr)
				}
				return
			}
			if err != nil || strings.Join(argv, " ") != c.want {
				t.Fatalf("argv = %q, %v; want %q", argv, err, c.want)
			}
		})
	}
}

func TestAppEnvDropsTmux(t *testing.T) {
	env := appEnv([]string{"HOME=/h", "TMUX=/tmp/tmux-1/default,1,0", "TMUX_PANE=%3", "TERM=screen"})
	if strings.Join(env, " ") != "HOME=/h TERM=screen" {
		t.Fatalf("env = %q: a window opened from tmux must not look like tmux", env)
	}
}

// testAppDeps fakes the system for cmdApp. The app log goes under a
// per-test XDG_STATE_HOME and the home directory is unavailable, so a
// test can never write to the real ~/.local/state; notify-send is only
// "installed" when listed in installed, and running it fails the test
// unless the test replaces notify.
func testAppDeps(t *testing.T, cfg *config.Config, cfgErr error, installed ...string) (appDeps, *[][]string) {
	t.Helper()
	var started [][]string
	state := t.TempDir()
	return appDeps{
		notify: func(argv []string) error {
			t.Errorf("unexpected notification %q", argv)
			return nil
		},
		getenv: func(key string) string {
			if key == "XDG_STATE_HOME" {
				return state
			}
			return ""
		},
		homeDir:    func() (string, error) { return "", errors.New("no home in tests") },
		now:        func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
		loadConfig: func() (*config.Config, error) { return cfg, cfgErr },
		executable: func() (string, error) { return "/opt/bunker", nil },
		lookPath:   onPath(installed...),
		environ:    func() []string { return []string{"TMUX=x", "HOME=/h"} },
		daemonUp:   func() error { return nil },
		start: func(argv, env []string) error {
			started = append(started, argv)
			if strings.Contains(strings.Join(env, " "), "TMUX") {
				return errors.New("tmux env leaked")
			}
			return nil
		},
	}, &started
}

func TestCmdApp(t *testing.T) {
	deps, started := testAppDeps(t, nil, fs.ErrNotExist, "ghostty")
	var stdout, stderr bytes.Buffer
	if code := cmdApp(nil, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	if len(*started) != 1 || (*started)[0][0] != "ghostty" {
		t.Fatalf("started = %q, want one ghostty window (no config file is fine)", *started)
	}

	cfg := &config.Config{App: config.App{Command: []string{"kitty", "bunker"}}}
	deps, started = testAppDeps(t, cfg, nil, "ghostty", "kitty")
	stdout.Reset()
	if code := cmdApp([]string{"--dry-run"}, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("dry-run code = %d", code)
	}
	if len(*started) != 0 || strings.TrimSpace(stdout.String()) != "kitty bunker" {
		t.Fatalf("dry-run started %q, printed %q; want nothing started and the configured command", *started, stdout.String())
	}

	deps, _ = testAppDeps(t, nil, fs.ErrNotExist)
	stderr.Reset()
	if code := cmdApp(nil, &stdout, &stderr, deps); code != 1 || !strings.Contains(stderr.String(), "no supported terminal") {
		t.Fatalf("code = %d, stderr = %q; want a clear failure", code, stderr.String())
	}

	deps, _ = testAppDeps(t, nil, errors.New("config: load: bad toml"), "ghostty")
	stderr.Reset()
	if code := cmdApp(nil, &stdout, &stderr, deps); code != 1 || !strings.Contains(stderr.String(), "bad toml") {
		t.Fatalf("a broken config should fail loudly: code = %d, stderr = %q", code, stderr.String())
	}

	deps, started = testAppDeps(t, nil, fs.ErrNotExist, "ghostty")
	deps.daemonUp = func() error { return errors.New("connection refused") }
	stderr.Reset()
	if code := cmdApp(nil, &stdout, &stderr, deps); code != 1 || len(*started) != 0 || !strings.Contains(stderr.String(), "cannot reach bunker daemon") {
		t.Fatalf("with the daemon down: code = %d, started %q, stderr = %q; want a clear error and no window", code, *started, stderr.String())
	}
}

// appLog reads the app log under deps' XDG_STATE_HOME ("" when absent).
func appLog(t *testing.T, deps appDeps) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(deps.getenv("XDG_STATE_HOME"), "bunker", "app.log"))
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestCmdAppReportsFailuresOnTheDesktop(t *testing.T) {
	deps, _ := testAppDeps(t, nil, fs.ErrNotExist, "ghostty", "notify-send")
	deps.daemonUp = func() error { return errors.New("connection refused") }
	var notified [][]string
	deps.notify = func(argv []string) error {
		notified = append(notified, argv)
		return nil
	}
	var stdout, stderr bytes.Buffer
	if code := cmdApp(nil, &stdout, &stderr, deps); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if len(notified) != 1 {
		t.Fatalf("notified = %q, want one notification", notified)
	}
	n := notified[0]
	if n[0] != "notify-send" || n[1] != "--app-name=bunker" || n[len(n)-2] != appNotifySummary ||
		!strings.Contains(n[len(n)-1], "cannot reach bunker daemon") || !strings.Contains(n[len(n)-1], "connection refused") {
		t.Fatalf("notification = %q, want app bunker, a Spanish summary and the error as the body", n)
	}
	if !strings.Contains(stderr.String(), "cannot reach bunker daemon") {
		t.Fatalf("stderr = %q, the error must still go there", stderr.String())
	}

	log := appLog(t, deps)
	if !strings.HasPrefix(log, "2026-01-02T03:04:05Z ") || !strings.Contains(log, "connection refused") ||
		strings.Count(log, "\n") != 1 {
		t.Fatalf("app.log = %q, want one timestamped line with the error", log)
	}
	info, err := os.Stat(filepath.Join(deps.getenv("XDG_STATE_HOME"), "bunker"))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("log dir: %v, %v; want mode 0700", info, err)
	}

	// A second failure appends rather than overwrites.
	deps.start = func(argv, env []string) error { return errors.New("exec format error") }
	deps.daemonUp = func() error { return nil }
	if code := cmdApp(nil, &stdout, &stderr, deps); code != 1 {
		t.Fatalf("start failure code = %d, want 1", code)
	}
	if log := appLog(t, deps); strings.Count(log, "\n") != 2 || !strings.Contains(log, "app: start ghostty: exec format error") {
		t.Fatalf("app.log = %q, want the start failure appended", log)
	}
}

func TestCmdAppSkipsNotifyWhenAbsent(t *testing.T) {
	// No terminal and no notify-send: testAppDeps fails the test if
	// notify runs, and the log still records the failure.
	deps, _ := testAppDeps(t, nil, fs.ErrNotExist)
	var stdout, stderr bytes.Buffer
	if code := cmdApp(nil, &stdout, &stderr, deps); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if log := appLog(t, deps); !strings.Contains(log, "no supported terminal found") {
		t.Fatalf("app.log = %q, want the failure logged without notify-send", log)
	}
	if strings.Contains(stderr.String(), "warning") {
		t.Fatalf("stderr = %q, a missing notify-send is not worth a warning", stderr.String())
	}
}

func TestCmdAppDryRunHasNoSideEffects(t *testing.T) {
	// A bad [app] command under --dry-run fails on stderr only.
	cfg := &config.Config{App: config.App{Command: []string{"wezterm"}}}
	deps, started := testAppDeps(t, cfg, nil, "ghostty", "notify-send")
	var stdout, stderr bytes.Buffer
	if code := cmdApp([]string{"--dry-run"}, &stdout, &stderr, deps); code != 1 || !strings.Contains(stderr.String(), `"wezterm" is not on PATH`) {
		t.Fatalf("code = %d, stderr = %q; want the bad command reported", code, stderr.String())
	}
	if len(*started) != 0 {
		t.Fatalf("dry-run started %q", *started)
	}
	if log := appLog(t, deps); log != "" {
		t.Fatalf("dry-run wrote app.log: %q", log)
	}
}
