package core_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// TestBoardCreateAndList is RED: core.Board, core.Column, core.Task,
// core.Member and core.InviteCode do not exist yet, so this file does not
// compile. It defines the expected API for the shared board domain model.
func TestBoardCreateAndList(t *testing.T) {
	board := core.Board{
		ID:   "board-1",
		Name: "Tareas compartidas",
	}
	if board.ID != "board-1" {
		t.Errorf("board.ID = %q, want %q", board.ID, "board-1")
	}
	if board.Name != "Tareas compartidas" {
		t.Errorf("board.Name = %q, want %q", board.Name, "Tareas compartidas")
	}
}

func TestColumnCreate(t *testing.T) {
	col := core.Column{
		ID:    "col-1",
		Board: "board-1",
		Name:  "Por hacer",
		Order: 0,
		Color: "#4db0ff",
	}
	if col.ID != "col-1" || col.Board != "board-1" || col.Name != "Por hacer" || col.Order != 0 || col.Color != "#4db0ff" {
		t.Errorf("col = %+v, want {ID:col-1 Board:board-1 Name:Por hacer Order:0 Color:#4db0ff}", col)
	}
}

func TestTaskCreate(t *testing.T) {
	task := core.Task{
		ID:          "task-1",
		BoardID:     "board-1",
		ColumnID:    "col-1",
		Title:       "Enviar informe",
		Description: "Enviar el informe de fin de mes a todos",
		AssigneeID:  "member-ana",
		Tags:        []string{"trabajo", "urgente"},
		CreatedBy:   "member-carla",
	}
	if task.ID != "task-1" || task.Title != "Enviar informe" || task.AssigneeID != "member-ana" || task.CreatedBy != "member-carla" {
		t.Errorf("task = %+v, unexpected fields", task)
	}
	if len(task.Tags) != 2 || task.Tags[0] != "trabajo" || task.Tags[1] != "urgente" {
		t.Errorf("task.Tags = %v, want [trabajo urgente]", task.Tags)
	}
}

func TestCommentCreate(t *testing.T) {
	comment := core.Comment{
		ID:        "comment-1",
		TaskID:    "task-1",
		MemberID:  "member-ana",
		Text:      "Listo, lo envié ayer",
		CreatedBy: "member-ana",
	}
	if comment.ID != "comment-1" || comment.TaskID != "task-1" || comment.MemberID != "member-ana" || comment.Text != "Listo, lo envié ayer" || comment.CreatedBy != "member-ana" {
		t.Errorf("comment = %+v, unexpected fields", comment)
	}
}

func TestMemberCreate(t *testing.T) {
	member := core.Member{
		ID:        "member-ana",
		BoardID:   "board-1",
		Name:      "Ana Pérez",
		PublicKey: "base64publickeyana",
		Role:      "member",
	}
	if member.ID != "member-ana" || member.BoardID != "board-1" || member.Name != "Ana Pérez" || member.PublicKey != "base64publickeyana" || member.Role != "member" {
		t.Errorf("member = %+v, unexpected fields", member)
	}
}

func TestInviteCodeCreate(t *testing.T) {
	invite := core.InviteCode{
		ID:        "invite-1",
		BoardID:   "board-1",
		Code:      "bunker-invite-abc123",
		CreatedBy: "member-carla",
	}
	if invite.ID != "invite-1" || invite.BoardID != "board-1" || invite.Code != "bunker-invite-abc123" || invite.CreatedBy != "member-carla" {
		t.Errorf("invite = %+v, unexpected fields", invite)
	}
}

func TestInviteCodeValidFormat(t *testing.T) {
	// El código de invitación debe tener un formato predecible para que
	// sea fácil de escribir manualmente.
	invite := core.InviteCode{Code: "bunker-invite-abc123"}
	if !strings.HasPrefix(invite.Code, "bunker-invite-") {
		t.Errorf("invite.Code = %q, want prefix %q", invite.Code, "bunker-invite-")
	}
}

func TestInviteCodeExpired(t *testing.T) {
	// Un código con ExpiresAt en el pasado debe considerarse expirado.
	invite := core.InviteCode{
		Code:      "bunker-invite-expired",
		ExpiresAt: time.Unix(0, 0), // hace mucho tiempo
		MaxUses:   1,
		Uses:      0,
		Revoked:   false,
	}
	if !invite.IsExpired() {
		t.Error("invite.IsExpired() = false, want true for a code expired in the past")
	}
}

func TestInviteCodeNotExpired(t *testing.T) {
	invite := core.InviteCode{
		Code:      "bunker-invite-valid",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		MaxUses:   1,
		Uses:      0,
		Revoked:   false,
	}
	if invite.IsExpired() {
		t.Error("invite.IsExpired() = true, want false for a valid code")
	}
}

func TestInviteCodeUsesExceeded(t *testing.T) {
	invite := core.InviteCode{
		Code:    "bunker-invite-max",
		MaxUses: 1,
		Uses:    1,
		Revoked: false,
	}
	if !invite.UsesExceeded() {
		t.Error("invite.UsesExceeded() = false, want true when uses >= maxUses")
	}
}

func TestInviteCodeRevoked(t *testing.T) {
	invite := core.InviteCode{
		Code:    "bunker-invite-revoked",
		Revoked: true,
	}
	if !invite.IsRevoked() {
		t.Error("invite.IsRevoked() = false, want true for a revoked code")
	}
}

func TestErrInviteNotFound(t *testing.T) {
	if !errors.Is(core.ErrInviteNotFound, core.ErrInviteNotFound) {
		t.Error("core.ErrInviteNotFound should wrap itself")
	}
}

func TestErrBoardNotFound(t *testing.T) {
	if !errors.Is(core.ErrBoardNotFound, core.ErrBoardNotFound) {
		t.Error("core.ErrBoardNotFound should wrap itself")
	}
}

func TestErrMemberNotFound(t *testing.T) {
	if !errors.Is(core.ErrMemberNotFound, core.ErrMemberNotFound) {
		t.Error("core.ErrMemberNotFound should wrap itself")
	}
}
