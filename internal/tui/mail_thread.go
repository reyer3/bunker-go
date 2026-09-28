package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
)

// threadLoadedMsg carries the K6 mail thread view's open result: the
// conversation's items (oldest→newest) and, only on the open itself
// (never on a later "toggle" — there is none for mail; K6 has no
// pagination requirement), the immediate mark-\Seen result.
type threadLoadedMsg struct {
	token    uint64
	items    []core.Item
	itemsErr error
	seenErr  error
}

// openThreadCmd loads the conversation and marks the WHOLE conversation
// \Seen via ReadThread — receipt=true mapping to mail's \Seen semantics —
// no confirm ("opening is the explicit action", conversation-view.md), in
// one command, mirroring openChatCmd's shape for the chat view. K9's fix:
// the old Organize(Seen=true) on a single id left every other unread item
// stranded.
func openThreadCmd(client Client, channel core.Channel, account, thread string, token uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		items, itemsErr := client.Thread(ctx, string(channel), account, thread, time.Time{}, threadPageLimit)
		_, seenErr := client.ReadThread(ctx, string(channel), account, thread, true)
		return threadLoadedMsg{token: token, items: items, itemsErr: itemsErr, seenErr: seenErr}
	}
}

// threadBodyLoadedMsg carries K8's on-demand body fetch result: id names
// which message this is (so a slow fetch racing a newer one can never
// land on the wrong cache entry), and token is the threadToken at the
// moment the fetch was requested — if the thread view has since moved on
// to a different conversation (a new threadToken), Update discards this
// message instead of caching a stale body into the wrong thread.
type threadBodyLoadedMsg struct {
	token uint64
	id    string
	body  string
	err   error
}

// fetchThreadBodyCmd fetches id's full body via Read(id, receipt=false) —
// no read receipt: K8's body fetch is incidental to viewing, not the
// explicit "open this conversation" action openThreadCmd's Organize
// Seen=true already covers.
func fetchThreadBodyCmd(client Client, id string, token uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		item, err := client.Read(ctx, id, false)
		if err != nil {
			return threadBodyLoadedMsg{token: token, id: id, err: err}
		}
		return threadBodyLoadedMsg{token: token, id: id, body: item.Body}
	}
}

// fetchThreadBodyIfNeeded starts K8's on-demand body fetch for id unless
// it is already cached or a fetch for it is already in flight — so
// re-expanding an already-fetched message, or the same message twice in a
// row, never issues a redundant Read call.
func (m Model) fetchThreadBodyIfNeeded(id string) (Model, tea.Cmd) {
	if _, cached := m.threadBodies[id]; cached {
		return m, nil
	}
	if m.threadBodyLoading[id] {
		return m, nil
	}
	if m.threadBodyLoading == nil {
		m.threadBodyLoading = map[string]bool{}
	}
	m.threadBodyLoading[id] = true
	return m, fetchThreadBodyCmd(m.client, id, m.threadToken)
}

// threadBodyLen is the total line count of the thread's full rendered
// body (threadBodyLinesWithStarts), for scroll-clamping without needing
// the caller to build/discard the starts slice too.
func (m Model) threadBodyLen() int {
	lines, _ := m.threadBodyLinesWithStarts()
	return len(lines)
}

// resetThreadScrollToSelected scrolls the view so the selected item's own
// starting line is the top of the window — called whenever the selection
// (j/k) or its expand state (Enter) changes, so a newly expanded long
// body always starts in view instead of leaving the window wherever a
// PgUp/PgDown scroll had last left it (the parent's "must stay visible"
// fix).
func (m Model) resetThreadScrollToSelected() Model {
	lines, starts := m.threadBodyLinesWithStarts()
	if m.threadSelected < 0 || m.threadSelected >= len(starts) {
		m.threadScroll = 0
		return m
	}
	m.threadScroll = clampScroll(starts[m.threadSelected], len(lines), m.threadScrollBudget())
	return m
}

// threadItemBody resolves item's displayed body: the K8 fetch cache once
// it has landed, else item.Body as already loaded by Thread (empty for a
// real mail sync until fetched — conversation-view.md's reported bug —
// but tests may set it directly).
func (m Model) threadItemBody(item core.Item) string {
	if b, ok := m.threadBodies[item.ID]; ok {
		return b
	}
	return item.Body
}

// threadSnippet is a collapsed row's "sender · date · snippet" third
// field: the first line of the resolved body, falling back to the
// Subject when no body is available yet — never the sender's name again
// (that was the reported K8 bug: previewLine's "Sender: body" format
// repeated the sender already shown before the "·").
func threadSnippet(item core.Item, body string) string {
	text := strings.TrimSpace(safeLine(firstLine(body)))
	if text == "" {
		text = strings.TrimSpace(safeLine(item.Subject))
	}
	return text
}

// firstLine returns s up to its first newline (or all of s if there is
// none).
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// mailPreviewMsg/mailSentMsg mirror replyPreviewMsg/replySentMsg but for
// the K6 editor's Send call.
type mailPreviewMsg struct {
	token uint64
	plan  core.Plan
	err   error
}

type mailSentMsg struct {
	token uint64
	err   error
}

func previewMailSend(client Client, out core.Outgoing, token uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), previewTimeout)
		defer cancel()
		plan, _, err := client.Send(ctx, out, true)
		return mailPreviewMsg{token: token, plan: plan, err: err}
	}
}

func sendMailSend(client Client, out core.Outgoing, token uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
		defer cancel()
		_, _, err := client.Send(ctx, out, false)
		return mailSentMsg{token: token, err: err}
	}
}

// newLineEditor returns a bubbles/textinput configured to match the
// composer's plain, static-cursor determinism (see composer.go's doc
// comment) for the K6 editor's single-line To/Cc/Subject fields. Width is
// left at its natural (content-driven) size rather than padded to the
// terminal width: the surrounding "To: "/"Cc: "/"Subject: " prefix and
// the outer wrapView already handle fitting the whole line, and a padded
// fixed Width here previously overflowed past that prefix and produced a
// stray wrapped remainder line.
func newLineEditor() textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Cursor.SetMode(cursor.CursorStatic)
	return ti
}

// mailSelfAddress returns the account's own address, identified as the
// From address of any FromMe item in the loaded thread (the contract's
// FromMe field, conversation-view.md). It returns "" when no such item
// has loaded yet — e.g. before the daemon's K2 Sent-folder sync lands, or
// on a thread with no prior reply — in which case reply-all degrades to
// including every participant (a disclosed, daemon-dependent gap).
func mailSelfAddress(items []core.Item) string {
	for _, item := range items {
		if item.FromMe && item.From.ID != "" {
			return item.From.ID
		}
	}
	return ""
}

// replyAllRecipients returns item's From plus To, deduplicated and
// excluding self (see mailSelfAddress).
func replyAllRecipients(item core.Item, self string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(addr string) {
		addr = strings.TrimSpace(addr)
		if addr == "" || addr == self || seen[addr] {
			return
		}
		seen[addr] = true
		out = append(out, addr)
	}
	add(item.From.ID)
	for _, to := range item.To {
		add(to.ID)
	}
	return out
}

// subjectWithPrefix adds prefix unless subject already carries it
// (case-insensitive), so replying to a reply doesn't pile up "Re: Re: ".
func subjectWithPrefix(subject, prefix string) string {
	trimmed := strings.TrimSpace(subject)
	if strings.HasPrefix(strings.ToLower(trimmed), strings.ToLower(prefix)) {
		return trimmed
	}
	return prefix + trimmed
}

// quoteOriginal renders item's body as a quoted block under an
// attribution line, the way a mail client's reply/forward body starts.
func quoteOriginal(item core.Item) string {
	var out strings.Builder
	fmt.Fprintf(&out, "El %s, %s escribió:\n", item.Timestamp.Format("02-01-2006 15:04"), safeLine(item.From.Name))
	body := sanitizeTerminalText(item.Body)
	for _, line := range strings.Split(body, "\n") {
		out.WriteString("> ")
		out.WriteString(line)
		out.WriteString("\n")
	}
	return out.String()
}

// splitRecipients parses a comma-separated To/Cc field into trimmed,
// non-empty addresses.
func splitRecipients(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
