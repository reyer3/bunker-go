package core

import (
	"context"
	"fmt"
)

// BoardPeer es una capacidad opcional del daemon: un transporte P2P que
// publica y recibe deltas de tableros compartidos sin servidor central.
//
// core no importa internal/p2p: el daemon (cmd/bunker) implementa este puerto
// con el nodo libp2p y lo registra con SetBoardPeer. Sin peer registrado, los
// métodos que dependen de él devuelven un error que envuelve ErrUnsupported.
type BoardPeer interface {
	// PeerID devuelve la identidad del nodo en la red P2P.
	PeerID() string
	// ListenAddr devuelve la dirección multiaddr de escucha.
	ListenAddr() string
	// ConnectedPeers devuelve los IDs de los peers conectados.
	ConnectedPeers() []string
	// Connect se conecta a un peer por su multiaddr completa
	// (/ip4/.../tcp/.../p2p/<id>).
	Connect(ctx context.Context, multiaddr string) error
	// Publish envía un delta de board a todos los peers conectados.
	Publish(boardID string, msg BoardPeerMessage) error
}

// BoardPeerMessage es el delta que viaja por el transporte P2P. Sus campos
// reflejan BoardEvent; el daemon lo traduce al mensaje concreto de su
// implementación (hoy p2p.RelayMessage).
type BoardPeerMessage struct {
	BoardID   string
	MemberID  string
	Type      string
	Payload   []byte
	Timestamp int64
	Version   int
}

// SetBoardPeer registra el transporte P2P del daemon. Si el transporte
// entrega claims entrantes (BoardClaimResponder), el Service queda como su
// respondedor: el daemon que tiene la invitación la atiende sin más
// cableado.
func (s *Service) SetBoardPeer(p BoardPeer) {
	s.boardPeer = p
	if r, ok := p.(BoardClaimResponder); ok {
		r.OnClaim(s.answerPeer)
	}
}

// BoardPeerStatus describe el estado del nodo P2P para `bunker p2p status`.
type BoardPeerStatus struct {
	PeerID string   `json:"id"`
	Addr   string   `json:"addr"`
	Peers  []string `json:"peers"`
}

// BoardPeerStatus devuelve el estado del nodo P2P. El descubrimiento automático
// está resuelto con `bunker p2p connect <multiaddr>`; no hay una DHT a la que
// consultar, así que este estado no acepta un secreto.
func (s *Service) BoardPeerStatus(ctx context.Context) (BoardPeerStatus, error) {
	peer, err := s.boardPeerOrErr()
	if err != nil {
		return BoardPeerStatus{}, err
	}
	return BoardPeerStatus{
		PeerID: peer.PeerID(),
		Addr:   peer.ListenAddr(),
		Peers:  peer.ConnectedPeers(),
	}, nil
}

// BoardPeerConnect conecta el nodo P2P con otro por su multiaddr y devuelve
// los peers conectados después del dial.
func (s *Service) BoardPeerConnect(ctx context.Context, multiaddr string) ([]string, error) {
	peer, err := s.boardPeerOrErr()
	if err != nil {
		return nil, err
	}
	if err := peer.Connect(ctx, multiaddr); err != nil {
		return nil, err
	}
	return peer.ConnectedPeers(), nil
}

// BoardPeerSync publica un delta de board por el transporte P2P y devuelve
// los peers a los que se intentó enviar. Es el nombre que expone el Backend
// del CLI (`bunker p2p sync`).
func (s *Service) BoardPeerSync(ctx context.Context, boardID, memberID, typ string, payload []byte, version int) ([]string, error) {
	peer, err := s.boardPeerOrErr()
	if err != nil {
		return nil, err
	}
	msg := BoardPeerMessage{BoardID: boardID, MemberID: memberID, Type: typ, Payload: payload, Version: version}
	if err := peer.Publish(boardID, msg); err != nil {
		return nil, err
	}
	return peer.ConnectedPeers(), nil
}

// boardPeerOrErr devuelve el peer registrado o un error que envuelve
// ErrUnsupported, igual que el resto de capacidades opcionales.
func (s *Service) boardPeerOrErr() (BoardPeer, error) {
	if s.boardPeer == nil {
		return nil, fmt.Errorf("core: transporte P2P no configurado: %w", ErrUnsupported)
	}
	return s.boardPeer, nil
}
