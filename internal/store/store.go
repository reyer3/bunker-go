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

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	secfile.SecureSidecars(path, "-wal", "-shm")
	return &Store{db: db}, nil
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
			to_json, subject, body, attachments_json, unread, timestamp, meta_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			channel=excluded.channel, account=excluded.account, thread=excluded.thread,
			thread_name=excluded.thread_name, from_id=excluded.from_id, from_name=excluded.from_name,
			to_json=excluded.to_json, subject=excluded.subject, body=excluded.body,
			attachments_json=excluded.attachments_json, unread=excluded.unread,
			timestamp=excluded.timestamp, meta_json=excluded.meta_json
	`,
		item.ID, string(item.Channel), item.Account, item.Thread, item.ThreadName,
		item.From.ID, item.From.Name, string(toJSON), item.Subject, item.Body,
		string(attJSON), boolToInt(item.Unread), item.Timestamp.UnixNano(), string(metaJSON),
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
			to_json, subject, body, attachments_json, unread, timestamp, meta_json
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
	return item, nil
}

// List returns items matching filter, most recent timestamp first.
func (s *Store) List(ctx context.Context, filter core.Filter) ([]core.Item, error) {
	query := `
		SELECT DISTINCT i.id, i.channel, i.account, i.thread, i.thread_name, i.from_id, i.from_name,
			i.to_json, i.subject, i.body, i.attachments_json, i.unread, i.timestamp, i.meta_json
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
	if filter.Unread != nil {
		conds = append(conds, "i.unread = ?")
		args = append(args, boolToInt(*filter.Unread))
	}
	if filter.Query != "" {
		conds = append(conds, "(i.subject LIKE ? OR i.body LIKE ?)")
		like := "%" + filter.Query + "%"
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

type rowScanner interface {
	Scan(dest ...any) error
}

func scanItem(row rowScanner) (core.Item, error) {
	var (
		item            core.Item
		channel         string
		unread          int
		timestamp       int64
		toJSON, attJSON string
		metaJSON        string
	)
	err := row.Scan(&item.ID, &channel, &item.Account, &item.Thread, &item.ThreadName,
		&item.From.ID, &item.From.Name, &toJSON, &item.Subject, &item.Body,
		&attJSON, &unread, &timestamp, &metaJSON)
	if err != nil {
		return core.Item{}, err
	}
	item.Channel = core.Channel(channel)
	item.Unread = unread != 0
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
