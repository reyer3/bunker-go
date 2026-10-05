package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Todo is something to do that came out of a conversation: a promise the
// user made (TodoMine) or something someone owes the user (TodoTheirs).
// bunker only stores, lists and completes to-dos; deciding that a message
// holds one is the agent's job (the MCP todo tools), so no model runs here.
type Todo struct {
	ID        string        `json:"id"`
	Text      string        `json:"text"`
	Direction TodoDirection `json:"direction"`
	Status    TodoStatus    `json:"status"`
	// Due is the start of the day it is due, zero for none.
	Due time.Time `json:"due,omitzero"`
	// ItemID is the message the to-do came from, when there is one;
	// Channel, Account, Thread and Person locate the conversation and
	// default to that message's.
	ItemID  string    `json:"item_id,omitempty"`
	Channel Channel   `json:"channel,omitempty"`
	Account string    `json:"account,omitempty"`
	Thread  string    `json:"thread,omitempty"`
	Person  string    `json:"person,omitempty"`
	Created time.Time `json:"created"`
	// Done is when it was completed, zero while open.
	Done time.Time `json:"done,omitzero"`
}

// TodoDirection says who owes the to-do.
type TodoDirection string

// TodoStatus is whether a to-do is still pending.
type TodoStatus string

const (
	// TodoMine is a promise the user made.
	TodoMine TodoDirection = "mine"
	// TodoTheirs is something someone owes the user.
	TodoTheirs TodoDirection = "theirs"

	TodoOpen TodoStatus = "open"
	TodoDone TodoStatus = "done"

	// MaxTodoText bounds a to-do's text in characters: a to-do is a line,
	// not a copy of the message it came from.
	MaxTodoText = 500
	// maxTodoRows bounds one listing.
	maxTodoRows = 500
)

// ErrInvalidTodo is returned for a to-do, filter or due date that does
// not validate.
var ErrInvalidTodo = errors.New("core: invalid to-do")

// ErrTodoNotFound is returned when a to-do id is not known to the store.
var ErrTodoNotFound = errors.New("core: to-do not found")

// TodoFilter narrows Service.Todos. Zero fields match everything; Limit
// <= 0 (or above the row bound) returns at most maxTodoRows.
type TodoFilter struct {
	Status    TodoStatus    `json:"status,omitempty"`
	Direction TodoDirection `json:"direction,omitempty"`
	Channel   Channel       `json:"channel,omitempty"`
	Account   string        `json:"account,omitempty"`
	Limit     int           `json:"limit,omitempty"`
}

// TodoStore is an optional Store capability holding to-dos.
// internal/store implements it.
type TodoStore interface {
	// AddTodo stores t, unless a to-do with its ID or its TodoKey already
	// exists, in which case that one is returned unchanged: adding is
	// idempotent, so an agent rescanning a message never duplicates it.
	// The key matches any status when t has an ItemID (a done to-do stays
	// done) and only open to-dos when it has none.
	AddTodo(ctx context.Context, t Todo) (Todo, error)
	// Todos lists to-dos matching filter: open first, then by due date
	// (soonest first, none last), then oldest created first.
	Todos(ctx context.Context, filter TodoFilter) ([]Todo, error)
	// SetTodoStatus completes (at) or reopens a to-do; setting the status
	// it already has changes nothing. ErrTodoNotFound when id is unknown.
	SetTodoStatus(ctx context.Context, id string, status TodoStatus, at time.Time) (Todo, error)
}

// NormalizeTodoText is the text form duplicates are detected by: case
// folded and whitespace collapsed.
func NormalizeTodoText(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// TodoKey identifies a to-do for AddTodo's idempotency: its source
// message, direction and normalized text.
func TodoKey(t Todo) string {
	return t.ItemID + "\x00" + string(t.Direction) + "\x00" + NormalizeTodoText(t.Text)
}

// ParseTodoDue resolves a due date: "" for none, YYYY-MM-DD, or a
// relative Nd or Nw (N days or weeks from now). The result is the start
// of that day in now's time zone.
func ParseTodoDue(value string, now time.Time) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	if t, err := time.ParseInLocation(queryDateLayout, value, now.Location()); err == nil {
		return t, nil
	}
	if n := len(value); n >= 2 && value[0] != '+' {
		count, err := strconv.Atoi(value[:n-1])
		if err == nil && count >= 0 && count <= 3650 {
			today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
			switch value[n-1] {
			case 'd':
				return today.AddDate(0, 0, count), nil
			case 'w':
				return today.AddDate(0, 0, 7*count), nil
			}
		}
	}
	return time.Time{}, fmt.Errorf("due %q: want YYYY-MM-DD or a relative 2d or 1w: %w", value, ErrInvalidTodo)
}

// AddTodo validates and stores a to-do. A missing ID, status or creation
// time is filled in; when t names its source message, the conversation
// fields left empty are taken from that message. Adding the same to-do
// again returns the stored one (see TodoStore.AddTodo).
func (s *Service) AddTodo(ctx context.Context, t Todo) (Todo, error) {
	store, ok := s.store.(TodoStore)
	if !ok {
		return Todo{}, fmt.Errorf("core: store cannot keep to-dos: %w", ErrUnsupported)
	}
	t.Text = strings.TrimSpace(t.Text)
	t.ID = strings.TrimSpace(t.ID)
	t.ItemID = strings.TrimSpace(t.ItemID)
	t.Person = strings.TrimSpace(t.Person)
	switch {
	case t.Text == "":
		return Todo{}, fmt.Errorf("core: to-do text is empty: %w", ErrInvalidTodo)
	case utf8.RuneCountInString(t.Text) > MaxTodoText:
		return Todo{}, fmt.Errorf("core: to-do text is longer than %d characters: %w", MaxTodoText, ErrInvalidTodo)
	case t.Direction != TodoMine && t.Direction != TodoTheirs:
		return Todo{}, fmt.Errorf("core: to-do direction %q: want %s or %s: %w", t.Direction, TodoMine, TodoTheirs, ErrInvalidTodo)
	}
	if t.ItemID != "" {
		item, err := s.store.Get(ctx, t.ItemID)
		if err != nil {
			return Todo{}, fmt.Errorf("core: to-do source message: %w", err)
		}
		fillTodoFromItem(&t, item)
	}
	if t.ID == "" {
		id, err := newTodoID()
		if err != nil {
			return Todo{}, err
		}
		t.ID = id
	}
	t.Status = TodoOpen
	t.Done = time.Time{}
	if t.Created.IsZero() {
		t.Created = s.queryClock()
	}
	return store.AddTodo(ctx, t)
}

// fillTodoFromItem sets the conversation fields t leaves empty from the
// message it came from. The person is the other side of that message
// (otherSide).
func fillTodoFromItem(t *Todo, item Item) {
	if t.Channel == "" {
		t.Channel = item.Channel
	}
	if t.Account == "" {
		t.Account = item.Account
	}
	if t.Thread == "" {
		t.Thread = item.Thread
	}
	if t.Person == "" {
		t.Person = otherSide(item)
	}
}

// otherSide is who a message is with: its sender, or for one the user
// sent, the chat's name or the mail's first recipient (a mail's thread
// name is its subject, not a person). "" when unknown.
func otherSide(item Item) string {
	switch {
	case !item.FromMe:
		return firstNonEmpty(item.From.Name, item.From.ID)
	case item.Channel == ChannelMail && len(item.To) > 0:
		return firstNonEmpty(item.To[0].Name, item.To[0].ID)
	case item.Channel != ChannelMail:
		return item.ThreadName
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// newTodoID is a short random id: short enough to type in
// "bunker todo done <id>", random enough never to collide in one store.
func newTodoID() (string, error) {
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("core: to-do id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// Todos lists the stored to-dos matching filter (see TodoStore.Todos).
func (s *Service) Todos(ctx context.Context, filter TodoFilter) ([]Todo, error) {
	store, ok := s.store.(TodoStore)
	if !ok {
		return nil, fmt.Errorf("core: store cannot keep to-dos: %w", ErrUnsupported)
	}
	if filter.Status != "" && filter.Status != TodoOpen && filter.Status != TodoDone {
		return nil, fmt.Errorf("core: to-do status %q: %w", filter.Status, ErrInvalidTodo)
	}
	if filter.Direction != "" && filter.Direction != TodoMine && filter.Direction != TodoTheirs {
		return nil, fmt.Errorf("core: to-do direction %q: %w", filter.Direction, ErrInvalidTodo)
	}
	if filter.Limit <= 0 || filter.Limit > maxTodoRows {
		filter.Limit = maxTodoRows
	}
	return store.Todos(ctx, filter)
}

// CompleteTodo marks a to-do done. Completing a done one keeps its
// original completion time.
func (s *Service) CompleteTodo(ctx context.Context, id string) (Todo, error) {
	return s.setTodoStatus(ctx, id, TodoDone)
}

// ReopenTodo marks a done to-do open again.
func (s *Service) ReopenTodo(ctx context.Context, id string) (Todo, error) {
	return s.setTodoStatus(ctx, id, TodoOpen)
}

func (s *Service) setTodoStatus(ctx context.Context, id string, status TodoStatus) (Todo, error) {
	store, ok := s.store.(TodoStore)
	if !ok {
		return Todo{}, fmt.Errorf("core: store cannot keep to-dos: %w", ErrUnsupported)
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return Todo{}, fmt.Errorf("core: to-do id is empty: %w", ErrInvalidTodo)
	}
	return store.SetTodoStatus(ctx, id, status, s.queryClock())
}
