package core_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

// TestBoardServiceNotifiesTaskEvents verifica que BoardService dispara un
// BoardEvent por cada cambio de tarea tras guardarlo en el store. El
// notificador es el sustituto del relay en las pruebas de core: core no
// importa internal/relay, el daemon es quien traduce BoardEvent a
// relay.RelayMessage y lo publica.
func TestBoardServiceNotifiesTaskEvents(t *testing.T) {
	store := newMemBoardStore()
	svc := core.NewBoardService(store)

	var events []core.BoardEvent
	svc.SetBoardNotifier(func(ev core.BoardEvent) error {
		events = append(events, ev)
		return nil
	})

	ctx := context.Background()

	board, err := svc.CreateBoard(ctx, "Tareas compartidas", "member-ana")
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}

	// task.create (version 0).
	task, err := svc.CreateTask(ctx, board.ID, "col-1", "Enviar informe", "desc", "member-ana", nil, nil, "member-ana")
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// task.move (version 0 -> 1).
	moved, err := svc.MoveTask(ctx, task.ID, "col-2")
	if err != nil {
		t.Fatalf("MoveTask: %v", err)
	}

	// task.done (version 1 -> 2).
	done, err := svc.DoneTask(ctx, task.ID, moved.Version)
	if err != nil {
		t.Fatalf("DoneTask: %v", err)
	}

	// task.comment.
	if _, err := svc.AddComment(ctx, task.ID, "member-ana", "Listo"); err != nil {
		t.Fatalf("AddComment: %v", err)
	}

	if len(events) != 4 {
		t.Fatalf("len(events) = %d, want 4 (create, move, done, comment)", len(events))
	}

	cases := []struct {
		index  int
		typ    string
		member string
	}{
		{0, "task.create", "member-ana"},
		{1, "task.move", ""},
		{2, "task.done", ""},
		{3, "task.comment", "member-ana"},
	}
	for _, c := range cases {
		ev := events[c.index]
		if ev.Type != c.typ {
			t.Errorf("events[%d].Type = %q, want %q", c.index, ev.Type, c.typ)
		}
		if ev.BoardID != board.ID {
			t.Errorf("events[%d].BoardID = %q, want %q", c.index, ev.BoardID, board.ID)
		}
		if ev.MemberID != c.member {
			t.Errorf("events[%d].MemberID = %q, want %q", c.index, ev.MemberID, c.member)
		}
		if len(ev.Payload) == 0 {
			t.Errorf("events[%d].Payload is empty (el relay rellena el timestamp al publicar)", c.index)
		}
	}

	// task.create payload deserializa a la tarea creada.
	var created core.Task
	if err := json.Unmarshal(events[0].Payload, &created); err != nil {
		t.Fatalf("unmarshal create payload: %v", err)
	}
	if created.ID != task.ID || created.Title != "Enviar informe" || created.CreatedBy != "member-ana" || created.ColumnID != "col-1" {
		t.Errorf("create payload = %+v, unexpected", created)
	}

	// task.move payload lleva la tarea ya movida.
	var movedTask core.Task
	if err := json.Unmarshal(events[1].Payload, &movedTask); err != nil {
		t.Fatalf("unmarshal move payload: %v", err)
	}
	if movedTask.ColumnID != "col-2" || movedTask.Version != 1 {
		t.Errorf("move payload = %+v, unexpected", movedTask)
	}

	// task.done payload lleva la tarea completada.
	var doneTask core.Task
	if err := json.Unmarshal(events[2].Payload, &doneTask); err != nil {
		t.Fatalf("unmarshal done payload: %v", err)
	}
	if doneTask.Version != done.Version {
		t.Errorf("done payload version = %d, want %d", doneTask.Version, done.Version)
	}

	// task.comment payload deserializa al comentario.
	var comment core.Comment
	if err := json.Unmarshal(events[3].Payload, &comment); err != nil {
		t.Fatalf("unmarshal comment payload: %v", err)
	}
	if comment.Text != "Listo" || comment.MemberID != "member-ana" || comment.TaskID != task.ID {
		t.Errorf("comment payload = %+v, unexpected", comment)
	}

	// Un done con version incorrecta falla y no dispara evento.
	if _, err := svc.DoneTask(ctx, task.ID, 99); err == nil {
		t.Error("DoneTask with wrong version: expected error, got nil")
	}
	if len(events) != 4 {
		t.Errorf("len(events) after failed DoneTask = %d, want 4 (no event on failure)", len(events))
	}
}

// TestBoardServiceNoNotifierDoesNotPanic verifica que BoardService funciona
// sin notificador configurado (el daemon puede no haberlo conectado todavia).
func TestBoardServiceNoNotifierDoesNotPanic(t *testing.T) {
	store := newMemBoardStore()
	svc := core.NewBoardService(store)

	ctx := context.Background()
	board, err := svc.CreateBoard(ctx, "Sin notificador", "member-ana")
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	task, err := svc.CreateTask(ctx, board.ID, "col-1", "T", "", "member-ana", nil, nil, "member-ana")
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := svc.MoveTask(ctx, task.ID, "col-2"); err != nil {
		t.Fatalf("MoveTask: %v", err)
	}
	if _, err := svc.DoneTask(ctx, task.ID, 1); err != nil {
		t.Fatalf("DoneTask: %v", err)
	}
	if _, err := svc.AddComment(ctx, task.ID, "member-ana", "x"); err != nil {
		t.Fatalf("AddComment: %v", err)
	}
}
