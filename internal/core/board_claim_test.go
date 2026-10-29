package core_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/board"
	"github.com/reyer3/bunker-go/internal/core"
)

// El dispositivo que se une cuenta con un welcome cifrado (clave derivada
// del codigo de invitación) y debe terminar con el board completo — con su
// clave simétrica, para cifrar sus propios deltas — y su membership local.
func TestAdoptRemoteBoardStoresBoardAndMember(t *testing.T) {
	store := newMemBoardStore()
	svc := core.NewBoardService(store)

	crypto, err := board.NewBoardCrypto()
	if err != nil {
		t.Fatal(err)
	}
	key := crypto.PublicKey()
	remoteBoard := &core.Board{
		ID:            "board-rem",
		Name:          "Compartido",
		OwnerID:       "device-creador",
		EncryptionKey: key,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	member := &core.Member{
		ID:        "member-carla",
		BoardID:   remoteBoard.ID,
		Name:      "Carla",
		PublicKey: string(key),
		JoinedAt:  time.Now(),
		Role:      "member",
	}

	if err := svc.AdoptRemoteBoard(remoteBoard, member); err != nil {
		t.Fatalf("AdoptRemoteBoard: %v", err)
	}
	row, err := store.GetBoard(remoteBoard.ID)
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	if !bytes.Equal(row.EncryptionKey, key) {
		t.Fatalf("EncryptionKey = %q, want la clave que trajo el welcome", row.EncryptionKey)
	}
	if got, err := store.GetMember(remoteBoard.ID, member.ID); err != nil || got.Name != "Carla" {
		t.Fatalf("member = %+v, err = %v", got, err)
	}
	// Reclamar dos veces es idempotente (reintento de claim): el board y el
	// miembro ya existen y no se destruyeron los datos.
	if err := svc.AdoptRemoteBoard(remoteBoard, member); err != nil {
		t.Fatalf("AdoptRemoteBoard repetido: %v", err)
	}

	// Un welcome SIN clave no se adopta: sin ella el device no puede
	// descifrar deltas ni cifrar los suyos (el E2EE se rompería en silencio).
	keyless := &core.Board{ID: "board-sinclave", Name: "sin clave", OwnerID: "x"}
	if err := svc.AdoptRemoteBoard(keyless, &core.Member{ID: "m2", BoardID: "board-sinclave"}); err == nil {
		t.Fatal("AdoptRemoteBoard sin clave de cifrado no fue rechazado")
	}
}

// claimFixture arma dos dispositivos con stores separados: el creador
// (tiene el board, sus columnas y la invitación) y el que se une (no tiene
// nada). El "peer" entre ambos es una llamada directa a HandleClaim: es el
// mismo intercambio que hacen el relay y el transporte P2P, sin red.
type claimFixture struct {
	creatorStore *memBoardStore
	creator      *core.BoardService
	joinerStore  *memBoardStore
	joiner       *core.BoardService
	board        *core.Board
	invite       *core.InviteCode
}

func newClaimFixture(t *testing.T, maxUses int) *claimFixture {
	t.Helper()
	f := &claimFixture{creatorStore: newMemBoardStore(), joinerStore: newMemBoardStore()}
	f.creator = core.NewBoardService(f.creatorStore)
	f.joiner = core.NewBoardService(f.joinerStore)
	ctx := context.Background()
	b, err := f.creator.CreateBoard(ctx, "Compartido", "device-creador")
	if err != nil {
		t.Fatal(err)
	}
	f.board = b
	if err := f.creatorStore.CreateColumn(&core.Column{ID: "col-todo", Board: b.ID, Name: "Por hacer"}); err != nil {
		t.Fatal(err)
	}
	inv, err := f.creator.GenerateInviteCode(ctx, b.ID, "device-creador", time.Hour, maxUses)
	if err != nil {
		t.Fatal(err)
	}
	f.invite = inv
	return f
}

// claim ejecuta el viaje completo joiner -> creador -> joiner con code.
func (f *claimFixture) claim(code, boardID, memberID string) (*core.Member, error) {
	req := core.NewBoardClaim(code, boardID, memberID, "Carla", "pk-carla")
	sealed, err := f.creator.HandleClaim(req)
	if err != nil {
		return nil, err
	}
	return f.joiner.AcceptWelcome(code, boardID, sealed)
}

func TestBoardClaimRoundTrip(t *testing.T) {
	f := newClaimFixture(t, 0)

	member, err := f.claim(f.invite.Code, f.board.ID, "member-carla")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if member.ID != "member-carla" || member.BoardID != f.board.ID || member.Role != "member" {
		t.Fatalf("member = %+v", member)
	}
	// El joiner termina con el board completo, su clave y las columnas.
	got, err := f.joinerStore.GetBoard(f.board.ID)
	if err != nil {
		t.Fatalf("joiner GetBoard: %v", err)
	}
	if !bytes.Equal(got.EncryptionKey, f.board.EncryptionKey) {
		t.Fatal("el joiner no recibió la clave del board")
	}
	if cols, _ := f.joinerStore.ListColumns(f.board.ID); len(cols) != 1 || cols[0].ID != "col-todo" {
		t.Fatalf("joiner columns = %+v", cols)
	}
	// El creador registró al miembro y consumió un uso.
	if _, err := f.creatorStore.GetMember(f.board.ID, "member-carla"); err != nil {
		t.Fatalf("creator GetMember: %v", err)
	}
	if inv, _ := f.creatorStore.GetInviteCode(f.invite.Code); inv.Uses != 1 {
		t.Fatalf("invite uses = %d, want 1", inv.Uses)
	}
	// El claim nunca lleva el código en claro.
	req := core.NewBoardClaim(f.invite.Code, f.board.ID, "member-carla", "Carla", "pk-carla")
	raw, _ := json.Marshal(req)
	if bytes.Contains(raw, []byte(f.invite.Code)) {
		t.Fatalf("el claim lleva el código en claro: %s", raw)
	}
}

// Un reintento del mismo miembro (welcome perdido en la red) es idempotente:
// vuelve a recibir el welcome sin gastar otro uso, aunque el código ya no
// tenga usos libres para nadie más.
func TestBoardClaimRetrySameMemberIsIdempotent(t *testing.T) {
	f := newClaimFixture(t, 1)
	if _, err := f.claim(f.invite.Code, f.board.ID, "member-carla"); err != nil {
		t.Fatalf("primer claim: %v", err)
	}
	if _, err := f.claim(f.invite.Code, f.board.ID, "member-carla"); err != nil {
		t.Fatalf("reintento: %v", err)
	}
	if inv, _ := f.creatorStore.GetInviteCode(f.invite.Code); inv.Uses != 1 {
		t.Fatalf("invite uses = %d, want 1", inv.Uses)
	}
}

func TestBoardClaimRejections(t *testing.T) {
	t.Run("expirado", func(t *testing.T) {
		f := newClaimFixture(t, 0)
		f.invite.ExpiresAt = time.Now().Add(-time.Minute)
		if err := f.creatorStore.UpdateInviteCode(f.invite); err != nil {
			t.Fatal(err)
		}
		_, err := f.claim(f.invite.Code, f.board.ID, "member-carla")
		assertClaimRejected(t, f, err)
	})
	t.Run("revocado", func(t *testing.T) {
		f := newClaimFixture(t, 0)
		f.invite.Revoked = true
		if err := f.creatorStore.UpdateInviteCode(f.invite); err != nil {
			t.Fatal(err)
		}
		_, err := f.claim(f.invite.Code, f.board.ID, "member-carla")
		assertClaimRejected(t, f, err)
	})
	t.Run("sin usos", func(t *testing.T) {
		f := newClaimFixture(t, 1)
		if _, err := f.claim(f.invite.Code, f.board.ID, "member-ana"); err != nil {
			t.Fatalf("primer claim: %v", err)
		}
		// Carla es otro dispositivo, con su propio store vacío.
		f.joinerStore = newMemBoardStore()
		f.joiner = core.NewBoardService(f.joinerStore)
		_, err := f.claim(f.invite.Code, f.board.ID, "member-carla")
		assertClaimRejected(t, f, err)
	})
	t.Run("codigo incorrecto", func(t *testing.T) {
		f := newClaimFixture(t, 0)
		_, err := f.claim("bunker-invite-otro", f.board.ID, "member-carla")
		assertClaimRejected(t, f, err)
	})
	t.Run("otro board", func(t *testing.T) {
		f := newClaimFixture(t, 0)
		other, err := f.creator.CreateBoard(context.Background(), "Otro", "device-creador")
		if err != nil {
			t.Fatal(err)
		}
		// El código es válido, pero para f.board: declarar otro board no
		// debe abrir la puerta a ese otro board.
		_, err = f.claim(f.invite.Code, other.ID, "member-carla")
		if !errors.Is(err, core.ErrInviteInvalid) {
			t.Fatalf("err = %v, want ErrInviteInvalid", err)
		}
		if _, gerr := f.creatorStore.GetMember(other.ID, "member-carla"); gerr == nil {
			t.Fatal("el creador agregó al miembro a otro board")
		}
	})
	t.Run("board inexistente", func(t *testing.T) {
		f := newClaimFixture(t, 0)
		_, err := f.claim(f.invite.Code, "board-que-no-existe", "member-carla")
		if !errors.Is(err, core.ErrInviteInvalid) {
			t.Fatalf("err = %v, want ErrInviteInvalid", err)
		}
	})
}

func assertClaimRejected(t *testing.T, f *claimFixture, err error) {
	t.Helper()
	if !errors.Is(err, core.ErrInviteInvalid) {
		t.Fatalf("err = %v, want ErrInviteInvalid", err)
	}
	if _, gerr := f.creatorStore.GetMember(f.board.ID, "member-carla"); gerr == nil {
		t.Fatal("el creador agregó al miembro pese al rechazo")
	}
	if _, gerr := f.joinerStore.GetBoard(f.board.ID); gerr == nil {
		t.Fatal("el joiner adoptó el board pese al rechazo")
	}
}

// El joiner solo adopta un welcome que se descifra con SU código y que es
// del board que pidió: un welcome ajeno o manipulado es un error ruidoso.
func TestBoardAcceptWelcomeRejectsForeignWelcome(t *testing.T) {
	f := newClaimFixture(t, 0)
	sealed, err := f.creator.HandleClaim(core.NewBoardClaim(f.invite.Code, f.board.ID, "member-carla", "Carla", "pk"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.joiner.AcceptWelcome("bunker-invite-otro", f.board.ID, sealed); !errors.Is(err, core.ErrInviteInvalid) {
		t.Fatalf("otro código: err = %v, want ErrInviteInvalid", err)
	}
	if _, err := f.joiner.AcceptWelcome(f.invite.Code, "board-distinto", sealed); !errors.Is(err, core.ErrInviteInvalid) {
		t.Fatalf("otro board: err = %v, want ErrInviteInvalid", err)
	}
	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 0xff
	if _, err := f.joiner.AcceptWelcome(f.invite.Code, f.board.ID, tampered); !errors.Is(err, core.ErrInviteInvalid) {
		t.Fatalf("manipulado: err = %v, want ErrInviteInvalid", err)
	}
	if _, gerr := f.joinerStore.GetBoard(f.board.ID); gerr == nil {
		t.Fatal("el joiner adoptó un welcome inválido")
	}
}
