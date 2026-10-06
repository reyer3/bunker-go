package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/reyer3/bunker-go/internal/config"
	"github.com/reyer3/bunker-go/internal/tui"
)

func mailConfig(names ...string) func() (*config.Config, error) {
	cfg := &config.Config{Accounts: []config.Account{{Channel: "whatsapp", Name: "phone"}}}
	for _, n := range names {
		cfg.Accounts = append(cfg.Accounts, config.Account{Channel: "mail", Name: n})
	}
	return func() (*config.Config, error) { return cfg, nil }
}

func TestRunComposeStartsTheEditorFromAMailtoURL(t *testing.T) {
	const url = "mailto:ana@example.com?subject=Hola&body=texto"
	cases := []struct {
		name        string
		args        []string
		env         map[string]string
		wantAccount string
	}{
		{"argument, first mail account", []string{"compose", url}, nil, "work"},
		{"environment", []string{"compose"}, map[string]string{"BUNKER_MAILTO": " " + url + " "}, "work"},
		{"explicit account", []string{"compose", "--account", "home", url}, nil, "home"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, launches := launchDeps(true, tc.env, nil)
			deps.loadConfig = mailConfig("work", "home")
			var stderr bytes.Buffer
			if code := runWithDependencies(tc.args, os.Stdin, io.Discard, &stderr, deps); code != 0 {
				t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr.String())
			}
			if len(*launches) != 1 {
				t.Fatalf("TUI started %d times, want 1", len(*launches))
			}
			got := (*launches)[0]
			want := tui.MailDraft{To: "ana@example.com", Subject: "Hola", Body: "texto"}
			if got.mailDraft == nil || *got.mailDraft != want || got.mailAccount != tc.wantAccount {
				t.Fatalf("launch draft=%+v account=%q, want %+v on %q", got.mailDraft, got.mailAccount, want, tc.wantAccount)
			}
			if got.sidebar || got.openID != "" {
				t.Fatalf("launch = %+v, want only the compose draft", got)
			}
		})
	}
}

func TestRunComposeRejectsBadInvocations(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		cfg      func() (*config.Config, error)
		terminal bool
		wantErr  string
	}{
		{"no url", []string{"compose"}, mailConfig("work"), true, "usage: bunker compose"},
		{"two urls", []string{"compose", "mailto:a@example.com", "mailto:b@example.com"}, mailConfig("work"), true, "usage: bunker compose"},
		{"not mailto", []string{"compose", "https://example.com"}, mailConfig("work"), true, "not a mailto"},
		{"no mail account", []string{"compose", "mailto:a@example.com"}, mailConfig(), true, "no mail account"},
		{"unknown account", []string{"compose", "--account", "nope", "mailto:a@example.com"}, mailConfig("work"), true, "no mail account \"nope\""},
		{"no terminal", []string{"compose", "mailto:a@example.com"}, mailConfig("work"), false, "needs a terminal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, launches := launchDeps(tc.terminal, nil, nil)
			deps.loadConfig = tc.cfg
			var stderr bytes.Buffer
			if code := runWithDependencies(tc.args, os.Stdin, io.Discard, &stderr, deps); code != 2 {
				t.Fatalf("exit code = %d, want 2 (stderr %q)", code, stderr.String())
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

func TestHerdrMailtoOpensTheComposePane(t *testing.T) {
	const url = "mailto:ana@example.com?subject=Hola"
	herdr := &fakeHerdr{}
	deps := herdrDeps{run: herdr.run, getenv: herdrEnv(map[string]string{"HERDR_PLUGIN_CLICKED_URL": url})}
	var stdout, stderr bytes.Buffer
	if code := cmdHerdr(context.Background(), []string{"mailto"}, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	want := "plugin pane open --plugin bunker --entrypoint compose --env BUNKER_MAILTO=" + url + " --focus"
	if len(herdr.calls) != 1 || herdr.calls[0] != want {
		t.Fatalf("herdr calls = %q, want [%q]", herdr.calls, want)
	}
}

func TestHerdrMailtoDryRunAndArgument(t *testing.T) {
	herdr := &fakeHerdr{}
	deps := herdrDeps{run: herdr.run, getenv: herdrEnv(nil)}
	var stdout bytes.Buffer
	if code := cmdHerdr(context.Background(), []string{"mailto", "--dry-run", "mailto:ana@example.com"}, &stdout, io.Discard, deps); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if len(herdr.calls) != 0 {
		t.Fatalf("dry run called herdr: %q", herdr.calls)
	}
	if want := "herdr plugin pane open --plugin bunker --entrypoint compose --env BUNKER_MAILTO=mailto:ana@example.com --focus\n"; stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestHerdrMailtoRejectsBadURLs(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"missing":    nil,
		"not mailto": {"HERDR_PLUGIN_CLICKED_URL": "https://example.com"},
		"injection":  {"HERDR_PLUGIN_CLICKED_URL": "mailto:a@example.com\nx"},
	} {
		t.Run(name, func(t *testing.T) {
			herdr := &fakeHerdr{}
			deps := herdrDeps{run: herdr.run, getenv: herdrEnv(env)}
			if code := cmdHerdr(context.Background(), []string{"mailto"}, io.Discard, io.Discard, deps); code == 0 {
				t.Fatal("exit code = 0, want a failure")
			}
			if len(herdr.calls) != 0 {
				t.Fatalf("herdr called with a bad URL: %q", herdr.calls)
			}
		})
	}
}
