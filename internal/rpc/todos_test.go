package rpc_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/rpc"
	"github.com/reyer3/bunker-go/internal/store"
)

func todoClient(t *testing.T) (*rpc.Client, context.Context) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bunker.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Upsert(context.Background(), core.Item{ID: "whatsapp:personal:A1", Channel: core.ChannelWhatsApp,
		Account: "personal", Thread: "demo-ana", ThreadName: "Demo Ana",
		From: core.Address{ID: "demo-ana", Name: "Demo Ana"}, Timestamp: time.Now()}); err != nil {
		t.Fatal(err)
	}
	svc := core.NewService(st, core.NewRegistry())
	socket := filepath.Join(dir, "bunker.sock")
	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- rpc.NewServer(svc).Serve(ctx, socket) }()
	t.Cleanup(func() {
		cancel()
		<-serveErr
	})
	client := dialUntilReady(t, socket)
	t.Cleanup(func() { client.Close() })
	return client, ctx
}

func TestClientTodosRoundTrip(t *testing.T) {
	client, ctx := todoClient(t)
	due := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	added, err := client.AddTodo(ctx, core.Todo{Text: "Mandar el informe", Direction: core.TodoMine,
		Due: due, ItemID: "whatsapp:personal:A1"})
	if err != nil {
		t.Fatalf("AddTodo: %v", err)
	}
	if added.ID == "" || added.Person != "Demo Ana" || added.Channel != core.ChannelWhatsApp || !added.Due.Equal(due) {
		t.Fatalf("AddTodo = %+v", added)
	}
	again, err := client.AddTodo(ctx, core.Todo{Text: "mandar el informe", Direction: core.TodoMine, ItemID: "whatsapp:personal:A1"})
	if err != nil || again.ID != added.ID {
		t.Fatalf("retry = %+v, %v; want %s", again, err, added.ID)
	}

	list, err := client.Todos(ctx, core.TodoFilter{Status: core.TodoOpen})
	if err != nil || len(list) != 1 || list[0].ID != added.ID {
		t.Fatalf("Todos = %+v, %v", list, err)
	}
	done, err := client.CompleteTodo(ctx, added.ID)
	if err != nil || done.Status != core.TodoDone || done.Done.IsZero() {
		t.Fatalf("CompleteTodo = %+v, %v", done, err)
	}
	if list, _ := client.Todos(ctx, core.TodoFilter{Status: core.TodoOpen}); len(list) != 0 {
		t.Errorf("open after done = %+v", list)
	}
	open, err := client.ReopenTodo(ctx, added.ID)
	if err != nil || open.Status != core.TodoOpen {
		t.Fatalf("ReopenTodo = %+v, %v", open, err)
	}
}

func TestClientTodoErrorsSurviveTheWire(t *testing.T) {
	client, ctx := todoClient(t)
	if _, err := client.CompleteTodo(ctx, "nope"); !errors.Is(err, core.ErrTodoNotFound) {
		t.Errorf("unknown id err = %v, want ErrTodoNotFound", err)
	}
	if _, err := client.AddTodo(ctx, core.Todo{Text: " ", Direction: core.TodoMine}); !errors.Is(err, core.ErrInvalidTodo) {
		t.Errorf("empty text err = %v, want ErrInvalidTodo", err)
	}
	if _, err := client.AddTodo(ctx, core.Todo{Text: "x", Direction: core.TodoMine, ItemID: "mail:cl:404"}); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown item err = %v, want ErrNotFound", err)
	}
}
