package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"

	"github.com/reyer3/bunker-go/internal/core"
)

// Editing, deleting and reacting from the chat view (issues #76, #17).
// The chat view has no message selection (the composer always has
// focus and every plain key is draft text), so these act on the latest
// message instead and use Alt keys: Alt+E edits our last message in the
// composer, Alt+X deletes our last message for everyone, and Alt++
// reacts to the last message received. Each goes through the same
// preview (a dry-run plan) and confirm as a send.

// MessageClient is the optional Client capability behind these keys; a
// connection without it reports the action as unavailable.
type MessageClient interface {
	EditMessage(ctx context.Context, id, text string, dryRun bool) (core.Plan, core.Receipt, error)
	DeleteMessage(ctx context.Context, id string, dryRun bool) (core.Plan, core.Receipt, error)
	React(ctx context.Context, id, emoji string, dryRun bool) (core.Plan, core.Receipt, error)
}

const (
	chatEditKey   = "alt+e"
	chatDeleteKey = "alt+x"
	// chatReactKey is Alt with "+"; Alt+= is the same key without Shift
	// on most layouts, so it works too.
	chatReactKey    = "alt++"
	chatReactKeyAlt = "alt+="
)

// reactionChoices are the reactions the picker offers on 1-6; 0 removes
// ours.
var reactionChoices = []string{"👍", "❤️", "😂", "😮", "😢", "🙏"}

// chatActionPreviewWidth bounds the quoted message in the action's line.
const chatActionPreviewWidth = 40

// chatAction is one edit, delete or reaction in progress: picking an
// emoji (react only), waiting for its plan, waiting for the user's
// confirm, then sending.
type chatAction struct {
	kind    string // "edit", "delete", "react" or "call" (calls.go)
	id      string
	text    string // the new text, or the emoji ("" removes ours)
	quoted  string // the target message's text, for the prompt
	picking bool
	pending bool
	confirm bool
	sending bool
}

type chatActionPlanMsg struct {
	token uint64
	plan  core.Plan
	err   error
}

type chatActionDoneMsg struct {
	token   uint64
	receipt core.Receipt
	call    core.Call
	err     error
}

var errNoMessageActions = errors.New("esta conexión no permite editar, eliminar ni reaccionar")

// lastOwnMessage is our newest message still there; forEdit also skips
// one with attachments or no text, which cannot be edited.
func (m Model) lastOwnMessage(forEdit bool) (core.Item, bool) {
	for i := len(m.chatItems) - 1; i >= 0; i-- {
		it := m.chatItems[i]
		if !it.FromMe || it.Deleted {
			continue
		}
		if forEdit && (len(it.Attachments) > 0 || it.Body == "") {
			continue
		}
		return it, true
	}
	return core.Item{}, false
}

// reactionTarget is the newest message received, or our own newest when
// the conversation has nothing from the other side.
func (m Model) reactionTarget() (core.Item, bool) {
	for i := len(m.chatItems) - 1; i >= 0; i-- {
		if it := m.chatItems[i]; !it.FromMe && !it.Deleted {
			return it, true
		}
	}
	return m.lastOwnMessage(false)
}

// startChatEdit puts our last message in the composer; Ctrl+S (or ↵)
// then previews the edit instead of a send, and Esc gives the draft back.
func (m Model) startChatEdit() (tea.Model, tea.Cmd) {
	if m.chatEditID != "" {
		return m, nil
	}
	item, ok := m.lastOwnMessage(true)
	if !ok {
		m.chatSendErr = errors.New("no hay un mensaje tuyo para editar")
		return m, nil
	}
	m.chatSendErr = nil
	m.chatEditDraft = m.composer.Value()
	m.chatEditID = item.ID
	m.composer.SetValue(item.Body)
	return m.resizeChatComposer(), nil
}

// cancelChatEdit leaves edit mode, restoring the draft it set aside.
func (m Model) cancelChatEdit() Model {
	if m.chatEditID == "" {
		return m
	}
	m.composer.SetValue(m.chatEditDraft)
	m.chatEditID, m.chatEditDraft = "", ""
	m.chatAction = nil
	return m.resizeChatComposer()
}

// previewChatEdit asks for the plan of the edit in the composer.
func (m Model) previewChatEdit() (tea.Model, tea.Cmd) {
	text := m.composer.Value()
	if strings.TrimSpace(text) == "" {
		m.chatSendErr = errors.New("el mensaje editado está vacío; para borrarlo usa Alt+X")
		return m, nil
	}
	if len(m.chatAttachments) > 0 {
		m.chatSendErr = errors.New("una edición no puede llevar adjuntos")
		return m, nil
	}
	m.chatSendErr = nil
	return m.planChatAction(&chatAction{kind: "edit", id: m.chatEditID, text: text})
}

func (m Model) startChatDelete() (tea.Model, tea.Cmd) {
	item, ok := m.lastOwnMessage(false)
	if !ok {
		m.chatSendErr = errors.New("no hay un mensaje tuyo para eliminar")
		return m, nil
	}
	m.chatSendErr = nil
	return m.planChatAction(&chatAction{kind: "delete", id: item.ID, quoted: item.Body})
}

func (m Model) startChatReact() (tea.Model, tea.Cmd) {
	item, ok := m.reactionTarget()
	if !ok {
		m.chatSendErr = errors.New("no hay mensajes a los que reaccionar")
		return m, nil
	}
	m.chatSendErr = nil
	m.chatAction = &chatAction{kind: "react", id: item.ID, quoted: item.Body, picking: true}
	return m, nil
}

// updateChatAction handles keys while an action is on screen: the emoji
// digits while picking, ↵/Ctrl+S to confirm, Esc to drop it (an edit
// stays in the composer). Everything else is ignored so a stray key
// cannot land in the draft behind the prompt.
func (m Model) updateChatAction(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	a := m.chatAction
	if a.pending || a.sending {
		return m, nil
	}
	key := msg.String()
	if key == "esc" {
		m.chatAction = nil
		return m, nil
	}
	switch {
	case a.picking:
		if len(key) != 1 || key[0] < '0' || key[0]-'0' > byte(len(reactionChoices)) {
			return m, nil
		}
		next := *a
		next.picking = false
		if n := int(key[0] - '0'); n > 0 {
			next.text = reactionChoices[n-1]
		}
		return m.planChatAction(&next)
	case a.confirm && (key == "enter" || key == "ctrl+s"):
		next := *a
		next.confirm, next.sending = false, true
		m.chatAction = &next
		m.chatActionToken++
		return m, m.chatActionCmd(&next, false)
	}
	return m, nil
}

func (m Model) planChatAction(a *chatAction) (tea.Model, tea.Cmd) {
	a.pending = true
	m.chatAction = a
	m.chatActionToken++
	return m, m.chatActionCmd(a, true)
}

// chatActionCmd runs the action's dry-run plan or the real call, tagged
// with the current chatActionToken (callers bump it first).
func (m Model) chatActionCmd(a *chatAction, dryRun bool) tea.Cmd {
	client, token, action := m.client, m.chatActionToken, *a
	channel, account := m.chatChannel, m.chatAccount
	return func() tea.Msg {
		if action.kind == "call" {
			return placeCallAction(client, channel, account, action.id, token, dryRun)
		}
		mc, ok := client.(MessageClient)
		if !ok {
			if dryRun {
				return chatActionPlanMsg{token: token, err: errNoMessageActions}
			}
			return chatActionDoneMsg{token: token, err: errNoMessageActions}
		}
		timeout := sendTimeout
		if dryRun {
			timeout = previewTimeout
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		var plan core.Plan
		var receipt core.Receipt
		var err error
		switch action.kind {
		case "edit":
			plan, receipt, err = mc.EditMessage(ctx, action.id, action.text, dryRun)
		case "delete":
			plan, receipt, err = mc.DeleteMessage(ctx, action.id, dryRun)
		default:
			plan, receipt, err = mc.React(ctx, action.id, action.text, dryRun)
		}
		if dryRun {
			return chatActionPlanMsg{token: token, plan: plan, err: err}
		}
		return chatActionDoneMsg{token: token, receipt: receipt, err: err}
	}
}

// placeCallAction previews (dryRun) or places the call to `to`, as the
// same plan/done messages the other chat actions use.
func placeCallAction(client Client, channel core.Channel, account, to string, token uint64, dryRun bool) tea.Msg {
	cc, ok := client.(CallClient)
	var plan core.Plan
	var call core.Call
	var err error
	if !ok {
		err = errNoCalls
	} else {
		timeout := sendTimeout
		if dryRun {
			timeout = previewTimeout
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		plan, call, err = cc.PlaceCall(ctx, channel, account, to, dryRun)
		err = callActionError(err)
	}
	if dryRun {
		return chatActionPlanMsg{token: token, plan: plan, err: err}
	}
	return chatActionDoneMsg{token: token, call: call, err: err}
}

func (m Model) handleChatActionPlan(msg chatActionPlanMsg) (tea.Model, tea.Cmd) {
	if msg.token != m.chatActionToken || m.chatAction == nil || !m.chatAction.pending || !m.chatMode {
		return m, nil
	}
	if msg.err != nil {
		m.chatAction = nil
		m.chatSendErr = msg.err
		return m, nil
	}
	next := *m.chatAction
	next.pending, next.confirm = false, true
	if next.quoted == "" && next.kind == "delete" {
		next.quoted = msg.plan.Preview
	}
	m.chatAction = &next
	return m, nil
}

func (m Model) handleChatActionDone(msg chatActionDoneMsg) (tea.Model, tea.Cmd) {
	if msg.token != m.chatActionToken || m.chatAction == nil || !m.chatAction.sending || !m.chatMode {
		return m, nil
	}
	a := m.chatAction
	m.chatAction = nil
	if msg.err != nil {
		m.chatSendErr = msg.err
		return m, nil
	}
	m.chatSendErr = nil
	var done string
	switch {
	case a.kind == "call":
		// The banner follows at once; the next poll confirms it.
		m = m.upsertCall(msg.call)
		return m.withFlash("llamando a " + safeLine(a.quoted)), nil
	case a.kind == "edit":
		m = m.cancelChatEdit()
		done = "mensaje editado"
	case a.kind == "delete":
		done = "mensaje eliminado para todos"
	case a.text == "":
		done = "reacción quitada"
	default:
		done = "reacción enviada"
	}
	m = m.withFlash(done)
	m.chatReplyToken++
	return m, reloadChatAfterSend(m.client, m.chatChannel, m.chatAccount, m.chatThread, "", m.chatReplyToken)
}

// chatActionLine is the tail line for the action on screen, or for edit
// mode while the edited text is in the composer.
func (m Model) chatActionLine() (string, bool) {
	a := m.chatAction
	if a == nil {
		if m.chatEditID != "" {
			return "Editando tu último mensaje · Ctrl+S guardar · Esc cancelar", true
		}
		return "", false
	}
	quoted := "«" + runewidth.Truncate(safeLine(a.quoted), chatActionPreviewWidth, "…") + "»"
	switch {
	case a.pending:
		return "Preparando…", true
	case a.sending:
		return "Enviando…", true
	case a.picking:
		opts := make([]string, 0, len(reactionChoices)+1)
		for i, e := range reactionChoices {
			opts = append(opts, fmt.Sprintf("%d %s", i+1, e))
		}
		return fmt.Sprintf("Reaccionar a %s: %s · 0 quitar · Esc cancelar", quoted, strings.Join(opts, " · ")), true
	case a.kind == "call":
		return fmt.Sprintf("¿Llamar a %s? ↵ llamar · Esc cancelar", quoted), true
	case a.kind == "edit":
		return fmt.Sprintf("¿Guardar la edición «%s»? ↵ confirmar · Esc cancelar", runewidth.Truncate(safeLine(a.text), chatActionPreviewWidth, "…")), true
	case a.kind == "delete":
		return fmt.Sprintf("¿Eliminar para todos %s? ↵ eliminar · Esc cancelar", quoted), true
	case a.text == "":
		return fmt.Sprintf("¿Quitar tu reacción de %s? ↵ confirmar · Esc cancelar", quoted), true
	default:
		return fmt.Sprintf("¿Reaccionar %s a %s? ↵ enviar · Esc cancelar", a.text, quoted), true
	}
}
