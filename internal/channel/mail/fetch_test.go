package mail

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

var multipartMessage = "From: Alice <alice@example.org>\r\n" +
	"To: alice@example.org\r\n" +
	"Subject: Meet recording\r\n" +
	"Message-Id: <multi@example.org>\r\n" +
	"Date: Fri, 25 Sep 2026 10:00:00 +0000\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=OUTER\r\n" +
	"\r\n" +
	"--OUTER\r\n" +
	"Content-Type: multipart/alternative; boundary=INNER\r\n" +
	"\r\n" +
	"--INNER\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"Plain body text\r\n" +
	"--INNER\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"\r\n" +
	"<p>Html body text</p>\r\n" +
	"--INNER--\r\n" +
	"--OUTER\r\n" +
	"Content-Type: video/mp4; name=recording.mp4\r\n" +
	"Content-Disposition: attachment; filename=recording.mp4\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"\r\n" +
	base64.StdEncoding.EncodeToString([]byte("fake video bytes")) + "\r\n" +
	"--OUTER--\r\n"

func TestAdapterFetch(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", multipartMessage)

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))

	// Learn the real UIDVALIDITY/UID the way Run would, via one short
	// sync, so the fetched id is valid.
	ctx, cancel := context.WithCancel(context.Background())
	sink := newFakeSink()
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()
	seed := waitForUpsert(t, sink, 5*time.Second)
	cancel()
	<-done

	item, err := adapter.Fetch(context.Background(), seed.ID)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}

	if item.Body != "Plain body text" {
		t.Errorf("Body = %q, want the text/plain part", item.Body)
	}
	if len(item.Attachments) != 1 {
		t.Fatalf("len(Attachments) = %d, want 1", len(item.Attachments))
	}
	att := item.Attachments[0]
	if att.Name != "recording.mp4" || att.MIME != "video/mp4" {
		t.Errorf("Attachment = %+v, want name=recording.mp4 mime=video/mp4", att)
	}
	if att.Size != int64(len("fake video bytes")) {
		t.Errorf("Attachment.Size = %d, want %d", att.Size, len("fake video bytes"))
	}

	// BODY.PEEK must never mark the message \Seen.
	if !item.Unread {
		t.Error("Fetch() marked the message read; it must use BODY.PEEK")
	}
}

func TestAdapterFetchPrefersPlainTextAndFallsBackToHTML(t *testing.T) {
	htmlOnly := "From: a@example.org\r\n" +
		"To: r@example.org\r\n" +
		"Subject: HTML only\r\n" +
		"Message-Id: <htmlonly@example.org>\r\n" +
		"Date: Fri, 25 Sep 2026 10:00:00 +0000\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		"<p>Only <b>html</b> here</p>\r\n"

	addr, _, _ := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", htmlOnly)

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))

	ctx, cancel := context.WithCancel(context.Background())
	sink := newFakeSink()
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()
	seed := waitForUpsert(t, sink, 5*time.Second)
	cancel()
	<-done

	item, err := adapter.Fetch(context.Background(), seed.ID)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if !strings.Contains(item.Body, "Only html here") {
		t.Errorf("Body = %q, want the HTML fallback rendered as text", item.Body)
	}
}
