package core_test

import (
	"context"
	"errors"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

// boardOnlyStore satisface core.Store solo para que NewService cablee el
// BoardService: los tests del claim nunca tocan los métodos de items.
type boardOnlyStore struct {
	core.Store
	*memBoardStore
}

// claimPeer es un transporte en memoria entre dos Service: el del creador
// registra su respuesta con OnClaim, y Request del joiner la invoca directo,
// el mismo ida y vuelta que hacen el stream libp2p y la conexión del relay.
type claimPeer struct {
	fakeBoardPeer
	answer func(core.BoardPeerMessage) core.BoardPeerMessage
	remote *claimPeer
}

func (p *claimPeer) OnClaim(fn func(core.BoardPeerMessage) core.BoardPeerMessage) { p.answer = fn }

func (p *claimPeer) Request(ctx context.Context, msg core.BoardPeerMessage) (core.BoardPeerMessage, error) {
	if p.remote == nil || p.remote.answer == nil {
		return core.BoardPeerMessage{}, errors.New("sin peer que atienda")
	}
	return p.remote.answer(msg), nil
}

type peerPair struct {
	creatorStore, joinerStore *memBoardStore
	creator, joiner           *core.Service
	board                     *core.Board
	invite                    *core.InviteCode
}

func newPeerPair(t *testing.T) *peerPair {
	t.Helper()
	p := &peerPair{creatorStore: newMemBoardStore(), joinerStore: newMemBoardStore()}
	p.creator = core.NewService(boardOnlyStore{memBoardStore: p.creatorStore}, core.NewRegistry())
	p.joiner = core.NewService(boardOnlyStore{memBoardStore: p.joinerStore}, core.NewRegistry())
	creatorPeer, joinerPeer := &claimPeer{}, &claimPeer{}
	joinerPeer.remote = creatorPeer
	p.creator.SetBoardPeer(creatorPeer)
	p.joiner.SetBoardPeer(joinerPeer)

	ctx := context.Background()
	b, err := p.creator.BoardCreate(ctx, "Compartido", "device-creador")
	if err != nil {
		t.Fatal(err)
	}
	p.board = b
	inv, err := p.creator.BoardInvite(ctx, b.ID, "device-creador", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	p.invite = inv
	return p
}

// SetBoardPeer deja al Service atendiendo los claims que llegan por el peer:
// el joiner pide por P2P y termina con el board, sin tocar el store del
// creador a mano.
func TestServiceClaimOverPeer(t *testing.T) {
	p := newPeerPair(t)
	member, err := p.joiner.BoardClaimPeer(context.Background(), p.invite.Code, p.board.ID, "member-carla", "Carla", "pk")
	if err != nil {
		t.Fatalf("BoardClaimPeer: %v", err)
	}
	if member.ID != "member-carla" {
		t.Fatalf("member = %+v", member)
	}
	if _, err := p.joinerStore.GetBoard(p.board.ID); err != nil {
		t.Fatalf("el joiner no adoptó el board: %v", err)
	}
	if _, err := p.creatorStore.GetMember(p.board.ID, "member-carla"); err != nil {
		t.Fatalf("el creador no registró al miembro: %v", err)
	}
}

func TestServiceClaimOverPeerRejectIsLoud(t *testing.T) {
	p := newPeerPair(t)
	_, err := p.joiner.BoardClaimPeer(context.Background(), "bunker-invite-otro", p.board.ID, "member-carla", "Carla", "pk")
	if !errors.Is(err, core.ErrInviteInvalid) {
		t.Fatalf("err = %v, want ErrInviteInvalid", err)
	}
	if _, gerr := p.joinerStore.GetBoard(p.board.ID); gerr == nil {
		t.Fatal("el joiner adoptó el board pese al rechazo")
	}
}

func TestServiceClaimPeerWithoutRequesterIsUnsupported(t *testing.T) {
	svc := core.NewService(boardOnlyStore{memBoardStore: newMemBoardStore()}, core.NewRegistry())
	svc.SetBoardPeer(&fakeBoardPeer{})
	_, err := svc.BoardClaimPeer(context.Background(), "c", "b", "m", "n", "pk")
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

// El sobre del mensaje y el claim deben coincidir: el relay registra al
// cliente con el board/miembro del sobre, así que un sobre que dice otra
// cosa que el claim firmado es un intento de registrarse con identidad
// ajena.
func TestBoardAnswerRejectsEnvelopeMismatch(t *testing.T) {
	p := newPeerPair(t)
	msg, err := core.NewClaimMessage(p.invite.Code, p.board.ID, "member-carla", "Carla", "pk")
	if err != nil {
		t.Fatal(err)
	}
	msg.MemberID = "member-intruso"
	if _, err := p.creator.BoardAnswer(msg); !errors.Is(err, core.ErrInviteInvalid) {
		t.Fatalf("err = %v, want ErrInviteInvalid", err)
	}
}

// member.join solo registra a un miembro existente que prueba conocer la
// clave del board; sin la prueba correcta es un rechazo.
func TestBoardAnswerJoinRequiresMembershipProof(t *testing.T) {
	p := newPeerPair(t)
	if _, err := p.joiner.BoardClaimPeer(context.Background(), p.invite.Code, p.board.ID, "member-carla", "Carla", "pk"); err != nil {
		t.Fatal(err)
	}
	joinerBoard, _ := p.joinerStore.GetBoard(p.board.ID)

	ok := core.NewJoinMessage(joinerBoard, "member-carla")
	if _, err := p.creator.BoardAnswer(ok); err != nil {
		t.Fatalf("join válido rechazado: %v", err)
	}
	stranger := core.NewJoinMessage(joinerBoard, "member-desconocido")
	if _, err := p.creator.BoardAnswer(stranger); !errors.Is(err, core.ErrInviteInvalid) {
		t.Fatalf("miembro desconocido: err = %v, want ErrInviteInvalid", err)
	}
	forged := core.NewJoinMessage(&core.Board{ID: p.board.ID, EncryptionKey: []byte("otra-clave-de-32-bytes-xxxxxxxxx")}, "member-carla")
	if _, err := p.creator.BoardAnswer(forged); !errors.Is(err, core.ErrInviteInvalid) {
		t.Fatalf("prueba falsa: err = %v, want ErrInviteInvalid", err)
	}
	if _, err := p.creator.BoardAnswer(core.BoardPeerMessage{BoardID: p.board.ID, MemberID: "member-carla", Type: "member.join"}); !errors.Is(err, core.ErrInviteInvalid) {
		t.Fatalf("join sin prueba: err = %v, want ErrInviteInvalid", err)
	}
}
