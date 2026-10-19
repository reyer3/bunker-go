package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/reyer3/bunker-go/internal/core"
)

// ---------------------------------------------------------------------------
// Board (kanban) view state lives on Model (see model.go). This file holds
// the board messages, commands, update handlers, key handling and views.
// ---------------------------------------------------------------------------

// Board messages.

type boardsLoadedMsg struct {
	token  uint64
	boards []*core.Board
	err    error
}

type boardLoadedMsg struct {
	token   uint64
	boardID string
	tasks   []*core.Task
	members []*core.Member
	err     error
}

type boardCreatedMsg struct {
	token uint64
	board *core.Board
	err   error
}

type taskCreatedMsg struct {
	token uint64
	task  *core.Task
	err   error
}

type taskMovedMsg struct {
	token uint64
	task  *core.Task
	err   error
}

type taskDoneMsg struct {
	token uint64
	task  *core.Task
	err   error
}

type memberJoinedMsg struct {
	token  uint64
	member *core.Member
	err    error
}

// ---------------------------------------------------------------------------
// Board commands
// ---------------------------------------------------------------------------

func loadBoardsCmd(client Client, token uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		boards, err := client.BoardList(ctx)
		return boardsLoadedMsg{token: token, boards: boards, err: err}
	}
}

func loadBoardCmd(client Client, boardID string, token uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		var (
			tasks   []*core.Task
			members []*core.Member
			err     error
		)
		tasks, err = client.BoardTasks(ctx, boardID)
		if err != nil {
			return boardLoadedMsg{token: token, boardID: boardID, err: err}
		}
		members, err2 := client.BoardMembers(ctx, boardID)
		if err2 != nil {
			err = err2
		}
		return boardLoadedMsg{token: token, boardID: boardID, tasks: tasks, members: members, err: err}
	}
}

func createBoardCmd(client Client, name string, token uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		board, err := client.BoardCreate(ctx, name, "")
		return boardCreatedMsg{token: token, board: board, err: err}
	}
}

func joinBoardCmd(client Client, code, memberID, name, publicKey string, token uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		member, err := client.BoardJoin(ctx, code, memberID, name, publicKey)
		return memberJoinedMsg{token: token, member: member, err: err}
	}
}

func createTaskCmd(client Client, boardID, columnID, title, description, assigneeID string, dueDate *time.Time, tags []string, createdBy string, token uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		task, err := client.BoardTaskCreate(ctx, boardID, columnID, title, description, assigneeID, dueDate, tags, createdBy)
		return taskCreatedMsg{token: token, task: task, err: err}
	}
}

func moveTaskCmd(client Client, taskID, columnID string, token uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		task, err := client.BoardTaskMove(ctx, taskID, columnID)
		return taskMovedMsg{token: token, task: task, err: err}
	}
}

func doneTaskCmd(client Client, taskID string, version int, token uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		task, err := client.BoardTaskDone(ctx, taskID, version)
		return taskDoneMsg{token: token, task: task, err: err}
	}
}

// ---------------------------------------------------------------------------
// Board update handlers
// ---------------------------------------------------------------------------

func (m Model) handleBoardsLoaded(msg boardsLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.token != m.boardToken || !m.boardMode || m.boardList {
		return m, nil
	}
	m.boardLoading = false
	if msg.err != nil {
		m.boardErr = msg.err
		return m, nil
	}
	m.boardErr = nil
	m.boards = msg.boards
	if len(m.boards) == 0 {
		m.boards = []*core.Board{}
	}
	m.boardSelRow = clamp(m.boardSelRow, 0, max(len(m.boards)-1, 0))
	return m, nil
}

func (m Model) handleBoardLoaded(msg boardLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.token != m.boardToken || !m.boardMode || m.boardList {
		return m, nil
	}
	m.boardLoading = false
	if msg.err != nil {
		m.boardErr = msg.err
		return m, nil
	}
	m.boardErr = nil
	m.tasks = msg.tasks
	if m.tasks == nil {
		m.tasks = []*core.Task{}
	}
	m.members = msg.members
	if m.members == nil {
		m.members = []*core.Member{}
	}
	m.boardSelRow = clamp(m.boardSelRow, 0, max(len(m.tasks)-1, 0))
	return m, nil
}

func (m Model) handleBoardCreated(msg boardCreatedMsg) (tea.Model, tea.Cmd) {
	if msg.token != m.boardToken || !m.boardMode || m.boardInputKind != 1 {
		return m, nil
	}
	m.boardInputKind = 0
	m.boardInput.Reset()
	m.boardLoading = false
	if msg.err != nil {
		m.boardErr = msg.err
		return m, nil
	}
	m.boardErr = nil
	if msg.board != nil {
		m.boardID = msg.board.ID
		m.boardName = msg.board.Name
		m.boardList = false
		m.boardToken++
		return m, loadBoardCmd(m.client, m.boardID, m.boardToken)
	}
	return m, nil
}

func (m Model) handleMemberJoined(msg memberJoinedMsg) (tea.Model, tea.Cmd) {
	if msg.token != m.boardToken || !m.boardMode || m.boardInputKind != 2 {
		return m, nil
	}
	m.boardInputKind = 0
	m.boardInput.Reset()
	m.boardLoading = false
	if msg.err != nil {
		m.boardErr = msg.err
		return m, nil
	}
	m.boardErr = nil
	if msg.member != nil {
		m.boardID = msg.member.BoardID
		m.boardList = false
		m.boardToken++
		return m, loadBoardCmd(m.client, m.boardID, m.boardToken)
	}
	return m, nil
}

func (m Model) handleTaskCreated(msg taskCreatedMsg) (tea.Model, tea.Cmd) {
	if msg.token != m.boardToken || !m.boardMode || m.boardInputKind != 3 {
		return m, nil
	}
	m.boardInputKind = 0
	m.boardInput.Reset()
	m.boardLoading = false
	m.boardNewCol = ""
	if msg.err != nil {
		m.boardErr = msg.err
		return m, nil
	}
	m.boardErr = nil
	if msg.task != nil {
		m.tasks = append(m.tasks, msg.task)
		m.boardSelRow = len(m.tasks) - 1
	}
	return m, nil
}

func (m Model) handleTaskMoved(msg taskMovedMsg) (tea.Model, tea.Cmd) {
	if msg.token != m.boardToken || !m.boardMode || m.boardList {
		return m, nil
	}
	m.boardLoading = false
	if msg.err != nil {
		m.boardErr = msg.err
		return m, nil
	}
	m.boardErr = nil
	if msg.task != nil {
		for i, t := range m.tasks {
			if t.ID == msg.task.ID {
				m.tasks[i].ColumnID = msg.task.ColumnID
				m.tasks[i].Version = msg.task.Version
				break
			}
		}
	}
	return m, nil
}

func (m Model) handleTaskDone(msg taskDoneMsg) (tea.Model, tea.Cmd) {
	if msg.token != m.boardToken || !m.boardMode || m.boardList {
		return m, nil
	}
	m.boardLoading = false
	if msg.err != nil {
		m.boardErr = msg.err
		return m, nil
	}
	m.boardErr = nil
	if msg.task != nil {
		for i, t := range m.tasks {
			if t.ID == msg.task.ID {
				m.tasks[i] = msg.task
				break
			}
		}
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// Board actions
// ---------------------------------------------------------------------------

// openBoards enters the board list mode and loads every board.
func (m Model) openBoards() (tea.Model, tea.Cmd) {
	if m.client == nil {
		return m, nil
	}
	m.boardMode = true
	m.boardList = true
	m.boardID = ""
	m.boardName = ""
	m.boards = nil
	m.tasks = nil
	m.members = nil
	m.boardSelRow = 0
	m.boardErr = nil
	m.boardLoading = true
	m.boardToken++
	return m, loadBoardsCmd(m.client, m.boardToken)
}

// openBoard enters a single board's kanban view and loads its tasks.
func (m Model) openBoard(boardID, boardName string) (tea.Model, tea.Cmd) {
	if m.client == nil {
		return m, nil
	}
	m.boardMode = true
	m.boardList = false
	m.boardID = boardID
	m.boardName = boardName
	m.tasks = nil
	m.members = nil
	m.boardSelRow = 0
	m.boardErr = nil
	m.boardLoading = true
	m.boardToken++
	return m, loadBoardCmd(m.client, boardID, m.boardToken)
}

// startCreateBoard opens the create-board input overlay.
func (m Model) startCreateBoard() (tea.Model, tea.Cmd) {
	m.boardInputKind = 1
	m.boardInput = textinput.New()
	m.boardInput.Placeholder = "Nombre del tablero"
	m.boardInput.Focus()
	m.boardInput.Width = 40
	return m, nil
}

// startJoinBoard opens the join-board input overlay.
func (m Model) startJoinBoard() (tea.Model, tea.Cmd) {
	m.boardInputKind = 2
	m.boardInput = textinput.New()
	m.boardInput.Placeholder = "bunker-invite-..."
	m.boardInput.Focus()
	m.boardInput.Width = 40
	return m, nil
}

// startCreateTask opens the create-task input overlay.
func (m Model) startCreateTask() (tea.Model, tea.Cmd) {
	m.boardInputKind = 3
	m.boardInput = textinput.New()
	m.boardInput.Placeholder = "Título de la tarea"
	m.boardInput.Focus()
	m.boardInput.Width = 40
	return m, nil
}

// ---------------------------------------------------------------------------
// Board key handling
// ---------------------------------------------------------------------------

func (m Model) updateBoard(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		if m.boardInputKind != 0 {
			m.boardInputKind = 0
			m.boardInput.Reset()
			return m, nil
		}
		if m.boardList {
			m.boardMode = false
			m.boardList = false
			m.boardID = ""
			m.boardName = ""
			m.boards = nil
			m.tasks = nil
			m.members = nil
			return m, nil
		}
		// Back to the board list.
		return m.openBoards()
	case "?":
		return m.openHelp(), nil
	}

	if m.boardInputKind != 0 {
		return m.updateBoardInput(msg)
	}

	if m.boardList {
		return m.updateBoardList(msg)
	}
	return m.updateBoardKanban(msg)
}

func (m Model) updateBoardList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "j", "down":
		if m.boardSelRow < len(m.boards)-1 {
			m.boardSelRow++
		}
		return m, nil
	case "k", "up":
		if m.boardSelRow > 0 {
			m.boardSelRow--
		}
		return m, nil
	case "enter":
		if len(m.boards) == 0 {
			return m, nil
		}
		b := m.boards[m.boardSelRow]
		return m.openBoard(b.ID, b.Name)
	case "c":
		return m.startCreateBoard()
	case "J":
		return m.startJoinBoard()
	case "g":
		if m.client == nil {
			return m, nil
		}
		m.boardLoading = true
		m.boardToken++
		return m, loadBoardsCmd(m.client, m.boardToken)
	}
	return m, nil
}

func (m Model) updateBoardKanban(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "j", "down":
		if m.boardSelRow < len(m.tasks)-1 {
			m.boardSelRow++
		}
		return m, nil
	case "k", "up":
		if m.boardSelRow > 0 {
			m.boardSelRow--
		}
		return m, nil
	case "enter":
		// Open the selected task's detail (placeholder: just show it).
		return m, nil
	case "n":
		return m.startCreateTask()
	case "d":
		if len(m.tasks) == 0 {
			return m, nil
		}
		task := m.tasks[m.boardSelRow]
		m.boardLoading = true
		m.boardToken++
		return m, doneTaskCmd(m.client, task.ID, task.Version, m.boardToken)
	case "m":
		// Move the selected task to another column: prompt for column id.
		if len(m.tasks) == 0 {
			return m, nil
		}
		m.boardInputKind = 4 // move task target column
		m.boardInput = textinput.New()
		m.boardInput.Placeholder = "ID de la columna destino"
		m.boardInput.Focus()
		m.boardInput.Width = 30
		return m, nil
	case "g":
		if m.client == nil {
			return m, nil
		}
		m.boardLoading = true
		m.boardToken++
		return m, loadBoardCmd(m.client, m.boardID, m.boardToken)
	}
	return m, nil
}

// updateBoardInput handles textinput keys while a board overlay is open.
func (m Model) updateBoardInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.boardInputKind = 0
		m.boardInput.Reset()
		return m, nil
	case "enter":
		return m.submitBoardInput()
	}
	// Let the textinput handle everything else.
	var cmd tea.Cmd
	m.boardInput, cmd = m.boardInput.Update(msg)
	return m, cmd
}

func (m Model) submitBoardInput() (tea.Model, tea.Cmd) {
	value := strings.TrimSpace(m.boardInput.Value())
	switch m.boardInputKind {
	case 1: // create board
		if value == "" {
			return m, nil
		}
		m.boardLoading = true
		m.boardToken++
		return m, createBoardCmd(m.client, value, m.boardToken)
	case 2: // join board
		if value == "" {
			return m, nil
		}
		m.boardLoading = true
		m.boardToken++
		return m, joinBoardCmd(m.client, value, "", "", "", m.boardToken)
	case 3: // create task title
		if value == "" {
			return m, nil
		}
		columnID := m.boardNewCol
		if columnID == "" && len(m.tasks) > 0 {
			columnID = m.tasks[0].ColumnID
		}
		m.boardLoading = true
		m.boardToken++
		return m, createTaskCmd(m.client, m.boardID, columnID, value, "", "", nil, nil, "", m.boardToken)
	case 4: // move task target column
		if value == "" {
			return m, nil
		}
		if len(m.tasks) == 0 {
			return m, nil
		}
		task := m.tasks[m.boardSelRow]
		m.boardInputKind = 0
		m.boardInput.Reset()
		m.boardLoading = true
		m.boardToken++
		return m, moveTaskCmd(m.client, task.ID, value, m.boardToken)
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// Board views
// ---------------------------------------------------------------------------

func (m Model) boardView() string {
	if m.boardInputKind != 0 {
		return m.boardInputView()
	}
	if m.boardList {
		return m.boardListView()
	}
	return m.boardKanbanView()
}

func (m Model) boardListView() string {
	var out strings.Builder

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#4db0ff"))
	out.WriteString(titleStyle.Render("Tableros compartidos"))
	out.WriteString("\n\n")

	if m.boardLoading {
		out.WriteString("Cargando tableros…\n\n")
	}
	if m.boardErr != nil {
		fmt.Fprintf(&out, "Error: %s\n\n", humanError(m.boardErr))
	}

	if len(m.boards) == 0 {
		out.WriteString("No hay tableros todavía.\n\n")
	} else {
		for i, b := range m.boards {
			selected := i == m.boardSelRow
			line := fmt.Sprintf("  %s  %s", b.Name, b.ID)
			if selected {
				line = "> " + line
				selStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#25d366"))
				out.WriteString(selStyle.Render(line))
			} else {
				out.WriteString(line)
			}
			out.WriteString("\n")
		}
		out.WriteString("\n")
	}

	out.WriteString(hintLine(m.width, m.boardListHints()...))
	return out.String()
}

func (m Model) boardListHints() []keyHint {
	hints := []keyHint{
		{"q", "volver", true},
		{"↵", "abrir", false},
		{"j/k", "seleccionar", false},
		{"c", "crear", false},
		{"J", "unirse", false},
		{"g", "refrescar", false},
		{"?", "ayuda", true},
	}
	return m.leaveHints(hints)
}

func (m Model) boardKanbanView() string {
	var out strings.Builder

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#25d366"))
	out.WriteString(titleStyle.Render(m.boardName))
	out.WriteString("\n\n")

	if m.boardLoading {
		out.WriteString("Cargando tablero…\n\n")
	}
	if m.boardErr != nil {
		fmt.Fprintf(&out, "Error: %s\n\n", humanError(m.boardErr))
	}

	columns := m.groupTasksByColumn()
	if len(columns) == 0 {
		out.WriteString("No hay columnas ni tareas.\n\n")
	} else {
		columnViews := make([]string, 0, len(columns))
		for _, col := range columns {
			columnViews = append(columnViews, m.renderColumn(col))
		}
		out.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, columnViews...))
		out.WriteString("\n\n")
	}

	out.WriteString(hintLine(m.width, m.boardKanbanHints()...))
	return out.String()
}

type boardColumn struct {
	id    string
	name  string
	tasks []*core.Task
}

func (m Model) groupTasksByColumn() []boardColumn {
	order := make([]string, 0, len(m.tasks))
	seen := make(map[string]bool)
	for _, t := range m.tasks {
		if !seen[t.ColumnID] {
			seen[t.ColumnID] = true
			order = append(order, t.ColumnID)
		}
	}
	columns := make([]boardColumn, 0, len(order))
	for _, cid := range order {
		col := boardColumn{id: cid, name: "Columna " + shortID(cid)}
		for _, t := range m.tasks {
			if t.ColumnID == cid {
				col.tasks = append(col.tasks, t)
			}
		}
		columns = append(columns, col)
	}
	return columns
}

// shortID returns the last segment of an id for display (or the whole id
// if it is shorter than 8 chars).
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[len(id)-8:]
}

func (m Model) renderColumn(col boardColumn) string {
	var out strings.Builder

	headStyle := lipgloss.NewStyle().Bold(true).Border(lipgloss.NormalBorder(), false, false, true, false)
	fmt.Fprintf(&out, "%s\n", headStyle.Render(fmt.Sprintf("%s (%d)", col.name, len(col.tasks))))
	out.WriteString("─────────\n")

	for _, t := range col.tasks {
		selected := m.boardSelRow >= 0 && m.boardSelRow < len(m.tasks) && m.tasks[m.boardSelRow].ID == t.ID
		line := fmt.Sprintf("  %s", safeLine(t.Title))
		if selected {
			selStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#25d366"))
			out.WriteString(selStyle.Render("> " + line[2:]))
		} else {
			out.WriteString(line)
		}
		out.WriteString("\n")
		if t.Description != "" {
			out.WriteString("    " + safeLine(truncate(t.Description, 40)) + "\n")
		}
		out.WriteString("\n")
	}
	return out.String()
}

func (m Model) boardKanbanHints() []keyHint {
	hints := []keyHint{
		{"q", "volver", true},
		{"↵", "abrir", false},
		{"j/k", "seleccionar", false},
		{"n", "nueva tarea", false},
		{"d", "marcar hecha", false},
		{"m", "mover columna", false},
		{"g", "refrescar", false},
		{"?", "ayuda", true},
	}
	return m.leaveHints(hints)
}

func (m Model) boardInputView() string {
	var out strings.Builder

	title := ""
	prompt := ""
	switch m.boardInputKind {
	case 1:
		title = "Crear tablero"
		prompt = "Nombre del tablero:"
	case 2:
		title = "Unirse a tablero"
		prompt = "Código de invitación:"
	case 3:
		title = "Nueva tarea"
		prompt = "Título de la tarea:"
	case 4:
		title = "Mover tarea"
		prompt = "ID de la columna destino:"
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0dbd8b"))
	out.WriteString(titleStyle.Render(title))
	out.WriteString("\n\n")
	fmt.Fprintf(&out, "%s\n", prompt)
	out.WriteString(m.boardInput.View())
	out.WriteString("\n\n")
	out.WriteString("↵ confirmar · esc cancelar\n")
	return out.String()
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// truncate cuts s to at most n runes, adding "…" when truncated.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}
