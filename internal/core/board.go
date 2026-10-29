// Package core defines the channel-agnostic domain model and ports for
// bunker-go. It must never import adapters or third-party network
// libraries: adapters depend on core, never the other way around.
package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/reyer3/bunker-go/internal/board"
)

// Board is a shared task board (kanban) that can be synchronized between
// two or more devices belonging to different users. It is independent of
// any user's personal mail, WhatsApp, Matrix or to-do accounts.
type Board struct {
	ID            string    `json:"id"`                       // UUID del tablero compartido
	Name          string    `json:"name"`                     // nombre del tablero (ej. "Tareas compartidas")
	OwnerID       string    `json:"owner_id"`                 // ID del dispositivo que creó el tablero
	EncryptionKey []byte    `json:"encryption_key,omitempty"` // clave simétrica AES-256 del tablero (nil si no tiene)
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Column is one column (lane) of a kanban board.
type Column struct {
	ID    string `json:"id"`
	Board string `json:"board_id"`
	Name  string `json:"name"` // ej. "Por hacer", "En progreso", "Hecho"
	Order int    `json:"order"`
	Color string `json:"color"` // color opcional para la UI
}

// Task is one task in a shared board.
type Task struct {
	ID          string     `json:"id"`
	BoardID     string     `json:"board_id"`
	ColumnID    string     `json:"column_id"` // columna en la que está la tarea
	Title       string     `json:"title"`
	Description string     `json:"description"`
	AssigneeID  string     `json:"assignee_id,omitempty"` // ID del miembro asignado (opcional)
	DueDate     *time.Time `json:"due_date,omitempty"`
	Tags        []string   `json:"tags,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	CreatedBy   string     `json:"created_by"` // ID del miembro que creó la tarea
	Version     int        `json:"version"`    // para detección de conflictos (optimistic concurrency)
}

// Comment is a comment on a task.
type Comment struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	MemberID  string    `json:"member_id"` // quién lo escribió
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
	CreatedBy string    `json:"created_by"` // ID del miembro que escribió el comentario
}

// Member is a member of a shared board.
type Member struct {
	ID        string    `json:"id"` // ID del dispositivo/usuario
	BoardID   string    `json:"board_id"`
	Name      string    `json:"name"`       // nombre para mostrar
	PublicKey string    `json:"public_key"` // clave pública para cifrado E2EE
	JoinedAt  time.Time `json:"joined_at"`
	Role      string    `json:"role"` // "owner" o "member"
}

// InviteCode is a one-time or multi-use code to join a shared board.
type InviteCode struct {
	ID        string    `json:"id"`
	BoardID   string    `json:"board_id"`
	Code      string    `json:"code"` // código corto legible por humanos (ej. bunker-invite-abc123)
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	MaxUses   int       `json:"max_uses"`   // 0 = ilimitado, >0 = número máximo de usos
	Uses      int       `json:"uses"`       // cuántas veces se ha usado
	CreatedBy string    `json:"created_by"` // ID del miembro que generó el código
	Revoked   bool      `json:"revoked"`
	PublicKey []byte    `json:"public_key,omitempty"` // clave del tablero para compartir con los miembros a través del invite code
}

// IsExpired reports whether the invite code has passed its expiration time.
// A zero ExpiresAt means "never expires".
func (i *InviteCode) IsExpired() bool {
	if i.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().After(i.ExpiresAt)
}

// UsesExceeded reports whether the invite code has been used as many times
// as allowed. A zero MaxUses means unlimited.
func (i *InviteCode) UsesExceeded() bool {
	if i.MaxUses <= 0 {
		return false
	}
	return i.Uses >= i.MaxUses
}

// IsRevoked reports whether the invite code has been explicitly revoked.
func (i *InviteCode) IsRevoked() bool {
	return i.Revoked
}

// Valid reports whether the invite code can be used to join a board:
// not expired, not revoked, and uses not exceeded.
func (i *InviteCode) Valid() bool {
	return !i.IsExpired() && !i.IsRevoked() && !i.UsesExceeded()
}

// Errors specific to the shared board domain.

// ErrInviteNotFound is returned when an invite code does not exist.
var ErrInviteNotFound = errors.New("core: invite code not found")

// ErrBoardNotFound is returned when a board id does not exist.
var ErrBoardNotFound = errors.New("core: board not found")

// ErrMemberNotFound is returned when a member id does not exist on a board.
var ErrMemberNotFound = errors.New("core: member not found on board")

// ErrInviteInvalid is returned when an invite code cannot be used
// (expired, revoked, or uses exceeded).
var ErrInviteInvalid = errors.New("core: invite code is invalid or has been used too many times")

// ErrBoardExists is returned when trying to create a board with an ID that
// already exists.
var ErrBoardExists = errors.New("core: board already exists")

// ErrColumnExists is returned when trying to create a column with an ID that
// already exists.
var ErrColumnExists = errors.New("core: column already exists")

// ErrColumnNotFound is returned when a column id does not exist.
var ErrColumnNotFound = errors.New("core: column not found")

// ErrTaskExists is returned when trying to create a task with an ID that
// already exists.
var ErrTaskExists = errors.New("core: task already exists")

// ErrTaskNotFound is returned when a task id does not exist.
var ErrTaskNotFound = errors.New("core: task not found")

// ErrCommentExists is returned when trying to create a comment with an ID that
// already exists.
var ErrCommentExists = errors.New("core: comment already exists")

// ErrMemberExists is returned when trying to create a member with an ID that
// already exists.
var ErrMemberExists = errors.New("core: member already exists")

// ErrInviteExists is returned when trying to create an invite code that
// already exists.
var ErrInviteExists = errors.New("core: invite code already exists")

// ErrTaskConflict is returned when a task's stored version does not match
// the expected one (optimistic concurrency).
var ErrTaskConflict = errors.New("core: task version conflict")

// BoardEvent es un cambio en un tablero compartido que se notifica a los
// demás miembros a través del relay. Sus campos reflejan relay.RelayMessage,
// pero este paquete no importa internal/relay: las capas de core no dependen
// de la red. El daemon es quien traduce un BoardEvent a relay.RelayMessage y
// lo publica en el relay.
type BoardEvent struct {
	BoardID   string
	MemberID  string
	Type      string // "task.create", "task.move", "task.done", "task.comment"
	Payload   []byte // JSON del payload (tarea o comentario)
	Timestamp int64  // segundos unix; 0 deja que el relay lo rellene
	Version   int
}

// BoardNotifier notifica un BoardEvent. Una implementación nil descarta el
// evento sin error: una falla del relay (o del distribuidor) nunca debe
// impedir guardar un cambio en el store.
type BoardNotifier func(BoardEvent) error

// BoardStore is the persistence port for shared boards. It is implemented by
// internal/store using SQLite.
type BoardStore interface {
	CreateBoard(b *Board) error
	GetBoard(id string) (*Board, error)
	ListBoards() ([]*Board, error)

	CreateColumn(c *Column) error
	GetColumn(id string) (*Column, error)
	ListColumns(boardID string) ([]*Column, error)

	CreateTask(t *Task) error
	GetTask(id string) (*Task, error)
	ListTasks(boardID string) ([]*Task, error)
	UpdateTask(t *Task) error

	CreateComment(c *Comment) error
	ListComments(taskID string) ([]*Comment, error)

	CreateMember(m *Member) error
	GetMember(boardID, memberID string) (*Member, error)
	ListMembers(boardID string) ([]*Member, error)

	CreateInviteCode(invite *InviteCode) error
	GetInviteCode(code string) (*InviteCode, error)
	ListInviteCodes(boardID string) ([]*InviteCode, error)
	UpdateInviteCode(invite *InviteCode) error
}

// BoardService is the service layer for shared boards: it creates boards,
// generates invite codes, and lets members join boards.
type BoardService struct {
	store  BoardStore
	notify BoardNotifier
}

// NewBoardService creates a BoardService backed by the given BoardStore.
func NewBoardService(store BoardStore) *BoardService {
	return &BoardService{store: store}
}

// SetBoardNotifier configura el callback al que BoardService avisa de cada
// cambio de tarea (crear, mover, completar, comentar) tras guardarlo en el
// store. El daemon lo conecta al relay para que los demás miembros del
// board reciban el cambio en tiempo real.
func (s *BoardService) SetBoardNotifier(n BoardNotifier) { s.notify = n }

// notifyEvent avisa de un cambio de tarea si hay notificador configurado.
// El error del notificador se ignora: el cambio ya está persistido y no
// debe fallar por un problema en el distribuidor.
func (s *BoardService) notifyEvent(ev BoardEvent) {
	if s.notify == nil {
		return
	}
	_ = s.notify(ev)
}

// jsonPayload codifica v como JSON para el payload del evento, o nil si no
// se puede serializar (no falla la operación por eso).
func jsonPayload(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// CreateBoard creates a new shared board with the given name and owner.
func (s *BoardService) CreateBoard(ctx context.Context, name, ownerID string) (*Board, error) {
	crypto, err := board.NewBoardCrypto()
	if err != nil {
		return nil, fmt.Errorf("core: crear cifrado de board: %w", err)
	}
	b := &Board{
		ID:            newID(),
		Name:          name,
		OwnerID:       ownerID,
		EncryptionKey: crypto.PublicKey(),
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	if err := s.store.CreateBoard(b); err != nil {
		return nil, err
	}
	return b, nil
}

// GenerateInviteCode creates a new invite code for the given board.
func (s *BoardService) GenerateInviteCode(ctx context.Context, boardID, createdBy string, ttl time.Duration, maxUses int) (*InviteCode, error) {
	board, err := s.store.GetBoard(boardID)
	if err != nil {
		return nil, err
	}
	code := "bunker-invite-" + newShortID()
	expiresAt := time.Time{}
	if ttl > 0 {
		expiresAt = time.Now().Add(ttl)
	} else {
		// Default TTL: 48 hours.
		expiresAt = time.Now().Add(48 * time.Hour)
	}
	// La clave pública (simétrica) del tablero viaja en el código de invitación
	// para que el miembro que se una pueda configurar el cifrado E2EE con
	// SetPeerKey. El relay nunca la recibe.
	publicKey := append([]byte(nil), board.EncryptionKey...)
	invite := &InviteCode{
		ID:        newID(),
		BoardID:   boardID,
		Code:      code,
		CreatedAt: time.Now(),
		ExpiresAt: expiresAt,
		MaxUses:   maxUses,
		CreatedBy: createdBy,
		PublicKey: publicKey,
	}
	if err := s.store.CreateInviteCode(invite); err != nil {
		return nil, err
	}
	return invite, nil
}

// JoinBoard lets a member join a board using an invite code.
func (s *BoardService) JoinBoard(ctx context.Context, code, memberID, name, publicKey string) (*Member, error) {
	invite, err := s.store.GetInviteCode(code)
	if err != nil {
		return nil, err
	}
	if !invite.Valid() {
		return nil, ErrInviteInvalid
	}
	// invite.PublicKey lleva la clave del tablero; el miembro que se une la usa
	// del lado del cliente con BoardCrypto.SetPeerKey para cifrar/descifrar los
	// datos del tablero. El relay nunca la ve.

	// Check if member already exists on this board
	if _, err := s.store.GetMember(invite.BoardID, memberID); err == nil {
		// Member already exists, return the existing member
		return s.store.GetMember(invite.BoardID, memberID)
	}

	member := &Member{
		ID:        memberID,
		BoardID:   invite.BoardID,
		Name:      name,
		PublicKey: publicKey,
		JoinedAt:  time.Now(),
		Role:      "member",
	}
	if err := s.store.CreateMember(member); err != nil {
		return nil, err
	}

	// Mark one use of the invite code
	invite.Uses++
	if err := s.store.UpdateInviteCode(invite); err != nil {
		return nil, err
	}

	return member, nil
}

// ListMembers returns all members of a board.
func (s *BoardService) ListMembers(ctx context.Context, boardID string) ([]*Member, error) {
	if _, err := s.store.GetBoard(boardID); err != nil {
		return nil, err
	}
	return s.store.ListMembers(boardID)
}

// ListBoards returns every shared board.
func (s *BoardService) ListBoards(ctx context.Context) ([]*Board, error) {
	return s.store.ListBoards()
}

// ListTasks returns the tasks of a board.
func (s *BoardService) ListTasks(ctx context.Context, boardID string) ([]*Task, error) {
	if _, err := s.store.GetBoard(boardID); err != nil {
		return nil, err
	}
	return s.store.ListTasks(boardID)
}

// CreateTask creates a new task in a column of the board. The board must
// exist. dueDate nil means "no due date".
func (s *BoardService) CreateTask(ctx context.Context, boardID, columnID, title, description, assigneeID string, dueDate *time.Time, tags []string, createdBy string) (*Task, error) {
	if _, err := s.store.GetBoard(boardID); err != nil {
		return nil, err
	}
	task := &Task{
		ID:          newID(),
		BoardID:     boardID,
		ColumnID:    columnID,
		Title:       title,
		Description: description,
		AssigneeID:  assigneeID,
		DueDate:     dueDate,
		Tags:        tags,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		CreatedBy:   createdBy,
		Version:     0,
	}
	if err := s.store.CreateTask(task); err != nil {
		return nil, err
	}
	s.notifyEvent(BoardEvent{
		BoardID:  task.BoardID,
		MemberID: task.CreatedBy,
		Type:     "task.create",
		Payload:  jsonPayload(task),
		Version:  task.Version,
	})
	return task, nil
}

// MoveTask moves task to another column, bumping its version for
// optimistic concurrency.
func (s *BoardService) MoveTask(ctx context.Context, taskID, columnID string) (*Task, error) {
	task, err := s.store.GetTask(taskID)
	if err != nil {
		return nil, err
	}
	task.ColumnID = columnID
	task.UpdatedAt = time.Now()
	task.Version++
	if err := s.store.UpdateTask(task); err != nil {
		return nil, err
	}
	s.notifyEvent(BoardEvent{
		BoardID: task.BoardID,
		Type:    "task.move",
		Payload: jsonPayload(task),
		Version: task.Version,
	})
	return task, nil
}

// DoneTask marks task as done. version is the expected current version
// (optimistic concurrency); the stored version is bumped and UpdatedAt
// refreshed.
func (s *BoardService) DoneTask(ctx context.Context, taskID string, version int) (*Task, error) {
	task, err := s.store.GetTask(taskID)
	if err != nil {
		return nil, err
	}
	if task.Version != version {
		return nil, fmt.Errorf("core: board task done %s: version mismatch: have %d, expected %d: %w", taskID, task.Version, version, ErrTaskConflict)
	}
	task.UpdatedAt = time.Now()
	task.Version = version + 1
	if err := s.store.UpdateTask(task); err != nil {
		return nil, err
	}
	s.notifyEvent(BoardEvent{
		BoardID: task.BoardID,
		Type:    "task.done",
		Payload: jsonPayload(task),
		Version: task.Version,
	})
	return task, nil
}

// AddComment appends a comment to task, written by member.
func (s *BoardService) AddComment(ctx context.Context, taskID, memberID, text string) (*Comment, error) {
	task, err := s.store.GetTask(taskID)
	if err != nil {
		return nil, err
	}
	comment := &Comment{
		ID:        newID(),
		TaskID:    taskID,
		MemberID:  memberID,
		Text:      text,
		CreatedAt: time.Now(),
		CreatedBy: memberID,
	}
	if err := s.store.CreateComment(comment); err != nil {
		return nil, err
	}
	s.notifyEvent(BoardEvent{
		BoardID:  task.BoardID,
		MemberID: memberID,
		Type:     "task.comment",
		Payload:  jsonPayload(comment),
	})
	return comment, nil
}

// newID generates a unique ID.
func newID() string {
	return "id-" + newShortID()
}

// newShortID generates a short unique ID suitable for primary keys and
// invite codes: 12 random bytes, hex-encoded. crypto/rand never fails in
// practice, so a silent zero read would surface as a duplicate-key error
// on insert rather than a placeholder id.
func newShortID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "" // keys must collide loudly at insert time, not silently share one id
	}
	return hex.EncodeToString(b)
}
