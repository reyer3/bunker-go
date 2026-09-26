package tui

import (
	"fmt"
	"io"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
	"github.com/reyer3/bunker-go/internal/core"
)

// notifyRateLimit is how often a desktop notification may fire; arrivals
// within the window are coalesced into one later "N new" notification
// instead of being dropped or spamming one per poll.
const notifyRateLimit = 10 * time.Second

// notifyBodyWidth is how much of a message body a notification shows;
// full message bodies never leave the panel via this path.
const notifyBodyWidth = 60

// notifyPayload builds the sanitized OSC 777 desktop-notification escape
// sequence ("notify;<title>;<body>", BEL-terminated). title and body are
// run through the same terminal-escape sanitizer as everything else the
// daemon returns, so a malicious sender name or message body can never
// inject its own escape sequence into what reaches the real terminal;
// only bunker's own OSC 777 survives.
func notifyPayload(title, body string) string {
	title = safeLine(title)
	body = runewidth.Truncate(safeLine(body), notifyBodyWidth+1, "…")
	return "\x1b]777;notify;" + title + ";" + body + "\a"
}

// tmuxPassthrough wraps payload in tmux's DCS passthrough sequence (ESC P
// tmux; ... ESC \), doubling every embedded ESC byte as tmux's passthrough
// protocol requires, so the outer terminal (Ghostty) receives payload
// unchanged instead of tmux swallowing it as its own DCS.
func tmuxPassthrough(payload string) string {
	return "\x1bPtmux;" + strings.ReplaceAll(payload, "\x1b", "\x1b\x1b") + "\x1b\\"
}

// newUnreadItems returns the items in newItems that are unread and whose
// ID is not present anywhere in oldGroups — i.e. conversations that were
// not part of the previous loaded snapshot. It never returns anything on
// the very first load because the caller only calls it on a subsequent
// poll (see the wasLoaded guard in Update's inboxLoadedMsg handling).
func newUnreadItems(oldGroups []inboxGroup, newItems []core.Item) []core.Item {
	seen := make(map[string]bool)
	for _, g := range oldGroups {
		for _, it := range g.items {
			seen[it.ID] = true
		}
	}
	var fresh []core.Item
	for _, it := range newItems {
		if it.Unread && !seen[it.ID] {
			fresh = append(fresh, it)
		}
	}
	return fresh
}

// notificationText builds a notification's title/body from fresh (this
// batch's newly-arrived unread items) plus pendingExtra (items from
// earlier batches coalesced into this one, see shouldNotify). More than
// one item total coalesces into a generic "bunker"/"N new" notification;
// exactly one shows its sender (never a raw id) and the first 60 cells
// of its body.
func notificationText(fresh []core.Item, pendingExtra int) (title, body string) {
	total := len(fresh) + pendingExtra
	if total > 1 {
		return "bunker", fmt.Sprintf("%d new", total)
	}
	item := fresh[len(fresh)-1]
	sender := strings.TrimSpace(item.From.Name)
	if sender == "" || looksLikeRawIdentifier(sender) {
		sender = channelNames[item.Channel]
	}
	return sender, item.Body
}

// shouldNotify decides whether a qualifying arrival of freshCount new
// unread items fires a notification now or is coalesced into pending for
// a later one, given lastNotifyAt (zero if none has fired yet) and
// pendingBefore (items already coalesced since the last actual send).
// Firing resets pending to 0; not firing carries pendingBefore+freshCount
// forward.
func shouldNotify(lastNotifyAt time.Time, pendingBefore, freshCount int, now time.Time) (fire bool, pendingAfter int) {
	if lastNotifyAt.IsZero() || now.Sub(lastNotifyAt) >= notifyRateLimit {
		return true, 0
	}
	return false, pendingBefore + freshCount
}

// resolveNotifyEnabled applies G3's two independent opt-outs: an
// explicit `false` in [tui] notify, or BUNKER_TUI_NOTIFY=0 in the
// environment. Either alone disables notifications; neither re-enables
// what the other disabled. The default, with neither set, is enabled.
func resolveNotifyEnabled(cfgNotify *bool, getenv func(string) string) bool {
	if getenv("BUNKER_TUI_NOTIFY") == "0" {
		return false
	}
	if cfgNotify != nil && !*cfgNotify {
		return false
	}
	return true
}

// maybeNotify is called after every successful poll with the snapshot
// from just before it (oldGroups) and the freshly-loaded items. It never
// fires on the very first load (wasLoaded false — the initial backlog is
// not "new"), when notifications are disabled ([tui] notify = false or
// BUNKER_TUI_NOTIFY=0, resolved into m.notifyEnabled by Run), or while
// the panel is focused (m.blurred false). Otherwise it applies the
// rate-limit/coalescing decision (shouldNotify) and, when it fires,
// returns a command that writes the sanitized, tmux-passthrough-wrapped
// OSC 777 sequence to m.notifyWriter.
func (m Model) maybeNotify(wasLoaded bool, oldGroups []inboxGroup, newItems []core.Item) (Model, tea.Cmd) {
	if !wasLoaded || !m.notifyEnabled || !m.blurred {
		return m, nil
	}
	fresh := newUnreadItems(oldGroups, newItems)
	if len(fresh) == 0 {
		return m, nil
	}
	now := m.clock()
	pendingBefore := m.pendingNotify
	fire, pending := shouldNotify(m.lastNotifyAt, pendingBefore, len(fresh), now)
	m.pendingNotify = pending
	if !fire {
		return m, nil
	}
	title, body := notificationText(fresh, pendingBefore)
	payload := notifyPayload(title, body)
	if m.tmuxPassthrough {
		payload = tmuxPassthrough(payload)
	}
	m.lastNotifyAt = now
	return m, notifyCmd(m.notifyWriter, payload)
}

// notifyCmd returns a command that writes payload to w (a no-op when w
// is nil, the default — tests and any Model built without explicit
// notify wiring never touch a real terminal). It never blocks Update:
// the write happens on the command's own goroutine, like every other
// I/O command in this package.
func notifyCmd(w io.Writer, payload string) tea.Cmd {
	return func() tea.Msg {
		if w != nil {
			_, _ = io.WriteString(w, payload)
		}
		return nil
	}
}
