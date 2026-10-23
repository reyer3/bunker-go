// Package relay implementa un relay ligero de sincronización para tableros
// compartidos.
//
// Escucha conexiones TCP y reenvía mensajes JSON entre los dispositivos de un
// mismo board. Cada dispositivo se identifica con un member ID y un board ID.
// El relay mantiene una lista de conexiones activas por board ID y, cuando un
// dispositivo envía un mensaje (delta de tarea, comentario, etc.), lo reenvía
// a todos los demás miembros del mismo board.
//
// El relay NO descifra el contenido: los mensajes viajan cifrados con la clave
// del board y el relay solo los transporta, de modo que nunca tiene acceso a
// los datos en claro.
package relay

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"
)

// RelayMessage es el formato de mensaje que circula por el relay. Todos los
// mensajes son una sola línea JSON.
type RelayMessage struct {
	BoardID   string          `json:"board_id"`
	MemberID  string          `json:"member_id"`
	Type      string          `json:"type"` // "task.create", "task.move", "task.done", "task.comment", "member.join"
	Payload   json.RawMessage `json:"payload"`
	Timestamp int64           `json:"timestamp"`
	Version   int             `json:"version"`
}

const (
	// maxLineBytes es el tamaño máximo de una línea JSON aceptada.
	maxLineBytes = 1 << 20 // 1 MB
	// sendBuffer es el tamaño del canal de envío de cada cliente.
	sendBuffer = 8
)

// Relay es un servidor de relay multi-board.
type Relay struct {
	listenAddr string
	listener   net.Listener
	// boards mapea boardID -> *sync.Map (memberID -> *client).
	boards sync.Map
	done   chan struct{}
	once   sync.Once
	wg     sync.WaitGroup
	// consumer es el receptor de los mensajes escritos POR CLIENTES (los
	// deltas que otro dispositivo publicó en el relay). El daemon lo usa
	// para aplicarlos en el store local; los mensajes inyectados con
	// Publish (los deltas ya persistidos localmente) no pasan por aquí,
	// porque aplicarlos de nuevo sería eco del propio daemon.
	consumer func(RelayMessage)
	version  int
}

// OnMessage registra la función a la que el relay entrega cada mensaje que
// llega de un cliente (member.join, task.create, task.move, task.done,
// task.comment). Solo una función. Los mensajes inyectados con Publish —
// los del propio daemon, ya en el store — no la invocan.
func (r *Relay) OnMessage(fn func(RelayMessage)) {
	r.consumer = fn
}

// NewRelay crea un Relay que escuchará en listenAddr (ej. "127.0.0.1:0" para
// un puerto asignado por el sistema).
func NewRelay(listenAddr string) *Relay {
	return &Relay{
		listenAddr: listenAddr,
		done:       make(chan struct{}),
		version:    1,
	}
}

// Addr devuelve la dirección de escucha del listener, o nil si no se inició.
func (r *Relay) Addr() net.Addr {
	if r.listener != nil {
		return r.listener.Addr()
	}
	return nil
}

// Start escucha en listenAddr y atiende conexiones hasta que ctx se cancele o
// se llame a Stop. Devuelve un error solo si no puede abrir el listener.
func (r *Relay) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", r.listenAddr)
	if err != nil {
		return err
	}
	r.listener = ln

	// Bucle de aceptación de conexiones.
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener cerrado
			}
			r.wg.Add(1)
			go r.handleConn(conn)
		}
	}()

	// Cancelación del contexto: cerrar el listener para detener el bucle.
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	return nil
}

// Publish inyecta msg en el relay y lo reenvía a todos los miembros de
// msg.BoardID excepto msg.MemberID, como si un dispositivo unido a ese
// board lo hubiera enviado. Lo usa el daemon para difundir en tiempo real
// los cambios de tarea (crear, mover, completar, comentar) que persiste en
// el store, sin que el relay descifre el contenido. board_id es obligatorio
// (sabe a qué board difundir); member_id es opcional para mensajes
// generados por el servidor (p. ej. mover/completar una tarea, donde no se
// conoce quién lo hizo): cuando está vacío no se excluye a nadie y lo
// reciben todos los miembros del board.
func (r *Relay) Publish(msg RelayMessage) error {
	if msg.BoardID == "" {
		return fmt.Errorf("relay: publish: board_id is required")
	}
	if msg.Timestamp == 0 {
		msg.Timestamp = time.Now().Unix()
	}
	msg.Version = r.version
	r.broadcast(msg.BoardID, msg.MemberID, &msg)
	return nil
}

// Stop detiene el relay: cierra el listener, cierra todas las conexiones de
// cliente y espera a que terminen los goroutines de atención.
func (r *Relay) Stop() error {
	r.once.Do(func() {
		close(r.done)
	})
	if r.listener != nil {
		_ = r.listener.Close()
	}
	// Cerrar todas las conexiones de cliente para que los lectores salgan.
	r.boards.Range(func(_, bm any) bool {
		bm.(*sync.Map).Range(func(_, c any) bool {
			_ = c.(*client).conn.Close()
			return true
		})
		return true
	})
	r.wg.Wait()
	return nil
}

// client representa un dispositivo conectado a un board.
type client struct {
	conn   net.Conn
	board  string
	member string
	send   chan *RelayMessage
	done   chan struct{}
	once   sync.Once
}

func (r *Relay) handleConn(conn net.Conn) {
	defer r.wg.Done()
	defer conn.Close()

	c := &client{
		conn: conn,
		send: make(chan *RelayMessage, sendBuffer),
		done: make(chan struct{}),
	}

	// Goroutine de escritura: reenvía mensajes del relay al cliente.
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		enc := json.NewEncoder(conn)
		for {
			select {
			case msg, ok := <-c.send:
				if !ok {
					return
				}
				if err := enc.Encode(msg); err != nil {
					return
				}
			case <-c.done:
				return
			case <-r.done:
				return
			}
		}
	}()

	// Goroutine de lectura: lee líneas JSON del cliente.
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 256), maxLineBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg RelayMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue // mensaje mal formado: ignorar
		}
		r.handleMessage(c, &msg)
		// Entregar al consumidor (apply del daemon) una copia del mensaje;
		// handleMessage ya se ocupó del enrutado hacia los otros clientes.
		if r.consumer != nil {
			inbound := msg
			r.consumer(inbound)
		}
	}
	// Desconexión del cliente (limpia).
	r.removeClient(c)
}

func (r *Relay) handleMessage(c *client, msg *RelayMessage) {
	if msg.Timestamp == 0 {
		msg.Timestamp = time.Now().Unix()
	}
	msg.Version = r.version

	switch msg.Type {
	case "member.join":
		// Unirse a un board: registrar el dispositivo. No se reenvía.
		if msg.BoardID == "" || msg.MemberID == "" {
			return
		}
		prevBoard, prevMember := c.board, c.member
		c.board = msg.BoardID
		c.member = msg.MemberID
		r.addClient(c, prevBoard, prevMember)
	default:
		// Cualquier otro tipo (task.create, task.move, task.done,
		// task.comment) se reenvía a los demás miembros del mismo board.
		if c.board == "" || c.member == "" {
			return // dispositivo no unido a ningún board
		}
		msg.BoardID = c.board
		msg.MemberID = c.member
		r.broadcast(c.board, c.member, msg)
	}
}

// addClient registra al cliente en su board. Si ya pertenecía a otro board,
// sale de éste sin cerrar el canal de envío del cliente.
func (r *Relay) addClient(c *client, prevBoard, prevMember string) {
	if prevBoard != "" && prevMember != "" {
		if bm, ok := r.boards.Load(prevBoard); ok {
			bm.(*sync.Map).Delete(prevMember)
		}
	}
	board, _ := r.boards.LoadOrStore(c.board, &sync.Map{})
	board.(*sync.Map).Store(c.member, c)
}

// removeClient da de baja al cliente de su board y cierra su canal de envío.
func (r *Relay) removeClient(c *client) {
	c.once.Do(func() {
		close(c.done)
	})
	if c.board != "" {
		if bm, ok := r.boards.Load(c.board); ok {
			bm.(*sync.Map).Delete(c.member)
		}
	}
}

// broadcast reenvía msg a todos los miembros de boardID excepto fromMember.
// Los canales llenos se descartan para no bloquear a otros miembros.
func (r *Relay) broadcast(boardID, fromMember string, msg *RelayMessage) {
	bm, ok := r.boards.Load(boardID)
	if !ok {
		return
	}
	bm.(*sync.Map).Range(func(_, value any) bool {
		c := value.(*client)
		if c.member == fromMember {
			return true // no reenviar al remitente
		}
		select {
		case c.send <- msg:
		default:
			// Canal lleno: descartar para este cliente.
		}
		return true
	})
}
