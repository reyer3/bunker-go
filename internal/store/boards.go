package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

var _ core.BoardStore = (*Store)(nil)

// migrateV8 añade el esquema de tableros compartidos (core.BoardStore):
// boards, columns, tasks, comments, members e invite_codes. Cada CREATE
// es IF NOT EXISTS y cada índice idem, así que volver a ejecutarla sobre
// una base de datos ya en la versión 8 (o superior) es un no-op, siguiendo
// el patrón de migrateV6 en internal/store/todos.go.
func migrateV8(db *sql.DB) error {
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS boards (
			id            TEXT PRIMARY KEY,
			name          TEXT NOT NULL,
			owner_id      TEXT NOT NULL,
			created_at    INTEGER NOT NULL DEFAULT 0,
			updated_at    INTEGER NOT NULL DEFAULT 0,
			encryption_key BLOB
		)
	`); err != nil {
		return fmt.Errorf("create boards table: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_boards_owner ON boards(owner_id)`); err != nil {
		return fmt.Errorf("create boards owner index: %w", err)
	}
	// Clave simétrica E2EE del tablero. Se añade a bases de datos ya en la
	// versión 8 de forma idempotente (hasColumn) para que un almacén existente
	// también pueda guardar y leer la clave del board.
	if hasEncKey, err := hasColumn(db, "boards", "encryption_key"); err != nil {
		return fmt.Errorf("check boards encryption_key column: %w", err)
	} else if !hasEncKey {
		if _, err := db.Exec(`ALTER TABLE boards ADD COLUMN encryption_key BLOB`); err != nil {
			return fmt.Errorf("add boards encryption_key column: %w", err)
		}
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS columns (
			id        TEXT PRIMARY KEY,
			board_id  TEXT NOT NULL,
			name      TEXT NOT NULL,
			"order"   INTEGER NOT NULL DEFAULT 0,
			color     TEXT NOT NULL DEFAULT '',
			FOREIGN KEY (board_id) REFERENCES boards(id) ON DELETE CASCADE
		)
	`); err != nil {
		return fmt.Errorf("create columns table: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_columns_board ON columns(board_id)`); err != nil {
		return fmt.Errorf("create columns board index: %w", err)
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS tasks (
			id           TEXT PRIMARY KEY,
			board_id     TEXT NOT NULL,
			column_id    TEXT NOT NULL DEFAULT '',
			title        TEXT NOT NULL DEFAULT '',
			description  TEXT NOT NULL DEFAULT '',
			assignee_id  TEXT NOT NULL DEFAULT '',
			due_date     INTEGER NOT NULL DEFAULT 0,
			tags_json    TEXT NOT NULL DEFAULT '[]',
			created_at   INTEGER NOT NULL DEFAULT 0,
			updated_at   INTEGER NOT NULL DEFAULT 0,
			created_by   TEXT NOT NULL DEFAULT '',
			version      INTEGER NOT NULL DEFAULT 0,
			FOREIGN KEY (board_id) REFERENCES boards(id) ON DELETE CASCADE,
			FOREIGN KEY (column_id) REFERENCES columns(id) ON DELETE SET NULL
		)
	`); err != nil {
		return fmt.Errorf("create tasks table: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_tasks_board ON tasks(board_id)`); err != nil {
		return fmt.Errorf("create tasks board index: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_tasks_column ON tasks(column_id)`); err != nil {
		return fmt.Errorf("create tasks column index: %w", err)
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS comments (
			id         TEXT PRIMARY KEY,
			task_id    TEXT NOT NULL,
			member_id  TEXT NOT NULL,
			text       TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT 0,
			created_by TEXT NOT NULL DEFAULT '',
			FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE
		)
	`); err != nil {
		return fmt.Errorf("create comments table: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_comments_task ON comments(task_id)`); err != nil {
		return fmt.Errorf("create comments task index: %w", err)
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS members (
			id        TEXT PRIMARY KEY,
			board_id  TEXT NOT NULL,
			name      TEXT NOT NULL DEFAULT '',
			public_key TEXT NOT NULL DEFAULT '',
			joined_at INTEGER NOT NULL DEFAULT 0,
			role      TEXT NOT NULL DEFAULT 'member',
			FOREIGN KEY (board_id) REFERENCES boards(id) ON DELETE CASCADE
		)
	`); err != nil {
		return fmt.Errorf("create members table: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_members_board ON members(board_id)`); err != nil {
		return fmt.Errorf("create members board index: %w", err)
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS invite_codes (
			id         TEXT PRIMARY KEY,
			board_id   TEXT NOT NULL,
			code       TEXT NOT NULL UNIQUE,
			created_at INTEGER NOT NULL DEFAULT 0,
			expires_at INTEGER NOT NULL DEFAULT 0,
			max_uses   INTEGER NOT NULL DEFAULT 0,
			uses       INTEGER NOT NULL DEFAULT 0,
			created_by TEXT NOT NULL DEFAULT '',
			revoked    INTEGER NOT NULL DEFAULT 0,
			public_key BLOB,
			FOREIGN KEY (board_id) REFERENCES boards(id) ON DELETE CASCADE
		)
	`); err != nil {
		return fmt.Errorf("create invite_codes table: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_invite_codes_board ON invite_codes(board_id)`); err != nil {
		return fmt.Errorf("create invite_codes board index: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_invite_codes_code ON invite_codes(code)`); err != nil {
		return fmt.Errorf("create invite_codes code index: %w", err)
	}
	// Clave pública del tablero compartida a través del invite code.
	if hasPubKey, err := hasColumn(db, "invite_codes", "public_key"); err != nil {
		return fmt.Errorf("check invite_codes public_key column: %w", err)
	} else if !hasPubKey {
		if _, err := db.Exec(`ALTER TABLE invite_codes ADD COLUMN public_key BLOB`); err != nil {
			return fmt.Errorf("add invite_codes public_key column: %w", err)
		}
	}
	return nil
}

const (
	boardColumns      = `id, name, owner_id, created_at, updated_at, encryption_key`
	columnColumns     = `id, board_id, name, "order", color`
	taskColumns       = `id, board_id, column_id, title, description, assignee_id, due_date, tags_json, created_at, updated_at, created_by, version`
	commentColumns    = `id, task_id, member_id, text, created_at, created_by`
	memberColumns     = `id, board_id, name, public_key, joined_at, role`
	inviteCodeColumns = `id, board_id, code, created_at, expires_at, max_uses, uses, created_by, revoked, public_key`
)

// CreateBoard implements core.BoardStore.
func (s *Store) CreateBoard(b *core.Board) error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: create board begin: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO boards (id, name, owner_id, created_at, updated_at, encryption_key)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING
	`)
	if err != nil {
		return fmt.Errorf("store: create board prepare: %w", err)
	}
	defer stmt.Close()

	res, err := stmt.ExecContext(ctx, b.ID, b.Name, b.OwnerID, b.CreatedAt.UnixNano(), b.UpdatedAt.UnixNano(), b.EncryptionKey)
	if err != nil {
		return fmt.Errorf("store: create board %s: %w", b.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: create board %s: %w", b.ID, err)
	}
	if n == 0 {
		return fmt.Errorf("store: create board %s: %w", b.ID, core.ErrBoardExists)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: create board commit: %w", err)
	}
	return nil
}

// GetBoard implements core.BoardStore.
func (s *Store) GetBoard(id string) (*core.Board, error) {
	ctx := context.Background()
	row := s.db.QueryRowContext(ctx, `SELECT `+boardColumns+` FROM boards WHERE id = ?`, id)
	b, err := scanBoard(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("store: get board %s: %w", id, core.ErrBoardNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: get board %s: %w", id, err)
	}
	return b, nil
}

// ListBoards implements core.BoardStore.
func (s *Store) ListBoards() ([]*core.Board, error) {
	ctx := context.Background()
	rows, err := s.db.QueryContext(ctx, `SELECT `+boardColumns+` FROM boards ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: list boards: %w", err)
	}
	defer rows.Close()

	boards := []*core.Board{}
	for rows.Next() {
		b, err := scanBoard(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list boards scan: %w", err)
		}
		boards = append(boards, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list boards rows: %w", err)
	}
	return boards, nil
}

// CreateColumn implements core.BoardStore.
func (s *Store) CreateColumn(c *core.Column) error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: create column begin: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO columns (id, board_id, name, "order", color)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING
	`)
	if err != nil {
		return fmt.Errorf("store: create column prepare: %w", err)
	}
	defer stmt.Close()

	res, err := stmt.ExecContext(ctx, c.ID, c.Board, c.Name, c.Order, c.Color)
	if err != nil {
		return fmt.Errorf("store: create column %s: %w", c.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: create column %s: %w", c.ID, err)
	}
	if n == 0 {
		return fmt.Errorf("store: create column %s: %w", c.ID, core.ErrColumnExists)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: create column commit: %w", err)
	}
	return nil
}

// GetColumn implements core.BoardStore.
func (s *Store) GetColumn(id string) (*core.Column, error) {
	ctx := context.Background()
	row := s.db.QueryRowContext(ctx, `SELECT `+columnColumns+` FROM columns WHERE id = ?`, id)
	c, err := scanColumn(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("store: get column %s: %w", id, core.ErrColumnNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: get column %s: %w", id, err)
	}
	return c, nil
}

// ListColumns implements core.BoardStore.
func (s *Store) ListColumns(boardID string) ([]*core.Column, error) {
	ctx := context.Background()
	rows, err := s.db.QueryContext(ctx, `SELECT `+columnColumns+` FROM columns WHERE board_id = ? ORDER BY "order", id`, boardID)
	if err != nil {
		return nil, fmt.Errorf("store: list columns %s: %w", boardID, err)
	}
	defer rows.Close()

	columns := []*core.Column{}
	for rows.Next() {
		c, err := scanColumn(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list columns %s scan: %w", boardID, err)
		}
		columns = append(columns, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list columns %s rows: %w", boardID, err)
	}
	return columns, nil
}

// CreateTask implements core.BoardStore.
func (s *Store) CreateTask(t *core.Task) error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: create task begin: %w", err)
	}
	defer tx.Rollback()

	tagsJSON, err := json.Marshal(t.Tags)
	if err != nil {
		return fmt.Errorf("store: marshal tags: %w", err)
	}
	dueDate := int64(0)
	if t.DueDate != nil {
		dueDate = t.DueDate.UnixNano()
	}

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO tasks (id, board_id, column_id, title, description, assignee_id, due_date, tags_json, created_at, updated_at, created_by, version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING
	`)
	if err != nil {
		return fmt.Errorf("store: create task prepare: %w", err)
	}
	defer stmt.Close()

	res, err := stmt.ExecContext(ctx, t.ID, t.BoardID, t.ColumnID, t.Title, t.Description, t.AssigneeID,
		dueDate, string(tagsJSON), t.CreatedAt.UnixNano(), t.UpdatedAt.UnixNano(), t.CreatedBy, t.Version)
	if err != nil {
		return fmt.Errorf("store: create task %s: %w", t.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: create task %s: %w", t.ID, err)
	}
	if n == 0 {
		return fmt.Errorf("store: create task %s: %w", t.ID, core.ErrTaskExists)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: create task commit: %w", err)
	}
	return nil
}

// GetTask implements core.BoardStore.
func (s *Store) GetTask(id string) (*core.Task, error) {
	ctx := context.Background()
	row := s.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, id)
	t, err := scanTask(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("store: get task %s: %w", id, core.ErrTaskNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: get task %s: %w", id, err)
	}
	return t, nil
}

// ListTasks implements core.BoardStore.
func (s *Store) ListTasks(boardID string) ([]*core.Task, error) {
	ctx := context.Background()
	rows, err := s.db.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE board_id = ? ORDER BY created_at, id`, boardID)
	if err != nil {
		return nil, fmt.Errorf("store: list tasks %s: %w", boardID, err)
	}
	defer rows.Close()

	tasks := []*core.Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list tasks %s scan: %w", boardID, err)
		}
		tasks = append(tasks, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list tasks %s rows: %w", boardID, err)
	}
	return tasks, nil
}

// UpdateTask implements core.BoardStore.
func (s *Store) UpdateTask(t *core.Task) error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: update task begin: %w", err)
	}
	defer tx.Rollback()

	tagsJSON, err := json.Marshal(t.Tags)
	if err != nil {
		return fmt.Errorf("store: marshal tags: %w", err)
	}
	dueDate := int64(0)
	if t.DueDate != nil {
		dueDate = t.DueDate.UnixNano()
	}

	stmt, err := tx.PrepareContext(ctx, `
		UPDATE tasks SET board_id = ?, column_id = ?, title = ?, description = ?, assignee_id = ?, due_date = ?,
			tags_json = ?, updated_at = ?, created_by = ?, version = ? WHERE id = ?
	`)
	if err != nil {
		return fmt.Errorf("store: update task prepare: %w", err)
	}
	defer stmt.Close()

	res, err := stmt.ExecContext(ctx, t.BoardID, t.ColumnID, t.Title, t.Description, t.AssigneeID,
		dueDate, string(tagsJSON), t.UpdatedAt.UnixNano(), t.CreatedBy, t.Version, t.ID)
	if err != nil {
		return fmt.Errorf("store: update task %s: %w", t.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update task %s: %w", t.ID, err)
	}
	if n == 0 {
		return fmt.Errorf("store: update task %s: %w", t.ID, core.ErrTaskNotFound)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: update task commit: %w", err)
	}
	return nil
}

// CreateComment implements core.BoardStore.
func (s *Store) CreateComment(c *core.Comment) error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: create comment begin: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO comments (id, task_id, member_id, text, created_at, created_by)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING
	`)
	if err != nil {
		return fmt.Errorf("store: create comment prepare: %w", err)
	}
	defer stmt.Close()

	res, err := stmt.ExecContext(ctx, c.ID, c.TaskID, c.MemberID, c.Text, c.CreatedAt.UnixNano(), c.CreatedBy)
	if err != nil {
		return fmt.Errorf("store: create comment %s: %w", c.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: create comment %s: %w", c.ID, err)
	}
	if n == 0 {
		return fmt.Errorf("store: create comment %s: %w", c.ID, core.ErrCommentExists)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: create comment commit: %w", err)
	}
	return nil
}

// ListComments implements core.BoardStore.
func (s *Store) ListComments(taskID string) ([]*core.Comment, error) {
	ctx := context.Background()
	rows, err := s.db.QueryContext(ctx, `SELECT `+commentColumns+` FROM comments WHERE task_id = ? ORDER BY created_at, id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("store: list comments %s: %w", taskID, err)
	}
	defer rows.Close()

	comments := []*core.Comment{}
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list comments %s scan: %w", taskID, err)
		}
		comments = append(comments, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list comments %s rows: %w", taskID, err)
	}
	return comments, nil
}

// CreateMember implements core.BoardStore.
func (s *Store) CreateMember(m *core.Member) error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: create member begin: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO members (id, board_id, name, public_key, joined_at, role)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING
	`)
	if err != nil {
		return fmt.Errorf("store: create member prepare: %w", err)
	}
	defer stmt.Close()

	res, err := stmt.ExecContext(ctx, m.ID, m.BoardID, m.Name, m.PublicKey, m.JoinedAt.UnixNano(), m.Role)
	if err != nil {
		return fmt.Errorf("store: create member %s: %w", m.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: create member %s: %w", m.ID, err)
	}
	if n == 0 {
		return fmt.Errorf("store: create member %s: %w", m.ID, core.ErrMemberExists)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: create member commit: %w", err)
	}
	return nil
}

// GetMember implements core.BoardStore.
func (s *Store) GetMember(boardID, memberID string) (*core.Member, error) {
	ctx := context.Background()
	row := s.db.QueryRowContext(ctx, `SELECT `+memberColumns+` FROM members WHERE board_id = ? AND id = ?`, boardID, memberID)
	m, err := scanMember(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("store: get member %s/%s: %w", boardID, memberID, core.ErrMemberNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: get member %s/%s: %w", boardID, memberID, err)
	}
	return m, nil
}

// ListMembers implements core.BoardStore.
func (s *Store) ListMembers(boardID string) ([]*core.Member, error) {
	ctx := context.Background()
	rows, err := s.db.QueryContext(ctx, `SELECT `+memberColumns+` FROM members WHERE board_id = ? ORDER BY id`, boardID)
	if err != nil {
		return nil, fmt.Errorf("store: list members %s: %w", boardID, err)
	}
	defer rows.Close()

	members := []*core.Member{}
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list members %s scan: %w", boardID, err)
		}
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list members %s rows: %w", boardID, err)
	}
	return members, nil
}

// CreateInviteCode implements core.BoardStore.
func (s *Store) CreateInviteCode(invite *core.InviteCode) error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: create invite code begin: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO invite_codes (id, board_id, code, created_at, expires_at, max_uses, uses, created_by, revoked, public_key)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING
	`)
	if err != nil {
		return fmt.Errorf("store: create invite code prepare: %w", err)
	}
	defer stmt.Close()

	res, err := stmt.ExecContext(ctx, invite.ID, invite.BoardID, invite.Code, invite.CreatedAt.UnixNano(),
		invite.ExpiresAt.UnixNano(), invite.MaxUses, invite.Uses, invite.CreatedBy, boolToInt(invite.Revoked), invite.PublicKey)
	if err != nil {
		return fmt.Errorf("store: create invite code %s: %w", invite.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: create invite code %s: %w", invite.ID, err)
	}
	if n == 0 {
		return fmt.Errorf("store: create invite code %s: %w", invite.ID, core.ErrInviteExists)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: create invite code commit: %w", err)
	}
	return nil
}

// GetInviteCode implements core.BoardStore.
func (s *Store) GetInviteCode(code string) (*core.InviteCode, error) {
	ctx := context.Background()
	row := s.db.QueryRowContext(ctx, `SELECT `+inviteCodeColumns+` FROM invite_codes WHERE code = ?`, code)
	i, err := scanInviteCode(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("store: get invite code %s: %w", code, core.ErrInviteNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: get invite code %s: %w", code, err)
	}
	return i, nil
}

// UpdateInviteCode implements core.BoardStore.
func (s *Store) UpdateInviteCode(invite *core.InviteCode) error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: update invite code begin: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		UPDATE invite_codes SET board_id = ?, code = ?, created_at = ?, expires_at = ?, max_uses = ?, uses = ?,
			created_by = ?, revoked = ?, public_key = ? WHERE id = ?
	`)
	if err != nil {
		return fmt.Errorf("store: update invite code prepare: %w", err)
	}
	defer stmt.Close()

	res, err := stmt.ExecContext(ctx, invite.BoardID, invite.Code, invite.CreatedAt.UnixNano(), invite.ExpiresAt.UnixNano(),
		invite.MaxUses, invite.Uses, invite.CreatedBy, boolToInt(invite.Revoked), invite.PublicKey, invite.ID)
	if err != nil {
		return fmt.Errorf("store: update invite code %s: %w", invite.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update invite code %s: %w", invite.ID, err)
	}
	if n == 0 {
		return fmt.Errorf("store: update invite code %s: %w", invite.ID, core.ErrInviteNotFound)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: update invite code commit: %w", err)
	}
	return nil
}

// scanBoard lee un core.Board de un rowScanner, siguiendo el patrón de scanItem.
func scanBoard(row rowScanner) (*core.Board, error) {
	var b core.Board
	var createdAt, updatedAt int64
	var encryptionKey []byte
	if err := row.Scan(&b.ID, &b.Name, &b.OwnerID, &createdAt, &updatedAt, &encryptionKey); err != nil {
		return nil, err
	}
	b.CreatedAt = time.Unix(0, createdAt).UTC()
	b.UpdatedAt = time.Unix(0, updatedAt).UTC()
	b.EncryptionKey = encryptionKey
	return &b, nil
}

// scanColumn lee un core.Column de un rowScanner.
func scanColumn(row rowScanner) (*core.Column, error) {
	var c core.Column
	var order int
	if err := row.Scan(&c.ID, &c.Board, &c.Name, &order, &c.Color); err != nil {
		return nil, err
	}
	c.Order = order
	return &c, nil
}

// scanTask lee un core.Task de un rowScanner.
func scanTask(row rowScanner) (*core.Task, error) {
	var t core.Task
	var dueDate, createdAt, updatedAt int64
	var tagsJSON string
	if err := row.Scan(&t.ID, &t.BoardID, &t.ColumnID, &t.Title, &t.Description, &t.AssigneeID,
		&dueDate, &tagsJSON, &createdAt, &updatedAt, &t.CreatedBy, &t.Version); err != nil {
		return nil, err
	}
	if dueDate != 0 {
		d := time.Unix(0, dueDate).UTC()
		t.DueDate = &d
	}
	t.CreatedAt = time.Unix(0, createdAt).UTC()
	t.UpdatedAt = time.Unix(0, updatedAt).UTC()
	if err := json.Unmarshal([]byte(tagsJSON), &t.Tags); err != nil {
		return nil, fmt.Errorf("unmarshal tags_json: %w", err)
	}
	if t.Tags == nil {
		t.Tags = []string{}
	}
	return &t, nil
}

// scanComment lee un core.Comment de un rowScanner.
func scanComment(row rowScanner) (*core.Comment, error) {
	var c core.Comment
	var createdAt int64
	if err := row.Scan(&c.ID, &c.TaskID, &c.MemberID, &c.Text, &createdAt, &c.CreatedBy); err != nil {
		return nil, err
	}
	c.CreatedAt = time.Unix(0, createdAt).UTC()
	return &c, nil
}

// scanMember lee un core.Member de un rowScanner.
func scanMember(row rowScanner) (*core.Member, error) {
	var m core.Member
	var joinedAt int64
	if err := row.Scan(&m.ID, &m.BoardID, &m.Name, &m.PublicKey, &joinedAt, &m.Role); err != nil {
		return nil, err
	}
	m.JoinedAt = time.Unix(0, joinedAt).UTC()
	return &m, nil
}

// scanInviteCode lee un core.InviteCode de un rowScanner.
func scanInviteCode(row rowScanner) (*core.InviteCode, error) {
	var i core.InviteCode
	var createdAt, expiresAt int64
	var revoked int
	var publicKey []byte
	if err := row.Scan(&i.ID, &i.BoardID, &i.Code, &createdAt, &expiresAt, &i.MaxUses, &i.Uses, &i.CreatedBy, &revoked, &publicKey); err != nil {
		return nil, err
	}
	i.CreatedAt = time.Unix(0, createdAt).UTC()
	i.ExpiresAt = time.Unix(0, expiresAt).UTC()
	i.Revoked = revoked != 0
	i.PublicKey = publicKey
	return &i, nil
}
