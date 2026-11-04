package mail

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

func TestBuildRawMessageHTMLIsMultipartAlternative(t *testing.T) {
	out := core.Outgoing{To: []string{"to@example.org"}, Subject: "Hola", Body: "<p>Hola <b>mundo</b></p>", HTML: true}
	raw, err := buildRawMessage(AccountConfig{Username: "me@example.org"}, out, "Hola", "<id@example.org>", "", nil, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("buildRawMessage: %v", err)
	}
	if !strings.Contains(string(raw), "multipart/alternative") {
		t.Fatalf("raw message is not multipart/alternative:\n%s", raw)
	}
	body, htmlBody, atts, _, err := walkMessageHTML(raw, false)
	if err != nil {
		t.Fatalf("walkMessageHTML: %v", err)
	}
	if strings.TrimSpace(body) != "Hola mundo" {
		t.Errorf("text part = %q, want the HTML rendered as text", body)
	}
	if strings.TrimSpace(htmlBody) != "<p>Hola <b>mundo</b></p>" {
		t.Errorf("html part = %q, want the body as given", htmlBody)
	}
	if len(atts) != 0 {
		t.Errorf("attachments = %+v, want none", atts)
	}
}

func TestBuildRawMessageHTMLWithAttachment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("adjunto"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := core.Outgoing{To: []string{"to@example.org"}, Body: "<p>con adjunto</p>", HTML: true, Attachments: []string{path}}
	raw, err := buildRawMessage(AccountConfig{Username: "me@example.org"}, out, "S", "<id@example.org>", "", nil, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("buildRawMessage: %v", err)
	}
	body, htmlBody, atts, _, err := walkMessageHTML(raw, false)
	if err != nil {
		t.Fatalf("walkMessageHTML: %v", err)
	}
	if strings.TrimSpace(body) != "con adjunto" || strings.TrimSpace(htmlBody) != "<p>con adjunto</p>" {
		t.Errorf("body = %q, html = %q", body, htmlBody)
	}
	if len(atts) != 1 || atts[0].Name != "a.txt" {
		t.Errorf("attachments = %+v, want a.txt", atts)
	}
}

func TestBuildRawMessagePlainStaysSinglePart(t *testing.T) {
	out := core.Outgoing{To: []string{"to@example.org"}, Body: "texto <b>literal</b>"}
	raw, err := buildRawMessage(AccountConfig{Username: "me@example.org"}, out, "S", "<id@example.org>", "", nil, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("buildRawMessage: %v", err)
	}
	s := string(raw)
	if strings.Contains(s, "multipart") || strings.Contains(s, "text/html") || !strings.Contains(s, "text/plain") {
		t.Errorf("plain send changed shape:\n%s", s)
	}
}
