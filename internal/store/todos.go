package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

var _ core.TodoStore = (*Store)(nil)

// migrateV6 adds the todos table (core.Todo). It has no foreign key to
// items: a to-do outlives the message it came from, which may be deleted
// or never synced on another machine. norm_text is the text in
// core.NormalizeTodoText form, so idx_todos_key answers AddTodo's
// duplicate check; idx_todos_status_due serves the open-first,
// soonest-due listing.
func migrateV6(db *sql.DB) error {
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS todos (
			id        TEXT PRIMARY KEY,
			text      TEXT NOT NULL,
			norm_text TEXT NOT NULL,
			direction TEXT NOT NULL,
			status    TEXT NOT NULL DEFAULT 'open',
			due       INTEGER NOT NULL DEFAULT 0,
			item_id   TEXT NOT NULL DEFAULT '',
			channel   TEXT NOT NULL DEFAULT '',
			account   TEXT NOT NULL DEFAULT '',
			thread    TEXT NOT NULL DEFAULT '',
			person    TEXT NOT NULL DEFAULT '',
			created   INTEGER NOT NULL DEFAULT 0,
			done      INTEGER NOT NULL DEFAULT 0
		)
	`); err != nil {
		return fmt.Errorf("create todos table: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_todos_status_due ON todos(status, due)`); err != nil {
		return fmt.Errorf("create todos status index: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_todos_key ON todos(item_id, direction, norm_text)`); err != nil {
		return fmt.Errorf("create todos key index: %w", err)
	}
	return nil
}

const todoColumns = `id, text, direction, status, due, item_id, channel, account, thread, person, created, done`

// AddTodo implements core.TodoStore: the lookup of an existing to-do
// (by id, then by core.TodoKey) and the insert share one transaction, so
// two concurrent retries cannot both insert.
func (s *Store) AddTodo(ctx context.Context, t core.Todo) (core.Todo, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.Todo{}, fmt.Errorf("store: add todo: %w", err)
	}
	defer tx.Rollback()

	norm := core.NormalizeTodoText(t.Text)
	existing, err := scanTodo(tx.QueryRowContext(ctx, `SELECT `+todoColumns+` FROM todos WHERE id = ?`, t.ID))
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return core.Todo{}, fmt.Errorf("store: add todo: %w", err)
	}
	existing, err = scanTodo(tx.QueryRowContext(ctx, `
		SELECT `+todoColumns+` FROM todos
		WHERE item_id = ? AND direction = ? AND norm_text = ? AND (item_id != '' OR status = 'open')
		ORDER BY created, id LIMIT 1
	`, t.ItemID, string(t.Direction), norm))
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return core.Todo{}, fmt.Errorf("store: add todo: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO todos (id, text, norm_text, direction, status, due, item_id, channel, account, thread, person, created, done)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, t.ID, t.Text, norm, string(t.Direction), string(t.Status), unixOrZero(t.Due), t.ItemID,
		string(t.Channel), t.Account, t.Thread, t.Person, unixOrZero(t.Created), unixOrZero(t.Done)); err != nil {
		return core.Todo{}, fmt.Errorf("store: add todo: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return core.Todo{}, fmt.Errorf("store: add todo: %w", err)
	}
	return truncateTodo(t), nil
}

// Todos implements core.TodoStore.
func (s *Store) Todos(ctx context.Context, filter core.TodoFilter) ([]core.Todo, error) {
	var where []string
	var args []any
	if filter.Status != "" {
		where = append(where, "status = ?")
		args = append(args, string(filter.Status))
	}
	if filter.Direction != "" {
		where = append(where, "direction = ?")
		args = append(args, string(filter.Direction))
	}
	if filter.Channel != "" {
		where = append(where, "channel = ?")
		args = append(args, string(filter.Channel))
	}
	if filter.Account != "" {
		where = append(where, "account = ?")
		args = append(args, filter.Account)
	}
	q := `SELECT ` + todoColumns + ` FROM todos`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	q += ` ORDER BY status = 'done', due = 0, due, created, id`
	if filter.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, filter.Limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list todos: %w", err)
	}
	defer rows.Close()
	out := []core.Todo{}
	for rows.Next() {
		t, err := scanTodo(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan todo: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list todos: %w", err)
	}
	return out, nil
}

// SetTodoStatus implements core.TodoStore. The status guard in the
// UPDATE keeps a repeated completion from moving its time.
func (s *Store) SetTodoStatus(ctx context.Context, id string, status core.TodoStatus, at time.Time) (core.Todo, error) {
	var done int64
	if status == core.TodoDone {
		done = unixOrZero(at)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE todos SET status = ?, done = ? WHERE id = ? AND status != ?`,
		string(status), done, id, string(status)); err != nil {
		return core.Todo{}, fmt.Errorf("store: set todo %s: %w", id, err)
	}
	t, err := scanTodo(s.db.QueryRowContext(ctx, `SELECT `+todoColumns+` FROM todos WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return core.Todo{}, fmt.Errorf("store: set todo %s: %w", id, core.ErrTodoNotFound)
	}
	if err != nil {
		return core.Todo{}, fmt.Errorf("store: set todo %s: %w", id, err)
	}
	return t, nil
}

func scanTodo(row interface{ Scan(...any) error }) (core.Todo, error) {
	var t core.Todo
	var direction, status, channel string
	var due, created, done int64
	if err := row.Scan(&t.ID, &t.Text, &direction, &status, &due, &t.ItemID, &channel,
		&t.Account, &t.Thread, &t.Person, &created, &done); err != nil {
		return core.Todo{}, err
	}
	t.Direction = core.TodoDirection(direction)
	t.Status = core.TodoStatus(status)
	t.Channel = core.Channel(channel)
	t.Due, t.Created, t.Done = timeOrZero(due), timeOrZero(created), timeOrZero(done)
	return t, nil
}

// truncateTodo is t as a read would return it: times at the store's
// one-second precision.
func truncateTodo(t core.Todo) core.Todo {
	t.Due, t.Created, t.Done = timeOrZero(unixOrZero(t.Due)), timeOrZero(unixOrZero(t.Created)), timeOrZero(unixOrZero(t.Done))
	return t
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func timeOrZero(sec int64) time.Time {
	if sec == 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}
