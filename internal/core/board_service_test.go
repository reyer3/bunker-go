package core_test

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// memBoardStore is an in-memory BoardStore for tests.
type memBoardStore struct {
	boards   map[string]*core.Board
	columns  map[string]*core.Column
	tasks    map[string]*core.Task
	comments map[string]*core.Comment
	members  map[string]*core.Member
	invites  map[string]*core.InviteCode
}

func newMemBoardStore() *memBoardStore {
	return &memBoardStore{
		boards:   make(map[string]*core.Board),
		columns:  make(map[string]*core.Column),
		tasks:    make(map[string]*core.Task),
		comments: make(map[string]*core.Comment),
		members:  make(map[string]*core.Member),
		invites:  make(map[string]*core.InviteCode),
	}
}

func (s *memBoardStore) CreateBoard(b *core.Board) error {
	if s.boards[b.ID] != nil {
		return core.ErrBoardExists
	}
	s.boards[b.ID] = b
	return nil
}

func (s *memBoardStore) GetBoard(id string) (*core.Board, error) {
	b, ok := s.boards[id]
	if !ok {
		return nil, core.ErrBoardNotFound
	}
	return b, nil
}

func (s *memBoardStore) ListBoards() ([]*core.Board, error) {
	out := make([]*core.Board, 0, len(s.boards))
	for _, b := range s.boards {
		out = append(out, b)
	}
	return out, nil
}

func (s *memBoardStore) CreateColumn(c *core.Column) error {
	if s.columns[c.ID] != nil {
		return core.ErrColumnExists
	}
	s.columns[c.ID] = c
	return nil
}

func (s *memBoardStore) GetColumn(id string) (*core.Column, error) {
	c, ok := s.columns[id]
	if !ok {
		return nil, core.ErrColumnNotFound
	}
	return c, nil
}

func (s *memBoardStore) ListColumns(boardID string) ([]*core.Column, error) {
	out := make([]*core.Column, 0)
	for _, c := range s.columns {
		if c.Board == boardID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *memBoardStore) CreateTask(t *core.Task) error {
	if s.tasks[t.ID] != nil {
		return core.ErrTaskExists
	}
	s.tasks[t.ID] = t
	return nil
}

func (s *memBoardStore) GetTask(id string) (*core.Task, error) {
	t, ok := s.tasks[id]
	if !ok {
		return nil, core.ErrTaskNotFound
	}
	return t, nil
}

func (s *memBoardStore) ListTasks(boardID string) ([]*core.Task, error) {
	out := make([]*core.Task, 0)
	for _, t := range s.tasks {
		if t.BoardID == boardID {
			out = append(out, t)
		}
	}
	return out, nil
}

func (s *memBoardStore) UpdateTask(t *core.Task) error {
	if s.tasks[t.ID] == nil {
		return core.ErrTaskNotFound
	}
	s.tasks[t.ID] = t
	return nil
}

func (s *memBoardStore) CreateComment(c *core.Comment) error {
	if s.comments[c.ID] != nil {
		return core.ErrCommentExists
	}
	s.comments[c.ID] = c
	return nil
}

func (s *memBoardStore) ListComments(taskID string) ([]*core.Comment, error) {
	out := make([]*core.Comment, 0)
	for _, c := range s.comments {
		if c.TaskID == taskID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *memBoardStore) CreateMember(m *core.Member) error {
	if s.members[m.ID] != nil {
		return core.ErrMemberExists
	}
	s.members[m.ID] = m
	return nil
}

func (s *memBoardStore) GetMember(boardID, memberID string) (*core.Member, error) {
	m, ok := s.members[memberID]
	if !ok || m.BoardID != boardID {
		return nil, core.ErrMemberNotFound
	}
	return m, nil
}

func (s *memBoardStore) ListMembers(boardID string) ([]*core.Member, error) {
	out := make([]*core.Member, 0)
	for _, m := range s.members {
		if m.BoardID == boardID {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *memBoardStore) CreateInviteCode(invite *core.InviteCode) error {
	if s.invites[invite.Code] != nil {
		return core.ErrInviteExists
	}
	s.invites[invite.Code] = invite
	return nil
}

func (s *memBoardStore) GetInviteCode(code string) (*core.InviteCode, error) {
	invite, ok := s.invites[code]
	if !ok {
		return nil, core.ErrInviteNotFound
	}
	return invite, nil
}

func (s *memBoardStore) ListInviteCodes(boardID string) ([]*core.InviteCode, error) {
	out := make([]*core.InviteCode, 0)
	for _, inv := range s.invites {
		if inv.BoardID == boardID {
			out = append(out, inv)
		}
	}
	return out, nil
}

func (s *memBoardStore) UpdateInviteCode(invite *core.InviteCode) error {
	if s.invites[invite.Code] == nil {
		return core.ErrInviteNotFound
	}
	s.invites[invite.Code] = invite
	return nil
}

var _ core.BoardStore = (*memBoardStore)(nil)

// TestBoardServiceCreateBoard is RED: core.BoardService and core.BoardStore
// do not exist yet.
func TestBoardServiceCreateBoard(t *testing.T) {
	store := newMemBoardStore()
	svc := core.NewBoardService(store)

	board, err := svc.CreateBoard(context.Background(), "Tareas compartidas", "member-carla")
	if err != nil {
		t.Fatalf("CreateBoard returned error: %v", err)
	}
	if board.ID == "" {
		t.Error("board.ID is empty, want a generated ID")
	}
	if board.Name != "Tareas compartidas" {
		t.Errorf("board.Name = %q, want %q", board.Name, "Tareas compartidas")
	}
	if board.OwnerID != "member-carla" {
		t.Errorf("board.OwnerID = %q, want %q", board.OwnerID, "member-carla")
	}
	if board.CreatedAt.IsZero() {
		t.Error("board.CreatedAt is zero, want a non-zero creation time")
	}
}

// TestBoardServiceGenerateInviteCode is RED.
func TestBoardServiceGenerateInviteCode(t *testing.T) {
	store := newMemBoardStore()
	svc := core.NewBoardService(store)

	board, err := svc.CreateBoard(context.Background(), "Tareas compartidas", "member-carla")
	if err != nil {
		t.Fatalf("CreateBoard returned error: %v", err)
	}

	invite, err := svc.GenerateInviteCode(context.Background(), board.ID, "member-carla", 0, 0)
	if err != nil {
		t.Fatalf("GenerateInviteCode returned error: %v", err)
	}
	if invite.Code == "" {
		t.Error("invite.Code is empty, want a generated code")
	}
	if !strings.HasPrefix(invite.Code, "bunker-invite-") {
		t.Errorf("invite.Code = %q, want prefix %q", invite.Code, "bunker-invite-")
	}
	if invite.BoardID != board.ID {
		t.Errorf("invite.BoardID = %q, want %q", invite.BoardID, board.ID)
	}
	if invite.CreatedBy != "member-carla" {
		t.Errorf("invite.CreatedBy = %q, want %q", invite.CreatedBy, "member-carla")
	}
	if invite.ExpiresAt.IsZero() {
		t.Error("invite.ExpiresAt is zero, want a non-zero expiration")
	}
}

// TestBoardServiceGenerateInviteCodeWithTTL is RED.
func TestBoardServiceGenerateInviteCodeWithTTL(t *testing.T) {
	store := newMemBoardStore()
	svc := core.NewBoardService(store)

	board, _ := svc.CreateBoard(context.Background(), "Tareas compartidas", "member-carla")

	ttl := 24 * time.Hour
	invite, err := svc.GenerateInviteCode(context.Background(), board.ID, "member-carla", ttl, 0)
	if err != nil {
		t.Fatalf("GenerateInviteCode returned error: %v", err)
	}
	expected := time.Now().Add(ttl)
	if !invite.ExpiresAt.Before(expected.Add(time.Second)) || !invite.ExpiresAt.After(expected.Add(-time.Second)) {
		t.Errorf("invite.ExpiresAt = %v, want approximately %v", invite.ExpiresAt, expected)
	}
}

// TestBoardServiceGenerateInviteCodeWithMaxUses is RED.
func TestBoardServiceGenerateInviteCodeWithMaxUses(t *testing.T) {
	store := newMemBoardStore()
	svc := core.NewBoardService(store)

	board, _ := svc.CreateBoard(context.Background(), "Tareas compartidas", "member-carla")

	invite, err := svc.GenerateInviteCode(context.Background(), board.ID, "member-carla", 0, 3)
	if err != nil {
		t.Fatalf("GenerateInviteCode returned error: %v", err)
	}
	if invite.MaxUses != 3 {
		t.Errorf("invite.MaxUses = %d, want 3", invite.MaxUses)
	}
}

// TestBoardServiceJoinBoard is RED.
func TestBoardServiceJoinBoard(t *testing.T) {
	store := newMemBoardStore()
	svc := core.NewBoardService(store)

	board, _ := svc.CreateBoard(context.Background(), "Tareas compartidas", "member-carla")
	invite, _ := svc.GenerateInviteCode(context.Background(), board.ID, "member-carla", 0, 0)

	member, err := svc.JoinBoard(context.Background(), invite.Code, "member-ana", "Ana Pérez", "base64publickeyana")
	if err != nil {
		t.Fatalf("JoinBoard returned error: %v", err)
	}
	if member.ID != "member-ana" {
		t.Errorf("member.ID = %q, want %q", member.ID, "member-ana")
	}
	if member.Name != "Ana Pérez" {
		t.Errorf("member.Name = %q, want %q", member.Name, "Ana Pérez")
	}
	if member.BoardID != board.ID {
		t.Errorf("member.BoardID = %q, want %q", member.BoardID, board.ID)
	}
	if member.PublicKey != "base64publickeyana" {
		t.Errorf("member.PublicKey = %q, want %q", member.PublicKey, "base64publickeyana")
	}
	if member.Role != "member" {
		t.Errorf("member.Role = %q, want %q", member.Role, "member")
	}
}

// TestBoardServiceJoinBoardExpiredInvite is RED.
func TestBoardServiceJoinBoardExpiredInvite(t *testing.T) {
	store := newMemBoardStore()
	svc := core.NewBoardService(store)

	board, _ := svc.CreateBoard(context.Background(), "Tareas compartidas", "member-carla")
	invite, _ := svc.GenerateInviteCode(context.Background(), board.ID, "member-carla", 0, 0)
	// Hacer que el código esté expirado
	invite.ExpiresAt = time.Unix(0, 0)
	if err := store.UpdateInviteCode(invite); err != nil {
		t.Fatalf("UpdateInviteCode returned error: %v", err)
	}

	_, err := svc.JoinBoard(context.Background(), invite.Code, "member-ana", "Ana Pérez", "base64publickeyana")
	if !errors.Is(err, core.ErrInviteInvalid) {
		t.Errorf("err = %v, want ErrInviteInvalid", err)
	}
}

// TestBoardServiceJoinBoardRevokedInvite is RED.
func TestBoardServiceJoinBoardRevokedInvite(t *testing.T) {
	store := newMemBoardStore()
	svc := core.NewBoardService(store)

	board, _ := svc.CreateBoard(context.Background(), "Tareas compartidas", "member-carla")
	invite, _ := svc.GenerateInviteCode(context.Background(), board.ID, "member-carla", 0, 0)
	invite.Revoked = true
	if err := store.UpdateInviteCode(invite); err != nil {
		t.Fatalf("UpdateInviteCode returned error: %v", err)
	}

	_, err := svc.JoinBoard(context.Background(), invite.Code, "member-ana", "Ana Pérez", "base64publickeyana")
	if !errors.Is(err, core.ErrInviteInvalid) {
		t.Errorf("err = %v, want ErrInviteInvalid", err)
	}
}

// TestBoardServiceJoinBoardUsesExceeded is RED.
func TestBoardServiceJoinBoardUsesExceeded(t *testing.T) {
	store := newMemBoardStore()
	svc := core.NewBoardService(store)

	board, _ := svc.CreateBoard(context.Background(), "Tareas compartidas", "member-carla")
	invite, _ := svc.GenerateInviteCode(context.Background(), board.ID, "member-carla", 0, 1)
	invite.Uses = 1 // ya se usó una vez
	if err := store.UpdateInviteCode(invite); err != nil {
		t.Fatalf("UpdateInviteCode returned error: %v", err)
	}

	_, err := svc.JoinBoard(context.Background(), invite.Code, "member-ana", "Ana Pérez", "base64publickeyana")
	if !errors.Is(err, core.ErrInviteInvalid) {
		t.Errorf("err = %v, want ErrInviteInvalid", err)
	}
}

// TestBoardServiceJoinBoardUnknownCode is RED.
func TestBoardServiceJoinBoardUnknownCode(t *testing.T) {
	store := newMemBoardStore()
	svc := core.NewBoardService(store)

	_, err := svc.JoinBoard(context.Background(), "bunker-invite-nonexistent", "member-ana", "Ana Pérez", "base64publickeyana")
	if !errors.Is(err, core.ErrInviteNotFound) {
		t.Errorf("err = %v, want ErrInviteNotFound", err)
	}
}

// TestBoardServiceListMembers is RED.
func TestBoardServiceListMembers(t *testing.T) {
	store := newMemBoardStore()
	svc := core.NewBoardService(store)

	board, _ := svc.CreateBoard(context.Background(), "Tareas compartidas", "member-carla")
	invite, _ := svc.GenerateInviteCode(context.Background(), board.ID, "member-carla", 0, 0)

	_, err := svc.JoinBoard(context.Background(), invite.Code, "member-ana", "Ana Pérez", "base64publickeyana")
	if err != nil {
		t.Fatalf("JoinBoard returned error: %v", err)
	}
	_, err = svc.JoinBoard(context.Background(), invite.Code, "member-beto", "Beto Gómez", "base64publickeybeto")
	if err != nil {
		t.Fatalf("JoinBoard returned error: %v", err)
	}

	members, err := svc.ListMembers(context.Background(), board.ID)
	if err != nil {
		t.Fatalf("ListMembers returned error: %v", err)
	}
	if len(members) != 2 {
		t.Errorf("len(members) = %d, want 2 (the two members who joined via invite)", len(members))
	}
}

// TestBoardServiceCreateBoardGeneratesEncryptionKey verifica que crear un
// board genera una clave simétrica AES-256 aleatoria y la guarda en el board.
func TestBoardServiceCreateBoardGeneratesEncryptionKey(t *testing.T) {
	store := newMemBoardStore()
	svc := core.NewBoardService(store)

	board, err := svc.CreateBoard(context.Background(), "Tareas compartidas", "member-carla")
	if err != nil {
		t.Fatalf("CreateBoard returned error: %v", err)
	}
	if board.EncryptionKey == nil {
		t.Fatal("board.EncryptionKey is nil, want a 32-byte symmetric key")
	}
	if len(board.EncryptionKey) != 32 {
		t.Errorf("len(board.EncryptionKey) = %d, want 32 (AES-256)", len(board.EncryptionKey))
	}
}

// TestBoardServiceGenerateInviteCodeIncludesPublicKey verifica que el código
// de invitación incluye la clave pública (simétrica) del board para que un
// nuevo miembro pueda configurar el cifrado E2EE.
func TestBoardServiceGenerateInviteCodeIncludesPublicKey(t *testing.T) {
	store := newMemBoardStore()
	svc := core.NewBoardService(store)

	board, _ := svc.CreateBoard(context.Background(), "Tareas compartidas", "member-carla")
	if board.EncryptionKey == nil {
		t.Fatal("board.EncryptionKey is nil")
	}

	invite, err := svc.GenerateInviteCode(context.Background(), board.ID, "member-carla", 0, 0)
	if err != nil {
		t.Fatalf("GenerateInviteCode returned error: %v", err)
	}
	if invite.PublicKey == nil {
		t.Fatal("invite.PublicKey is nil, want the board's public key")
	}
	if string(invite.PublicKey) != string(board.EncryptionKey) {
		t.Errorf("invite.PublicKey = %x, want board.EncryptionKey %x", invite.PublicKey, board.EncryptionKey)
	}
}

// TestBoardServiceJoinBoardReceivesPublicKey verifica que al unirse con un
// código de invitación, el miembro recibe la clave pública del board (el
// código de invitación ya la trae embebida).
func TestBoardServiceJoinBoardReceivesPublicKey(t *testing.T) {
	store := newMemBoardStore()
	svc := core.NewBoardService(store)

	board, _ := svc.CreateBoard(context.Background(), "Tareas compartidas", "member-carla")
	invite, _ := svc.GenerateInviteCode(context.Background(), board.ID, "member-carla", 0, 0)
	if invite.PublicKey == nil {
		t.Fatal("invite.PublicKey is nil")
	}

	// El miembro que se une usa la clave pública del invite code (base64).
	memberPublicKey := base64.RawURLEncoding.EncodeToString(invite.PublicKey)
	member, err := svc.JoinBoard(context.Background(), invite.Code, "member-ana", "Ana Pérez", memberPublicKey)
	if err != nil {
		t.Fatalf("JoinBoard returned error: %v", err)
	}
	if member.PublicKey != memberPublicKey {
		t.Errorf("member.PublicKey = %q, want %q (la clave pública del invite code)", member.PublicKey, memberPublicKey)
	}
}
