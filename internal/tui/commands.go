package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
)

const inboxLimit = 200

const (
	pollInterval   = 5 * time.Second
	pollTimeout    = 4 * time.Second
	readTimeout    = 30 * time.Second
	previewTimeout = 10 * time.Second
	sendTimeout    = 15 * time.Minute
)

type inboxLoadedMsg struct {
	token     uint64
	items     []core.Item
	counts    map[core.Channel]map[string]int
	chats     map[core.Channel][]core.Conversation
	listErr   error
	countsErr error
	health    []core.AdapterHealth
	update    core.UpdateStatus
	// meetings is the poll's upcoming meetings; meetingsOK is false when
	// they could not be loaded, so the model keeps what it showed.
	meetings   []core.UpcomingMeeting
	meetingsOK bool
	// todos and awaiting feed the Pendientes section; pendingOK is false
	// when they could not be loaded, so the model keeps what it showed.
	todos     []core.Todo
	awaiting  []core.Awaiting
	pendingOK bool
}

type pollTickMsg struct{ token uint64 }

func nextPoll(token uint64) tea.Cmd {
	return tea.Tick(pollInterval, func(time.Time) tea.Msg { return pollTickMsg{token: token} })
}

type itemReadMsg struct {
	id    string
	token uint64
	item  core.Item
	err   error
}

func readItem(client Client, id string, token uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		item, err := client.Read(ctx, id, false)
		return itemReadMsg{id: id, token: token, item: item, err: err}
	}
}

type replyPreviewMsg struct {
	token uint64
	plan  core.Plan
	err   error
}

type replySentMsg struct {
	token   uint64
	plan    core.Plan
	receipt core.Receipt
	err     error
}

// previewReply asks the daemon to plan (but not send) a reply. The dry-run
// is mandatory before any real send and may still hit the network (e.g. a
// Matrix media policy check), so it gets its own bounded deadline rather
// than pretending it is offline-only.
func previewReply(client Client, id, body string, attachments []string, token uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), previewTimeout)
		defer cancel()
		plan, _, err := client.Reply(ctx, id, body, nil, attachments, true)
		return replyPreviewMsg{token: token, plan: plan, err: err}
	}
}

// sendReply performs the real send after an explicit confirm. It is called
// at most once per confirm (the model guards re-entry while sending) and is
// never retried automatically on error.
func sendReply(client Client, id, body string, attachments []string, token uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
		defer cancel()
		plan, receipt, err := client.Reply(ctx, id, body, nil, attachments, false)
		return replySentMsg{token: token, plan: plan, receipt: receipt, err: err}
	}
}

type markPreviewMsg struct {
	token uint64
	plan  core.Plan
	err   error
}

type markSentMsg struct {
	token uint64
	plan  core.Plan
	err   error
}

// previewMarkRead asks the daemon to plan (but not apply) marking id read.
// Opening/reading an item never does this; only the explicit "m" key does.
func previewMarkRead(client Client, id string, token uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), previewTimeout)
		defer cancel()
		seen := true
		plan, err := client.Organize(ctx, id, core.OrganizeOp{Seen: &seen}, true)
		return markPreviewMsg{token: token, plan: plan, err: err}
	}
}

// sendMarkRead applies the mark-read after an explicit confirm. Called at
// most once per confirm (the model guards re-entry while sending) and is
// never retried automatically on error.
func sendMarkRead(client Client, id string, token uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), previewTimeout)
		defer cancel()
		seen := true
		plan, err := client.Organize(ctx, id, core.OrganizeOp{Seen: &seen}, false)
		return markSentMsg{token: token, plan: plan, err: err}
	}
}

// statAttachment stats a local attachment path (no shell, spaces allowed)
// and returns the name/size the compose view shows, or a visible error.
func statAttachment(path string) (name string, size int64, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", 0, fmt.Errorf("attachment %s: %w", path, err)
	}
	if info.IsDir() {
		return "", 0, fmt.Errorf("attachment %s: is a directory", path)
	}
	return filepath.Base(path), info.Size(), nil
}

// validateAttachments re-stats every draft attachment right before a
// preview or a real send, so a file removed or replaced after it was added
// is caught locally instead of silently reaching the daemon.
func validateAttachments(paths []string) error {
	for _, path := range paths {
		if _, _, err := statAttachment(path); err != nil {
			return err
		}
	}
	return nil
}

func loadInbox(client Client, token uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), pollTimeout)
		defer cancel()
		unread := true
		items, err := client.List(ctx, core.Filter{Unread: &unread, Limit: inboxLimit})
		if err != nil {
			return inboxLoadedMsg{token: token, listErr: err}
		}
		counts, countsErr := client.Counts(ctx)
		chats := fetchChatLists(ctx, client)
		report := fetchHealthReport(ctx, client)
		meetings, meetingsOK := fetchMeetings(ctx, client)
		todos, awaiting, pendingOK := fetchPending(ctx, client)
		return inboxLoadedMsg{token: token, items: items, counts: counts, chats: chats, countsErr: countsErr, health: report.Adapters, update: report.Update, meetings: meetings, meetingsOK: meetingsOK,
			todos: todos, awaiting: awaiting, pendingOK: pendingOK}
	}
}
