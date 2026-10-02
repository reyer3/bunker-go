// Package store persists core.Item and cursor state in a SQLite database
// via modernc.org/sqlite (pure Go, no CGO). Store implements core.Store.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	_ "modernc.org/sqlite"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/secfile"
)

// Store is a SQLite-backed core.Store.
type Store struct {
	db *sql.DB
}

var _ core.PageLister = (*Store)(nil)

const schema = `
CREATE TABLE IF NOT EXISTS items (
	id           TEXT PRIMARY KEY,
	channel      TEXT NOT NULL,
	account      TEXT NOT NULL,
	thread       TEXT NOT NULL DEFAULT '',
	thread_name  TEXT NOT NULL DEFAULT '',
	from_id      TEXT NOT NULL DEFAULT '',
	from_name    TEXT NOT NULL DEFAULT '',
	to_json      TEXT NOT NULL DEFAULT '[]',
	subject      TEXT NOT NULL DEFAULT '',
	body         TEXT NOT NULL DEFAULT '',
	attachments_json TEXT NOT NULL DEFAULT '[]',
	unread       INTEGER NOT NULL DEFAULT 0,
	from_me      INTEGER NOT NULL DEFAULT 0,
	timestamp    INTEGER NOT NULL DEFAULT 0,
	meta_json    TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS labels (
	item_id TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
	label   TEXT NOT NULL,
	PRIMARY KEY (item_id, label)
);

CREATE TABLE IF NOT EXISTS cursors (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_items_channel_account ON items(channel, account);
CREATE INDEX IF NOT EXISTS idx_items_unread ON items(unread);
CREATE INDEX IF NOT EXISTS idx_labels_label ON labels(label);
-- Backs Thread's conversation-scoped, oldest-first pagination (K1):
-- CREATE INDEX IF NOT EXISTS is safe to (re-)run against a database that
-- predates it, since channel/account/thread/timestamp already existed.
CREATE INDEX IF NOT EXISTS idx_items_thread ON items(channel, account, thread, timestamp);
`

// Open creates or opens the SQLite database at path, applies migrations
// and configures WAL mode plus a busy timeout so concurrent readers
// (daemon + CLI) do not fail with "database is locked". The database
// holds message content, so its parent directory is created (or
// tightened) at 0700 and the file itself at 0600, never whatever mode
// the process's umask would otherwise leave: see internal/secfile.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := secfile.EnsureDir(dir); err != nil {
			return nil, fmt.Errorf("store: create state dir: %w", err)
		}
	}
	if err := secfile.EnsureFile(path); err != nil {
		return nil, fmt.Errorf("store: create private file %s: %w", path, err)
	}

	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1) // modernc.org/sqlite: serialize writers, WAL still allows concurrent readers across processes.

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	secfile.SecureSidecars(path, "-wal", "-shm")
	return &Store{db: db}, nil
}

// migration is one ordered, append-only step of the schema's evolution,
// tracked by PRAGMA user_version. apply must be safe to run against a
// database already at or past its own version — every CREATE is IF NOT
// EXISTS and every ALTER first checks hasColumn — so a legacy database
// that predates schema versioning (user_version 0) is brought up to date
// and stamped by running every migration idempotently, never by
// rewriting existing rows: a migration whose shape the database already
// has (e.g. a column added by an older, ad hoc version of this code) is
// a no-op that still advances user_version to record it.
type migration struct {
	version int
	apply   func(db *sql.DB) error
}

// migrations is append-only: never edit an existing entry once
// released, only append a new one with the next version number.
var migrations = []migration{
	{version: 1, apply: migrateV1},
	{version: 2, apply: migrateV2},
	{version: 3, apply: migrateV3},
	{version: 4, apply: migrateV4},
}

// CurrentSchemaVersion returns the latest schema version this binary
// understands (the highest entry in the append-only migrations list),
// e.g. for ops/debugging or a test asserting against "however many
// migrations exist today" instead of a version number that would
// otherwise need editing every time a new migration is appended.
func CurrentSchemaVersion() int {
	return migrations[len(migrations)-1].version
}

// migrateV1 creates the base schema (items, labels, cursors) and adds
// from_me (K1) to any items table that predates it.
func migrateV1(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}

	hasFromMe, err := hasColumn(db, "items", "from_me")
	if err != nil {
		return fmt.Errorf("check from_me column: %w", err)
	}
	if !hasFromMe {
		if _, err := db.Exec(`ALTER TABLE items ADD COLUMN from_me INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add from_me column: %w", err)
		}
	}
	return nil
}

// migrateV2 adds the channel-agnostic edit/revoke/reaction model (S2):
// edited and deleted flags on items, plus a reactions table keyed on
// (item_id, sender) so a newer reaction from the same sender replaces
// the previous one via ON CONFLICT rather than accumulating duplicates.
func migrateV2(db *sql.DB) error {
	for _, col := range []string{"edited", "deleted"} {
		has, err := hasColumn(db, "items", col)
		if err != nil {
			return fmt.Errorf("check %s column: %w", col, err)
		}
		if !has {
			if _, err := db.Exec(fmt.Sprintf(`ALTER TABLE items ADD COLUMN %s INTEGER NOT NULL DEFAULT 0`, col)); err != nil {
				return fmt.Errorf("add %s column: %w", col, err)
			}
		}
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS reactions (
			item_id TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
			sender  TEXT NOT NULL,
			emoji   TEXT NOT NULL,
			PRIMARY KEY (item_id, sender)
		)
	`); err != nil {
		return fmt.Errorf("create reactions table: %w", err)
	}
	return nil
}

// ftsSchema is the full-text index behind List's Filter.Query (#59).
//
// Design, and why:
//
//   - items_fts is a contentless FTS5 table (its content option is the
//     empty string), so message bodies are not stored a second time: the
//     index only needs to answer "which items match", and List reads the
//     columns themselves from items. contentless_delete=1 (SQLite 3.43+,
//     which modernc.org/sqlite bundles) lets a row be removed by rowid
//     alone, without replaying the exact old column values.
//   - The unicode61 tokenizer with remove_diacritics 2 folds case and
//     accents on both sides, so "jose" finds "José".
//   - Several indexed values are derived rather than stored as plain
//     columns: recipients live in to_json and attachment names in
//     attachments_json. An external-content table cannot express that
//     (FTS5 would read the raw JSON back), so the indexed text is built
//     with json_each at write time.
//   - FTS rowids come from items_fts_map, not items.rowid: items has a
//     TEXT primary key, so its implicit rowid is not stable and VACUUM may
//     renumber it, silently pointing every index entry at the wrong item.
//     items_fts_map's INTEGER PRIMARY KEY survives VACUUM.
//   - Triggers, not the Go write paths, keep the index in sync: they fire
//     for every statement that touches items (Upsert, EditItem,
//     RevokeItem, Delete, and any future writer) inside the same
//     transaction, so the index cannot drift from a forgotten call site.
//     The UPDATE trigger is limited to the indexed columns, so MarkRead
//     and MarkThreadReadUpTo (unread only) never reindex. The map row
//     is added with INSERT ... WHERE NOT EXISTS rather than INSERT OR
//     IGNORE because a trigger statement's conflict clause is overridden
//     by the outer statement's: under Upsert's ON CONFLICT DO UPDATE, OR
//     IGNORE would still fail with a UNIQUE constraint error.
var ftsSchema = `
CREATE TABLE IF NOT EXISTS items_fts_map (
	fts_rowid INTEGER PRIMARY KEY,
	item_id   TEXT NOT NULL UNIQUE
);

CREATE VIRTUAL TABLE IF NOT EXISTS items_fts USING fts5(
	subject, from_name, from_id, recipients, body, attachments, thread_name,
	content = '',
	contentless_delete = 1,
	tokenize = 'unicode61 remove_diacritics 2'
);

CREATE TRIGGER IF NOT EXISTS items_fts_ai AFTER INSERT ON items BEGIN
	INSERT INTO items_fts_map (item_id)
	SELECT new.id WHERE NOT EXISTS (SELECT 1 FROM items_fts_map WHERE item_id = new.id);
	INSERT INTO items_fts (rowid, subject, from_name, from_id, recipients, body, attachments, thread_name)
	SELECT m.fts_rowid, new.subject, new.from_name, new.from_id,
		` + ftsRecipientsExpr("new") + `,
		new.body,
		` + ftsAttachmentsExpr("new") + `,
		new.thread_name
	FROM items_fts_map m WHERE m.item_id = new.id;
END;

CREATE TRIGGER IF NOT EXISTS items_fts_au
AFTER UPDATE OF subject, from_name, from_id, to_json, body, attachments_json, thread_name ON items BEGIN
	DELETE FROM items_fts WHERE rowid = (SELECT fts_rowid FROM items_fts_map WHERE item_id = old.id);
	INSERT INTO items_fts_map (item_id)
	SELECT new.id WHERE NOT EXISTS (SELECT 1 FROM items_fts_map WHERE item_id = new.id);
	INSERT INTO items_fts (rowid, subject, from_name, from_id, recipients, body, attachments, thread_name)
	SELECT m.fts_rowid, new.subject, new.from_name, new.from_id,
		` + ftsRecipientsExpr("new") + `,
		new.body,
		` + ftsAttachmentsExpr("new") + `,
		new.thread_name
	FROM items_fts_map m WHERE m.item_id = new.id;
END;

CREATE TRIGGER IF NOT EXISTS items_fts_ad AFTER DELETE ON items BEGIN
	DELETE FROM items_fts WHERE rowid = (SELECT fts_rowid FROM items_fts_map WHERE item_id = old.id);
	DELETE FROM items_fts_map WHERE item_id = old.id;
END;
`

// ftsRecipientsExpr is the SQL expression, over row alias r (new, or i
// in the backfill), that flattens to_json's addresses and display names
// into one space-separated string for the index. json_valid guards a
// hand-edited or corrupt value from failing the write it is attached to.
func ftsRecipientsExpr(r string) string {
	return `(SELECT coalesce(group_concat(coalesce(json_extract(value, '$.ID'), '') || ' ' || coalesce(json_extract(value, '$.Name'), ''), ' '), '')
		FROM json_each(CASE WHEN json_valid(` + r + `.to_json) THEN ` + r + `.to_json ELSE '[]' END))`
}

// ftsAttachmentsExpr is ftsRecipientsExpr for attachments_json's file
// names.
func ftsAttachmentsExpr(r string) string {
	return `(SELECT coalesce(group_concat(coalesce(json_extract(value, '$.Name'), ''), ' '), '')
		FROM json_each(CASE WHEN json_valid(` + r + `.attachments_json) THEN ` + r + `.attachments_json ELSE '[]' END))`
}

// migrateV3 adds the full-text index (see ftsSchema) and backfills it
// from every existing item. The backfill rebuilds the index from scratch
// rather than appending, and runs in one transaction with the DDL, so
// re-running it (e.g. after a crash before user_version was stamped)
// converges on the same index instead of duplicating entries.
func migrateV3(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin fts migration: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(ftsSchema); err != nil {
		return fmt.Errorf("create fts schema: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO items_fts (items_fts) VALUES ('delete-all')`); err != nil {
		return fmt.Errorf("clear fts index: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM items_fts_map`); err != nil {
		return fmt.Errorf("clear fts map: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO items_fts_map (item_id) SELECT id FROM items`); err != nil {
		return fmt.Errorf("backfill fts map: %w", err)
	}
	if _, err := tx.Exec(`
		INSERT INTO items_fts (rowid, subject, from_name, from_id, recipients, body, attachments, thread_name)
		SELECT m.fts_rowid, i.subject, i.from_name, i.from_id,
			` + ftsRecipientsExpr("i") + `,
			i.body,
			` + ftsAttachmentsExpr("i") + `,
			i.thread_name
		FROM items i JOIN items_fts_map m ON m.item_id = i.id
	`); err != nil {
		return fmt.Errorf("backfill fts index: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit fts migration: %w", err)
	}
	return nil
}

// migrateV4 adds the covering index Conversations aggregates over: with
// timestamp and unread stored in it, the per-conversation newest time and
// unread total come from an index-only scan instead of reading every
// message row of the channel. The partial index serves the items without
// a thread, each a conversation of its own, without scanning the rest.
func migrateV4(db *sql.DB) error {
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_items_conversation ON items(channel, account, thread, timestamp, unread)`); err != nil {
		return fmt.Errorf("create conversation index: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_items_threadless ON items(timestamp) WHERE thread = ''`); err != nil {
		return fmt.Errorf("create threadless index: %w", err)
	}
	return nil
}

// migrate brings db up to the latest schema version this binary knows,
// via PRAGMA user_version: a fresh database (version 0, no tables) runs
// every migration in order; a legacy unversioned database is detected by
// its existing columns (each migration's own idempotency check) and
// stamped with the matching version without its data being rewritten. A
// database whose version is newer than this binary understands refuses
// to open at all, rather than risk operating on a schema it does not
// recognize.
func migrate(db *sql.DB) error {
	current, err := userVersion(db)
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	latest := CurrentSchemaVersion()
	if current > latest {
		return fmt.Errorf("database schema version %d is newer than this binary supports (%d); upgrade bunker", current, latest)
	}

	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		if err := m.apply(db); err != nil {
			return fmt.Errorf("migrate to version %d: %w", m.version, err)
		}
		if err := setUserVersion(db, m.version); err != nil {
			return fmt.Errorf("set schema version %d: %w", m.version, err)
		}
	}
	return nil
}

// userVersion reads the database's PRAGMA user_version: 0 for a brand
// new database or one that predates schema versioning.
func userVersion(db *sql.DB) (int, error) {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

// setUserVersion stamps the database's PRAGMA user_version. SQLite does
// not support binding a parameter inside a PRAGMA statement, so v (only
// ever one of this package's own migration version constants, never
// user input) is formatted directly into the statement.
func setUserVersion(db *sql.DB, v int) error {
	_, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v))
	return err
}

// hasColumn reports whether table already has column, via PRAGMA
// table_info — the only portable way to inspect a SQLite table's
// current shape before deciding whether an ALTER TABLE is needed.
func hasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var cid, notNull, pk int
		var name, colType string
		var dflt any
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// Close releases the underlying database handle.
func (s *Store) Close() error {
	return s.db.Close()
}

// Upsert inserts or replaces item and its labels. It is idempotent: an
// Upsert with the same ID replaces the row instead of duplicating it, and
// its labels are replaced with exactly the ones the item carries.
//
// One exception to "replaces" (#91): for mail, an empty body or empty
// attachment list never overwrites a stored non-empty one. Mail reaches
// the store in copies of different depth (a header-only or truncated
// sync, a flags/keywords refresh, a full read), and without this a later
// shallow copy would blank the body a read had stored and drop it from
// the full-text index. Chat channels keep plain replace semantics: they
// deliver each message whole (edits and revokes go through EditItem and
// RevokeItem), so they never need the merge, and scoping it to mail
// leaves their Upsert behavior exactly as it was. The FTS update trigger
// indexes the merged value, since it reads new.body/new.attachments_json.
func (s *Store) Upsert(ctx context.Context, item core.Item) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: upsert begin: %w", err)
	}
	defer tx.Rollback()

	toJSON, err := json.Marshal(item.To)
	if err != nil {
		return fmt.Errorf("store: marshal to: %w", err)
	}
	attJSON, err := json.Marshal(item.Attachments)
	if err != nil {
		return fmt.Errorf("store: marshal attachments: %w", err)
	}
	meta := item.Meta
	if meta == nil {
		meta = map[string]string{}
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("store: marshal meta: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO items (id, channel, account, thread, thread_name, from_id, from_name,
			to_json, subject, body, attachments_json, unread, from_me, timestamp, meta_json,
			edited, deleted)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			channel=excluded.channel, account=excluded.account, thread=excluded.thread,
			thread_name=excluded.thread_name, from_id=excluded.from_id, from_name=excluded.from_name,
			to_json=excluded.to_json, subject=excluded.subject,
			body=CASE WHEN excluded.channel = ? AND excluded.body = ''
				THEN items.body ELSE excluded.body END,
			attachments_json=CASE WHEN excluded.channel = ? AND excluded.attachments_json IN ('[]', 'null')
				THEN items.attachments_json ELSE excluded.attachments_json END,
			unread=excluded.unread,
			from_me=excluded.from_me, timestamp=excluded.timestamp, meta_json=excluded.meta_json,
			edited=excluded.edited, deleted=excluded.deleted
	`,
		item.ID, string(item.Channel), item.Account, item.Thread, item.ThreadName,
		item.From.ID, item.From.Name, string(toJSON), item.Subject, item.Body,
		string(attJSON), boolToInt(item.Unread), boolToInt(item.FromMe), item.Timestamp.UnixNano(), string(metaJSON),
		boolToInt(item.Edited), boolToInt(item.Deleted),
		string(core.ChannelMail), string(core.ChannelMail),
	)
	if err != nil {
		return fmt.Errorf("store: upsert item: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM labels WHERE item_id = ?`, item.ID); err != nil {
		return fmt.Errorf("store: clear labels: %w", err)
	}
	for _, label := range item.Labels {
		if _, err := tx.ExecContext(ctx, `INSERT INTO labels (item_id, label) VALUES (?, ?)`, item.ID, label); err != nil {
			return fmt.Errorf("store: insert label %q: %w", label, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: upsert commit: %w", err)
	}
	return nil
}

// MarkRead sets item id's Unread flag to !read, preserving every other
// field.
func (s *Store) MarkRead(ctx context.Context, id string, read bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE items SET unread = ? WHERE id = ?`, boolToInt(!read), id)
	if err != nil {
		return fmt.Errorf("store: mark read %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: mark read %s: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("store: mark read %s: %w", id, core.ErrNotFound)
	}
	return nil
}

// MarkThreadReadUpTo marks every unread item of the (channel, account,
// thread) conversation whose timestamp is at or before upTo as read, in
// one UPDATE backed by idx_items_thread. It never touches an item whose
// from_me is set (our own sent messages) and never touches any other
// (channel, account, thread) tuple. No matching rows is not an error:
// the conversation may have nothing unread left, or nothing at all.
func (s *Store) MarkThreadReadUpTo(ctx context.Context, channel core.Channel, account, thread string, upTo time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE items SET unread = 0
		WHERE channel = ? AND account = ? AND thread = ?
			AND from_me = 0 AND unread = 1 AND timestamp <= ?
	`, string(channel), account, thread, upTo.UnixNano())
	if err != nil {
		return fmt.Errorf("store: mark thread read up to %s/%s/%s: %w", channel, account, thread, err)
	}
	return nil
}

// Delete removes item id (and its labels, via ON DELETE CASCADE). It
// reports core.ErrNotFound when id is already gone, mirroring MarkRead.
func (s *Store) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM items WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete %s: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("store: delete %s: %w", id, core.ErrNotFound)
	}
	return nil
}

// EditItem replaces id's stored body and marks it edited, preserving
// every other field (attachments, labels, reactions). It is a no-op
// error (core.ErrNotFound) when id is unknown, mirroring MarkRead/Delete.
func (s *Store) EditItem(ctx context.Context, id, body string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE items SET body = ?, edited = 1 WHERE id = ?`, body, id)
	if err != nil {
		return fmt.Errorf("store: edit %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: edit %s: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("store: edit %s: %w", id, core.ErrNotFound)
	}
	return nil
}

// RevokeItem clears id's stored body and marks it deleted, keeping the
// row (unlike Delete) so the conversation's history and thread
// pagination are undisturbed. It is a no-op error (core.ErrNotFound)
// when id is unknown.
func (s *Store) RevokeItem(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE items SET body = '', deleted = 1 WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: revoke %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: revoke %s: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("store: revoke %s: %w", id, core.ErrNotFound)
	}
	return nil
}

// SetReaction stores reaction.Sender's reaction to item id: an empty
// Emoji removes it (a no-op if none was stored); otherwise it is
// inserted, or replaces that sender's previous reaction to the same
// item via ON CONFLICT. Reacting to an unknown id is a no-op error
// (core.ErrNotFound): the INSERT ... SELECT only runs when id exists
// (the reactions.item_id foreign key would otherwise reject it), so
// RowsAffected distinguishes "unknown item" from "reaction stored".
func (s *Store) SetReaction(ctx context.Context, id string, reaction core.Reaction) error {
	if reaction.Emoji == "" {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM reactions WHERE item_id = ? AND sender = ?`, id, reaction.Sender); err != nil {
			return fmt.Errorf("store: remove reaction on %s: %w", id, err)
		}
		return nil
	}

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO reactions (item_id, sender, emoji)
		SELECT ?, ?, ? WHERE EXISTS (SELECT 1 FROM items WHERE id = ?)
		ON CONFLICT(item_id, sender) DO UPDATE SET emoji = excluded.emoji
	`, id, reaction.Sender, reaction.Emoji, id)
	if err != nil {
		return fmt.Errorf("store: set reaction on %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: set reaction on %s: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("store: set reaction on %s: %w", id, core.ErrNotFound)
	}
	return nil
}

// Cursor returns the stored sync cursor for key, or "" if unset.
func (s *Store) Cursor(ctx context.Context, key string) (string, error) {
	var val string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM cursors WHERE key = ?`, key).Scan(&val)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: cursor %s: %w", key, err)
	}
	return val, nil
}

// SetCursor persists the sync cursor for key.
func (s *Store) SetCursor(ctx context.Context, key, val string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO cursors (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, key, val)
	if err != nil {
		return fmt.Errorf("store: set cursor %s: %w", key, err)
	}
	return nil
}

// Get returns the item for id, or core.ErrNotFound.
func (s *Store) Get(ctx context.Context, id string) (core.Item, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, channel, account, thread, thread_name, from_id, from_name,
			to_json, subject, body, attachments_json, unread, from_me, timestamp, meta_json,
			edited, deleted
		FROM items WHERE id = ?
	`, id)
	item, err := scanItem(row)
	if err == sql.ErrNoRows {
		return core.Item{}, fmt.Errorf("store: get %s: %w", id, core.ErrNotFound)
	}
	if err != nil {
		return core.Item{}, fmt.Errorf("store: get %s: %w", id, err)
	}
	item.Labels, err = s.labelsFor(ctx, id)
	if err != nil {
		return core.Item{}, err
	}
	item.Reactions, err = s.reactionsFor(ctx, id)
	if err != nil {
		return core.Item{}, err
	}
	return item, nil
}

// List returns items matching filter, most recent timestamp first (ties
// broken by id, so the order is total and ListPage can resume it).
func (s *Store) List(ctx context.Context, filter core.Filter) ([]core.Item, error) {
	return s.list(ctx, filter, nil, filter.Limit)
}

// ListPage implements core.PageLister: List, resumed after
// filter.Cursor, plus the cursor of the page that follows. It asks for
// one row more than filter.Limit to learn whether a next page exists
// without a second COUNT query, so the last page never carries a cursor
// that would lead to an empty one.
func (s *Store) ListPage(ctx context.Context, filter core.Filter) (core.Page, error) {
	var after *listPosition
	if filter.Cursor != "" {
		ts, id, err := core.DecodeCursor(filter.Cursor)
		if err != nil {
			return core.Page{}, fmt.Errorf("store: list page: %w", err)
		}
		after = &listPosition{timestamp: ts.UnixNano(), id: id}
	}
	limit := filter.Limit
	if limit > 0 {
		limit++
	}
	items, err := s.list(ctx, filter, after, limit)
	if err != nil {
		return core.Page{}, err
	}
	page := core.Page{Items: items}
	if filter.Limit > 0 && len(items) > filter.Limit {
		page.Items = items[:filter.Limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = core.EncodeCursor(last.Timestamp, last.ID)
	}
	if page.Items == nil {
		page.Items = []core.Item{}
	}
	return page, nil
}

// listPosition is a decoded page cursor: the last item already returned.
type listPosition struct {
	timestamp int64
	id        string
}

func (s *Store) list(ctx context.Context, filter core.Filter, after *listPosition, limit int) ([]core.Item, error) {
	query := `
		SELECT DISTINCT i.id, i.channel, i.account, i.thread, i.thread_name, i.from_id, i.from_name,
			i.to_json, i.subject, i.body, i.attachments_json, i.unread, i.from_me, i.timestamp, i.meta_json,
			i.edited, i.deleted
		FROM items i
	`
	var (
		conds []string
		args  []any
	)
	if filter.Label != "" {
		query += ` JOIN labels l ON l.item_id = i.id`
		conds = append(conds, "l.label = ?")
		args = append(args, filter.Label)
	}
	if filter.Channel != "" {
		conds = append(conds, "i.channel = ?")
		args = append(args, string(filter.Channel))
	}
	if filter.Account != "" {
		conds = append(conds, "i.account = ?")
		args = append(args, filter.Account)
	}
	if filter.Thread != "" {
		conds = append(conds, "i.thread = ?")
		args = append(args, filter.Thread)
	}
	if filter.Unread != nil {
		conds = append(conds, "i.unread = ?")
		args = append(args, boolToInt(*filter.Unread))
	}
	if filter.Query != "" {
		cond, condArgs := textCond(ftsAll, filter.Query, false)
		conds = append(conds, cond)
		args = append(args, condArgs...)
	}
	if filter.Match != nil {
		for _, term := range filter.Match.Terms {
			cond, condArgs, err := termCond(term)
			if err != nil {
				return nil, fmt.Errorf("store: list: %w", err)
			}
			if term.Negate {
				cond = "NOT (" + cond + ")"
			}
			conds = append(conds, cond)
			args = append(args, condArgs...)
		}
	}
	if after != nil {
		conds = append(conds, "(i.timestamp < ? OR (i.timestamp = ? AND i.id < ?))")
		args = append(args, after.timestamp, after.timestamp, after.id)
	}
	if len(conds) > 0 {
		query += " WHERE " + strings.Join(conds, " AND ")
	}
	query += " ORDER BY i.timestamp DESC, i.id DESC"
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list: %w", err)
	}
	defer rows.Close()

	var items []core.Item
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list scan: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list rows: %w", err)
	}

	for i := range items {
		items[i].Labels, err = s.labelsFor(ctx, items[i].ID)
		if err != nil {
			return nil, err
		}
		items[i].Reactions, err = s.reactionsFor(ctx, items[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return items, nil
}

// ftsScope is one text field's slice of the index: the FTS5 column
// filter that scopes a match to it, and the stored columns the literal
// LIKE fallback reads when the text has no token the index could match.
// Both are fixed strings from this file, never user input.
type ftsScope struct {
	columns string // "" for every indexed column
	like    []string
}

var (
	ftsAll     = ftsScope{like: []string{"i.subject", "i.body"}}
	ftsFrom    = ftsScope{columns: "{from_name from_id}", like: []string{"i.from_name", "i.from_id"}}
	ftsTo      = ftsScope{columns: "{recipients}", like: []string{"i.to_json"}}
	ftsSubject = ftsScope{columns: "{subject}", like: []string{"i.subject"}}
)

// ftsMatchSQL selects the ids whose index row matches one bound MATCH
// expression. Filtering by id rather than ordering by rank keeps List's
// contract (and every caller's): newest first.
const ftsMatchSQL = `i.id IN (
	SELECT m.item_id FROM items_fts f
	JOIN items_fts_map m ON m.fts_rowid = f.rowid
	WHERE items_fts MATCH ?)`

// textCond is the condition for text within scope: a full-text match
// when the text has a token to match, else a literal substring match.
// phrase matches text as one exact phrase instead of word by word.
func textCond(scope ftsScope, text string, phrase bool) (string, []any) {
	var match string
	if phrase {
		match = ftsPhraseExpr(text)
	} else {
		match = ftsMatchExpr(text)
	}
	if match != "" {
		if scope.columns != "" {
			match = scope.columns + " : (" + match + ")"
		}
		return ftsMatchSQL, []any{match}
	}
	// A query with no word characters (e.g. "%" or "->") gives the
	// tokenizer nothing to match, so fall back to a literal substring
	// search. SQLite's LIKE has no default escape character, so without
	// ESCAPE a query such as "%" or "_" would act as a wildcard pattern
	// instead of matching literally.
	like := "%" + escapeLike(text) + "%"
	parts := make([]string, len(scope.like))
	args := make([]any, len(scope.like))
	for i, col := range scope.like {
		parts[i] = col + ` LIKE ? ESCAPE '\'`
		args[i] = like
	}
	return "(" + strings.Join(parts, " OR ") + ")", args
}

// termCond is the SQL condition (without its negation) for one term of a
// parsed query. Every value is a bound parameter; only fixed column names
// and fixed FTS5 column filters are written into the statement.
func termCond(term core.QueryTerm) (string, []any, error) {
	switch term.Field {
	case core.QueryText:
		cond, args := textCond(ftsAll, term.Value, term.Phrase)
		return cond, args, nil
	case core.QueryFrom:
		cond, args := textCond(ftsFrom, term.Value, term.Phrase)
		return cond, args, nil
	case core.QueryTo:
		cond, args := textCond(ftsTo, term.Value, term.Phrase)
		return cond, args, nil
	case core.QuerySubject:
		cond, args := textCond(ftsSubject, term.Value, term.Phrase)
		return cond, args, nil
	case core.QueryIs:
		return "i.unread = ?", []any{boolToInt(term.Value == "unread")}, nil
	case core.QueryHas:
		// json_valid guards a hand-edited or corrupt value; "null" (a nil
		// slice marshaled) is not an array, so json_array_length is 0.
		return `coalesce(json_array_length(CASE WHEN json_valid(i.attachments_json) THEN i.attachments_json ELSE '[]' END), 0) > 0`, nil, nil
	case core.QueryIn:
		// coalesce makes an item with no folder (every chat) compare as
		// "", so -in:inbox keeps it instead of NOT(NULL) dropping it.
		// NOCASE lets in:inbox find the canonical "INBOX".
		return `coalesce(json_extract(CASE WHEN json_valid(i.meta_json) THEN i.meta_json ELSE '{}' END, '$.folder'), '') = ? COLLATE NOCASE`,
			[]any{term.Value}, nil
	case core.QueryChannel:
		return "i.channel = ?", []any{term.Value}, nil
	case core.QueryAccount:
		return "i.account = ?", []any{term.Value}, nil
	case core.QueryLabel:
		return "EXISTS (SELECT 1 FROM labels lq WHERE lq.item_id = i.id AND lq.label = ? COLLATE NOCASE)", []any{term.Value}, nil
	case core.QueryBefore:
		return "i.timestamp < ?", []any{term.Time.UnixNano()}, nil
	case core.QueryAfter:
		return "i.timestamp >= ?", []any{term.Time.UnixNano()}, nil
	default:
		// A term the parser never produces (e.g. a hand-built Filter over
		// RPC) is an error, never an ignored condition that would widen
		// the result.
		return "", nil, fmt.Errorf("unknown query field %q: %w", term.Field, core.ErrInvalidQuery)
	}
}

// ftsPhraseExpr is ftsMatchExpr for a quoted phrase: the whole text as
// one FTS5 string (quotes doubled), matched as consecutive tokens with no
// prefix, or "" when it has no token to match.
func ftsPhraseExpr(q string) string {
	if !hasWordChar(q) {
		return ""
	}
	return `"` + strings.ReplaceAll(q, `"`, `""`) + `"`
}

// hasWordChar reports whether s has a letter or digit, i.e. a token the
// unicode61 tokenizer would index.
func hasWordChar(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) })
}

// ftsMatchExpr turns free user text into an FTS5 MATCH expression, or
// "" when the text has no token the index could match. Every
// whitespace-separated word becomes an FTS5 string literal (double quotes
// doubled), so operators such as OR, NEAR, -, * or ( in user input are
// searched for as text and can never change the query's structure. The
// words are ANDed (FTS5's implicit operator) and the last one gets a *
// prefix so results appear while the user is still typing. Inside a
// string, FTS5 runs the same tokenizer as the index, so "ana@example.com"
// becomes the phrase "ana example com" and still matches the address.
func ftsMatchExpr(q string) string {
	var terms []string
	for _, w := range strings.Fields(q) {
		// A word with no letter or digit (e.g. "-" or "%") tokenizes to
		// nothing; FTS5 treats an empty phrase as matching no rows, which
		// would make the whole AND fail, so it is dropped instead.
		if !hasWordChar(w) {
			continue
		}
		terms = append(terms, `"`+strings.ReplaceAll(w, `"`, `""`)+`"`)
	}
	if len(terms) == 0 {
		return ""
	}
	terms[len(terms)-1] += "*"
	return strings.Join(terms, " ")
}

// likeEscaper escapes LIKE's wildcards (and the escape character itself)
// for use with ESCAPE '\'.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// escapeLike makes s match literally inside a LIKE ... ESCAPE '\' pattern.
func escapeLike(s string) string {
	return likeEscaper.Replace(s)
}

// defaultThreadLimit mirrors core.Service's own default: Thread is safe
// to call directly (e.g. from a test) with limit <= 0 without scanning
// the whole conversation.
const defaultThreadLimit = 50

// Thread returns filter.Channel/Account/Thread's items, oldest→newest,
// at most limit items strictly before the before cursor (zero = the
// newest window). It reads through idx_items_thread (channel, account,
// thread, timestamp): the query below asks SQLite for the most recent
// window in descending timestamp order (index range scan, no sort byte
// spilled), then reverses that page in Go to the oldest→newest order
// callers actually want — cheaper than an ORDER BY ASC with an offset-
// free "last N" query, which SQLite cannot serve from this index without
// a full scan.
func (s *Store) Thread(ctx context.Context, filter core.Filter, before time.Time, limit int) ([]core.Item, error) {
	if limit <= 0 {
		limit = defaultThreadLimit
	}

	query := `
		SELECT id, channel, account, thread, thread_name, from_id, from_name,
			to_json, subject, body, attachments_json, unread, from_me, timestamp, meta_json,
			edited, deleted
		FROM items
		WHERE channel = ? AND account = ? AND thread = ?
	`
	args := []any{string(filter.Channel), filter.Account, filter.Thread}
	if !before.IsZero() {
		query += " AND timestamp < ?"
		args = append(args, before.UnixNano())
	}
	query += " ORDER BY timestamp DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: thread: %w", err)
	}
	defer rows.Close()

	var items []core.Item
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, fmt.Errorf("store: thread scan: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: thread rows: %w", err)
	}

	for i := range items {
		items[i].Labels, err = s.labelsFor(ctx, items[i].ID)
		if err != nil {
			return nil, err
		}
		items[i].Reactions, err = s.reactionsFor(ctx, items[i].ID)
		if err != nil {
			return nil, err
		}
	}

	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
	return items, nil
}

// Counts returns the number of unread items per channel and account.
func (s *Store) Counts(ctx context.Context) (map[core.Channel]map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT channel, account, COUNT(*) FROM items WHERE unread = 1 GROUP BY channel, account
	`)
	if err != nil {
		return nil, fmt.Errorf("store: counts: %w", err)
	}
	defer rows.Close()

	out := make(map[core.Channel]map[string]int)
	for rows.Next() {
		var (
			channel string
			account string
			n       int
		)
		if err := rows.Scan(&channel, &account, &n); err != nil {
			return nil, fmt.Errorf("store: counts scan: %w", err)
		}
		ch := core.Channel(channel)
		if out[ch] == nil {
			out[ch] = make(map[string]int)
		}
		out[ch][account] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: counts rows: %w", err)
	}
	return out, nil
}

func (s *Store) labelsFor(ctx context.Context, id string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT label FROM labels WHERE item_id = ? ORDER BY label`, id)
	if err != nil {
		return nil, fmt.Errorf("store: labels for %s: %w", id, err)
	}
	defer rows.Close()

	var labels []string
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			return nil, fmt.Errorf("store: labels scan: %w", err)
		}
		labels = append(labels, label)
	}
	return labels, rows.Err()
}

// reactionsFor returns id's current reactions, ordered by sender for a
// deterministic result (one row per sender: see migrateV2's PRIMARY KEY).
func (s *Store) reactionsFor(ctx context.Context, id string) ([]core.Reaction, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT sender, emoji FROM reactions WHERE item_id = ? ORDER BY sender`, id)
	if err != nil {
		return nil, fmt.Errorf("store: reactions for %s: %w", id, err)
	}
	defer rows.Close()

	var reactions []core.Reaction
	for rows.Next() {
		var r core.Reaction
		if err := rows.Scan(&r.Sender, &r.Emoji); err != nil {
			return nil, fmt.Errorf("store: reactions scan: %w", err)
		}
		reactions = append(reactions, r)
	}
	return reactions, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanItem(row rowScanner) (core.Item, error) {
	var (
		item            core.Item
		channel         string
		unread, fromMe  int
		edited, deleted int
		timestamp       int64
		toJSON, attJSON string
		metaJSON        string
	)
	err := row.Scan(&item.ID, &channel, &item.Account, &item.Thread, &item.ThreadName,
		&item.From.ID, &item.From.Name, &toJSON, &item.Subject, &item.Body,
		&attJSON, &unread, &fromMe, &timestamp, &metaJSON, &edited, &deleted)
	if err != nil {
		return core.Item{}, err
	}
	item.Channel = core.Channel(channel)
	item.Unread = unread != 0
	item.FromMe = fromMe != 0
	item.Edited = edited != 0
	item.Deleted = deleted != 0
	item.Timestamp = time.Unix(0, timestamp).UTC()

	if err := json.Unmarshal([]byte(toJSON), &item.To); err != nil {
		return core.Item{}, fmt.Errorf("unmarshal to_json: %w", err)
	}
	if err := json.Unmarshal([]byte(attJSON), &item.Attachments); err != nil {
		return core.Item{}, fmt.Errorf("unmarshal attachments_json: %w", err)
	}
	if err := json.Unmarshal([]byte(metaJSON), &item.Meta); err != nil {
		return core.Item{}, fmt.Errorf("unmarshal meta_json: %w", err)
	}
	return item, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
