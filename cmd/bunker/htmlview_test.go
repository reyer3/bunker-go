package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

func TestSanitizeHTML(t *testing.T) {
	in := `<html><head><title>x</title><style>p{color:red}</style></head><body>` +
		`<p onclick="evil()" style="x">Hola <b>mundo</b></p>` +
		`<script>alert(1)</script>` +
		`<img src="https://tracker.example/p.gif" alt="logo">` +
		`<a href="javascript:evil()">bad</a> <a href="https://example.org/ok" target="_blank">ok</a>` +
		`<iframe src="https://example.org"></iframe></body></html>`
	got := sanitizeHTML(in)
	for _, banned := range []string{"<script", "alert(1)", "onclick", "style=", "<img", "tracker.example", "javascript:", "<iframe", "color:red", "target="} {
		if strings.Contains(got, banned) {
			t.Errorf("sanitizeHTML() kept %q in %q", banned, got)
		}
	}
	for _, want := range []string{"Hola", "<b>mundo</b>", `href="https://example.org/ok"`, "bad"} {
		if !strings.Contains(got, want) {
			t.Errorf("sanitizeHTML() dropped %q from %q", want, got)
		}
	}
}

func TestCmdReadHTMLOpensSanitizedFile(t *testing.T) {
	backend := newFakeBackend()
	backend.items["mail:cl:1"] = core.Item{ID: "mail:cl:1", Body: "plain", Meta: map[string]string{"html": `<p>Hola</p><script>alert(1)</script>`}}

	var opened string
	old := openHTMLFile
	openHTMLFile = func(path string) error { opened = path; return nil }
	t.Cleanup(func() { openHTMLFile = old })

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"read", "mail:cl:1", "--html"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if opened == "" {
		t.Fatal("read --html opened nothing")
	}
	t.Cleanup(func() { os.Remove(opened) })
	data, err := os.ReadFile(opened)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Hola") || strings.Contains(string(data), "script") {
		t.Errorf("file = %q, want the sanitized body", data)
	}
	if len(backend.readCalls) != 1 || backend.readCalls[0].MarkReceipt {
		t.Errorf("readCalls = %+v, want one pure read", backend.readCalls)
	}
}

func TestCmdReadHTMLWithoutHTMLPartFails(t *testing.T) {
	backend := newFakeBackend()
	backend.items["mail:cl:1"] = core.Item{ID: "mail:cl:1", Body: "plain only"}

	called := false
	old := openHTMLFile
	openHTMLFile = func(string) error { called = true; return nil }
	t.Cleanup(func() { openHTMLFile = old })

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"read", "mail:cl:1", "--html"}, strings.NewReader(""), &stdout, &stderr)
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout.String())
	}
	if called {
		t.Error("read --html opened a browser for a mail with no HTML part")
	}
	if !strings.Contains(stderr.String()+stdout.String(), "HTML") {
		t.Errorf("output = %q %q, want it to say the mail has no HTML part", stdout.String(), stderr.String())
	}
}

func TestCmdReadHTMLRejectsJSON(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"read", "mail:cl:1", "--html", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if len(backend.readCalls) != 0 {
		t.Error("read ran despite contradictory flags")
	}
}
