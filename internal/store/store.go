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

	_ "modernc.org/sqlite"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/secfile"
)

// Store is a SQLite-backed core.Store.
type Store struct {
	db *sql.DB
}

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
			to_json=excluded.to_json, subject=excluded.subject, body=excluded.body,
			attachments_json=excluded.attachments_json, unread=excluded.unread,
			from_me=excluded.from_me, timestamp=excluded.timestamp, meta_json=excluded.meta_json,
			edited=excluded.edited, deleted=excluded.deleted
	`,
		item.ID, string(item.Channel), item.Account, item.Thread, item.ThreadName,
		item.From.ID, item.From.Name, string(toJSON), item.Subject, item.Body,
		string(attJSON), boolToInt(item.Unread), boolToInt(item.FromMe), item.Timestamp.UnixNano(), string(metaJSON),
		boolToInt(item.Edited), boolToInt(item.Deleted),
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

// List returns items matching filter, most recent timestamp first.
func (s *Store) List(ctx context.Context, filter core.Filter) ([]core.Item, error) {
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
		// SQLite's LIKE has no default escape character, so without
		// ESCAPE a user query such as "50%" or "a_b" would act as a
		// wildcard pattern instead of matching literally.
		conds = append(conds, `(i.subject LIKE ? ESCAPE '\' OR i.body LIKE ? ESCAPE '\')`)
		like := "%" + escapeLike(filter.Query) + "%"
		args = append(args, like, like)
	}
	if len(conds) > 0 {
		query += " WHERE " + strings.Join(conds, " AND ")
	}
	query += " ORDER BY i.timestamp DESC"
	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
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
