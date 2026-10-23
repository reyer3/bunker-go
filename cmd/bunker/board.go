package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/relay"
)

const boardUsage = `usage: bunker board create --name <nombre>
       bunker board invite --board <board-id> [--ttl <horas>] [--max-uses <n>]
       bunker board join <codigo-invitacion> [--member-id <id>] [--name <nombre>] [--public-key <clave>] [--relay <addr>]
       bunker board list [--board <board-id>]
       bunker board task create --board <board-id> --column <col-id> --titulo <texto>
              [--descripcion <texto>] [--asignar <member-id>] [--vencimiento <YYYY-MM-DD>]
              [--etiqueta <etiqueta>]...
       bunker board task move --task <task-id> --column <col-id>
       bunker board task done --task <task-id> [--version <n>]
       bunker board task comment --task <task-id> --texto <texto>
       bunker board members [--board <board-id>]
       bunker board relay start [--puerto <puerto>]
        bunker board report --board <board-id>   reporte de trazabilidad del tablero`

// cmdBoard dispatches the shared-board commands. Every subcommand talks to
// the Backend, which exposes core.BoardService's methods; the live daemon
// wiring (rpc.Client / core.Service) is a separate change, so these are
// written against the interface and stubbed today.
func cmdBoard(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(stderr, boardUsage)
		return 2
	}
	switch args[0] {
	case "create":
		return cmdBoardCreate(ctx, backend, args[1:], stdout, stderr)
	case "invite":
		return cmdBoardInvite(ctx, backend, args[1:], stdout, stderr)
	case "join":
		return cmdBoardJoin(ctx, backend, args[1:], stdout, stderr)
	case "list":
		return cmdBoardList(ctx, backend, args[1:], stdout, stderr)
	case "task":
		return cmdBoardTask(ctx, backend, args[1:], stdout, stderr)
	case "members":
		return cmdBoardMembers(ctx, backend, args[1:], stdout, stderr)
	case "relay":
		return cmdBoardRelay(ctx, backend, args[1:], stdout, stderr)
	case "report":
		return cmdBoardReport(ctx, backend, args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, boardUsage)
		return 2
	}
}

func cmdBoardCreate(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("board create", stderr)
	name := fs.String("name", "", "nombre del tablero compartido")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) > 0 || strings.TrimSpace(*name) == "" {
		fmt.Fprintln(stderr, boardUsage)
		return 2
	}
	board, err := backend.BoardCreate(ctx, *name, "")
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	if *jsonOut {
		writeJSON(stdout, map[string]any{"board": board})
		return 0
	}
	fmt.Fprintf(stdout, "%s\n", board.ID)
	return 0
}

func cmdBoardInvite(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("board invite", stderr)
	boardID := fs.String("board", "", "id del tablero")
	ttl := fs.String("ttl", "", "validez del codigo en horas (default 48h)")
	maxUses := fs.Int("max-uses", 0, "numero maximo de usos del codigo (0 = ilimitado)")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) > 0 || strings.TrimSpace(*boardID) == "" {
		fmt.Fprintln(stderr, boardUsage)
		return 2
	}
	ttlHours, err := parseInviteTTL(*ttl)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	invite, err := backend.BoardInvite(ctx, *boardID, "", ttlHours, *maxUses)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	if *jsonOut {
		writeJSON(stdout, map[string]any{"invite": invite})
		return 0
	}
	// El código de invitación completo ya incluye la clave pública del tablero
	// (embebida tras un punto, en base64 URL-safe), así que no hay que pedir
	// --public-key por separado al generar.
	fmt.Fprintf(stdout, "%s\n", inviteCodeString(invite))
	return 0
}

// parseInviteTTL turns --ttl (hours as text, empty for default) into a
// time.Duration. Empty means "use the service default (48h)".
func parseInviteTTL(raw string) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	var hours int
	if _, err := fmt.Sscanf(raw, "%d", &hours); err != nil || hours < 0 {
		return 0, fmt.Errorf("ttl invalido %q: espero un numero entero de horas", raw)
	}
	return time.Duration(hours) * time.Hour, nil
}

// inviteCodeString renderiza el código de invitación completo: el código
// corto seguido de la clave pública del tablero en base64 URL-safe, embebida
// tras un punto. Así quien reciba el código puede configurar el cifrado E2EE
// sin que el relay conozca la clave.
func inviteCodeString(invite *core.InviteCode) string {
	if len(invite.PublicKey) == 0 {
		return invite.Code
	}
	return invite.Code + "." + base64.RawURLEncoding.EncodeToString(invite.PublicKey)
}

// splitInviteCode separa el código de invitación completo en el código
// corto (para buscarlo en el servidor) y la clave pública del tablero
// embebida tras un punto (base64 URL-safe). Si no hay clave embebida,
// devuelve el código tal cual y una clave vacía.
func splitInviteCode(full string) (code, publicKey string) {
	code = full
	if parts := strings.SplitN(full, ".", 2); len(parts) == 2 && parts[1] != "" {
		if _, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
			code = parts[0]
			publicKey = parts[1]
		}
	}
	return code, publicKey
}

func cmdBoardJoin(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("board join", stderr)
	memberID := fs.String("member-id", "", "id del dispositivo/membre que se une (default: el actual)")
	memberName := fs.String("name", "", "nombre para mostrar del miembro")
	publicKey := fs.String("public-key", "", "clave publica para E2EE")
	relayAddr := fs.String("relay", "", "direccion del relay (host:puerto); default $BUNKER_RELAY_ADDR")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, boardUsage)
		return 2
	}
	// El código de invitación completo trae la clave pública del tablero
	// embebida (codigo.<clave en base64 URL-safe)). Si el usuario no pasa
	// --public-key, se usa la embebida; si no hay ninguna, se une sin clave
	// (el cifrado E2EE no funcionará hasta que se configure).
	code, embeddedKey := splitInviteCode(positionals[0])
	joinPublicKey := *publicKey
	if joinPublicKey == "" {
		joinPublicKey = embeddedKey
	}
	member, err := backend.BoardJoin(ctx, code, *memberID, *memberName, joinPublicKey)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}

	// Registrarse en el relay ligero para recibir los deltas del board en
	// tiempo real. Falla suave: el join ya esta confirmado por el backend,
	// el relay es solo sincronizacion y nunca debe hacer fallar el join.
	addr := strings.TrimSpace(*relayAddr)
	if addr == "" {
		addr = os.Getenv("BUNKER_RELAY_ADDR")
	}
	if addr != "" {
		if _, err := relayJoinMember(ctx, addr, member.BoardID, member.ID); err != nil {
			fmt.Fprintf(stderr, "warning: relay %s: %v\n", addr, err)
		}
	}

	if *jsonOut {
		writeJSON(stdout, map[string]any{"member": member})
		return 0
	}
	fmt.Fprintf(stdout, "%s\n", member.ID)
	return 0
}

// relayJoinMember conecta al relay en relayAddr y se registra como miembro
// del board enviando un mensaje member.join (BoardID + MemberID). Mantiene
// la conexion TCP abierta hasta que ctx se cancele para recibir sincronizacion
// del board. Un error aqui no revierte el join del board.
func relayJoinMember(ctx context.Context, relayAddr, boardID, memberID string) (net.Conn, error) {
	conn, err := net.Dial("tcp", relayAddr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", relayAddr, err)
	}
	msg := relay.RelayMessage{
		BoardID:   boardID,
		MemberID:  memberID,
		Type:      "member.join",
		Timestamp: time.Now().Unix(),
	}
	if err := json.NewEncoder(conn).Encode(msg); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send member.join: %w", err)
	}
	// Mantener abierta para recibir los deltas del board hasta que el
	// comando (o el daemon) termine.
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	return conn, nil
}

func cmdBoardList(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("board list", stderr)
	boardID := fs.String("board", "", "si se da, lista las tareas de ese tablero en vez de los tableros")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) > 0 {
		fmt.Fprintln(stderr, boardUsage)
		return 2
	}
	if *boardID != "" {
		tasks, err := backend.BoardTasks(ctx, *boardID)
		if err != nil {
			return fail(*jsonOut, stdout, stderr, err)
		}
		if *jsonOut {
			if tasks == nil {
				tasks = []*core.Task{}
			}
			writeJSON(stdout, map[string]any{"tasks": tasks})
			return 0
		}
		if len(tasks) == 0 {
			fmt.Fprintln(stdout, "no hay tareas en ese tablero")
			return 0
		}
		for _, t := range tasks {
			fmt.Fprintln(stdout, formatTask(t))
		}
		return 0
	}
	boards, err := backend.BoardList(ctx)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	if *jsonOut {
		if boards == nil {
			boards = []*core.Board{}
		}
		writeJSON(stdout, map[string]any{"boards": boards})
		return 0
	}
	if len(boards) == 0 {
		fmt.Fprintln(stdout, "no hay tableros")
		return 0
	}
	for _, b := range boards {
		fmt.Fprintln(stdout, formatBoard(b))
	}
	return 0
}

func cmdBoardTask(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(stderr, boardUsage)
		return 2
	}
	switch args[0] {
	case "create":
		return cmdBoardTaskCreate(ctx, backend, args[1:], stdout, stderr)
	case "move":
		return cmdBoardTaskMove(ctx, backend, args[1:], stdout, stderr)
	case "done":
		return cmdBoardTaskDone(ctx, backend, args[1:], stdout, stderr)
	case "comment":
		return cmdBoardTaskComment(ctx, backend, args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, boardUsage)
		return 2
	}
}

func cmdBoardTaskCreate(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("board task create", stderr)
	boardID := fs.String("board", "", "id del tablero")
	columnID := fs.String("column", "", "id de la columna")
	titulo := fs.String("titulo", "", "texto de la tarea")
	descripcion := fs.String("descripcion", "", "descripcion de la tarea")
	asignar := fs.String("asignar", "", "id del miembro asignado (opcional)")
	vencimiento := fs.String("vencimiento", "", "fecha limite YYYY-MM-DD (opcional)")
	var etiquetas stringSliceTag
	fs.Var(&etiquetas, "etiqueta", "etiqueta de la tarea (repetible)")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) > 0 || strings.TrimSpace(*boardID) == "" || strings.TrimSpace(*columnID) == "" || strings.TrimSpace(*titulo) == "" {
		fmt.Fprintln(stderr, boardUsage)
		return 2
	}
	var dueDate *time.Time
	if strings.TrimSpace(*vencimiento) != "" {
		d, err := time.Parse("2006-01-02", *vencimiento)
		if err != nil {
			return fail(*jsonOut, stdout, stderr, fmt.Errorf("vencimiento invalido %q: use YYYY-MM-DD", *vencimiento))
		}
		dueDate = &d
	}
	task, err := backend.BoardTaskCreate(ctx, *boardID, *columnID, *titulo, *descripcion, *asignar, dueDate, []string(etiquetas), "")
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	if *jsonOut {
		writeJSON(stdout, map[string]any{"task": task})
		return 0
	}
	fmt.Fprintf(stdout, "%s\n", task.ID)
	return 0
}

func cmdBoardTaskMove(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("board task move", stderr)
	taskID := fs.String("task", "", "id de la tarea")
	columnID := fs.String("column", "", "id de la columna destino")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) > 0 || strings.TrimSpace(*taskID) == "" || strings.TrimSpace(*columnID) == "" {
		fmt.Fprintln(stderr, boardUsage)
		return 2
	}
	task, err := backend.BoardTaskMove(ctx, *taskID, *columnID)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	if *jsonOut {
		writeJSON(stdout, map[string]any{"task": task})
		return 0
	}
	fmt.Fprintf(stdout, "%s movida a %s\n", task.ID, task.ColumnID)
	return 0
}

func cmdBoardTaskDone(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("board task done", stderr)
	taskID := fs.String("task", "", "id de la tarea")
	version := fs.Int("version", 0, "version de la tarea para control de versiones optimista")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) > 0 || strings.TrimSpace(*taskID) == "" {
		fmt.Fprintln(stderr, boardUsage)
		return 2
	}
	task, err := backend.BoardTaskDone(ctx, *taskID, *version)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	if *jsonOut {
		writeJSON(stdout, map[string]any{"task": task})
		return 0
	}
	fmt.Fprintf(stdout, "%s hecha (version %d)\n", task.ID, task.Version)
	return 0
}

func cmdBoardTaskComment(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("board task comment", stderr)
	taskID := fs.String("task", "", "id de la tarea")
	texto := fs.String("texto", "", "texto del comentario")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) > 0 || strings.TrimSpace(*taskID) == "" || strings.TrimSpace(*texto) == "" {
		fmt.Fprintln(stderr, boardUsage)
		return 2
	}
	comment, err := backend.BoardTaskComment(ctx, *taskID, "", *texto)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	if *jsonOut {
		writeJSON(stdout, map[string]any{"comment": comment})
		return 0
	}
	fmt.Fprintf(stdout, "%s\n", comment.ID)
	return 0
}

func cmdBoardMembers(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("board members", stderr)
	boardID := fs.String("board", "", "id del tablero")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) > 0 || strings.TrimSpace(*boardID) == "" {
		fmt.Fprintln(stderr, boardUsage)
		return 2
	}
	members, err := backend.BoardMembers(ctx, *boardID)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	if *jsonOut {
		if members == nil {
			members = []*core.Member{}
		}
		writeJSON(stdout, map[string]any{"members": members})
		return 0
	}
	if len(members) == 0 {
		fmt.Fprintln(stdout, "no hay miembros en ese tablero")
		return 0
	}
	for _, m := range members {
		fmt.Fprintln(stdout, formatMember(m))
	}
	return 0
}

func cmdBoardReport(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("board report", stderr)
	boardID := fs.String("board", "", "id del tablero a reportar")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) > 0 || strings.TrimSpace(*boardID) == "" {
		fmt.Fprintln(stderr, boardUsage)
		return 2
	}
	tasks, err := backend.BoardTasks(ctx, *boardID)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	if tasks == nil {
		tasks = []*core.Task{}
	}

	// Contar por columna
	counts := map[string]int{}
	for _, t := range tasks {
		counts[t.ColumnID]++
	}

	if *jsonOut {
		summary := map[string]any{
			"board_id":  *boardID,
			"total":     len(tasks),
			"by_column": counts,
		}
		writeJSON(stdout, map[string]any{"report": summary})
		return 0
	}

	fmt.Fprintf(stdout, "=== Reporte de tablero %s ===\n", *boardID)
	fmt.Fprintf(stdout, "Total de tareas: %d\n\n", len(tasks))
	if len(tasks) == 0 {
		fmt.Fprintln(stdout, "No hay tareas en este tablero.")
		return 0
	}
	fmt.Fprintf(stdout, "Distribución por columna:\n")
	for colID, cnt := range counts {
		fmt.Fprintf(stdout, "  %-20s %d tarea(s)\n", colID, cnt)
	}
	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "Detalle de tareas:\n")
	for _, t := range tasks {
		due := "-"
		if t.DueDate != nil {
			due = t.DueDate.Local().Format("2006-01-02")
		}
		fmt.Fprintf(stdout, "  %-20s %-30s col=%s estado=%s\n", t.ID, t.Title, t.ColumnID, due)
	}
	return 0
}

func cmdBoardRelay(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(stderr, boardUsage)
		return 2
	}
	switch args[0] {
	case "start":
		return cmdBoardRelayStart(ctx, backend, args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, boardUsage)
		return 2
	}
}

// cmdBoardRelayStart is the light relay placeholder: it prints that it is
// listening and blocks forever. The real relay implementation is a separate
// change; this only wires the CLI surface.
func cmdBoardRelayStart(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("board relay start", stderr)
	puerto := fs.String("puerto", "8080", "puerto en el que escucha el relay")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) > 0 {
		fmt.Fprintln(stderr, boardUsage)
		return 2
	}
	if *jsonOut {
		writeJSON(stdout, map[string]any{"relay": "listening", "port": *puerto})
	} else {
		fmt.Fprintf(stdout, "relay escuchando en :%s\n", *puerto)
	}
	// Bloquea: el relay real se implementa por separado.
	select {}
}

func formatBoard(b *core.Board) string {
	return fmt.Sprintf("%s\t%s\t%s", b.ID, b.Name, b.OwnerID)
}

func formatTask(t *core.Task) string {
	due := "-"
	if t.DueDate != nil {
		due = t.DueDate.Local().Format("2006-01-02")
	}
	tags := strings.Join(t.Tags, ",")
	line := fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s", t.ID, t.ColumnID, t.Title, due, tags, t.AssigneeID)
	if t.Version > 0 {
		line += fmt.Sprintf("\tv%d", t.Version)
	}
	return line
}

func formatMember(m *core.Member) string {
	return fmt.Sprintf("%s\t%s\t%s", m.ID, m.Name, m.Role)
}

// stringSliceTag collects a repeatable --etiqueta flag into a slice.
type stringSliceTag []string

func (s *stringSliceTag) String() string { return strings.Join(*s, ",") }
func (s *stringSliceTag) Set(v string) error {
	*s = append(*s, v)
	return nil
}
