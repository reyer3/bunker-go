package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	herdr := &fakeHerdr{replies: map[string]string{"pane list": chatList(), "plugin pane open": openedReply}}
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
	if len(herdr.calls) != 3 || herdr.calls[1] != want {
		t.Fatalf("herdr calls = %q, want the open %q", herdr.calls, want)
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

	herdr = &fakeHerdr{replies: map[string]string{"pane list": chatList()}, errs: map[string]error{"plugin pane open": errors.New("herdr: plugin pane open: no such plugin")}}
	if err := herdrItemOpener(inside, herdr.run)("mail:cl:1"); err == nil || !strings.Contains(err.Error(), "no such plugin") {
		t.Fatalf("open error = %v, want herdr's message", err)
	}
}

// chatList is a tab with the side panel (focused, as when Enter is
// pressed) and optionally a conversation pane, plus a bunker pane in
// another tab that must never count.
func chatList(chat ...string) string {
	panes := []herdrPane{
		{PaneID: "w1:p1", TabID: "w1:t1", Focused: true, Label: herdrPaneLabel},
		{PaneID: "w1:p2", TabID: "w1:t1"},
		{PaneID: "w1:p9", TabID: "w1:t2", Label: herdrChatLabel},
	}
	for _, id := range chat {
		panes = append(panes, herdrPane{PaneID: id, TabID: "w1:t1", Label: herdrChatLabel})
	}
	return paneList(panes...)
}

func TestHerdrItemOpenerReusesOnePane(t *testing.T) {
	inside := herdrEnv(map[string]string{"HERDR_ENV": "1", "HERDR_PANE_ID": "w1:p1"})
	const openNew = "plugin pane open --plugin bunker --entrypoint open --placement split --direction right --env BUNKER_OPEN_ID=mail:cl:1 --focus"
	const openSplit = "plugin pane open --plugin bunker --entrypoint open --placement split --target-pane w1:p7 --direction right --env BUNKER_OPEN_ID=%s --focus"

	t.Run("none: open and label", func(t *testing.T) {
		herdr := &fakeHerdr{replies: map[string]string{"pane list": chatList(), "plugin pane open": openedReply}}
		if err := herdrItemOpener(inside, herdr.run)("mail:cl:1"); err != nil {
			t.Fatal(err)
		}
		wantCalls(t, herdr, "pane list", "pane edges --pane w1:p1", openNew, "pane rename w1:p5 bunker:chat")
	})

	t.Run("same id: focus only", func(t *testing.T) {
		herdr := &fakeHerdr{replies: map[string]string{"pane list": chatList(), "plugin pane open": openedReply}}
		open := herdrItemOpener(inside, herdr.run)
		if err := open("mail:cl:1"); err != nil {
			t.Fatal(err)
		}
		herdr.calls = nil
		herdr.replies["pane list"] = chatList("w1:p5")
		if err := open("mail:cl:1"); err != nil {
			t.Fatal(err)
		}
		wantCalls(t, herdr, "pane list", "plugin pane focus w1:p5")
	})

	t.Run("other id: replaced in the same slot", func(t *testing.T) {
		herdr := &fakeHerdr{replies: map[string]string{"pane list": chatList(), "plugin pane open": openedReply}}
		open := herdrItemOpener(inside, herdr.run)
		if err := open("mail:cl:1"); err != nil {
			t.Fatal(err)
		}
		herdr.calls = nil
		herdr.replies["pane list"] = chatList("w1:p5")
		if err := open("mail:cl:2"); err != nil {
			t.Fatal(err)
		}
		want := "plugin pane open --plugin bunker --entrypoint open --placement split --target-pane w1:p5 --direction right --env BUNKER_OPEN_ID=mail:cl:2 --focus"
		wantCalls(t, herdr, "pane list", want, "pane rename w1:p5 bunker:chat", "pane close w1:p5")
		for _, c := range herdr.calls {
			if strings.Contains(c, "w1:p1") && !strings.HasPrefix(c, "pane list") {
				t.Fatalf("the side panel was touched: %q", c)
			}
		}
	})

	t.Run("a pane this process did not open is replaced", func(t *testing.T) {
		herdr := &fakeHerdr{replies: map[string]string{"pane list": chatList("w1:p7"), "plugin pane open": openedReply}}
		if err := herdrItemOpener(inside, herdr.run)("mail:cl:1"); err != nil {
			t.Fatal(err)
		}
		wantCalls(t, herdr, "pane list", fmt.Sprintf(openSplit, "mail:cl:1"), "pane rename w1:p5 bunker:chat", "pane close w1:p7")
	})

	t.Run("vanished pane: opens a new one", func(t *testing.T) {
		herdr := &fakeHerdr{replies: map[string]string{"pane list": chatList(), "plugin pane open": openedReply}}
		open := herdrItemOpener(inside, herdr.run)
		if err := open("mail:cl:1"); err != nil {
			t.Fatal(err)
		}
		herdr.calls = nil // the user closed it: the list has no chat pane
		if err := open("mail:cl:1"); err != nil {
			t.Fatal(err)
		}
		wantCalls(t, herdr, "pane list", "pane edges --pane w1:p1", openNew, "pane rename w1:p5 bunker:chat")
	})

	t.Run("the sidebar's own pane is never reused", func(t *testing.T) {
		herdr := &fakeHerdr{replies: map[string]string{"pane list": chatList("w1:p1"), "plugin pane open": openedReply}}
		if err := herdrItemOpener(inside, herdr.run)("mail:cl:1"); err != nil {
			t.Fatal(err)
		}
		wantCalls(t, herdr, "pane list", "pane edges --pane w1:p1", openNew, "pane rename w1:p5 bunker:chat")
	})

	t.Run("a failed open keeps the old pane", func(t *testing.T) {
		herdr := &fakeHerdr{replies: map[string]string{"pane list": chatList("w1:p7")},
			errs: map[string]error{"plugin pane open": errors.New("herdr: plugin pane open: boom")}}
		if err := herdrItemOpener(inside, herdr.run)("mail:cl:1"); err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("err = %v, want boom", err)
		}
		wantCalls(t, herdr, "pane list", fmt.Sprintf(openSplit, "mail:cl:1"))
	})

	t.Run("a failed rename closes the new pane", func(t *testing.T) {
		herdr := &fakeHerdr{replies: map[string]string{"pane list": chatList(), "plugin pane open": openedReply},
			errs: map[string]error{"pane rename": errors.New("herdr: pane rename: boom")}}
		if err := herdrItemOpener(inside, herdr.run)("mail:cl:1"); err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("err = %v, want boom", err)
		}
		wantCalls(t, herdr, "pane list", "pane edges --pane w1:p1", openNew, "pane rename w1:p5 bunker:chat", "pane close w1:p5")
	})

	t.Run("herdr errors surface", func(t *testing.T) {
		for name, herdr := range map[string]*fakeHerdr{
			"pane list fails":   {errs: map[string]error{"pane list": errors.New("herdr: pane list: down")}},
			"bad pane list":     {replies: map[string]string{"pane list": "not json"}},
			"no tab":            {replies: map[string]string{"pane list": paneList()}},
			"invalid chat pane": {replies: map[string]string{"pane list": chatList("--x")}},
			"bad open reply":    {replies: map[string]string{"pane list": chatList(), "plugin pane open": `{"result":{}}`}},
		} {
			if err := herdrItemOpener(inside, herdr.run)("mail:cl:1"); err == nil {
				t.Errorf("%s: no error", name)
			}
		}
	})
}

func wantCalls(t *testing.T, herdr *fakeHerdr, want ...string) {
	t.Helper()
	if got, w := strings.Join(herdr.calls, "\n"), strings.Join(want, "\n"); got != w {
		t.Fatalf("herdr calls:\n%s\nwant:\n%s", got, w)
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

// edgesReply is a "pane edges" reply for a tab laid out as main (w1:p2,
// 137 columns) | bunker panel (w1:p1, 46 columns), as chatList has them.
const edgesReply = `{"id":"1","result":{"type":"pane_edges","edges":{"pane_id":"w1:p1","layout":{"panes":[` +
	`{"pane_id":"w1:p2","rect":{"x":0,"y":0,"width":137,"height":61}},` +
	`{"pane_id":"w1:p1","rect":{"x":137,"y":0,"width":46,"height":61}}]}}}}`

func TestHerdrItemOpenerSplitsLeftOfPanel(t *testing.T) {
	inside := herdrEnv(map[string]string{"HERDR_ENV": "1", "HERDR_PANE_ID": "w1:p1"})
	herdr := &fakeHerdr{replies: map[string]string{"pane list": chatList(), "pane edges": edgesReply, "plugin pane open": openedReply}}
	if err := herdrItemOpener(inside, herdr.run)("mail:cl:1"); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, herdr, "pane list", "pane edges --pane w1:p1",
		"plugin pane open --plugin bunker --entrypoint open --placement split --target-pane w1:p2 --direction right --env BUNKER_OPEN_ID=mail:cl:1 --focus",
		"pane rename w1:p5 bunker:chat")
}

func TestHerdrItemOpenerFallsBackBesideItself(t *testing.T) {
	plain := "plugin pane open --plugin bunker --entrypoint open --placement split --direction right --env BUNKER_OPEN_ID=mail:cl:1 --focus"
	for name, herdr := range map[string]*fakeHerdr{
		"no pane on its left": {replies: map[string]string{"pane list": chatList(), "plugin pane open": openedReply,
			"pane edges": `{"id":"1","result":{"type":"pane_edges","edges":{"layout":{"panes":[{"pane_id":"w1:p1","rect":{"x":0,"y":0,"width":46,"height":61}}]}}}}`}},
		"edges fails": {replies: map[string]string{"pane list": chatList(), "plugin pane open": openedReply},
			errs: map[string]error{"pane edges": errors.New("herdr: pane edges: boom")}},
	} {
		inside := herdrEnv(map[string]string{"HERDR_ENV": "1", "HERDR_PANE_ID": "w1:p1"})
		if err := herdrItemOpener(inside, herdr.run)("mail:cl:1"); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		wantCalls(t, herdr, "pane list", "pane edges --pane w1:p1", plain, "pane rename w1:p5 bunker:chat")
	}
}
