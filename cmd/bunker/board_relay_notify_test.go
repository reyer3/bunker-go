package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/relay"
	"github.com/reyer3/bunker-go/internal/store"
)

// relaySyncClient es un cliente de prueba que se conecta al relay real y
// lee mensajes JSON de una linea.
type relaySyncClient struct {
	conn   net.Conn
	reader *bufio.Reader
}

func dialRelaySync(t *testing.T, addr string) *relaySyncClient {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	return &relaySyncClient{conn: conn, reader: bufio.NewReader(conn)}
}

func (c *relaySyncClient) close() { _ = c.conn.Close() }

func (c *relaySyncClient) join(t *testing.T, boardID, memberID string) {
	t.Helper()
	line, err := json.Marshal(relay.RelayMessage{
		BoardID:  boardID,
		MemberID: memberID,
		Type:     "member.join",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	line = append(line, '\n')
	if _, err := c.conn.Write(line); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func (c *relaySyncClient) setReadDeadline(d time.Duration) {
	c.conn.SetReadDeadline(time.Now().Add(d))
}

func (c *relaySyncClient) clearReadDeadline() {
	c.conn.SetReadDeadline(time.Time{})
}

func (c *relaySyncClient) receive(t *testing.T) *relay.RelayMessage {
	t.Helper()
	c.setReadDeadline(10 * time.Second)
	defer c.clearReadDeadline()
	line, err := c.reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var msg relay.RelayMessage
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return &msg
}

// TestBoardServiceRelayNotify verifica end-to-end que un cambio de tarea en
// BoardService llega al relay y lo reciben los demas miembros del board:
// task.create, task.move, task.done y task.comment.
func TestBoardServiceRelayNotify(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bunker.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	r := relay.NewRelay("127.0.0.1:0")
	r.SetAdmission(admitAllForTest) // prueba el enrutado, no la admisión
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("relay.Start: %v", err)
	}
	defer r.Stop()

	svc := core.NewBoardService(st)
	svc.SetBoardNotifier(func(ev core.BoardEvent) error {
		msg := relay.RelayMessage{
			BoardID:   ev.BoardID,
			MemberID:  ev.MemberID,
			Type:      ev.Type,
			Payload:   ev.Payload,
			Timestamp: ev.Timestamp,
			Version:   ev.Version,
		}
		return r.Publish(msg)
	})

	board, err := svc.CreateBoard(ctx, "Tablero de prueba", "member-ana")
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	for _, col := range []*core.Column{
		{ID: "col-1", Board: board.ID, Name: "Por hacer", Order: 0, Color: "#4db0ff"},
		{ID: "col-2", Board: board.ID, Name: "En progreso", Order: 1, Color: "#ffb04d"},
	} {
		if err := st.CreateColumn(col); err != nil {
			t.Fatalf("CreateColumn: %v", err)
		}
	}

	// member-2 se une al board por el relay para recibir la sincronizacion.
	c2 := dialRelaySync(t, r.Addr().String())
	t.Cleanup(c2.close)
	c2.join(t, board.ID, "member-2")
	waitForJoin()

	// member-ana crea una tarea: member-2 recibe task.create.
	task, err := svc.CreateTask(ctx, board.ID, "col-1", "Hacer compras", "", "member-ana", nil, nil, "member-ana")
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	got := c2.receive(t)
	if got.Type != "task.create" || got.BoardID != board.ID || got.MemberID != "member-ana" {
		t.Errorf("create: got %+v, want task.create on %q by member-ana", got, board.ID)
	}
	var created core.Task
	if err := json.Unmarshal(got.Payload, &created); err != nil {
		t.Fatalf("unmarshal create payload: %v", err)
	}
	if created.ID != task.ID {
		t.Errorf("create payload ID = %q, want %q", created.ID, task.ID)
	}

	// member-ana la mueve de columna (version 0 -> 1): task.move.
	if _, err := svc.MoveTask(ctx, task.ID, "col-2"); err != nil {
		t.Fatalf("MoveTask: %v", err)
	}
	got = c2.receive(t)
	if got.Type != "task.move" || got.BoardID != board.ID {
		t.Errorf("move: got %+v, want task.move on %q", got, board.ID)
	}

	// member-ana la completa (version 1 -> 2): task.done.
	if _, err := svc.DoneTask(ctx, task.ID, 1); err != nil {
		t.Fatalf("DoneTask: %v", err)
	}
	got = c2.receive(t)
	if got.Type != "task.done" || got.BoardID != board.ID {
		t.Errorf("done: got %+v, want task.done on %q", got, board.ID)
	}

	// member-ana comenta: task.comment.
	if _, err := svc.AddComment(ctx, task.ID, "member-ana", "Listo"); err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	got = c2.receive(t)
	if got.Type != "task.comment" || got.BoardID != board.ID || got.MemberID != "member-ana" {
		t.Errorf("comment: got %+v, want task.comment on %q by member-ana", got, board.ID)
	}
}
