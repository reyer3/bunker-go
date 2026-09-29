package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/reyer3/bunker-go/internal/tui"
)

// launchDeps returns fake runDependencies that record the tuiLaunch the
// TUI was started with, and how many times it started.
func launchDeps(terminal bool, env map[string]string, herdr *fakeHerdr) (runDependencies, *[]tuiLaunch) {
	var launches []tuiLaunch
	deps := runDependencies{
		isTerminal: func(*os.File) bool { return terminal },
		dial:       func(context.Context, string) (tui.Client, error) { return &entryTestClient{}, nil },
		startTUI: func(_ tui.Client, _ io.Reader, _ io.Writer, l tuiLaunch) error {
			launches = append(launches, l)
			return nil
		},
		getenv: herdrEnv(env),
	}
	if herdr != nil {
		deps.herdrRun = herdr.run
	}
	return deps, &launches
}

func TestRunOpenStartsTUIOnTheItem(t *testing.T) {
	cases := []struct {
		name string
		args []string
		env  map[string]string
		want string
	}{
		{"argument", []string{"open", "mail:cl:1"}, nil, "mail:cl:1"},
		{"environment", []string{"open"}, map[string]string{"BUNKER_OPEN_ID": " whatsapp:p:2 "}, "whatsapp:p:2"},
		{"argument wins over environment", []string{"open", "matrix:h:3"}, map[string]string{"BUNKER_OPEN_ID": "mail:cl:1"}, "matrix:h:3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, launches := launchDeps(true, tc.env, nil)
			if code := runWithDependencies(tc.args, os.Stdin, io.Discard, io.Discard, deps); code != 0 {
				t.Fatalf("exit code = %d, want 0", code)
			}
			if len(*launches) != 1 {
				t.Fatalf("TUI started %d times, want 1", len(*launches))
			}
			if got := (*launches)[0]; got.openID != tc.want || got.sidebar || got.opener != nil {
				t.Fatalf("launch = %+v, want only openID %q", got, tc.want)
			}
		})
	}
}

func TestRunOpenRejectsBadInvocations(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		env      map[string]string
		terminal bool
		wantErr  string
	}{
		{"no id", []string{"open"}, nil, true, "usage: bunker open"},
		{"two ids", []string{"open", "a", "b"}, nil, true, "usage: bunker open"},
		{"flag-like id", []string{"open", "--evil"}, nil, true, "starts with -"},
		{"flag-like env id", []string{"open"}, map[string]string{"BUNKER_OPEN_ID": "-x"}, true, "starts with -"},
		{"no terminal", []string{"open", "mail:cl:1"}, nil, false, "needs a terminal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, launches := launchDeps(tc.terminal, tc.env, nil)
			var stderr bytes.Buffer
			if code := runWithDependencies(tc.args, os.Stdin, io.Discard, &stderr, deps); code != 2 {
				t.Fatalf("exit code = %d, want 2", code)
			}
			if len(*launches) != 0 {
				t.Fatal("the TUI started")
			}
			if !strings.Contains(stderr.String(), tc.wantErr) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tc.wantErr)
			}
		})
	}
}

func TestRunOpenAndSidebarReportAnUnreachableDaemon(t *testing.T) {
	for _, args := range [][]string{{"open", "mail:cl:1"}, {"sidebar"}} {
		deps, launches := launchDeps(true, nil, nil)
		deps.dial = func(context.Context, string) (tui.Client, error) { return nil, errors.New("connection refused") }
		var stderr bytes.Buffer
		if code := runWithDependencies(args, os.Stdin, io.Discard, &stderr, deps); code != 1 {
			t.Fatalf("%v: exit code = %d, want 1", args, code)
		}
		if len(*launches) != 0 || !strings.Contains(stderr.String(), "cannot reach bunker daemon") {
			t.Fatalf("%v: launches=%d stderr=%q, want the daemon hint", args, len(*launches), stderr.String())
		}
	}
}

func TestRunSidebarWiresTheHerdrOpenerOnlyInsideHerdr(t *testing.T) {
	herdr := &fakeHerdr{}
	deps, launches := launchDeps(true, map[string]string{"HERDR_ENV": "1"}, herdr)
	if code := runWithDependencies([]string{"sidebar"}, os.Stdin, io.Discard, io.Discard, deps); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	got := (*launches)[0]
	if !got.sidebar || got.openID != "" || got.opener == nil {
		t.Fatalf("launch = %+v, want the sidebar with a herdr opener", got)
	}
	if err := got.opener("whatsapp:p:1"); err != nil {
		t.Fatal(err)
	}
	want := "plugin pane open --plugin bunker --entrypoint open --placement split --direction right --env BUNKER_OPEN_ID=whatsapp:p:1 --focus"
	if len(herdr.calls) != 1 || herdr.calls[0] != want {
		t.Fatalf("herdr calls = %q, want %q", herdr.calls, want)
	}

	deps, launches = launchDeps(true, nil, &fakeHerdr{})
	if code := runWithDependencies([]string{"sidebar"}, os.Stdin, io.Discard, io.Discard, deps); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if got := (*launches)[0]; !got.sidebar || got.opener != nil {
		t.Fatalf("launch outside herdr = %+v, want the sidebar opening in place", got)
	}
}

func TestRunSidebarRejectsArgumentsAndNoTerminal(t *testing.T) {
	for _, tc := range []struct {
		args     []string
		terminal bool
		wantErr  string
	}{
		{[]string{"sidebar", "extra"}, true, "usage: bunker sidebar"},
		{[]string{"sidebar"}, false, "needs a terminal"},
	} {
		deps, launches := launchDeps(tc.terminal, nil, nil)
		var stderr bytes.Buffer
		if code := runWithDependencies(tc.args, os.Stdin, io.Discard, &stderr, deps); code != 2 {
			t.Fatalf("%v: exit code = %d, want 2", tc.args, code)
		}
		if len(*launches) != 0 || !strings.Contains(stderr.String(), tc.wantErr) {
			t.Fatalf("%v: launches=%d stderr=%q, want %q", tc.args, len(*launches), stderr.String(), tc.wantErr)
		}
	}
}

func TestHerdrItemOpener(t *testing.T) {
	inside := herdrEnv(map[string]string{"HERDR_ENV": "1"})
	if herdrItemOpener(herdrEnv(nil), (&fakeHerdr{}).run) != nil {
		t.Fatal("an opener outside herdr")
	}
	if herdrItemOpener(herdrEnv(map[string]string{"HERDR_ENV": "0"}), (&fakeHerdr{}).run) != nil {
		t.Fatal("an opener with HERDR_ENV=0")
	}
	if herdrItemOpener(inside, nil) != nil {
		t.Fatal("an opener without a runner")
	}

	herdr := &fakeHerdr{}
	open := herdrItemOpener(inside, herdr.run)
	for _, bad := range []string{"", "-x", "--focus", "a\nb", "a\x00b"} {
		if err := open(bad); err == nil || !strings.HasPrefix(err.Error(), "herdr: open: ") {
			t.Fatalf("open(%q) = %v, want a herdr: open: error", bad, err)
		}
	}
	if len(herdr.calls) != 0 {
		t.Fatalf("herdr ran %q for an invalid id", herdr.calls)
	}

	herdr = &fakeHerdr{errs: map[string]error{"plugin pane open": errors.New("herdr: plugin pane open: no such plugin")}}
	if err := herdrItemOpener(inside, herdr.run)("mail:cl:1"); err == nil || !strings.Contains(err.Error(), "no such plugin") {
		t.Fatalf("open error = %v, want herdr's message", err)
	}
}

func TestValidItemID(t *testing.T) {
	for _, id := range []string{"mail:cl:1", "whatsapp:personal:3EB0ABC@s.whatsapp.net", "matrix:home:$ev:example.org", "a-b"} {
		if err := validItemID(id); err != nil {
			t.Errorf("validItemID(%q) = %v, want nil", id, err)
		}
	}
	for _, id := range []string{"", "-", "-rf", "x\ty", "x\ny", strings.Repeat("a", 1025)} {
		if err := validItemID(id); err == nil {
			t.Errorf("validItemID(%q) = nil, want an error", id)
		}
	}
}
