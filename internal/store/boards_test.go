package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/store"
)

// TestBoardStoreImplementsInterface afirma que *store.Store implementa el
// puerto core.BoardStore. Mientras internal/store/boards.go no exista, esta
// línea no compila: *store.Store no provee CreateBoard, GetBoard, etc.
func TestBoardStoreImplementsInterface(t *testing.T) {
	var _ core.BoardStore = (*store.Store)(nil)
}

func TestBoardsRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	b := &core.Board{ID: "board-1", Name: "Tareas compartidas", OwnerID: "owner-1"}
	if err := s.CreateBoard(b); err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}

	got, err := s.GetBoard("board-1")
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	if got.ID != b.ID || got.Name != b.Name || got.OwnerID != b.OwnerID {
		t.Errorf("GetBoard round trip = %+v, want %+v", got, b)
	}

	boards, err := s.ListBoards()
	if err != nil {
		t.Fatalf("ListBoards: %v", err)
	}
	if len(boards) != 1 || boards[0].ID != b.ID {
		t.Errorf("ListBoards = %+v, want 1 board %s", boards, b.ID)
	}

	// El mismo ID ya existe.
	if err := s.CreateBoard(b); !errors.Is(err, core.ErrBoardExists) {
		t.Errorf("duplicate CreateBoard err = %v, want core.ErrBoardExists", err)
	}
	// ID desconocido.
	if _, err := s.GetBoard("no-existe"); !errors.Is(err, core.ErrBoardNotFound) {
		t.Errorf("GetBoard missing err = %v, want core.ErrBoardNotFound", err)
	}

	_ = ctx
}

func TestBoardsMigrationV8(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bunker.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	const wantVersion = 8
	if got := rawUserVersion(t, path); got != wantVersion {
		t.Fatalf("user_version = %d, want %d (migrateV8 debe ejecutarse sobre una DB nueva)", got, wantVersion)
	}

	wantTables := []string{"boards", "columns", "tasks", "comments", "members", "invite_codes"}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer raw.Close()
	rows, err := raw.Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}
	defer rows.Close()
	have := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read tables: %v", err)
	}
	for _, want := range wantTables {
		if !have[want] {
			t.Errorf("falta la tabla %s", want)
		}
	}
}

func TestBoardsMigrationIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bunker.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.CreateBoard(&core.Board{ID: "board-1", Name: "Tablero", OwnerID: "owner-1"}); err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	before := snapshotBoards(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	if got := rawUserVersion(t, path); got != store.CurrentSchemaVersion() {
		t.Fatalf("user_version = %d, want %d", got, store.CurrentSchemaVersion())
	}

	// Reabrir con la versión ya al día: migrateV8 (y las demás) son no-op.
	s2 := openAtVersion(t, path, store.CurrentSchemaVersion())
	defer s2.Close()
	after := snapshotBoards(t, s2)
	if after != before {
		t.Fatalf("reabrir cambió los boards:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func snapshotBoards(t *testing.T, s *store.Store) string {
	t.Helper()
	ctx := context.Background()
	boards, err := s.ListBoards()
	if err != nil {
		t.Fatalf("ListBoards: %v", err)
	}
	sort.Slice(boards, func(i, j int) bool { return boards[i].ID < boards[j].ID })
	var lines []string
	for _, b := range boards {
		lines = append(lines, fmt.Sprintf("%s|%s|%s", b.ID, b.Name, b.OwnerID))
	}
	_ = ctx
	return strings.Join(lines, "\n")
}

func TestBoardColumnsRoundTrip(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateBoard(&core.Board{ID: "board-1", Name: "Tablero", OwnerID: "owner-1"}); err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}

	cols := []*core.Column{
		{ID: "col-1", Board: "board-1", Name: "Por hacer", Order: 1, Color: "#ffffff"},
		{ID: "col-2", Board: "board-1", Name: "Hecho", Order: 2, Color: "#00ff00"},
	}
	for _, c := range cols {
		if err := s.CreateColumn(c); err != nil {
			t.Fatalf("CreateColumn %s: %v", c.ID, err)
		}
	}
	if err := s.CreateColumn(cols[0]); !errors.Is(err, core.ErrColumnExists) {
		t.Errorf("duplicate CreateColumn err = %v, want core.ErrColumnExists", err)
	}

	got, err := s.GetColumn("col-1")
	if err != nil {
		t.Fatalf("GetColumn: %v", err)
	}
	if got.ID != "col-1" || got.Name != "Por hacer" || got.Board != "board-1" {
		t.Errorf("GetColumn = %+v", got)
	}

	list, err := s.ListColumns("board-1")
	if err != nil {
		t.Fatalf("ListColumns: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListColumns = %d, want 2", len(list))
	}
	ids := map[string]bool{}
	for _, c := range list {
		ids[c.ID] = true
	}
	if !ids["col-1"] || !ids["col-2"] {
		t.Errorf("ListColumns ids = %v, want col-1 and col-2", ids)
	}
	if _, err := s.GetColumn("no-existe"); !errors.Is(err, core.ErrColumnNotFound) {
		t.Errorf("GetColumn missing err = %v, want core.ErrColumnNotFound", err)
	}
}

func TestBoardTasksRoundTrip(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateBoard(&core.Board{ID: "board-1", Name: "Tablero", OwnerID: "owner-1"}); err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	if err := s.CreateColumn(&core.Column{ID: "col-1", Board: "board-1", Name: "Por hacer", Order: 1}); err != nil {
		t.Fatalf("CreateColumn: %v", err)
	}

	task := &core.Task{
		ID:          "task-1",
		BoardID:     "board-1",
		ColumnID:    "col-1",
		Title:       "Reparar la fuga",
		Description: "Bajo el fregadero",
		AssigneeID:  "member-1",
		Tags:        []string{"urgente"},
		CreatedBy:   "member-1",
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	got, err := s.GetTask("task-1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.ID != task.ID || got.Title != task.Title || got.ColumnID != task.ColumnID {
		t.Errorf("GetTask = %+v, want %+v", got, task)
	}

	list, err := s.ListTasks("board-1")
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(list) != 1 || list[0].ID != "task-1" {
		t.Errorf("ListTasks = %+v, want 1 task task-1", list)
	}

	// Actualizar.
	got.Title = "Reparar la fuga (urgente)"
	if err := s.UpdateTask(got); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	got2, err := s.GetTask("task-1")
	if err != nil {
		t.Fatalf("GetTask after update: %v", err)
	}
	if got2.Title != got.Title {
		t.Errorf("UpdateTask not reflected: %+v", got2)
	}
	if _, err := s.GetTask("no-existe"); !errors.Is(err, core.ErrTaskNotFound) {
		t.Errorf("GetTask missing err = %v, want core.ErrTaskNotFound", err)
	}
}

func TestBoardMembersRoundTrip(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateBoard(&core.Board{ID: "board-1", Name: "Tablero", OwnerID: "owner-1"}); err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}

	m := &core.Member{ID: "member-1", BoardID: "board-1", Name: "Ana", PublicKey: "pk-ana", JoinedAt: time.Now(), Role: "owner"}
	if err := s.CreateMember(m); err != nil {
		t.Fatalf("CreateMember: %v", err)
	}
	if err := s.CreateMember(m); !errors.Is(err, core.ErrMemberExists) {
		t.Errorf("duplicate CreateMember err = %v, want core.ErrMemberExists", err)
	}

	got, err := s.GetMember("board-1", "member-1")
	if err != nil {
		t.Fatalf("GetMember: %v", err)
	}
	if got.ID != "member-1" || got.Name != "Ana" || got.Role != "owner" {
		t.Errorf("GetMember = %+v", got)
	}

	list, err := s.ListMembers("board-1")
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if len(list) != 1 || list[0].ID != "member-1" {
		t.Errorf("ListMembers = %+v, want 1 member member-1", list)
	}
	if _, err := s.GetMember("board-1", "no-existe"); !errors.Is(err, core.ErrMemberNotFound) {
		t.Errorf("GetMember missing err = %v, want core.ErrMemberNotFound", err)
	}
}

func TestBoardInviteCodes(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateBoard(&core.Board{ID: "board-1", Name: "Tablero", OwnerID: "owner-1"}); err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}

	invite := &core.InviteCode{
		ID:        "invite-1",
		BoardID:   "board-1",
		Code:      "bunker-invite-abc123",
		MaxUses:   2,
		Uses:      0,
		CreatedBy: "owner-1",
	}
	if err := s.CreateInviteCode(invite); err != nil {
		t.Fatalf("CreateInviteCode: %v", err)
	}
	if err := s.CreateInviteCode(invite); !errors.Is(err, core.ErrInviteExists) {
		t.Errorf("duplicate CreateInviteCode err = %v, want core.ErrInviteExists", err)
	}

	got, err := s.GetInviteCode("bunker-invite-abc123")
	if err != nil {
		t.Fatalf("GetInviteCode: %v", err)
	}
	if got.Code != "bunker-invite-abc123" || got.ID != "invite-1" || got.Uses != 0 || got.Revoked {
		t.Errorf("GetInviteCode = %+v", got)
	}

	// Usar: incrementar Uses.
	got.Uses++
	if err := s.UpdateInviteCode(got); err != nil {
		t.Fatalf("UpdateInviteCode: %v", err)
	}
	got2, err := s.GetInviteCode("bunker-invite-abc123")
	if err != nil {
		t.Fatalf("GetInviteCode after use: %v", err)
	}
	if got2.Uses != 1 {
		t.Errorf("UpdateInviteCode uses = %d, want 1", got2.Uses)
	}

	// Revocar.
	got2.Revoked = true
	if err := s.UpdateInviteCode(got2); err != nil {
		t.Fatalf("UpdateInviteCode revoke: %v", err)
	}
	got3, err := s.GetInviteCode("bunker-invite-abc123")
	if err != nil {
		t.Fatalf("GetInviteCode after revoke: %v", err)
	}
	if !got3.Revoked {
		t.Errorf("UpdateInviteCode did not revoke: %+v", got3)
	}
	if _, err := s.GetInviteCode("no-existe"); !errors.Is(err, core.ErrInviteNotFound) {
		t.Errorf("GetInviteCode missing err = %v, want core.ErrInviteNotFound", err)
	}
}

func TestBoardStoreEncryptionKeyRoundTrip(t *testing.T) {
	s := openTestStore(t)
	// Clave simétrica AES-256 (32 bytes).
	key := []byte("01234567890123456789012345678901")
	b := &core.Board{ID: "board-1", Name: "Tablero", OwnerID: "owner-1", EncryptionKey: key}
	if err := s.CreateBoard(b); err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	got, err := s.GetBoard("board-1")
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	if got.EncryptionKey == nil {
		t.Fatal("GetBoard.EncryptionKey is nil, want the stored key")
	}
	if string(got.EncryptionKey) != string(key) {
		t.Errorf("GetBoard EncryptionKey = %x, want %x", got.EncryptionKey, key)
	}
	// Un board creado sin clave la lee como nil.
	if err := s.CreateBoard(&core.Board{ID: "board-2", Name: "Sin clave", OwnerID: "owner-1"}); err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	got2, err := s.GetBoard("board-2")
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	if got2.EncryptionKey != nil {
		t.Errorf("GetBoard EncryptionKey = %x, want nil for a board without encryption key", got2.EncryptionKey)
	}
}

func TestInviteCodePublicKeyRoundTrip(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateBoard(&core.Board{ID: "board-1", Name: "Tablero", OwnerID: "owner-1"}); err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	publicKey := []byte("clave-publica-del-tablero-para-el-invite-code")
	invite := &core.InviteCode{
		ID:        "invite-1",
		BoardID:   "board-1",
		Code:      "bunker-invite-abc123",
		PublicKey: publicKey,
		CreatedBy: "owner-1",
	}
	if err := s.CreateInviteCode(invite); err != nil {
		t.Fatalf("CreateInviteCode: %v", err)
	}
	got, err := s.GetInviteCode("bunker-invite-abc123")
	if err != nil {
		t.Fatalf("GetInviteCode: %v", err)
	}
	if got.PublicKey == nil {
		t.Fatal("GetInviteCode.PublicKey is nil, want the stored public key")
	}
	if string(got.PublicKey) != string(publicKey) {
		t.Errorf("GetInviteCode PublicKey = %x, want %x", got.PublicKey, publicKey)
	}
	// Actualizar la clave pública y volver a leer.
	got.PublicKey = append([]byte(nil), publicKey...)
	got.PublicKey[0] ^= 0xff
	if err := s.UpdateInviteCode(got); err != nil {
		t.Fatalf("UpdateInviteCode: %v", err)
	}
	got2, err := s.GetInviteCode("bunker-invite-abc123")
	if err != nil {
		t.Fatalf("GetInviteCode after update: %v", err)
	}
	if string(got2.PublicKey) != string(got.PublicKey) {
		t.Errorf("GetInviteCode PublicKey after update = %x, want %x", got2.PublicKey, got.PublicKey)
	}
}

// tableHasColumn reports whether table has column, mirroring store.hasColumn.
func tableHasColumn(t *testing.T, raw *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := raw.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, colType string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &colType, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan pragma_table_info: %v", err)
		}
		if name == column {
			return true
		}
	}
	return false
}

func TestBoardsMigrationV8HasEncryptionColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bunker.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer raw.Close()
	for _, tc := range []struct{ table, column string }{
		{"boards", "encryption_key"},
		{"invite_codes", "public_key"},
	} {
		if !tableHasColumn(t, raw, tc.table, tc.column) {
			t.Errorf("la tabla %s no tiene la columna %s tras migrateV8", tc.table, tc.column)
		}
	}
	// user_version debe seguir siendo 8 (no agregamos una nueva versión de migración).
	if got := rawUserVersion(t, path); got != store.CurrentSchemaVersion() {
		t.Fatalf("user_version = %d, want %d", got, store.CurrentSchemaVersion())
	}
}

// ListInviteCodes devuelve solo las invitaciones del board pedido: el claim
// remoto las recorre para encontrar la que corresponde a la prueba.
func TestListInviteCodesByBoard(t *testing.T) {
	s := openTestStore(t)
	for _, id := range []string{"board-1", "board-2"} {
		if err := s.CreateBoard(&core.Board{ID: id, Name: id, OwnerID: "owner-1"}); err != nil {
			t.Fatal(err)
		}
	}
	for i, b := range []string{"board-1", "board-1", "board-2"} {
		inv := &core.InviteCode{ID: fmt.Sprintf("invite-%d", i), BoardID: b, Code: fmt.Sprintf("code-%d", i)}
		if err := s.CreateInviteCode(inv); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListInviteCodes("board-1")
	if err != nil {
		t.Fatalf("ListInviteCodes: %v", err)
	}
	if len(got) != 2 || got[0].BoardID != "board-1" || got[1].BoardID != "board-1" {
		t.Fatalf("ListInviteCodes = %+v", got)
	}
	if empty, err := s.ListInviteCodes("board-x"); err != nil || len(empty) != 0 {
		t.Fatalf("board sin invitaciones = %v, %v", empty, err)
	}
}
