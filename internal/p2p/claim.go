package p2p

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// claimProtocol lleva los claims de invitación: a diferencia de
// boardProtocol (difusión de deltas sin respuesta), cada stream es una
// petición y su respuesta, así quien se une recibe el welcome por el mismo
// stream en que mandó el claim.
const claimProtocol protocol.ID = "/bunker/board-claim/1.0.0"

// maxClaimBytes acota lo que se lee de un stream de claim: un welcome lleva
// el board y sus columnas, nunca megas.
const maxClaimBytes = 1 << 20

// OnRequest registra la función que responde los mensajes que llegan por
// claimProtocol. Sin función, el nodo responde member.reject. Solo una;
// nil la quita.
func (n *Node) OnRequest(fn func(RelayMessage) RelayMessage) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.request = fn
}

// Request manda msg al peer peerID por claimProtocol y espera su respuesta.
func (n *Node) Request(ctx context.Context, peerID string, msg RelayMessage) (RelayMessage, error) {
	id, err := peer.Decode(peerID)
	if err != nil {
		return RelayMessage{}, fmt.Errorf("p2p: peer id inválido %q: %w", peerID, err)
	}
	ctx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	s, err := n.host.NewStream(ctx, id, claimProtocol)
	if err != nil {
		return RelayMessage{}, fmt.Errorf("p2p: abrir stream de claim con %s: %w", peerID, err)
	}
	defer s.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = s.SetDeadline(deadline)
	}
	if err := json.NewEncoder(s).Encode(msg); err != nil {
		return RelayMessage{}, fmt.Errorf("p2p: enviar claim a %s: %w", peerID, err)
	}
	_ = s.CloseWrite()
	var reply RelayMessage
	if err := json.NewDecoder(&limitedStream{s: s, left: maxClaimBytes}).Decode(&reply); err != nil {
		return RelayMessage{}, fmt.Errorf("p2p: leer respuesta de %s: %w", peerID, err)
	}
	return reply, nil
}

// handleClaimStream responde un claim entrante con la función registrada.
func (n *Node) handleClaimStream(s network.Stream) {
	defer s.Close()
	var msg RelayMessage
	if err := json.NewDecoder(&limitedStream{s: s, left: maxClaimBytes}).Decode(&msg); err != nil {
		n.logger.Error("p2p: claim mal formado", "error", err)
		return
	}
	n.mu.Lock()
	fn := n.request
	n.mu.Unlock()
	var reply RelayMessage
	if fn == nil {
		reason, _ := json.Marshal("este nodo no atiende claims")
		reply = RelayMessage{BoardID: msg.BoardID, MemberID: msg.MemberID, Type: "member.reject", Payload: reason}
	} else {
		reply = fn(msg)
	}
	if err := json.NewEncoder(s).Encode(reply); err != nil {
		n.logger.Warn("p2p: responder claim", "error", err)
	}
}

// limitedStream corta la lectura tras left bytes, para que un peer no
// llene la memoria con un claim infinito.
type limitedStream struct {
	s    network.Stream
	left int
}

func (l *limitedStream) Read(p []byte) (int, error) {
	if l.left <= 0 {
		return 0, fmt.Errorf("p2p: mensaje de claim supera %d bytes", maxClaimBytes)
	}
	if len(p) > l.left {
		p = p[:l.left]
	}
	k, err := l.s.Read(p)
	l.left -= k
	return k, err
}
