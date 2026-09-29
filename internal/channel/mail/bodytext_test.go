package mail

import (
	"context"
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/reyer3/bunker-go/internal/config"
	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/store"
)

// syncOne runs the adapter against sink until its first upsert and
// returns that item, then stops Run.
func syncOne(t *testing.T, cfg AccountConfig, addr string) core.Item {
	t.Helper()
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))
	ctx, cancel := context.WithCancel(context.Background())
	sink := newFakeSink()
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()
	item := waitForUpsert(t, sink, 5*time.Second)
	cancel()
	<-done
	return item
}

// TestSyncStoresSearchableBodyWithoutSeen (#91): after sync, a mail is
// found by a word of its body through the store's full-text search, and
// fetching that body left the message unread on the server.
func TestSyncStoresSearchableBodyWithoutSeen(t *testing.T) {
	srv := newProfileIMAPServer(t, dovecotProfile(dovecotOptions{}))
	seeded := srv.Seed(t, "INBOX", seedMessage{
		Subject: "Aviso",
		Body:    "La factura de septiembre está pendiente de pago.\r\n",
	})

	st, err := store.Open(filepath.Join(t.TempDir(), "bunker.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	cfg := srv.Config("cl")
	cfg.IndexBodyMaxKB = defaultIndexBodyMaxKB
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(srv.Addr))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, st) }()

	var found []core.Item
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		found, err = st.List(context.Background(), core.Filter{Query: "factura septiembre"})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(found) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done

	if len(found) != 1 {
		t.Fatalf("List(Query) = %d items, want the synced mail found by its body", len(found))
	}
	if !strings.Contains(found[0].Body, "está pendiente") {
		t.Errorf("Body = %q, want the message text", found[0].Body)
	}
	if !found[0].Unread {
		t.Error("stored item is read, want it unread like on the server")
	}
	if flags := srv.FetchMessage(t, "INBOX", seeded.MessageID).Flags; hasSeenFlag(flags) {
		t.Errorf("server flags = %v after sync, want no \\Seen", flags)
	}
}

// TestSyncTruncatesBodyAtRuneBoundary: a body over index_body_max_kb is
// cut to the limit without leaving half a character, even when the
// server's byte range ends mid-rune.
func TestSyncTruncatesBodyAtRuneBoundary(t *testing.T) {
	srv := newProfileIMAPServer(t, dovecotProfile(dovecotOptions{}))
	// One ASCII byte then two-byte runes: the 1024-byte range ends on
	// the first byte of a rune.
	full := "a" + strings.Repeat("ñ", 1000)
	srv.Seed(t, "INBOX", seedMessage{Body: full})

	cfg := srv.Config("cl")
	cfg.IndexBodyMaxKB = 1
	item := syncOne(t, cfg, srv.Addr)

	if len(item.Body) > 1024 {
		t.Errorf("len(Body) = %d, want at most 1024", len(item.Body))
	}
	if !utf8.ValidString(item.Body) {
		t.Errorf("Body ends in %q, want valid UTF-8", item.Body[len(item.Body)-4:])
	}
	if !strings.HasPrefix(full, item.Body) || len(item.Body) != 1023 {
		t.Errorf("len(Body) = %d, want the first 1023 bytes of the text", len(item.Body))
	}
}

// TestSyncIndexBodyDisabled: index_body_max_kb = 0 keeps the header-only
// sync, storing no body.
func TestSyncIndexBodyDisabled(t *testing.T) {
	srv := newProfileIMAPServer(t, dovecotProfile(dovecotOptions{}))
	srv.Seed(t, "INBOX", seedMessage{Body: "texto que no se indexa"})

	cfg := srv.Config("cl")
	cfg.IndexBodyMaxKB = 0
	item := syncOne(t, cfg, srv.Addr)
	if item.Body != "" {
		t.Errorf("Body = %q, want empty with index_body_max_kb = 0", item.Body)
	}
	if item.Subject != "Prueba" {
		t.Errorf("Subject = %q, want the headers still synced", item.Subject)
	}
}

// TestSyncBodySkipsLargeAttachment: the text before a big attachment is
// indexed from a bounded range, the attachment itself never recorded
// from the cut copy, and the thread still comes from References alone.
func TestSyncBodySkipsLargeAttachment(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	big := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 64*1024)))
	appendMessage(t, addr, "INBOX", "From: Alice <alice@example.org>\r\n"+
		"Subject: Planos\r\n"+
		"Message-Id: <planos@example.org>\r\n"+
		"References: <raiz@example.org>\r\n"+
		"Date: Fri, 25 Sep 2026 10:00:00 +0000\r\n"+
		"MIME-Version: 1.0\r\n"+
		"Content-Type: multipart/mixed; boundary=B\r\n\r\n"+
		"--B\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n"+
		"Adjunto los planos del edificio.\r\n"+
		"--B\r\nContent-Type: application/pdf\r\n"+
		"Content-Disposition: attachment; filename=planos.pdf\r\n"+
		"Content-Transfer-Encoding: base64\r\n\r\n"+
		big+"\r\n--B--\r\n")

	item := syncOne(t, AccountConfig{Name: "cl", IMAPHost: "unused", IndexBodyMaxKB: 1}, addr)
	if !strings.Contains(item.Body, "Adjunto los planos del edificio.") {
		t.Errorf("Body = %q, want the text part", item.Body)
	}
	if len(item.Attachments) != 0 {
		t.Errorf("Attachments = %+v, want none from a truncated copy", item.Attachments)
	}
	if want := ThreadID("planos@example.org", "", []string{"raiz@example.org"}); item.Thread != want {
		t.Errorf("Thread = %q, want %q (from References only)", item.Thread, want)
	}
}

// TestSyncBodyHTMLOnly: an HTML-only mail stores the text the same
// HTML-to-text rendering a full read uses.
func TestSyncBodyHTMLOnly(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", "From: a@example.org\r\n"+
		"Subject: Boletín\r\n"+
		"Date: Fri, 25 Sep 2026 10:00:00 +0000\r\n"+
		"MIME-Version: 1.0\r\n"+
		"Content-Type: text/html; charset=utf-8\r\n\r\n"+
		"<html><body><p>Novedades de <b>octubre</b></p></body></html>\r\n")

	item := syncOne(t, AccountConfig{Name: "cl", IMAPHost: "unused", IndexBodyMaxKB: 64}, addr)
	if want := HTMLToText("<html><body><p>Novedades de <b>octubre</b></p></body></html>\r\n"); item.Body != want {
		t.Errorf("Body = %q, want %q", item.Body, want)
	}
	if strings.Contains(item.Body, "<p>") {
		t.Errorf("Body = %q, want text without markup", item.Body)
	}
}

func TestTruncateUTF8(t *testing.T) {
	for _, tc := range []struct {
		in    string
		limit int
		want  string
	}{
		{"hola", 10, "hola"},
		{"añb", 2, "a"},        // the cut would split ñ
		{"añb", 3, "añ"},       // exactly on a boundary
		{"a\xc3", 10, "a"},     // an incomplete rune left by the byte range
		{"a\xe2\x82", 10, "a"}, // a longer incomplete rune
	} {
		if got := truncateUTF8(tc.in, tc.limit); got != tc.want {
			t.Errorf("truncateUTF8(%q, %d) = %q, want %q", tc.in, tc.limit, got, tc.want)
		}
	}
}

func TestParseAccountConfigIndexBodyMaxKB(t *testing.T) {
	base := func(opts map[string]interface{}) config.Account {
		opts["imap_host"] = "mail.example.cl"
		opts["username"] = "x"
		return config.Account{Channel: "mail", Name: "cl", Options: opts}
	}

	cfg, err := ParseAccountConfig(base(map[string]interface{}{}))
	if err != nil {
		t.Fatalf("ParseAccountConfig() error = %v", err)
	}
	if cfg.IndexBodyMaxKB != defaultIndexBodyMaxKB {
		t.Errorf("default IndexBodyMaxKB = %d, want %d", cfg.IndexBodyMaxKB, defaultIndexBodyMaxKB)
	}

	cfg, err = ParseAccountConfig(base(map[string]interface{}{"index_body_max_kb": int64(0)}))
	if err != nil {
		t.Fatalf("ParseAccountConfig(0) error = %v", err)
	}
	if cfg.IndexBodyMaxKB != 0 {
		t.Errorf("IndexBodyMaxKB = %d, want 0 (disabled)", cfg.IndexBodyMaxKB)
	}

	if _, err := ParseAccountConfig(base(map[string]interface{}{"index_body_max_kb": int64(-1)})); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("ParseAccountConfig(-1) error = %v, want ErrInvalidConfig", err)
	}
}
