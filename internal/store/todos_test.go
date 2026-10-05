package store_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/store"
)

var todoBase = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func newTodo(id, text string, dir core.TodoDirection, due time.Time, created time.Duration) core.Todo {
	return core.Todo{ID: id, Text: text, Direction: dir, Status: core.TodoOpen, Due: due,
		Created: todoBase.Add(created)}
}

func TestTodosRoundTripAndOrder(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	day := 24 * time.Hour
	full := core.Todo{ID: "t1", Text: "Mandar el informe", Direction: core.TodoMine, Status: core.TodoOpen,
		Due: todoBase.Add(3 * day), ItemID: "whatsapp:personal:A1", Channel: core.ChannelWhatsApp,
		Account: "personal", Thread: "51900000001@s.whatsapp.net", Person: "Ana", Created: todoBase}
	for _, td := range []core.Todo{
		full,
		newTodo("t2", "Sin fecha vieja", core.TodoTheirs, time.Time{}, -2*time.Hour),
		newTodo("t3", "Vence pronto", core.TodoMine, todoBase.Add(day), time.Hour),
		newTodo("t4", "Sin fecha nueva", core.TodoMine, time.Time{}, time.Hour),
		newTodo("t5", "Ya hecha", core.TodoMine, todoBase.Add(-day), 0),
	} {
		if _, err := s.AddTodo(ctx, td); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SetTodoStatus(ctx, "t5", core.TodoDone, todoBase); err != nil {
		t.Fatal(err)
	}

	all, err := s.Todos(ctx, core.TodoFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, td := range all {
		ids = append(ids, td.ID)
	}
	want := []string{"t3", "t1", "t2", "t4", "t5"}
	if len(ids) != len(want) {
		t.Fatalf("order = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("order = %v, want %v", ids, want)
		}
	}
	got := all[1]
	if got.Text != full.Text || got.Direction != full.Direction || !got.Due.Equal(full.Due) ||
		got.ItemID != full.ItemID || got.Channel != full.Channel || got.Account != full.Account ||
		got.Thread != full.Thread || got.Person != full.Person || !got.Created.Equal(full.Created) || !got.Done.IsZero() {
		t.Errorf("round trip = %+v, want %+v", got, full)
	}

	for _, tc := range []struct {
		name   string
		filter core.TodoFilter
		want   int
	}{
		{"open", core.TodoFilter{Status: core.TodoOpen}, 4},
		{"done", core.TodoFilter{Status: core.TodoDone}, 1},
		{"theirs", core.TodoFilter{Direction: core.TodoTheirs}, 1},
		{"channel", core.TodoFilter{Channel: core.ChannelWhatsApp}, 1},
		{"account", core.TodoFilter{Account: "personal"}, 1},
		{"limit", core.TodoFilter{Limit: 2}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.Todos(ctx, tc.filter)
			if err != nil || len(got) != tc.want {
				t.Errorf("Todos(%+v) = %d, %v; want %d", tc.filter, len(got), err, tc.want)
			}
		})
	}
}

func TestAddTodoDedupes(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	first := newTodo("t1", "Enviar contrato", core.TodoMine, time.Time{}, 0)
	first.ItemID = "mail:cl:1"
	if _, err := s.AddTodo(ctx, first); err != nil {
		t.Fatal(err)
	}
	retry := newTodo("t9", "enviar  CONTRATO", core.TodoMine, time.Time{}, time.Hour)
	retry.ItemID = "mail:cl:1"
	got, err := s.AddTodo(ctx, retry)
	if err != nil || got.ID != "t1" {
		t.Fatalf("retry = %+v, %v; want t1", got, err)
	}
	// Same text, other direction: a different to-do.
	other := retry
	other.ID, other.Direction = "t2", core.TodoTheirs
	if got, _ := s.AddTodo(ctx, other); got.ID != "t2" {
		t.Errorf("other direction = %+v", got)
	}
	// Same caller id: the stored one comes back untouched.
	if got, _ := s.AddTodo(ctx, newTodo("t2", "otra cosa", core.TodoMine, time.Time{}, 0)); got.Text != "enviar  CONTRATO" || got.Direction != core.TodoTheirs {
		t.Errorf("same id = %+v", got)
	}
	// A done to-do from a message stays done when the agent sees the
	// message again.
	if _, err := s.SetTodoStatus(ctx, "t1", core.TodoDone, todoBase); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.AddTodo(ctx, retry); got.ID != "t1" || got.Status != core.TodoDone {
		t.Errorf("rescan of done = %+v", got)
	}
	// Without a source message only an open one dedupes.
	loose := newTodo("t3", "Pagar luz", core.TodoMine, time.Time{}, 0)
	if _, err := s.AddTodo(ctx, loose); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.AddTodo(ctx, newTodo("t4", "pagar luz", core.TodoMine, time.Time{}, 0)); got.ID != "t3" {
		t.Errorf("open loose retry = %+v", got)
	}
	if _, err := s.SetTodoStatus(ctx, "t3", core.TodoDone, todoBase); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.AddTodo(ctx, newTodo("t5", "pagar luz", core.TodoMine, time.Time{}, 0)); got.ID != "t5" {
		t.Errorf("new loose after done = %+v", got)
	}
	all, _ := s.Todos(ctx, core.TodoFilter{})
	if len(all) != 4 {
		t.Errorf("stored %d to-dos, want 4", len(all))
	}
}

func TestSetTodoStatus(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.AddTodo(ctx, newTodo("t1", "Pagar luz", core.TodoMine, time.Time{}, 0)); err != nil {
		t.Fatal(err)
	}
	done, err := s.SetTodoStatus(ctx, "t1", core.TodoDone, todoBase)
	if err != nil || done.Status != core.TodoDone || !done.Done.Equal(todoBase) {
		t.Fatalf("done = %+v, %v", done, err)
	}
	// Completing again keeps the first completion time.
	again, err := s.SetTodoStatus(ctx, "t1", core.TodoDone, todoBase.Add(time.Hour))
	if err != nil || !again.Done.Equal(todoBase) {
		t.Errorf("again = %+v, %v", again, err)
	}
	open, err := s.SetTodoStatus(ctx, "t1", core.TodoOpen, todoBase.Add(time.Hour))
	if err != nil || open.Status != core.TodoOpen || !open.Done.IsZero() {
		t.Errorf("reopen = %+v, %v", open, err)
	}
	if _, err := s.SetTodoStatus(ctx, "nope", core.TodoDone, todoBase); !errors.Is(err, core.ErrTodoNotFound) {
		t.Errorf("unknown err = %v", err)
	}
}

func TestMigrateV6CreatesTodosOnAnOlderDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bunker.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE todos; PRAGMA user_version = 5`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.AddTodo(context.Background(), newTodo("t1", "Uno", core.TodoMine, time.Time{}, 0)); err != nil {
		t.Fatalf("add after migrating: %v", err)
	}
	if store.CurrentSchemaVersion() < 6 {
		t.Errorf("schema version = %d, want >= 6", store.CurrentSchemaVersion())
	}
}
