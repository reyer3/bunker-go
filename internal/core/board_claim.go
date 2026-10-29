package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/reyer3/bunker-go/internal/board"
)

// Tipos de mensaje del claim de invitación sobre el relay y el transporte
// P2P. Viven en core porque ambos transportes y el daemon los comparten, y
// core es el único paquete que todos ellos importan.
const (
	// ClaimMessageType es la petición de quien se une: un BoardClaim en JSON.
	ClaimMessageType = "member.claim"
	// WelcomeMessageType es la respuesta del creador: el BoardWelcome
	// cifrado con la clave del claim.
	WelcomeMessageType = "member.welcome"
	// RejectMessageType es la respuesta a un claim o join rechazado; el
	// payload es el motivo en texto, para que quien se une falle ruidoso.
	RejectMessageType = "member.reject"
)

// claimProofDomain separa la prueba del claim de cualquier otra firma hecha
// con la misma clave.
const claimProofDomain = "member.claim"

// BoardClaim es el payload de member.claim: lo que el dispositivo que se une
// manda al creador para pedir su membership. Nunca lleva el código de
// invitación: Proof demuestra que quien lo envía lo conoce, ligado al board,
// al member y a su clave pública, así que el relay o un peer que lo vea no
// puede reusarlo para otra identidad ni para otro board.
type BoardClaim struct {
	BoardID   string `json:"board_id"`
	MemberID  string `json:"member_id"`
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
	Proof     string `json:"proof"`
}

// BoardWelcome es lo que el creador devuelve (cifrado con la clave del
// claim) a un claim aceptado: el board con su clave simétrica, la
// membership creada y las columnas, lo necesario para que el dispositivo
// nuevo trabaje y sincronice sin otra consulta.
type BoardWelcome struct {
	Board   *Board    `json:"board"`
	Member  *Member   `json:"member"`
	Columns []*Column `json:"columns"`
}

// NewBoardClaim arma el member.claim para code. memberID vacío genera uno:
// la prueba liga el id del miembro, así que hay que decidirlo antes de
// firmar y no puede rellenarlo el creador.
func NewBoardClaim(code, boardID, memberID, name, publicKey string) BoardClaim {
	if memberID == "" {
		memberID = newID()
	}
	c := BoardClaim{BoardID: boardID, MemberID: memberID, Name: name, PublicKey: publicKey}
	c.Proof = claimProof(code, c)
	return c
}

func claimProof(code string, c BoardClaim) string {
	return board.Proof(board.InviteCodeClaimKey(code), claimProofDomain, c.BoardID, c.MemberID, c.Name, c.PublicKey)
}

// HandleClaim es el lado del creador: valida el claim contra las
// invitaciones del board (la que coincide con la prueba, vigente, no
// revocada y con usos libres), crea al miembro, consume un uso y devuelve
// el BoardWelcome cifrado con la clave del claim, que solo quien conoce el
// código puede abrir.
//
// Un reintento del mismo miembro (el welcome anterior se perdió en la red)
// vuelve a recibir el welcome sin gastar otro uso, aunque la invitación ya
// no tenga usos libres para nadie más. Todo rechazo envuelve
// ErrInviteInvalid y no toca el store.
func (s *BoardService) HandleClaim(c BoardClaim) ([]byte, error) {
	if c.BoardID == "" || c.MemberID == "" || c.Proof == "" {
		return nil, fmt.Errorf("core: claim: falta board, miembro o prueba: %w", ErrInviteInvalid)
	}
	b, err := s.store.GetBoard(c.BoardID)
	if err != nil {
		if errors.Is(err, ErrBoardNotFound) {
			return nil, fmt.Errorf("core: claim: board %s: %w", c.BoardID, ErrInviteInvalid)
		}
		return nil, fmt.Errorf("core: claim: board %s: %w", c.BoardID, err)
	}
	invite, err := s.matchClaimInvite(c)
	if err != nil {
		return nil, err
	}
	if invite.IsExpired() || invite.IsRevoked() {
		return nil, fmt.Errorf("core: claim: invitación vencida o revocada: %w", ErrInviteInvalid)
	}

	member, err := s.store.GetMember(c.BoardID, c.MemberID)
	switch {
	case err == nil:
		// Reintento del mismo miembro: no se gasta otro uso.
	case errors.Is(err, ErrMemberNotFound):
		if invite.UsesExceeded() {
			return nil, fmt.Errorf("core: claim: la invitación ya no tiene usos libres: %w", ErrInviteInvalid)
		}
		member = &Member{
			ID:        c.MemberID,
			BoardID:   c.BoardID,
			Name:      c.Name,
			PublicKey: c.PublicKey,
			JoinedAt:  time.Now(),
			Role:      "member",
		}
		if err := s.store.CreateMember(member); err != nil {
			return nil, fmt.Errorf("core: claim: crear miembro %s: %w", member.ID, err)
		}
		invite.Uses++
		if err := s.store.UpdateInviteCode(invite); err != nil {
			return nil, fmt.Errorf("core: claim: consumir invitación: %w", err)
		}
	default:
		return nil, fmt.Errorf("core: claim: miembro %s: %w", c.MemberID, err)
	}

	cols, err := s.store.ListColumns(c.BoardID)
	if err != nil {
		return nil, fmt.Errorf("core: claim: columnas de %s: %w", c.BoardID, err)
	}
	plain, err := json.Marshal(BoardWelcome{Board: b, Member: member, Columns: cols})
	if err != nil {
		return nil, fmt.Errorf("core: claim: codificar welcome: %w", err)
	}
	return sealWithClaimKey(invite.Code, plain)
}

// matchClaimInvite busca, entre las invitaciones del board del claim, la
// cuya clave reproduce la prueba. Solo se miran las de ese board: un código
// válido para otro board no abre éste.
func (s *BoardService) matchClaimInvite(c BoardClaim) (*InviteCode, error) {
	invites, err := s.store.ListInviteCodes(c.BoardID)
	if err != nil {
		return nil, fmt.Errorf("core: claim: invitaciones de %s: %w", c.BoardID, err)
	}
	for _, inv := range invites {
		if board.ProofEqual(c.Proof, claimProof(inv.Code, c)) {
			return inv, nil
		}
	}
	return nil, fmt.Errorf("core: claim: ninguna invitación de %s coincide con la prueba: %w", c.BoardID, ErrInviteInvalid)
}

// AcceptWelcome es el lado de quien se une: descifra el welcome con la
// clave de su código, comprueba que es del board que pidió y lo adopta
// (board con su clave, membership y columnas). Un welcome que no se
// descifra, que trae otro board o que viene sin clave envuelve
// ErrInviteInvalid y no toca el store.
func (s *BoardService) AcceptWelcome(code, boardID string, sealed []byte) (*Member, error) {
	plain, err := openWithClaimKey(code, sealed)
	if err != nil {
		return nil, fmt.Errorf("core: welcome de %s: %v: %w", boardID, err, ErrInviteInvalid)
	}
	var w BoardWelcome
	if err := json.Unmarshal(plain, &w); err != nil {
		return nil, fmt.Errorf("core: welcome de %s: %v: %w", boardID, err, ErrInviteInvalid)
	}
	if w.Board == nil || w.Member == nil || w.Board.ID != boardID || w.Member.BoardID != boardID {
		return nil, fmt.Errorf("core: welcome: no corresponde al board %s: %w", boardID, ErrInviteInvalid)
	}
	for _, col := range w.Columns {
		if col.Board != boardID {
			return nil, fmt.Errorf("core: welcome: columna %s de otro board: %w", col.ID, ErrInviteInvalid)
		}
	}
	if err := s.AdoptRemoteBoard(w.Board, w.Member); err != nil {
		return nil, err
	}
	for _, col := range w.Columns {
		if err := s.store.CreateColumn(col); err != nil && !errors.Is(err, ErrColumnExists) {
			return nil, fmt.Errorf("core: adopt column %s: %w", col.ID, err)
		}
	}
	return w.Member, nil
}

// AdoptRemoteBoard adopta el board y la membership que el dispositivo
// creador devolvió en un member.welcome: el store local termina con el
// board completo (id, nombre y la clave simétrica del board, que es la que
// descifra los deltas y cifra los propios) y su fila de member. Es el paso
// final del claim de invitación sobre el transporte P2P/relay.
//
// Un welcome sin EncryptionKey se rechaza: sin clave el device no podría
// descifrar los deltas del board ni cifrar los propios, y el E2EE se
// rompería en silencio. Reclamar dos veces (claim repetido, re-join) es
// idempotente: el board y el member ya guardados permanecen intactos.
func (s *BoardService) AdoptRemoteBoard(remoteBoard *Board, member *Member) error {
	if remoteBoard.EncryptionKey == nil {
		return fmt.Errorf("core: adopt board %s: welcome sin clave de cifrado: %w", remoteBoard.ID, ErrInviteInvalid)
	}
	if err := s.store.CreateBoard(remoteBoard); err != nil && !errors.Is(err, ErrBoardExists) {
		return fmt.Errorf("core: adopt board %s: %w", remoteBoard.ID, err)
	}
	if err := s.store.CreateMember(member); err != nil && !errors.Is(err, ErrMemberExists) {
		return fmt.Errorf("core: adopt member %s: %w", member.ID, err)
	}
	return nil
}

func sealWithClaimKey(code string, plain []byte) ([]byte, error) {
	c, err := board.NewBoardCrypto()
	if err != nil {
		return nil, fmt.Errorf("core: claim: cifrador: %w", err)
	}
	c.SetPeerKey(board.InviteCodeClaimKey(code))
	return c.Encrypt(plain)
}

func openWithClaimKey(code string, sealed []byte) ([]byte, error) {
	c, err := board.NewBoardCrypto()
	if err != nil {
		return nil, err
	}
	c.SetPeerKey(board.InviteCodeClaimKey(code))
	return c.Decrypt(sealed)
}
