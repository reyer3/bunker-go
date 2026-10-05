package core_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// todoStore is a fake core.TodoStore over memStore: it keeps to-dos in
// insertion order and dedupes the way the SQLite store does, by
// core.TodoKey, so Service tests see the same contract.
type todoStore struct {
	*memStore
	todos []core.Todo
}

func (s *todoStore) AddTodo(_ context.Context, t core.Todo) (core.Todo, error) {
	for _, old := range s.todos {
		if old.ID == t.ID || (core.TodoKey(old) == core.TodoKey(t) && (t.ItemID != "" || old.Status == core.TodoOpen)) {
			return old, nil
		}
	}
	s.todos = append(s.todos, t)
	return t, nil
}

func (s *todoStore) Todos(_ context.Context, f core.TodoFilter) ([]core.Todo, error) {
	var out []core.Todo
	for _, t := range s.todos {
		if (f.Status == "" || t.Status == f.Status) && (f.Direction == "" || t.Direction == f.Direction) {
			out = append(out, t)
		}
	}
	return out, nil
}

func (s *todoStore) SetTodoStatus(_ context.Context, id string, status core.TodoStatus, at time.Time) (core.Todo, error) {
	for i, t := range s.todos {
		if t.ID != id {
			continue
		}
		if t.Status != status {
			t.Status = status
			t.Done = time.Time{}
			if status == core.TodoDone {
				t.Done = at
			}
			s.todos[i] = t
		}
		return t, nil
	}
	return core.Todo{}, core.ErrTodoNotFound
}

var todoNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func todoService(items ...core.Item) (*core.Service, *todoStore) {
	st := &todoStore{memStore: newMemStore(items...)}
	svc := core.NewService(st, core.NewRegistry())
	svc.SetQueryClock(func() time.Time { return todoNow })
	return svc, st
}

func TestAddTodoValidates(t *testing.T) {
	svc, _ := todoService()
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		todo core.Todo
	}{
		{"empty text", core.Todo{Text: "   ", Direction: core.TodoMine}},
		{"no direction", core.Todo{Text: "Enviar el informe"}},
		{"bad direction", core.Todo{Text: "Enviar el informe", Direction: "ours"}},
		{"text too long", core.Todo{Text: strings.Repeat("a", core.MaxTodoText+1), Direction: core.TodoMine}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.AddTodo(ctx, tc.todo); !errors.Is(err, core.ErrInvalidTodo) {
				t.Fatalf("err = %v, want ErrInvalidTodo", err)
			}
		})
	}
}

func TestAddTodoFillsDefaultsAndSourceItem(t *testing.T) {
	chat := core.Item{ID: "whatsapp:personal:A1", Channel: core.ChannelWhatsApp, Account: "personal",
		Thread: "51900000001@s.whatsapp.net", ThreadName: "Ana Ejemplo",
		From: core.Address{ID: "51900000001@s.whatsapp.net", Name: "Ana"}, Timestamp: todoNow}
	mail := core.Item{ID: "mail:cl:7", Channel: core.ChannelMail, Account: "cl", Thread: "t7",
		ThreadName: "Informe mensual", FromMe: true,
		From: core.Address{ID: "me@example.com"}, To: []core.Address{{ID: "jefe@example.com", Name: "Jefe Ejemplo"}}}
	svc, _ := todoService(chat, mail)
	ctx := context.Background()

	got, err := svc.AddTodo(ctx, core.Todo{Text: "  Mandar el informe  ", Direction: core.TodoMine, ItemID: chat.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == "" || got.Status != core.TodoOpen || !got.Created.Equal(todoNow) {
		t.Errorf("defaults = %+v", got)
	}
	if got.Text != "Mandar el informe" {
		t.Errorf("text = %q, want trimmed", got.Text)
	}
	if got.Channel != core.ChannelWhatsApp || got.Account != "personal" || got.Thread != chat.Thread || got.Person != "Ana" {
		t.Errorf("from chat item = %+v", got)
	}

	// My own mail: the person is who it went to, not the subject.
	got, err = svc.AddTodo(ctx, core.Todo{Text: "Revisar cifras", Direction: core.TodoTheirs, ItemID: mail.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got.Person != "Jefe Ejemplo" || got.Channel != core.ChannelMail || got.Thread != "t7" {
		t.Errorf("from own mail = %+v", got)
	}

	// Caller-given fields win over the item's.
	got, err = svc.AddTodo(ctx, core.Todo{Text: "Llamar", Direction: core.TodoMine, ItemID: chat.ID, Person: "Ana Ejemplo"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Person != "Ana Ejemplo" {
		t.Errorf("person = %q, want the caller's", got.Person)
	}

	if _, err := svc.AddTodo(ctx, core.Todo{Text: "x", Direction: core.TodoMine, ItemID: "mail:cl:404"}); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown item err = %v, want ErrNotFound", err)
	}
}

func TestAddTodoIsIdempotent(t *testing.T) {
	item := core.Item{ID: "matrix:m:1", Channel: core.ChannelMatrix, Account: "m", Thread: "!room"}
	svc, st := todoService(item)
	ctx := context.Background()
	first, err := svc.AddTodo(ctx, core.Todo{Text: "Enviar contrato", Direction: core.TodoMine, ItemID: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	again, err := svc.AddTodo(ctx, core.Todo{Text: "enviar   CONTRATO ", Direction: core.TodoMine, ItemID: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID || len(st.todos) != 1 {
		t.Fatalf("retry made a duplicate: %+v vs %+v (%d stored)", again, first, len(st.todos))
	}
	byID, err := svc.AddTodo(ctx, core.Todo{ID: "fixed-1", Text: "Otra cosa", Direction: core.TodoTheirs})
	if err != nil || byID.ID != "fixed-1" {
		t.Fatalf("caller id = %+v, %v", byID, err)
	}
	if again, _ := svc.AddTodo(ctx, core.Todo{ID: "fixed-1", Text: "Otra cosa", Direction: core.TodoTheirs}); again.ID != "fixed-1" || len(st.todos) != 2 {
		t.Errorf("caller id retry duplicated: %d stored", len(st.todos))
	}
}

func TestCompleteAndReopenTodo(t *testing.T) {
	svc, _ := todoService()
	ctx := context.Background()
	added, err := svc.AddTodo(ctx, core.Todo{Text: "Pagar luz", Direction: core.TodoMine})
	if err != nil {
		t.Fatal(err)
	}
	done, err := svc.CompleteTodo(ctx, added.ID)
	if err != nil || done.Status != core.TodoDone || !done.Done.Equal(todoNow) {
		t.Fatalf("complete = %+v, %v", done, err)
	}
	open, err := svc.ReopenTodo(ctx, added.ID)
	if err != nil || open.Status != core.TodoOpen || !open.Done.IsZero() {
		t.Fatalf("reopen = %+v, %v", open, err)
	}
	if _, err := svc.CompleteTodo(ctx, "nope"); !errors.Is(err, core.ErrTodoNotFound) {
		t.Errorf("unknown id err = %v", err)
	}
	if _, err := svc.CompleteTodo(ctx, " "); !errors.Is(err, core.ErrInvalidTodo) {
		t.Errorf("empty id err = %v", err)
	}
}

func TestTodosDefaultsAndValidatesFilter(t *testing.T) {
	svc, _ := todoService()
	ctx := context.Background()
	if _, err := svc.AddTodo(ctx, core.Todo{Text: "Uno", Direction: core.TodoMine}); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Todos(ctx, core.TodoFilter{Status: core.TodoOpen})
	if err != nil || len(got) != 1 {
		t.Fatalf("todos = %v, %v", got, err)
	}
	if _, err := svc.Todos(ctx, core.TodoFilter{Direction: "ours"}); !errors.Is(err, core.ErrInvalidTodo) {
		t.Errorf("bad direction err = %v", err)
	}
}

func TestTodosUnsupportedStore(t *testing.T) {
	svc := core.NewService(newMemStore(), core.NewRegistry())
	ctx := context.Background()
	if _, err := svc.AddTodo(ctx, core.Todo{Text: "x", Direction: core.TodoMine}); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("add err = %v", err)
	}
	if _, err := svc.Todos(ctx, core.TodoFilter{}); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("list err = %v", err)
	}
	if _, err := svc.CompleteTodo(ctx, "x"); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("complete err = %v", err)
	}
}

func TestParseTodoDue(t *testing.T) {
	loc := time.FixedZone("UTC-5", -5*3600)
	now := time.Date(2026, 10, 5, 22, 30, 0, 0, loc)
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, loc) }
	for _, tc := range []struct {
		in   string
		want time.Time
		err  bool
	}{
		{in: "", want: time.Time{}},
		{in: "2026-10-09", want: day(2026, 10, 9)},
		{in: "0d", want: day(2026, 10, 5)},
		{in: "2d", want: day(2026, 10, 7)},
		{in: "1w", want: day(2026, 10, 12)},
		{in: "mañana", err: true},
		{in: "-2d", err: true},
	} {
		got, err := core.ParseTodoDue(tc.in, now)
		if tc.err {
			if !errors.Is(err, core.ErrInvalidTodo) {
				t.Errorf("%q: err = %v, want ErrInvalidTodo", tc.in, err)
			}
			continue
		}
		if err != nil || !got.Equal(tc.want) {
			t.Errorf("%q = %v, %v; want %v", tc.in, got, err, tc.want)
		}
	}
}
