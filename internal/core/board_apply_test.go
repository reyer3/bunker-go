package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// ingestBoard builds a board with one column and returns the service plus
// the recorder of outgoing events, so tests can tell a remote-applied delta
// from a locally-made one.
func ingestBoard(t *testing.T) (*core.BoardService, core.BoardStore, *core.Board, *[]core.BoardEvent) {
	t.Helper()
	store := newMemBoardStore()
	svc := core.NewBoardService(store)
	events := &[]core.BoardEvent{}
	svc.SetBoardNotifier(func(ev core.BoardEvent) error { *events = append(*events, ev); return nil })
	ctx := context.Background()
	b, err := svc.CreateBoard(ctx, "Tablero", "member-ana")
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	if err := store.CreateColumn(&core.Column{ID: "col-1", Board: b.ID, Name: "Por hacer", Order: 0, Color: "#4db0ff"}); err != nil {
		t.Fatalf("CreateColumn: %v", err)
	}
	return svc, store, b, events
}

// A remote task delta applied into the local store must be readable
// afterwards, and must NOT notify the transport again (the anti-echo rule:
// what the relay/P2P delivers back must not round-trip to its sender).
func TestApplyBoardEventCreateTask(t *testing.T) {
	svc, _, b, out := ingestBoard(t)

	remote := core.Task{ID: "task-remote-1", BoardID: b.ID, ColumnID: "col-1", Title: "Segundo CPU", CreatedBy: "member-bob", Version: 0}
	payload, _ := json.Marshal(remote)
	if err := svc.ApplyBoardEvent(core.BoardEvent{BoardID: b.ID, MemberID: "member-bob", Type: "task.create", Payload: payload, Version: 0}); err != nil {
		t.Fatalf("ApplyBoardEvent: %v", err)
	}
	tasks, err := svc.ListTasks(context.Background(), b.ID)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].ID != remote.ID || tasks[0].Title != remote.Title {
		t.Fatalf("tasks = %+v, want the remote-applied one", tasks)
	}
	if len(*out) != 0 {
		t.Fatalf("applied remote delta re-notified the transport: %+v", *out)
	}
}

// A stale delta (older than the local task) must be a no-op: optimistic
// concurrency is decided locally, so a lagging device never replaces code
// the user just did.
func TestApplyBoardEventStaleMoveIsSkipped(t *testing.T) {
	svc, store, b, out := ingestBoard(t)
	task, err := svc.CreateTask(context.Background(), b.ID, "col-1", "Local", "", "member-ana", nil, nil, "member-ana")
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := svc.MoveTask(context.Background(), task.ID, "col-2"); err != nil {
		t.Fatalf("MoveTask: %v", err)
	}
	*out = nil // los eventos de la preparación local no cuentan: solo el apply
	// The remote delta still shows col-1 with version 0: stale save.
	stale := *task
	stale.ColumnID = "col-1"
	stale.Version = 0
	payload, _ := json.Marshal(stale)
	if err := svc.ApplyBoardEvent(core.BoardEvent{BoardID: b.ID, MemberID: "member-bob", Type: "task.move", Payload: payload, Version: stale.Version}); err != nil {
		t.Fatalf("ApplyBoardEvent: %v", err)
	}
	got, err := store.GetTask(task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.ColumnID != "col-2" {
		t.Fatalf("stale delta replaced the local move: %+v", got)
	}
	if len(*out) != 0 {
		t.Fatalf("skipped delta re-notified the transport: %+v", *out)
	}
}

// A move for a task whose create never arrived locally must still create
// it in full (the relay keeps no ordering guarantees): the payload carries
// the whole task, like an eventual delivery.
func TestApplyBoardEventMoveForUnknownTaskCreatesIt(t *testing.T) {
	svc, _, b, out := ingestBoard(t)

	remote := core.Task{ID: "task-remote-2", BoardID: b.ID, ColumnID: "col-2", Title: "Re-sync", CreatedBy: "member-bob", Version: 1}
	payload, _ := json.Marshal(remote)
	if err := svc.ApplyBoardEvent(core.BoardEvent{BoardID: b.ID, MemberID: "member-bob", Type: "task.move", Payload: payload, Version: remote.Version}); err != nil {
		t.Fatalf("ApplyBoardEvent: %v", err)
	}
	tasks, err := svc.ListTasks(context.Background(), b.ID)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].ID != remote.ID || tasks[0].ColumnID != "col-2" || tasks[0].Title != remote.Title {
		t.Fatalf("tasks = %+v, want the moved task recreated whole", tasks)
	}
	if len(*out) != 0 {
		t.Fatalf("applied delta re-notified the transport: %+v", *out)
	}
}

// A remote comment delta appends the comment; a repeated delivery of the
// same comment is idempotent (same id) rather than duplicating.
func TestApplyBoardEventCommentIsIdempotent(t *testing.T) {
	svc, store, b, out := ingestBoard(t)
	if _, err := svc.CreateTask(context.Background(), b.ID, "col-1", "local", "", "member-ana", nil, nil, "member-ana"); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	commentPayload, _ := json.Marshal(core.Comment{ID: "comment-1", TaskID: "task-1", MemberID: "member-bob", Text: "hola", CreatedBy: "member-bob", CreatedAt: time.Now()})
	// The local db owns "task-1", created above with a server-generated id;
	// find it (ListTasks) so the comment really lands on a local task.
	tasks, _ := svc.ListTasks(context.Background(), b.ID)
	commentPayload, _ = json.Marshal(core.Comment{ID: "comment-1", TaskID: tasks[0].ID, MemberID: "member-bob", Text: "hola", CreatedBy: "member-bob", CreatedAt: time.Now()})
	*out = nil // los eventos de la preparación local no cuentan

	if err := svc.ApplyBoardEvent(core.BoardEvent{BoardID: b.ID, MemberID: "member-bob", Type: "task.comment", Payload: commentPayload}); err != nil {
		t.Fatalf("ApplyBoardEvent: %v", err)
	}
	if err := svc.ApplyBoardEvent(core.BoardEvent{BoardID: b.ID, MemberID: "member-bob", Type: "task.comment", Payload: commentPayload}); err != nil {
		t.Fatalf("Repeat ApplyBoardEvent: %v", err)
	}
	comments, err := store.ListComments(tasks[0].ID)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("comments = %d, want 1 (idempotent re-delivery)", len(comments))
	}
	if len(*out) != 0 {
		t.Fatalf("applied delta re-notified the transport: %+v", *out)
	}
}

// An event whose board is not stored locally is rejected rather than
// polluting the local store with unknown boards.
func TestApplyBoardEventRejectsUnknownBoard(t *testing.T) {
	svc, _, _, _ := ingestBoard(t)
	payload, _ := json.Marshal(core.Task{ID: "task-x", BoardID: "board-unknow", ColumnID: "col-1", Title: "x", Version: 0})
	if err := svc.ApplyBoardEvent(core.BoardEvent{BoardID: "board-unknow", MemberID: "member-bob", Type: "task.create", Payload: payload, Version: 0}); !errors.Is(err, core.ErrBoardNotFound) {
		t.Fatalf("ApplyBoardEvent error = %v, want ErrBoardNotFound", err)
	}
}
