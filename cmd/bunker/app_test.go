package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os/exec"
	"strings"
	"testing"

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

func testAppDeps(cfg *config.Config, cfgErr error, installed ...string) (appDeps, *[][]string) {
	var started [][]string
	return appDeps{
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
	deps, started := testAppDeps(nil, fs.ErrNotExist, "ghostty")
	var stdout, stderr bytes.Buffer
	if code := cmdApp(nil, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	if len(*started) != 1 || (*started)[0][0] != "ghostty" {
		t.Fatalf("started = %q, want one ghostty window (no config file is fine)", *started)
	}

	cfg := &config.Config{App: config.App{Command: []string{"kitty", "bunker"}}}
	deps, started = testAppDeps(cfg, nil, "ghostty", "kitty")
	stdout.Reset()
	if code := cmdApp([]string{"--dry-run"}, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("dry-run code = %d", code)
	}
	if len(*started) != 0 || strings.TrimSpace(stdout.String()) != "kitty bunker" {
		t.Fatalf("dry-run started %q, printed %q; want nothing started and the configured command", *started, stdout.String())
	}

	deps, _ = testAppDeps(nil, fs.ErrNotExist)
	stderr.Reset()
	if code := cmdApp(nil, &stdout, &stderr, deps); code != 1 || !strings.Contains(stderr.String(), "no supported terminal") {
		t.Fatalf("code = %d, stderr = %q; want a clear failure", code, stderr.String())
	}

	deps, _ = testAppDeps(nil, errors.New("config: load: bad toml"), "ghostty")
	stderr.Reset()
	if code := cmdApp(nil, &stdout, &stderr, deps); code != 1 || !strings.Contains(stderr.String(), "bad toml") {
		t.Fatalf("a broken config should fail loudly: code = %d, stderr = %q", code, stderr.String())
	}

	deps, started = testAppDeps(nil, fs.ErrNotExist, "ghostty")
	deps.daemonUp = func() error { return errors.New("connection refused") }
	stderr.Reset()
	if code := cmdApp(nil, &stdout, &stderr, deps); code != 1 || len(*started) != 0 || !strings.Contains(stderr.String(), "cannot reach bunker daemon") {
		t.Fatalf("with the daemon down: code = %d, started %q, stderr = %q; want a clear error and no window", code, *started, stderr.String())
	}
}
