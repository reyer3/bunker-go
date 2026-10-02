package openurl

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestCommandOverrideAndDefault(t *testing.T) {
	got, err := Command("https://meet.google.com/abc-defg-hij", env(map[string]string{Env: "firefox --new-window"}))
	if err != nil || !reflect.DeepEqual(got, []string{"firefox", "--new-window", "https://meet.google.com/abc-defg-hij"}) {
		t.Fatalf("override = %v, %v", got, err)
	}
	got, err = Command("https://zoom.us/j/1", env(nil))
	if err != nil || len(got) != 2 || got[1] != "https://zoom.us/j/1" {
		t.Fatalf("default = %v, %v", got, err)
	}
}

func TestCommandRejectsEmptyAndUnsafeURLs(t *testing.T) {
	for _, u := range []string{"", "  ", "file:///etc/passwd", "javascript:alert(1)", "ftp://x/y", "https://", "not a url"} {
		if _, err := Command(u, env(nil)); err == nil {
			t.Errorf("Command(%q) succeeded, want an error", u)
		}
	}
}

func TestOpenRunsOverrideAndReportsMissingOpener(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	script := filepath.Join(dir, "opener.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$1\" > "+out+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Open("https://meet.jit.si/Sala", env(map[string]string{Env: script})); err != nil {
		t.Fatalf("Open: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		b, _ := os.ReadFile(out)
		if strings.TrimSpace(string(b)) == "https://meet.jit.si/Sala" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("opener never received the URL, out = %q", b)
		}
		time.Sleep(10 * time.Millisecond)
	}
	err := Open("https://meet.jit.si/Sala", env(map[string]string{Env: filepath.Join(dir, "no-such-opener")}))
	if err == nil || !strings.Contains(err.Error(), Env) {
		t.Fatalf("missing opener err = %v, want one naming %s", err, Env)
	}
}
