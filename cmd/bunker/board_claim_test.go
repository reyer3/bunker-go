package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/p2p"
	"github.com/reyer3/bunker-go/internal/relay"
	"github.com/reyer3/bunker-go/internal/store"
)

// claimDevice es un daemon en proceso: su propio store SQLite y su Service.
type claimDevice struct {
	st  *store.Store
	svc *core.Service
}

func newClaimDevice(t *testing.T) *claimDevice {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "bunker.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &claimDevice{st: st, svc: core.NewService(st, core.NewRegistry())}
}

// creatorWithInvite deja al creador con un board, una columna y una
// invitación vigente.
func creatorWithInvite(t *testing.T, d *claimDevice) (*core.Board, *core.InviteCode) {
	t.Helper()
	ctx := context.Background()
	b, err := d.svc.BoardCreate(ctx, "Compartido", "device-creador")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.st.CreateColumn(&core.Column{ID: "col-1", Board: b.ID, Name: "Por hacer"}); err != nil {
		t.Fatal(err)
	}
	inv, err := d.svc.BoardInvite(ctx, b.ID, "device-creador", time.Hour, 1)
	if err != nil {
		t.Fatal(err)
	}
	return b, inv
}

// Dos daemons en proceso, unidos solo por P2P loopback: invitación en el
// creador, claim desde el otro, y un delta cifrado del creador que el
// joiner descifra y aplica con la clave que recibió en el welcome.
func TestBoardClaimOverP2PThenSyncsDelta(t *testing.T) {
	creator, joiner := newClaimDevice(t), newClaimDevice(t)
	b, inv := creatorWithInvite(t, creator)

	cNode, err := p2p.NewNode("/ip4/127.0.0.1/tcp/0")
	if err != nil {
		t.Fatal(err)
	}
	defer cNode.Stop()
	jNode, err := p2p.NewNode("/ip4/127.0.0.1/tcp/0")
	if err != nil {
		t.Fatal(err)
	}
	defer jNode.Stop()
	creator.svc.SetBoardPeer(newPeerTransport(cNode))
	joiner.svc.SetBoardPeer(newPeerTransport(jNode))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := joiner.svc.BoardPeerConnect(ctx, cNode.FullAddrs()[0]); err != nil {
		t.Fatalf("connect: %v", err)
	}

	member, err := joiner.svc.BoardClaimPeer(ctx, inv.Code, b.ID, "member-carla", "Carla", "pk")
	if err != nil {
		t.Fatalf("BoardClaimPeer: %v", err)
	}
	if member.ID != "member-carla" {
		t.Fatalf("member = %+v", member)
	}
	if _, err := creator.st.GetMember(b.ID, "member-carla"); err != nil {
		t.Fatalf("el creador no registró al miembro: %v", err)
	}

	task, _ := json.Marshal(core.Task{ID: "task-1", BoardID: b.ID, ColumnID: "col-1", Title: "Compras"})
	sealed, err := encryptBoardDelta(creator.st, core.BoardEvent{BoardID: b.ID, Type: "task.create", Payload: task})
	if err != nil {
		t.Fatalf("encryptBoardDelta: %v", err)
	}
	var stderr bytes.Buffer
	boardSyncConsumer(joiner.st, joiner.svc, &stderr)(b.ID, "device-creador", "task.create", sealed, 0, 0)
	if got, err := joiner.st.GetTask("task-1"); err != nil || got.Title != "Compras" {
		t.Fatalf("el joiner no aplicó el delta: %+v, %v (stderr %q)", got, err, stderr.String())
	}
}

// Por el relay: el CLI del joiner habla con el relay del creador, que
// valida el claim en su store; un código ajeno vuelve como rechazo ruidoso.
func TestBoardClaimOverRelay(t *testing.T) {
	creator, joiner := newClaimDevice(t), newClaimDevice(t)
	b, inv := creatorWithInvite(t, creator)

	r := relay.NewRelay("127.0.0.1:0")
	r.SetAdmission(boardRelayAdmission(creator.svc))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer r.Stop()
	addr := r.Addr().String()

	// Código equivocado: rechazo y nada adoptado.
	if _, err := claimViaRelay(ctx, joiner.svc, addr, "bunker-invite-otro", b.ID, "member-carla", "Carla", "pk"); !errors.Is(err, core.ErrInviteInvalid) {
		t.Fatalf("código ajeno: err = %v, want ErrInviteInvalid", err)
	}
	if _, err := joiner.st.GetBoard(b.ID); err == nil {
		t.Fatal("el joiner adoptó el board pese al rechazo")
	}

	member, err := claimViaRelay(ctx, joiner.svc, addr, inv.Code, b.ID, "member-carla", "Carla", "pk")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if member.ID != "member-carla" {
		t.Fatalf("member = %+v", member)
	}
	jb, err := joiner.st.GetBoard(b.ID)
	if err != nil {
		t.Fatalf("el joiner no adoptó el board: %v", err)
	}

	// Al reconectar, el miembro se registra con member.join firmado con la
	// clave del board; uno sin prueba válida es rechazado.
	if reply := rawRelayExchange(t, addr, core.NewJoinMessage(jb, "member-carla")); reply.Type == core.RejectMessageType {
		t.Fatalf("join válido rechazado: %s", reply.Payload)
	}
	forged := core.BoardPeerMessage{BoardID: b.ID, MemberID: "member-carla", Type: core.JoinMessageType}
	if reply := rawRelayExchange(t, addr, forged); reply.Type != core.RejectMessageType {
		t.Fatalf("join sin prueba: reply = %+v, want member.reject", reply)
	}
}

// rawRelayExchange manda msg al relay y devuelve la respuesta, o un
// mensaje vacío si no hay respuesta en un rato (un join aceptado no
// responde nada).
func rawRelayExchange(t *testing.T, addr string, msg core.BoardPeerMessage) relay.RelayMessage {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(toRelayMessage(msg)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return relay.RelayMessage{}
	}
	var reply relay.RelayMessage
	_ = json.Unmarshal(line, &reply)
	return reply
}

// admitAllForTest admite cualquier join sin respuesta, para los tests que
// prueban el enrutado del relay y no su admisión.
func admitAllForTest(relay.RelayMessage) (*relay.RelayMessage, error) { return nil, nil }
