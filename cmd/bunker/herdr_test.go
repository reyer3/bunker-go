package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// fakeHerdr records every herdr argv and answers from a table keyed by
// the argv's leading words ("pane list", "plugin pane open", ...).
type fakeHerdr struct {
	replies map[string]string
	errs    map[string]error
	calls   []string
}

func (f *fakeHerdr) run(_ context.Context, args ...string) ([]byte, error) {
	line := strings.Join(args, " ")
	f.calls = append(f.calls, line)
	verb := herdrVerb(args)
	if err := f.errs[verb]; err != nil {
		return nil, err
	}
	return []byte(f.replies[verb]), nil
}

func herdrEnv(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

func paneList(panes ...herdrPane) string {
	b, _ := json.Marshal(map[string]any{"id": "1", "result": map[string]any{"type": "pane_list", "panes": panes}})
	return string(b)
}

const openedReply = `{"id":"2","result":{"type":"plugin_pane_opened","plugin_pane":{"pane":{"pane_id":"w1:p5","tab_id":"w1:t1"}}}}`

func TestHerdrToggle(t *testing.T) {
	insideHerdr := map[string]string{"HERDR_ENV": "1"}
	cases := []struct {
		name    string
		panes   string
		env     map[string]string
		dryRun  bool
		errs    map[string]error
		want    []string // every argv run, in order
		wantOut string
		wantErr string
	}{
		{
			name:  "open docks right of the focused pane",
			panes: paneList(herdrPane{PaneID: "w1:p1", TabID: "w1:t1", Focused: true}, herdrPane{PaneID: "w1:p2", TabID: "w1:t1"}),
			env:   insideHerdr,
			want: []string{
				"pane list",
				"plugin pane open --plugin bunker --entrypoint sidebar --placement split --target-pane w1:p1 --direction right --no-focus",
				"pane resize --direction right --amount 0.25 --pane w1:p5",
				"pane rename w1:p5 bunker",
				"plugin pane focus w1:p5",
			},
			wantOut: "open w1:p5\n",
		},
		{
			name: "a bunker pane in another tab does not count",
			panes: paneList(herdrPane{PaneID: "w1:p1", TabID: "w1:t1", Focused: true},
				herdrPane{PaneID: "w1:p9", TabID: "w1:t2", Label: "bunker"}),
			env:     insideHerdr,
			want:    []string{"pane list", "plugin pane open --plugin bunker --entrypoint sidebar --placement split --target-pane w1:p1 --direction right --no-focus", "pane resize --direction right --amount 0.25 --pane w1:p5", "pane rename w1:p5 bunker", "plugin pane focus w1:p5"},
			wantOut: "open w1:p5\n",
		},
		{
			name:    "an unfocused bunker pane is focused",
			panes:   paneList(herdrPane{PaneID: "w1:p1", TabID: "w1:t1", Focused: true}, herdrPane{PaneID: "w1:p3", TabID: "w1:t1", Label: "bunker"}),
			env:     insideHerdr,
			want:    []string{"pane list", "plugin pane focus w1:p3"},
			wantOut: "focus w1:p3\n",
		},
		{
			name:    "a focused bunker pane is closed",
			panes:   paneList(herdrPane{PaneID: "w1:p1", TabID: "w1:t1"}, herdrPane{PaneID: "w1:p3", TabID: "w1:t1", Label: "bunker", Focused: true}),
			env:     insideHerdr,
			want:    []string{"pane list", "pane close w1:p3"},
			wantOut: "close w1:p3\n",
		},
		{
			name:    "HERDR_TAB_ID picks the tab when nothing is focused",
			panes:   paneList(herdrPane{PaneID: "w1:p1", TabID: "w1:t1", Label: "bunker"}, herdrPane{PaneID: "w2:p1", TabID: "w2:t1", Label: "bunker"}),
			env:     map[string]string{"HERDR_ENV": "1", "HERDR_TAB_ID": "w2:t1"},
			want:    []string{"pane list", "plugin pane focus w2:p1"},
			wantOut: "focus w2:p1\n",
		},
		{
			name:   "dry run open only lists panes",
			panes:  paneList(herdrPane{PaneID: "w1:p1", TabID: "w1:t1", Focused: true}),
			env:    insideHerdr,
			dryRun: true,
			want:   []string{"pane list"},
			wantOut: "herdr plugin pane open --plugin bunker --entrypoint sidebar --placement split --target-pane w1:p1 --direction right --no-focus\n" +
				"herdr pane resize --direction right --amount 0.25 --pane <new-pane>\n" +
				"herdr pane rename <new-pane> bunker\n" +
				"herdr plugin pane focus <new-pane>\n",
		},
		{
			name:    "dry run close only lists panes",
			panes:   paneList(herdrPane{PaneID: "w1:p3", TabID: "w1:t1", Label: "bunker", Focused: true}),
			env:     insideHerdr,
			dryRun:  true,
			want:    []string{"pane list"},
			wantOut: "herdr pane close w1:p3\n",
		},
		{
			name:    "a malformed bunker pane id is rejected",
			panes:   paneList(herdrPane{PaneID: "w1:p1", TabID: "w1:t1", Focused: true}, herdrPane{PaneID: "--all", TabID: "w1:t1", Label: "bunker"}),
			env:     insideHerdr,
			want:    []string{"pane list"},
			wantErr: `invalid pane id "--all"`,
		},
		{
			name:    "a malformed focused pane id is rejected",
			panes:   paneList(herdrPane{PaneID: "w1 p1", TabID: "w1:t1", Focused: true}),
			env:     insideHerdr,
			want:    []string{"pane list"},
			wantErr: `invalid pane id "w1 p1"`,
		},
		{
			name:    "no focused pane and no HERDR_TAB_ID",
			panes:   paneList(herdrPane{PaneID: "w1:p1", TabID: "w1:t1"}),
			env:     insideHerdr,
			want:    []string{"pane list"},
			wantErr: "no focused pane and HERDR_TAB_ID is not set",
		},
		{
			name:    "herdr error JSON propagates",
			panes:   paneList(herdrPane{PaneID: "w1:p1", TabID: "w1:t1", Focused: true}),
			env:     insideHerdr,
			errs:    map[string]error{"pane resize": errors.New("herdr: pane resize: pane not found (pane_not_found)")},
			want:    []string{"pane list", "plugin pane open --plugin bunker --entrypoint sidebar --placement split --target-pane w1:p1 --direction right --no-focus", "pane resize --direction right --amount 0.25 --pane w1:p5", "pane close w1:p5"},
			wantErr: "herdr: pane resize: pane not found (pane_not_found)",
		},
		{
			name:    "error JSON on stdout propagates",
			panes:   `{"id":"1","error":{"code":"server_unavailable","message":"no server"}}`,
			env:     insideHerdr,
			want:    []string{"pane list"},
			wantErr: "herdr: pane list: no server (server_unavailable)",
		},
		{
			name:    "outside herdr with no server",
			env:     map[string]string{},
			errs:    map[string]error{"pane list": errors.New("herdr: pane list: exit status 1: connection refused")},
			want:    []string{"pane list"},
			wantErr: "herdr: not inside herdr and no herdr server answered: herdr: pane list",
		},
		{
			name:    "missing binary",
			env:     map[string]string{},
			errs:    map[string]error{"pane list": errHerdrNotFound},
			want:    []string{"pane list"},
			wantErr: "herdr: herdr not found (set HERDR_BIN_PATH or put herdr on PATH)",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeHerdr{replies: map[string]string{"pane list": c.panes, "plugin pane open": openedReply}, errs: c.errs}
			args := []string{"toggle"}
			if c.dryRun {
				args = append(args, "--dry-run")
			}
			var stdout, stderr bytes.Buffer
			code := cmdHerdr(context.Background(), args, &stdout, &stderr, herdrDeps{run: f.run, getenv: herdrEnv(c.env)})
			if got := strings.Join(f.calls, "\n"); got != strings.Join(c.want, "\n") {
				t.Errorf("herdr calls:\n%s\nwant:\n%s", got, strings.Join(c.want, "\n"))
			}
			if c.wantErr != "" {
				if code != 1 || !strings.Contains(stderr.String(), c.wantErr) {
					t.Fatalf("code %d, stderr %q; want 1 and %q", code, stderr.String(), c.wantErr)
				}
				return
			}
			if code != 0 || stdout.String() != c.wantOut {
				t.Fatalf("code %d, stdout %q, stderr %q; want 0 and %q", code, stdout.String(), stderr.String(), c.wantOut)
			}
		})
	}
}

// The stdout check above only covers a response that is not a pane list;
// herdrResult is where an exit-0 response carrying "error" becomes one.
func TestHerdrResult(t *testing.T) {
	exit1 := errors.New("exit status 1")
	cases := []struct {
		name           string
		stdout, stderr string
		runErr         error
		want, wantErr  string
	}{
		{"ok", `{"result":{}}`, "", nil, `{"result":{}}`, ""},
		{"error object on stderr", "", `{"error":{"code":"pane_not_found","message":"no pane w1:p9"}}`, exit1, "", "herdr: pane close: no pane w1:p9 (pane_not_found)"},
		{"error string on stderr", "", `{"error":"server not running"}`, exit1, "", "herdr: pane close: server not running"},
		{"error on stdout with exit 0", `{"error":{"message":"bad request"}}`, "", nil, "", "herdr: pane close: bad request"},
		{"plain stderr", "", "boom\n", exit1, "", "herdr: pane close: exit status 1: boom"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := herdrResult([]string{"pane", "close", "w1:p9"}, []byte(c.stdout), []byte(c.stderr), c.runErr)
			if c.wantErr != "" {
				if err == nil || err.Error() != c.wantErr {
					t.Fatalf("err = %v, want %q", err, c.wantErr)
				}
				return
			}
			if err != nil || string(out) != c.want {
				t.Fatalf("out = %q, %v; want %q", out, err, c.want)
			}
		})
	}
}

func TestHerdrBinary(t *testing.T) {
	missing := func(string) (string, error) { return "", exec.ErrNotFound }
	if _, err := herdrBinary(herdrEnv(nil), missing); !errors.Is(err, errHerdrNotFound) {
		t.Fatalf("err = %v, want errHerdrNotFound", err)
	}
	bin, err := herdrBinary(herdrEnv(map[string]string{"HERDR_BIN_PATH": "/opt/herdr/bin/herdr"}), missing)
	if err != nil || bin != "/opt/herdr/bin/herdr" {
		t.Fatalf("bin = %q, %v: HERDR_BIN_PATH must win over PATH", bin, err)
	}
	bin, err = herdrBinary(herdrEnv(nil), onPath("herdr"))
	if err != nil || bin != "/usr/bin/herdr" {
		t.Fatalf("bin = %q, %v", bin, err)
	}
	// A HERDR_BIN_PATH that does not exist is reported as not found, not
	// as a bare exec error.
	run := execHerdrRunner(herdrEnv(map[string]string{"HERDR_BIN_PATH": t.TempDir() + "/nope"}), missing)
	if _, err := run(context.Background(), "pane", "list"); !errors.Is(err, errHerdrNotFound) {
		t.Fatalf("err = %v, want errHerdrNotFound", err)
	}
}

func TestHerdrToggleJSON(t *testing.T) {
	f := &fakeHerdr{replies: map[string]string{"pane list": paneList(herdrPane{PaneID: "w1:p1", TabID: "w1:t1", Focused: true})}}
	var stdout, stderr bytes.Buffer
	code := cmdHerdr(context.Background(), []string{"toggle", "--dry-run", "--json"}, &stdout, &stderr, herdrDeps{run: f.run, getenv: herdrEnv(nil)})
	if code != 0 {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
	var got herdrToggle
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Action != "open" || got.Tab != "w1:t1" || got.Anchor != "w1:p1" || !got.DryRun || len(got.Commands) != 4 {
		t.Fatalf("plan = %+v", got)
	}
}

func TestHerdrUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"open"}, {"toggle", "extra"}} {
		var stdout, stderr bytes.Buffer
		f := &fakeHerdr{}
		if code := cmdHerdr(context.Background(), args, &stdout, &stderr, herdrDeps{run: f.run, getenv: herdrEnv(nil)}); code != 2 || len(f.calls) != 0 {
			t.Fatalf("args %q: code %d, calls %q; want 2 and no herdr call", args, code, f.calls)
		}
	}
}
