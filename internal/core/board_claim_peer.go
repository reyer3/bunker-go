package core

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/reyer3/bunker-go/internal/board"
)

// JoinMessageType es el registro de un miembro ya admitido en el relay
// (al reconectar). Lleva una prueba de que conoce la clave del board.
const JoinMessageType = "member.join"

// joinProofDomain separa la prueba de membership de la del claim.
const joinProofDomain = "member.join"

// BoardPeerRequester es una capacidad opcional de un BoardPeer: mandar un
// mensaje y esperar la respuesta de quien lo atiende (un claim y su
// welcome). Publish es difusión sin respuesta y no sirve para esto.
type BoardPeerRequester interface {
	Request(ctx context.Context, msg BoardPeerMessage) (BoardPeerMessage, error)
}

// BoardClaimResponder es la otra mitad: un BoardPeer que puede entregar los
// claims entrantes a quien los responda. SetBoardPeer registra al Service
// como respondedor, así que el daemon que crea el tablero atiende claims
// por P2P sin cableado extra.
type BoardClaimResponder interface {
	OnClaim(fn func(BoardPeerMessage) BoardPeerMessage)
}

// joinPayload es el payload de member.join.
type joinPayload struct {
	Proof string `json:"proof"`
}

// NewClaimMessage arma el sobre de member.claim para code. El sobre lleva
// el board y el miembro en claro (el relay los necesita para registrar al
// cliente); BoardAnswer exige que coincidan con el claim firmado.
func NewClaimMessage(code, boardID, memberID, name, publicKey string) (BoardPeerMessage, error) {
	c := NewBoardClaim(code, boardID, memberID, name, publicKey)
	payload, err := json.Marshal(c)
	if err != nil {
		return BoardPeerMessage{}, fmt.Errorf("core: claim: codificar: %w", err)
	}
	return BoardPeerMessage{BoardID: c.BoardID, MemberID: c.MemberID, Type: ClaimMessageType, Payload: payload}, nil
}

// JoinProof es la prueba de membership de member.join: HMAC con la clave
// del board, que solo tienen sus miembros.
func JoinProof(boardKey []byte, boardID, memberID string) string {
	return board.Proof(boardKey, joinProofDomain, boardID, memberID)
}

// NewJoinMessage arma el member.join de memberID para b, firmado con la
// clave de b.
func NewJoinMessage(b *Board, memberID string) BoardPeerMessage {
	payload, _ := json.Marshal(joinPayload{Proof: JoinProof(b.EncryptionKey, b.ID, memberID)})
	return BoardPeerMessage{BoardID: b.ID, MemberID: memberID, Type: JoinMessageType, Payload: payload}
}

// RejectMessage es la respuesta de rechazo a msg, con el motivo en el
// payload para que quien se une falle ruidoso y no espere un welcome.
func RejectMessage(msg BoardPeerMessage, reason error) BoardPeerMessage {
	payload, _ := json.Marshal(reason.Error())
	return BoardPeerMessage{BoardID: msg.BoardID, MemberID: msg.MemberID, Type: RejectMessageType, Payload: payload}
}

// Answer atiende un mensaje de admisión del lado del creador: un claim
// devuelve el welcome (payload: el welcome cifrado, como string JSON
// base64 para que viaje dentro de un mensaje JSON); un join válido devuelve
// el mismo mensaje como acuse. Un error significa no registrar al cliente.
func (s *BoardService) Answer(msg BoardPeerMessage) (BoardPeerMessage, error) {
	switch msg.Type {
	case ClaimMessageType:
		var c BoardClaim
		if err := json.Unmarshal(msg.Payload, &c); err != nil {
			return BoardPeerMessage{}, fmt.Errorf("core: claim: payload ilegible: %v: %w", err, ErrInviteInvalid)
		}
		if c.BoardID != msg.BoardID || c.MemberID != msg.MemberID {
			return BoardPeerMessage{}, fmt.Errorf("core: claim: el sobre no coincide con el claim firmado: %w", ErrInviteInvalid)
		}
		sealed, err := s.HandleClaim(c)
		if err != nil {
			return BoardPeerMessage{}, err
		}
		payload, err := json.Marshal(sealed)
		if err != nil {
			return BoardPeerMessage{}, fmt.Errorf("core: claim: codificar welcome: %w", err)
		}
		return BoardPeerMessage{BoardID: c.BoardID, MemberID: c.MemberID, Type: WelcomeMessageType, Payload: payload}, nil
	case JoinMessageType:
		var p joinPayload
		if len(msg.Payload) > 0 {
			if err := json.Unmarshal(msg.Payload, &p); err != nil {
				return BoardPeerMessage{}, fmt.Errorf("core: join: payload ilegible: %v: %w", err, ErrInviteInvalid)
			}
		}
		if err := s.VerifyJoin(msg.BoardID, msg.MemberID, p.Proof); err != nil {
			return BoardPeerMessage{}, err
		}
		return msg, nil
	default:
		return BoardPeerMessage{}, fmt.Errorf("core: admisión: tipo %q no es claim ni join: %w", msg.Type, ErrInviteInvalid)
	}
}

// VerifyJoin acepta un member.join solo de un miembro registrado del board
// que prueba conocer su clave. Un board sin clave no puede probar nada y
// se rechaza: sin prueba, cualquiera se registraría con un id ajeno.
func (s *BoardService) VerifyJoin(boardID, memberID, proof string) error {
	b, err := s.store.GetBoard(boardID)
	if err != nil {
		return fmt.Errorf("core: join: board %s: %v: %w", boardID, err, ErrInviteInvalid)
	}
	if len(b.EncryptionKey) == 0 {
		return fmt.Errorf("core: join: board %s sin clave, no hay prueba posible: %w", boardID, ErrInviteInvalid)
	}
	if _, err := s.store.GetMember(boardID, memberID); err != nil {
		return fmt.Errorf("core: join: miembro %s: %v: %w", memberID, err, ErrInviteInvalid)
	}
	if !board.ProofEqual(proof, JoinProof(b.EncryptionKey, boardID, memberID)) {
		return fmt.Errorf("core: join: prueba de membership inválida para %s: %w", memberID, ErrInviteInvalid)
	}
	return nil
}

// AcceptClaimReply es el lado de quien se une ante la respuesta a su claim:
// un member.reject es un error ruidoso con el motivo del creador; un
// member.welcome se descifra y se adopta.
func (s *BoardService) AcceptClaimReply(code, boardID string, reply BoardPeerMessage) (*Member, error) {
	switch reply.Type {
	case WelcomeMessageType:
		var sealed []byte
		if err := json.Unmarshal(reply.Payload, &sealed); err != nil {
			return nil, fmt.Errorf("core: welcome de %s ilegible: %v: %w", boardID, err, ErrInviteInvalid)
		}
		return s.AcceptWelcome(code, boardID, sealed)
	case RejectMessageType:
		var reason string
		_ = json.Unmarshal(reply.Payload, &reason)
		return nil, fmt.Errorf("core: claim rechazado por el creador: %s: %w", reason, ErrInviteInvalid)
	default:
		return nil, fmt.Errorf("core: respuesta al claim de tipo %q: %w", reply.Type, ErrInviteInvalid)
	}
}

// BoardAnswer atiende un mensaje de admisión (claim o join) con el
// BoardService del daemon. Lo usan el relay y el transporte P2P.
func (s *Service) BoardAnswer(msg BoardPeerMessage) (BoardPeerMessage, error) {
	bs, err := s.boardSvcOrErr()
	if err != nil {
		return BoardPeerMessage{}, err
	}
	return bs.Answer(msg)
}

// BoardAcceptClaimReply adopta la respuesta a un claim que viajó por fuera
// del daemon (el CLI habló directo con el relay del creador).
func (s *Service) BoardAcceptClaimReply(ctx context.Context, code, boardID string, reply BoardPeerMessage) (*Member, error) {
	bs, err := s.boardSvcOrErr()
	if err != nil {
		return nil, err
	}
	return bs.AcceptClaimReply(code, boardID, reply)
}

// BoardClaimPeer reclama una invitación por el transporte P2P: manda el
// member.claim a los peers conectados y adopta el welcome del que tenga la
// invitación. Un transporte que no sabe pedir y esperar respuesta envuelve
// ErrUnsupported.
func (s *Service) BoardClaimPeer(ctx context.Context, code, boardID, memberID, name, publicKey string) (*Member, error) {
	bs, err := s.boardSvcOrErr()
	if err != nil {
		return nil, err
	}
	peer, err := s.boardPeerOrErr()
	if err != nil {
		return nil, err
	}
	req, ok := peer.(BoardPeerRequester)
	if !ok {
		return nil, fmt.Errorf("core: el transporte P2P no admite claims: %w", ErrUnsupported)
	}
	msg, err := NewClaimMessage(code, boardID, memberID, name, publicKey)
	if err != nil {
		return nil, err
	}
	reply, err := req.Request(ctx, msg)
	if err != nil {
		return nil, fmt.Errorf("core: claim por P2P: %w", err)
	}
	return bs.AcceptClaimReply(code, boardID, reply)
}

// answerPeer es la respuesta que el Service registra en un transporte con
// BoardClaimResponder: todo error vuelve como member.reject.
func (s *Service) answerPeer(msg BoardPeerMessage) BoardPeerMessage {
	reply, err := s.BoardAnswer(msg)
	if err != nil {
		return RejectMessage(msg, err)
	}
	return reply
}
