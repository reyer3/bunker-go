package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/config"
	"github.com/reyer3/bunker-go/internal/tui"
)

func agentList(agents ...herdrAgent) string {
	b, _ := json.Marshal(map[string]any{"id": "1", "result": map[string]any{"type": "agent_list", "agents": agents}})
	return string(b)
}

func TestParseHerdrAgents(t *testing.T) {
	cases := []struct {
		name    string
		out     string
		want    []herdrAgent
		wantErr string
	}{
		{
			name: "schema shape, unknown fields ignored",
			out:  `{"id":"1","result":{"type":"agent_list","agents":[{"terminal_id":"t1","pane_id":"w1:p2","agent":"claude","agent_status":"idle","name":"rev","focused":false,"tokens":{"x":"1"},"revision":3}]}}`,
			want: []herdrAgent{{PaneID: "w1:p2", Name: "rev", Agent: "claude", Status: "idle"}},
		},
		{
			name: "bare result array",
			out:  `{"result":[{"pane_id":"w1:p3","agent":"codex","agent_status":"working"}]}`,
			want: []herdrAgent{{PaneID: "w1:p3", Agent: "codex", Status: "working"}},
		},
		{name: "no agents", out: `{"result":{"type":"agent_list","agents":[]}}`, want: []herdrAgent{}},
		{name: "error response", out: `{"error":{"code":"server_down","message":"no server"}}`, wantErr: "agent list: no server (server_down)"},
		{name: "wrong type", out: `{"result":{"type":"pane_list","panes":[]}}`, wantErr: `unexpected result type "pane_list"`},
		{name: "not json", out: `nope`, wantErr: "decode response"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseHerdrAgents([]byte(tc.out))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("agents = %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("agent %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestPickClaudeAgent(t *testing.T) {
	codex := herdrAgent{PaneID: "w1:p1", Agent: "codex", Status: "idle"}
	busy := herdrAgent{PaneID: "w1:p2", Agent: "claude", Status: "working"}
	blocked := herdrAgent{PaneID: "w1:p3", Agent: "claude", Status: "blocked"}
	done := herdrAgent{PaneID: "w1:p4", Agent: "Claude", Status: "done"}
	idle := herdrAgent{PaneID: "w1:p5", Agent: "claude", Status: "idle"}
	cases := []struct {
		name   string
		agents []herdrAgent
		want   string
	}{
		{"one", []herdrAgent{codex, busy}, "w1:p2"},
		{"several: the first ready one wins", []herdrAgent{busy, blocked, done, idle}, "w1:p4"},
		{"several: idle after a working one", []herdrAgent{busy, idle}, "w1:p5"},
		{"none ready: the first", []herdrAgent{blocked, busy}, "w1:p3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := pickClaudeAgent(tc.agents)
			if err != nil || got.PaneID != tc.want {
				t.Fatalf("pick = %+v, %v; want %s", got, err, tc.want)
			}
		})
	}
	if _, err := pickClaudeAgent([]herdrAgent{codex}); !errors.Is(err, errNoClaudeAgent) {
		t.Fatalf("no claude: err = %v, want errNoClaudeAgent", err)
	}
}

func TestAskHerdrClaude(t *testing.T) {
	const id = "whatsapp:personal:1"
	prompt := "agent prompt w1:p2 " + herdrClaudePrompt(id)
	cases := []struct {
		name        string
		list        string
		errs        map[string]error
		want        []string
		wantErr     string
		wantBlocked bool
	}{
		{
			name: "prompts and focuses the only claude",
			list: agentList(herdrAgent{PaneID: "w1:p1", Agent: "codex", Status: "idle"}, herdrAgent{PaneID: "w1:p2", Agent: "claude", Status: "working"}),
			want: []string{"agent list", prompt, "agent focus w1:p2"},
		},
		{
			name:    "no claude",
			list:    agentList(herdrAgent{PaneID: "w1:p1", Agent: "codex", Status: "idle"}),
			want:    []string{"agent list"},
			wantErr: "no hay ningún Claude Code",
		},
		{
			name:        "blocked agent is focused, not prompted",
			list:        agentList(herdrAgent{PaneID: "w1:p2", Agent: "claude", Status: "blocked"}),
			want:        []string{"agent list", "agent focus w1:p2"},
			wantBlocked: true,
		},
		{
			name:        "herdr refuses with agent_blocked",
			list:        agentList(herdrAgent{PaneID: "w1:p2", Agent: "claude", Status: "idle"}),
			errs:        map[string]error{"agent prompt": errors.New("herdr: agent prompt: agent is blocked (agent_blocked)")},
			want:        []string{"agent list", prompt, "agent focus w1:p2"},
			wantBlocked: true,
		},
		{
			name:    "a pane id that could be a flag is never passed on",
			list:    agentList(herdrAgent{PaneID: "--all", Agent: "claude", Status: "idle"}),
			want:    []string{"agent list"},
			wantErr: "invalid pane id",
		},
		{
			name:    "list error",
			errs:    map[string]error{"agent list": errors.New("herdr: agent list: no server")},
			want:    []string{"agent list"},
			wantErr: "no server",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			herdr := &fakeHerdr{replies: map[string]string{"agent list": tc.list}, errs: tc.errs}
			err := askHerdrClaude(context.Background(), herdr.run, id)
			if strings.Join(herdr.calls, "\n") != strings.Join(tc.want, "\n") {
				t.Fatalf("calls =\n%s\nwant\n%s", strings.Join(herdr.calls, "\n"), strings.Join(tc.want, "\n"))
			}
			if got := errors.Is(err, tui.ErrAgentBlocked); got != tc.wantBlocked {
				t.Fatalf("err = %v, blocked = %v, want %v", err, got, tc.wantBlocked)
			}
			switch {
			case tc.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
			case !tc.wantBlocked && err != nil:
				t.Fatal(err)
			}
		})
	}
}

func TestAskHerdrClaudeValidatesTheItemID(t *testing.T) {
	for _, id := range []string{"", "-x", "mail:a\nb"} {
		herdr := &fakeHerdr{}
		if err := askHerdrClaude(context.Background(), herdr.run, id); err == nil {
			t.Fatalf("id %q: no error", id)
		}
		if len(herdr.calls) != 0 {
			t.Fatalf("id %q: herdr ran %q", id, herdr.calls)
		}
	}
}

func TestHerdrClaudePromptArgv(t *testing.T) {
	herdr := &fakeHerdr{replies: map[string]string{"agent list": agentList(herdrAgent{PaneID: "w1:p2", Agent: "claude", Status: "idle"})}}
	var argv []string
	run := func(ctx context.Context, args ...string) ([]byte, error) {
		if len(args) > 1 && args[1] == "prompt" {
			argv = args
		}
		return herdr.run(ctx, args...)
	}
	if err := askHerdrClaude(context.Background(), run, "mail:cl:7"); err != nil {
		t.Fatal(err)
	}
	want := []string{"agent", "prompt", "w1:p2", "Usa bunker mcp: lee la conversación mail:cl:7 (herramienta read o thread) y dime qué necesito saber; si hay que responder, propón una respuesta como plan sin enviarla."}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("prompt argv = %q, want %q", argv, want)
	}
}

func TestHerdrUnreadReporterThrottles(t *testing.T) {
	herdr := &fakeHerdr{}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	r := &herdrUnreadReporter{run: herdr.run, pane: "w1:p1", ttl: 10 * time.Second, now: func() time.Time { return now }}
	step := func(n int, after time.Duration) {
		t.Helper()
		now = now.Add(after)
		if err := r.report(n); err != nil {
			t.Fatal(err)
		}
	}
	step(3, 0)             // first: reported
	step(3, 2*time.Second) // same, fresh: skipped
	step(4, time.Second)   // changed: reported
	step(4, 4*time.Second) // same, 4s old: skipped
	step(4, 1*time.Second) // same, 5s = ttl/2 old: refreshed
	step(4, 100*time.Millisecond)
	want := []string{
		"pane report-metadata w1:p1 --source bunker --token unread=3 --ttl-ms 10000",
		"pane report-metadata w1:p1 --source bunker --token unread=4 --ttl-ms 10000",
		"pane report-metadata w1:p1 --source bunker --token unread=4 --ttl-ms 10000",
	}
	if strings.Join(herdr.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls =\n%s\nwant\n%s", strings.Join(herdr.calls, "\n"), strings.Join(want, "\n"))
	}

	// A failed report is not remembered: the next poll retries it.
	herdr.errs = map[string]error{"pane report-metadata": errors.New("herdr: pane report-metadata: boom")}
	now = now.Add(time.Second)
	if err := r.report(5); err == nil {
		t.Fatal("a failed report returned no error")
	}
	herdr.errs = nil
	if err := r.report(5); err != nil {
		t.Fatal(err)
	}
	if last := herdr.calls[len(herdr.calls)-1]; !strings.Contains(last, "unread=5") || len(herdr.calls) != 5 {
		t.Fatalf("calls = %q, want the failed report retried", herdr.calls)
	}
}

func TestHerdrUnreadReporterFor(t *testing.T) {
	herdr := &fakeHerdr{}
	if herdrUnreadReporterFor(herdrEnv(map[string]string{"HERDR_PANE_ID": "w1:p1"}), herdr.run) != nil {
		t.Fatal("reporter wired outside herdr")
	}
	if herdrUnreadReporterFor(herdrEnv(map[string]string{"HERDR_ENV": "1", "HERDR_PANE_ID": "-x"}), herdr.run) != nil {
		t.Fatal("reporter wired with a pane id that could be a flag")
	}
	report := herdrUnreadReporterFor(herdrEnv(map[string]string{"HERDR_ENV": "1", "HERDR_PANE_ID": "w1:p1"}), herdr.run)
	if report == nil {
		t.Fatal("no reporter inside herdr")
	}
	if err := report(2); err != nil {
		t.Fatal(err)
	}
	if want := "pane report-metadata w1:p1 --source bunker --token unread=2 --ttl-ms 10000"; len(herdr.calls) != 1 || herdr.calls[0] != want {
		t.Fatalf("calls = %q, want %q (ttl = 2 poll intervals)", herdr.calls, want)
	}
}

func TestHerdrMessageNotifier(t *testing.T) {
	herdr := &fakeHerdr{}
	inside := herdrEnv(map[string]string{"HERDR_ENV": "1"})
	if herdrMessageNotifier(inside, herdr.run, false) != nil {
		t.Fatal("notifier wired while [herdr] notify is off")
	}
	if herdrMessageNotifier(herdrEnv(nil), herdr.run, true) != nil {
		t.Fatal("notifier wired outside herdr")
	}
	notify := herdrMessageNotifier(inside, herdr.run, true)
	if err := notify("-Alice: hola"); err != nil {
		t.Fatal(err)
	}
	if want := "notification show bunker --body=-Alice: hola --sound request"; len(herdr.calls) != 1 || herdr.calls[0] != want {
		t.Fatalf("calls = %q, want %q", herdr.calls, want)
	}
}

func TestRunSidebarWiresTheHerdrHooks(t *testing.T) {
	env := map[string]string{"HERDR_ENV": "1", "HERDR_PANE_ID": "w1:p1"}
	deps, launches := launchDeps(true, env, &fakeHerdr{})
	deps.loadConfig = func() (*config.Config, error) { return &config.Config{Herdr: config.Herdr{Notify: true}}, nil }
	if code := runWithDependencies([]string{"sidebar"}, os.Stdin, io.Discard, io.Discard, deps); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if got := (*launches)[0]; got.asker == nil || got.unread == nil || got.notify == nil {
		t.Fatalf("launch = %+v, want asker, unread reporter and notifier", got)
	}

	deps, launches = launchDeps(true, env, &fakeHerdr{})
	deps.loadConfig = func() (*config.Config, error) { return nil, errors.New("config: missing") }
	runWithDependencies([]string{"sidebar"}, os.Stdin, io.Discard, io.Discard, deps)
	if got := (*launches)[0]; got.notify != nil {
		t.Fatal("notifier wired without [herdr] notify = true")
	}

	deps, launches = launchDeps(true, nil, &fakeHerdr{})
	runWithDependencies([]string{"sidebar"}, os.Stdin, io.Discard, io.Discard, deps)
	if got := (*launches)[0]; got.asker != nil || got.unread != nil || got.notify != nil {
		t.Fatalf("launch outside herdr = %+v, want no herdr hooks", got)
	}

	deps, launches = launchDeps(true, env, &fakeHerdr{})
	runWithDependencies([]string{"open", "mail:cl:1"}, os.Stdin, io.Discard, io.Discard, deps)
	if got := (*launches)[0]; got.asker == nil || got.unread != nil || got.notify != nil {
		t.Fatalf("open launch = %+v, want only the asker", got)
	}
}
