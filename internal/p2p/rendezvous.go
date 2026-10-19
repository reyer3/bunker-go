package p2p

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/routing"
)

// rendezvousNamespace deriva el namespace de descubrimiento a partir del
// string secreto compartido. Ambos nodos usan el mismo secreto, así que
// calculan el mismo namespace y se encuentran en la DHT sin conocerse antes.
func rendezvousNamespace(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return fmt.Sprintf("/bunker/board/%x", sum)
}

// Rendezvous es un punto de encuentro sobre la DHT de libp2p. Dos nodos que
// comparten el mismo secreto anuncian y buscan en el mismo namespace; la DHT
// les devuelve las direcciones del otro para que se conecten directamente.
type Rendezvous struct {
	node      *Node
	dht       *dht.IpfsDHT
	discovery *routing.RoutingDiscovery
}

// NewRendezvous crea un servicio de rendezvous sobre el nodo n. El nodo debe
// estar creado; el DHT arranca en modo cliente (no se anuncia como servidor de
// routing) para no exigir una IP pública.
func NewRendezvous(ctx context.Context, n *Node) (*Rendezvous, error) {
	d, err := dht.New(n.host, dht.Mode(dht.ModeClient))
	if err != nil {
		return nil, fmt.Errorf("p2p: crear DHT: %w", err)
	}
	return &Rendezvous{
		node:      n,
		dht:       d,
		discovery: routing.NewRoutingDiscovery(d),
	}, nil
}

// JoinRendezvous anuncia este nodo en el namespace derivado de secret y
// arranca un bucle que busca peers con el mismo secreto y se conecta a ellos.
// Devuelve un canal con los peers encontrados. El bucle termina cuando ctx se
// cancela.
func (r *Rendezvous) JoinRendezvous(ctx context.Context, secret string) (<-chan peer.AddrInfo, error) {
	if err := r.dht.Bootstrap(ctx); err != nil {
		return nil, fmt.Errorf("p2p: bootstrap DHT: %w", err)
	}

	ns := rendezvousNamespace(secret)
	if _, err := r.discovery.Advertise(ctx, ns); err != nil {
		return nil, fmt.Errorf("p2p: anunciarse en rendezvous %s: %w", ns, err)
	}

	found := make(chan peer.AddrInfo, 8)
	go func() {
		defer close(found)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		seen := make(map[peer.ID]struct{})
		for {
			peers, err := r.discovery.FindPeers(ctx, ns)
			if err == nil {
				for pi := range peers {
					if pi.ID == r.node.host.ID() {
						continue
					}
					if _, ok := seen[pi.ID]; ok {
						continue
					}
					seen[pi.ID] = struct{}{}
					if err := r.node.host.Connect(ctx, pi); err != nil {
						continue // NAT estricto: el peer no es alcanzable todavía
					}
					select {
					case found <- pi:
					case <-ctx.Done():
						return
					}
				}
			}

			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return found, nil
}

// FindPeers busca peers que compartan el secreto sin conectarse a ellos.
// Es la variante de solo lectura para `bunker p2p status`.
func (r *Rendezvous) FindPeers(ctx context.Context, secret string) ([]peer.AddrInfo, error) {
	ns := rendezvousNamespace(secret)
	peers, err := r.discovery.FindPeers(ctx, ns)
	if err != nil {
		return nil, fmt.Errorf("p2p: buscar peers en rendezvous %s: %w", ns, err)
	}

	out := make([]peer.AddrInfo, 0, 4)
	for pi := range peers {
		if pi.ID == r.node.host.ID() {
			continue
		}
		out = append(out, pi)
	}
	return out, nil
}

// Close cierra el DHT.
func (r *Rendezvous) Close() error {
	return r.dht.Close()
}
