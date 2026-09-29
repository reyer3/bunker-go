package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/store"
)

// ftsItem is a search fixture with no incidental text: sampleItem's
// addresses, thread name and attachment would otherwise match many of
// these queries and hide which field a match came from.
func ftsItem(id string) core.Item {
	return core.Item{
		ID:        id,
		Channel:   core.ChannelMail,
		Account:   "cl",
		Thread:    "t-" + id,
		Timestamp: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
}

// queryIDs runs List with q and returns the matching ids sorted, so
// assertions do not depend on the fixtures' shared timestamp.
func queryIDs(t *testing.T, s *store.Store, q string) []string {
	t.Helper()
	got, err := s.List(context.Background(), core.Filter{Query: q})
	if err != nil {
		t.Fatalf("List query %q: %v", q, err)
	}
	ids := make([]string, len(got))
	for i, it := range got {
		ids[i] = it.ID
	}
	slices.Sort(ids)
	return ids
}

func assertQuery(t *testing.T, s *store.Store, q string, want ...string) {
	t.Helper()
	slices.Sort(want)
	if got := queryIDs(t, s, q); !slices.Equal(got, want) {
		t.Fatalf("List query %q = %v, want %v", q, got, want)
	}
}

func upsertAll(t *testing.T, s *store.Store, items ...core.Item) {
	t.Helper()
	for _, it := range items {
		if err := s.Upsert(context.Background(), it); err != nil {
			t.Fatalf("Upsert %s: %v", it.ID, err)
		}
	}
}

func TestListQueryFindsEveryIndexedField(t *testing.T) {
	s := openTestStore(t)

	subject := ftsItem("mail:cl:subject")
	subject.Subject = "Reunión de presupuesto"
	sender := ftsItem("mail:cl:sender")
	sender.From = core.Address{ID: "remitente@example.com", Name: "José Ejemplo"}
	recipient := ftsItem("mail:cl:recipient")
	recipient.To = []core.Address{{ID: "destino@example.org", Name: "Destinataria Prueba"}}
	body := ftsItem("mail:cl:body")
	body.Body = "el informe trimestral va adjunto"
	attachment := ftsItem("mail:cl:attachment")
	attachment.Attachments = []core.Attachment{{Name: "factura-marzo.pdf", MIME: "application/pdf"}}
	thread := ftsItem("whatsapp:personal:thread")
	thread.Channel = core.ChannelWhatsApp
	thread.ThreadName = "Grupo Montañismo"
	upsertAll(t, s, subject, sender, recipient, body, attachment, thread)

	for _, tc := range []struct {
		name, query, want string
	}{
		{"subject", "reunion", subject.ID},
		{"sender name, accent-insensitive", "jose", sender.ID},
		{"sender name, accented query", "JOSÉ", sender.ID},
		{"sender address", "remitente@example.com", sender.ID},
		{"recipient address", "destino@example.org", recipient.ID},
		{"recipient name", "destinataria", recipient.ID},
		{"body", "informe trimestral", body.ID},
		{"attachment name", "factura", attachment.ID},
		{"attachment full name", "factura-marzo.pdf", attachment.ID},
		{"thread name", "montanismo", thread.ID},
		{"prefix on the last word", "trimes", body.ID},
		{"prefix with accents folded", "presu", subject.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertQuery(t, s, tc.query, tc.want)
		})
	}

	// Only the last word is a prefix: earlier words must match whole
	// tokens, so "infor trimestral" does not find "informe trimestral".
	assertQuery(t, s, "infor trimestral")
	// Every word must match (AND), not any of them.
	assertQuery(t, s, "informe factura")
}

func TestListQueryKeepsOtherFiltersAndNewestFirst(t *testing.T) {
	s := openTestStore(t)

	older := ftsItem("mail:cl:older")
	older.Body = "cotización pendiente"
	older.Timestamp = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := ftsItem("mail:cl:newer")
	newer.Body = "cotizacion enviada"
	newer.Timestamp = time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)
	other := ftsItem("whatsapp:personal:1")
	other.Channel = core.ChannelWhatsApp
	other.Account = "personal"
	other.Body = "otra cotización"
	upsertAll(t, s, older, newer, other)

	got, err := s.List(context.Background(), core.Filter{Query: "cotizacion", Channel: core.ChannelMail})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 || got[0].ID != newer.ID || got[1].ID != older.ID {
		ids := make([]string, len(got))
		for i, it := range got {
			ids[i] = it.ID
		}
		t.Fatalf("List = %v, want [%s %s] (mail only, newest first)", ids, newer.ID, older.ID)
	}

	limited, err := s.List(context.Background(), core.Filter{Query: "cotizacion", Limit: 1})
	if err != nil {
		t.Fatalf("List with limit: %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("List with limit len = %d, want 1", len(limited))
	}
}

// TestListQueryNeverInjectsFTSSyntax feeds FTS5 operators and quoting
// through Filter.Query: each must be searched for as text (or, with no
// word characters, through the literal LIKE fallback), never error and
// never widen or restructure the query.
func TestListQueryNeverInjectsFTSSyntax(t *testing.T) {
	s := openTestStore(t)

	alpha := ftsItem("mail:cl:alpha")
	alpha.Body = "alfa uno"
	beta := ftsItem("mail:cl:beta")
	beta.Body = "beta dos"
	quoted := ftsItem("mail:cl:quoted")
	quoted.Body = `dijo "hola" (sin más) *importante* -fin`
	upsertAll(t, s, alpha, beta, quoted)

	for _, q := range []string{
		`"`, `""`, `*`, `-`, `(`, `)`, `^`, `:`, `NEAR`, `OR`, `AND`, `NOT`,
		`alfa"`, `"alfa`, `alfa*beta`, `NEAR(alfa beta)`, `alfa NEAR/2 beta`,
		`body:alfa`, `{body}:alfa`, `alfa^`, `- alfa`, `alfa -beta`,
		`" OR "`, `alfa" OR "beta`,
	} {
		if _, err := s.List(context.Background(), core.Filter{Query: q}); err != nil {
			t.Errorf("List query %q: %v", q, err)
		}
	}

	// OR, NOT and NEAR are searched for as words, so combining two
	// items' words with them finds neither item.
	assertQuery(t, s, "alfa OR beta")
	assertQuery(t, s, "alfa NOT uno")
	assertQuery(t, s, "NEAR(alfa beta)")
	// A quote cannot close the literal early and smuggle in an OR.
	assertQuery(t, s, `alfa" OR "beta`)
	// A column filter is text, not a restriction to the body column.
	assertQuery(t, s, "body:alfa")
	// Punctuation around words still finds them.
	assertQuery(t, s, `"hola"`, quoted.ID)
	assertQuery(t, s, "(sin más)", quoted.ID)
	assertQuery(t, s, "*importante*", quoted.ID)
	assertQuery(t, s, "-fin", quoted.ID)
	// With no word characters the query falls back to a literal
	// substring search.
	assertQuery(t, s, `"`, quoted.ID)
	assertQuery(t, s, `*`, quoted.ID)
	assertQuery(t, s, `(`, quoted.ID)
	assertQuery(t, s, `-`, quoted.ID)
}

func TestListQueryIndexFollowsEveryWritePath(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	it := ftsItem("whatsapp:personal:1")
	it.Channel = core.ChannelWhatsApp
	it.Account = "personal"
	it.Subject = "asunto inicial"
	it.Body = "texto original"
	upsertAll(t, s, it)
	assertQuery(t, s, "original", it.ID)

	// Upsert over an existing id replaces the indexed text.
	it.Subject = "asunto nuevo"
	it.Attachments = []core.Attachment{{Name: "plano.png"}}
	upsertAll(t, s, it)
	assertQuery(t, s, "inicial")
	assertQuery(t, s, "nuevo", it.ID)
	assertQuery(t, s, "plano", it.ID)

	// MarkRead does not touch indexed columns and must leave the entry.
	if err := s.MarkRead(ctx, it.ID, true); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	assertQuery(t, s, "original", it.ID)

	if err := s.EditItem(ctx, it.ID, "texto corregido"); err != nil {
		t.Fatalf("EditItem: %v", err)
	}
	assertQuery(t, s, "original")
	assertQuery(t, s, "corregido", it.ID)

	if err := s.RevokeItem(ctx, it.ID); err != nil {
		t.Fatalf("RevokeItem: %v", err)
	}
	assertQuery(t, s, "corregido")
	// The row is kept, so its other fields stay searchable.
	assertQuery(t, s, "nuevo", it.ID)

	if err := s.Delete(ctx, it.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	assertQuery(t, s, "nuevo")

	// Re-inserting a deleted id indexes it afresh, with no stale entry.
	it.Subject = "asunto renacido"
	upsertAll(t, s, it)
	assertQuery(t, s, "renacido", it.ID)
	assertQuery(t, s, "nuevo")
}

// TestMigrationV3BackfillsFTSIndexFromExistingRows builds a version-2
// database with rows already in it, the way the earlier migration tests
// build legacy databases, and checks migrateV3 indexes those rows.
func TestMigrationV3BackfillsFTSIndexFromExistingRows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bunker.db")

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := raw.Exec(preFromMeSchema); err != nil {
		t.Fatalf("create pre-FromMe schema: %v", err)
	}
	if _, err := raw.Exec(`
		ALTER TABLE items ADD COLUMN from_me INTEGER NOT NULL DEFAULT 0;
		ALTER TABLE items ADD COLUMN edited INTEGER NOT NULL DEFAULT 0;
		ALTER TABLE items ADD COLUMN deleted INTEGER NOT NULL DEFAULT 0;
		CREATE TABLE labels (
			item_id TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
			label   TEXT NOT NULL,
			PRIMARY KEY (item_id, label)
		);
		CREATE TABLE cursors (key TEXT PRIMARY KEY, value TEXT NOT NULL);
		CREATE TABLE reactions (
			item_id TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
			sender  TEXT NOT NULL,
			emoji   TEXT NOT NULL,
			PRIMARY KEY (item_id, sender)
		);
	`); err != nil {
		t.Fatalf("build version-2 schema: %v", err)
	}
	if _, err := raw.Exec(`
		INSERT INTO items (id, channel, account, thread, from_id, from_name, to_json, subject, body, attachments_json, timestamp)
		VALUES
			('mail:cl:1', 'mail', 'cl', 't1', 'remitente@example.com', 'José Ejemplo',
				'[{"ID":"destino@example.org","Name":"Destinataria"}]', 'Informe', 'cuerpo antiguo',
				'[{"Name":"anexo-final.pdf","MIME":"application/pdf","Size":1,"Ref":"r"}]', 1000),
			('whatsapp:personal:1', 'whatsapp', 'personal', 't2', '', '', 'null', '', 'mensaje heredado', 'null', 2000)
	`); err != nil {
		t.Fatalf("insert legacy rows: %v", err)
	}
	if _, err := raw.Exec(`PRAGMA user_version = 2`); err != nil {
		t.Fatalf("stamp version 2: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw handle: %v", err)
	}

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open on a version-2 database: %v", err)
	}
	if got, want := rawUserVersion(t, path), store.CurrentSchemaVersion(); got != want {
		t.Fatalf("user_version after migrating = %d, want %d", got, want)
	}

	assertQuery(t, s, "jose", "mail:cl:1")
	assertQuery(t, s, "remitente@example.com", "mail:cl:1")
	assertQuery(t, s, "destino", "mail:cl:1")
	assertQuery(t, s, "anexo", "mail:cl:1")
	assertQuery(t, s, "antiguo", "mail:cl:1")
	assertQuery(t, s, "heredado", "whatsapp:personal:1")

	// The triggers are live on the migrated database too.
	if err := s.EditItem(context.Background(), "whatsapp:personal:1", "mensaje editado"); err != nil {
		t.Fatalf("EditItem: %v", err)
	}
	assertQuery(t, s, "heredado")
	assertQuery(t, s, "editado", "whatsapp:personal:1")
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopening an already-migrated database leaves the index intact
	// and does not duplicate entries.
	s, err = store.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	assertQuery(t, s, "jose", "mail:cl:1")
	assertQuery(t, s, "editado", "whatsapp:personal:1")
}

// TestUpsertMailKeepsBodyOverHeaderOnlyCopy (#91): mail sync upserts
// header-only copies, so one landing after a read (which stored the full
// body and attachments) must not blank them or drop the body from the
// index, while the header fields it carries still replace the old ones.
func TestUpsertMailKeepsBodyOverHeaderOnlyCopy(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	full := ftsItem("mail:cl:INBOX.1.7")
	full.Subject = "asunto viejo"
	full.Body = "el informe trimestral va adjunto"
	full.Attachments = []core.Attachment{{Name: "planilla.xlsx", MIME: "application/vnd.ms-excel", Size: 10, Ref: "part-1"}}
	upsertAll(t, s, full)

	headers := ftsItem(full.ID)
	headers.Subject = "asunto nuevo"
	headers.Unread = true
	upsertAll(t, s, headers)

	got, err := s.Get(ctx, full.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Body != full.Body {
		t.Errorf("Body = %q, want the stored %q kept", got.Body, full.Body)
	}
	if len(got.Attachments) != 1 || got.Attachments[0].Name != "planilla.xlsx" {
		t.Errorf("Attachments = %+v, want the stored attachment kept", got.Attachments)
	}
	if got.Subject != "asunto nuevo" || !got.Unread {
		t.Errorf("Subject/Unread = %q/%v, want the header-only copy's values", got.Subject, got.Unread)
	}
	assertQuery(t, s, "trimestral", full.ID)
	assertQuery(t, s, "planilla", full.ID)
	assertQuery(t, s, "nuevo", full.ID)
	assertQuery(t, s, "viejo")

	// An explicitly empty attachment list is empty too, not a replacement.
	headers.Attachments = []core.Attachment{}
	upsertAll(t, s, headers)
	assertQuery(t, s, "planilla", full.ID)

	// A non-empty body still replaces the stored one.
	headers.Body = "texto corregido"
	upsertAll(t, s, headers)
	assertQuery(t, s, "corregido", full.ID)
	assertQuery(t, s, "trimestral")
}

// TestUpsertChatStillReplacesBody: the #91 merge is scoped to mail, so a
// chat Upsert with an empty body keeps replacing the stored one.
func TestUpsertChatStillReplacesBody(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	it := ftsItem("whatsapp:personal:2")
	it.Channel = core.ChannelWhatsApp
	it.Account = "personal"
	it.Body = "hola mundo"
	it.Attachments = []core.Attachment{{Name: "foto.jpg"}}
	upsertAll(t, s, it)

	it.Body = ""
	it.Attachments = nil
	upsertAll(t, s, it)

	got, err := s.Get(ctx, it.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Body != "" || len(got.Attachments) != 0 {
		t.Errorf("Body/Attachments = %q/%+v, want both replaced by the empty copy", got.Body, got.Attachments)
	}
	assertQuery(t, s, "mundo")
	assertQuery(t, s, "foto")
}
