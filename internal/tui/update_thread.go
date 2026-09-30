package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
)

// updateThread handles keys in the K6 mail thread view: j/k/up/down move
// the selection, Enter toggles the selected message's collapsed/expanded
// state, r/R/f open the full editor on the selected message, and Esc
// leaves the thread view.
func (m Model) updateThread(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "?":
		return m.openHelp(), nil
	case "a":
		if next, cmd, ok := m.plainCallKey("a"); ok {
			return next, cmd
		}
		return m.askAgent()
	case "x", "h":
		return m.plainCallKeyOrNothing(msg.String())
	case "esc", "q":
		if m.openID != "" {
			return m, tea.Quit
		}
		if msg.String() == "q" {
			return m, nil
		}
		m.detail = false
		m.threadMode = false
		return m, nil
	case "enter":
		if len(m.threadItems) == 0 {
			return m, nil
		}
		if m.threadExpanded == nil {
			m.threadExpanded = map[int]bool{}
		}
		nowExpanded := !m.threadExpanded[m.threadSelected]
		m.threadExpanded[m.threadSelected] = nowExpanded
		m = m.resetThreadScrollToSelected()
		if nowExpanded {
			return m.fetchThreadBodyIfNeeded(m.threadItems[m.threadSelected].ID)
		}
		return m, nil
	case "j", "down":
		if m.threadSelected < len(m.threadItems)-1 {
			m.threadSelected++
			m = m.resetThreadScrollToSelected()
		}
	case "k", "up":
		if m.threadSelected > 0 {
			m.threadSelected--
			m = m.resetThreadScrollToSelected()
		}
	case "pgup":
		budget := m.threadScrollBudget()
		m.threadScroll = clampScroll(m.threadScroll-budget, m.threadBodyLen(), budget)
	case "pgdown":
		budget := m.threadScrollBudget()
		m.threadScroll = clampScroll(m.threadScroll+budget, m.threadBodyLen(), budget)
	case "r":
		return m.openMailEditor("reply")
	case "R":
		return m.openMailEditor("replyAll")
	case "f":
		return m.openMailEditor("forward")
	case "d":
		if m.threadSelected >= 0 && m.threadSelected < len(m.threadItems) {
			item := m.threadItems[m.threadSelected]
			if len(item.Attachments) > 0 {
				return m.openDownload(item.ID, item.Attachments)
			}
		}
	}
	return m, nil
}

// openMailEditor opens K6's full To/Cc/Subject editor on the currently
// selected thread message, prefilled per action: "reply" (the sender),
// "replyAll" (every participant excluding our own address, see
// mailSelfAddress) or "forward" (empty To, the original's attachments
// listed informationally — re-attaching them needs a download first,
// which is not implemented here; see the K6 commit's disclosure).
func (m Model) openMailEditor(action string) (tea.Model, tea.Cmd) {
	if m.threadSelected < 0 || m.threadSelected >= len(m.threadItems) {
		return m, nil
	}
	item := m.threadItems[m.threadSelected]
	m.mailComposing = true
	m.mailAction = action
	m.mailTargetID = item.ID
	m.mailChannel = item.Channel
	m.mailAccount = item.Account
	m.mailThread = item.Thread
	m.mailTo = newLineEditor()
	m.mailCc = newLineEditor()
	m.mailSubject = newLineEditor()
	m.mailAttachInfo = nil
	m.mailPreviewing = false
	m.mailSending = false
	m.mailSendErr = nil
	m.mailPlan = core.Plan{}
	m.composer = newComposer(m.width, m.renderer())
	m.mailFocus = 3

	switch action {
	case "reply":
		m.mailTo.SetValue(item.From.ID)
		m.mailSubject.SetValue(subjectWithPrefix(item.Subject, "Re: "))
	case "replyAll":
		self := mailSelfAddress(m.threadItems)
		m.mailTo.SetValue(strings.Join(replyAllRecipients(item, self), ", "))
		m.mailSubject.SetValue(subjectWithPrefix(item.Subject, "Re: "))
	case "forward":
		m.mailSubject.SetValue(subjectWithPrefix(item.Subject, "Fwd: "))
		m.mailAttachInfo = item.Attachments
		m.mailFocus = 0
	}
	m.composer.SetValue("\n\n" + quoteOriginal(item))
	m.composer.CursorStart()
	m = m.restoreMailDraft()
	m = m.withMailFocusApplied()
	return m, nil
}

// withMailFocusApplied blurs every editor field and focuses only the one
// mailFocus names (0=To, 1=Cc, 2=Subject, else the composer/body).
func (m Model) withMailFocusApplied() Model {
	m.mailTo.Blur()
	m.mailCc.Blur()
	m.mailSubject.Blur()
	m.composer.Blur()
	switch m.mailFocus {
	case 0:
		m.mailTo.Focus()
	case 1:
		m.mailCc.Focus()
	case 2:
		m.mailSubject.Focus()
	default:
		m.composer.Focus()
	}
	return m
}

// buildMailOutgoing assembles the editor's fields into the core.Outgoing
// Send needs.
func (m Model) buildMailOutgoing() core.Outgoing {
	return core.Outgoing{
		Channel: m.mailChannel,
		Account: m.mailAccount,
		To:      splitRecipients(m.mailTo.Value()),
		Cc:      splitRecipients(m.mailCc.Value()),
		Thread:  m.mailThread,
		ReplyTo: m.mailTargetID,
		Subject: m.mailSubject.Value(),
		Body:    m.composer.Value(),
	}
}

// updateMailEditor handles keys in K6's full editor: Tab/Shift+Tab cycle
// To/Cc/Subject/body focus, Ctrl+S requests a dry-run preview, Esc
// discards the draft and closes the editor, and every other key goes to
// whichever field is focused.
func (m Model) updateMailEditor(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.mailPreviewing {
		return m.updateMailPreview(msg)
	}
	switch msg.String() {
	case "esc":
		if m.mailDrafts == nil {
			m.mailDrafts = map[string]mailDraft{}
		}
		m.mailDrafts[mailDraftKey(m.mailAction, m.mailTargetID)] = mailDraft{to: m.mailTo.Value(), cc: m.mailCc.Value(), subject: m.mailSubject.Value(), body: m.composer.Value()}
		m.mailComposing = false
		return m.withFlash("borrador guardado"), nil
	case "tab":
		m.mailFocus = (m.mailFocus + 1) % 4
		m = m.withMailFocusApplied()
		return m, nil
	case "shift+tab":
		m.mailFocus = (m.mailFocus - 1 + 4) % 4
		m = m.withMailFocusApplied()
		return m, nil
	case "ctrl+s":
		out := m.buildMailOutgoing()
		m.mailSendErr = nil
		m.mailToken++
		return m, previewMailSend(m.client, out, m.mailToken)
	}
	var cmd tea.Cmd
	switch m.mailFocus {
	case 0:
		m.mailTo, cmd = m.mailTo.Update(msg)
	case 1:
		m.mailCc, cmd = m.mailCc.Update(msg)
	case 2:
		m.mailSubject, cmd = m.mailSubject.Update(msg)
	default:
		m.composer, cmd = m.composer.Update(msg)
	}
	return m, cmd
}

// updateMailPreview handles keys once the editor's dry-run preview is
// showing: Enter is the only way to send for real, guarded against a
// second send while one is in flight; Esc returns to editing without
// ever sending.
func (m Model) updateMailPreview(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.mailSending {
		return m, nil
	}
	switch msg.String() {
	case "esc":
		m.mailPreviewing = false
		return m, nil
	case "enter", "ctrl+s":
		m.mailSending = true
		out := m.buildMailOutgoing()
		return m, sendMailSend(m.client, out, m.mailToken)
	}
	return m, nil
}
