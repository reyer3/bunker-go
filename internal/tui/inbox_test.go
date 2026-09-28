package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
)

type inboxClient struct {
	items       []core.Item
	counts      map[core.Channel]map[string]int
	listErr     error
	countsErr   error
	listFilter  core.Filter
	listCalls   int
	countsCalls int
	otherCalls  int
	readCalls   int
	readID      string
	readReceipt bool
	readResult  core.Item
	readErr     error
	// readIDs logs every id Read was called with, in order — K8's
	// body-fetch cache tests assert on this to prove a cached/in-flight
	// id is never fetched twice. readResults, when non-nil, answers a
	// specific id with its own Item instead of the single readResult
	// (K8 needs a different fetched body per message).
	readIDs     []string
	readResults map[string]core.Item

	threadCalls []threadCall
	threadItems []core.Item
	threadErr   error

	readThreadCalls []readThreadCall
	readThreadCount int
	readThreadErr   error
	presenceCalls   int
	presenceResult  core.Presence
	presenceErr     error
	keepaliveCalls  []keepaliveCall
	keepaliveErr    error
	typingCalls     []typingCall
	typingErr       error

	organizeCalls  []inboxOrganizeCall
	organizeResult core.Plan
	organizeErr    error

	downloadCalls  []downloadCall
	downloadResult core.DownloadResult
	downloadErr    error
}

// downloadCall records one Download call so K5/K6 download tests can
// assert the exact (id, index, destPath, Force) it sent without a real
// daemon or filesystem write.
type downloadCall struct {
	id       string
	index    int
	destPath string
	opts     core.DownloadOptions
}

// threadCall/keepaliveCall/typingCall record every K5/K6 chat/thread RPC
// call so tests can assert pagination bounds, the ≤20s keepalive cadence,
// and the ≤5s typing throttle without a real daemon.
type threadCall struct {
	channel         core.Channel
	account, thread string
	before          time.Time
	limit           int
}
type readThreadCall struct {
	channel         core.Channel
	account, thread string
	receipt         bool
}
type keepaliveCall struct {
	channel         core.Channel
	account, thread string
	focused         bool
}
type typingCall struct {
	channel         core.Channel
	account, thread string
	composing       bool
}

func (c *inboxClient) Thread(_ context.Context, channel string, account, thread string, before time.Time, limit int) ([]core.Item, error) {
	c.otherCalls++
	c.threadCalls = append(c.threadCalls, threadCall{channel: core.Channel(channel), account: account, thread: thread, before: before, limit: limit})
	return c.threadItems, c.threadErr
}

func (c *inboxClient) ReadThread(_ context.Context, channel string, account, thread string, receipt bool) (int, error) {
	c.otherCalls++
	c.readThreadCalls = append(c.readThreadCalls, readThreadCall{channel: core.Channel(channel), account: account, thread: thread, receipt: receipt})
	return c.readThreadCount, c.readThreadErr
}

func (c *inboxClient) Presence(context.Context, string, string, string) (core.Presence, error) {
	c.otherCalls++
	c.presenceCalls++
	return c.presenceResult, c.presenceErr
}

func (c *inboxClient) PresenceKeepalive(_ context.Context, channel string, account, thread string, focused bool) error {
	c.otherCalls++
	c.keepaliveCalls = append(c.keepaliveCalls, keepaliveCall{channel: core.Channel(channel), account: account, thread: thread, focused: focused})
	return c.keepaliveErr
}

func (c *inboxClient) Typing(_ context.Context, channel string, account, thread string, composing bool) error {
	c.otherCalls++
	c.typingCalls = append(c.typingCalls, typingCall{channel: core.Channel(channel), account: account, thread: thread, composing: composing})
	return c.typingErr
}

func (c *inboxClient) List(_ context.Context, filter core.Filter) ([]core.Item, error) {
	c.listCalls++
	c.listFilter = filter
	return c.items, c.listErr
}

func (c *inboxClient) Counts(context.Context) (map[core.Channel]map[string]int, error) {
	c.countsCalls++
	return c.counts, c.countsErr
}

func (c *inboxClient) Read(_ context.Context, id string, receipt bool) (core.Item, error) {
	c.otherCalls++
	c.readCalls++
	c.readID = id
	c.readReceipt = receipt
	c.readIDs = append(c.readIDs, id)
	if c.readResults != nil {
		if item, ok := c.readResults[id]; ok {
			return item, c.readErr
		}
	}
	return c.readResult, c.readErr
}

func (c *inboxClient) Reply(context.Context, string, string, []string, []string, bool) (core.Plan, core.Receipt, error) {
	c.otherCalls++
	return core.Plan{}, core.Receipt{}, nil
}

// inboxOrganizeCall records one Organize call for tests that need to assert
// on it via the shared inboxClient fake (K6's mark-\Seen-on-open, without
// needing the separate markClient fake mark_test.go uses for its own
// dry-run/confirm flow).
type inboxOrganizeCall struct {
	id     string
	op     core.OrganizeOp
	dryRun bool
}

func (c *inboxClient) Organize(_ context.Context, id string, op core.OrganizeOp, dryRun bool) (core.Plan, error) {
	c.otherCalls++
	c.organizeCalls = append(c.organizeCalls, inboxOrganizeCall{id: id, op: op, dryRun: dryRun})
	return c.organizeResult, c.organizeErr
}

func (c *inboxClient) Send(context.Context, core.Outgoing, bool) (core.Plan, core.Receipt, error) {
	c.otherCalls++
	return core.Plan{}, core.Receipt{}, nil
}

func (c *inboxClient) Download(_ context.Context, id string, index int, destPath string, opts core.DownloadOptions) (core.DownloadResult, error) {
	c.otherCalls++
	c.downloadCalls = append(c.downloadCalls, downloadCall{id: id, index: index, destPath: destPath, opts: opts})
	return c.downloadResult, c.downloadErr
}

func (c *inboxClient) Close() error { return nil }

func item(id string, channel core.Channel, account, thread string, at time.Time) core.Item {
	return core.Item{ID: id, Channel: channel, Account: account, Thread: thread, Unread: true, Timestamp: at}
}

func TestGroupUnreadKeepsAccountsChannelsAndEmptyThreadsSeparate(t *testing.T) {
	at := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	items := []core.Item{
		item("mail:a:1", core.ChannelMail, "a", "same", at),
		item("mail:a:2", core.ChannelMail, "a", "same", at.Add(time.Minute)),
		item("mail:b:3", core.ChannelMail, "b", "same", at.Add(2*time.Minute)),
		item("matrix:a:4", core.ChannelMatrix, "a", "same", at.Add(3*time.Minute)),
		item("mail:a:5", core.ChannelMail, "a", "", at.Add(4*time.Minute)),
		item("mail:a:6", core.ChannelMail, "a", "", at.Add(5*time.Minute)),
		{ID: "mail:a:read", Channel: core.ChannelMail, Account: "a", Thread: "same", Unread: false},
	}
	groups := groupUnread(items)
	if len(groups) != 5 {
		t.Fatalf("groups = %d, want 5", len(groups))
	}
	want := []string{"mail:a:6", "mail:a:5", "matrix:a:4", "mail:b:3", "mail:a:2"}
	for i, group := range groups {
		if group.items[0].ID != want[i] {
			t.Errorf("group %d starts with %q, want %q", i, group.items[0].ID, want[i])
		}
	}
	if len(groups[4].items) != 2 || groups[4].items[1].ID != "mail:a:1" {
		t.Fatalf("shared thread items = %+v, want newest first", groups[4].items)
	}
}

func TestGroupUnreadHasDeterministicTimestampTies(t *testing.T) {
	at := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	a := item("a", core.ChannelMail, "one", "", at)
	b := item("b", core.ChannelMail, "one", "", at)
	for _, input := range [][]core.Item{{b, a}, {a, b}} {
		groups := groupUnread(input)
		if len(groups) != 2 || groups[0].items[0].ID != "a" || groups[1].items[0].ID != "b" {
			t.Fatalf("groups for %+v = %+v, want a then b", input, groups)
		}
	}
}

func TestModelLoadsBoundedUnreadInboxAndCounts(t *testing.T) {
	at := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	client := &inboxClient{
		items: []core.Item{
			item("mail:a:1", core.ChannelMail, "a", "thread", at),
			item("mail:a:2", core.ChannelMail, "a", "thread", at.Add(time.Minute)),
		},
		counts: map[core.Channel]map[string]int{
			core.ChannelMail:   {"a": 3},
			core.ChannelMatrix: {"b": 1},
		},
	}
	model := NewModel(client)
	cmd := model.Init()
	if cmd == nil {
		t.Fatal("Init did not start inbox load")
	}
	updated, _ := model.Update(cmd())
	loaded := updated.(Model)
	if client.listCalls != 1 || client.countsCalls != 1 || client.otherCalls != 0 {
		t.Fatalf("RPC calls: list=%d counts=%d other=%d", client.listCalls, client.countsCalls, client.otherCalls)
	}
	if client.listFilter.Unread == nil || !*client.listFilter.Unread || client.listFilter.Limit != 200 {
		t.Fatalf("list filter = %+v, want unread and limit 200", client.listFilter)
	}
	if len(loaded.groups) != 1 || len(loaded.groups[0].items) != 2 {
		t.Fatalf("groups = %+v, want one conversation with two loaded items", loaded.groups)
	}
	view := loaded.View()
	// The overview shows one section per channel, each headed by its
	// brand glyph, name and the total unread count from Counts (not the
	// loaded/rendered row count): Mail's real total is 3 even though only
	// one two-item conversation was loaded, and Matrix has no loaded
	// group at all yet still shows its header and count.
	for _, text := range []string{"Mail (3)", "WhatsApp (0)", "Matrix (1)", "sin pendientes"} {
		if !strings.Contains(view, text) {
			t.Errorf("view %q lacks %q", view, text)
		}
	}
}

func TestModelClampsOversizedResponseAndShowsErrors(t *testing.T) {
	client := &inboxClient{counts: map[core.Channel]map[string]int{core.ChannelMail: {"a": 201}}}
	for i := 0; i < 201; i++ {
		it := item(strings.Repeat("x", i+1), core.ChannelMail, "a", "", time.Time{})
		// A distinct sender per item (mail-sender-groups.md merges by
		// From address): 201 separately collapsed sender rows, so the
		// pane still has to truncate, not one merged row absorbing all
		// 200 loaded threads.
		it.From = core.Address{ID: fmt.Sprintf("sender%d@example.com", i)}
		client.items = append(client.items, it)
	}
	model := NewModel(client)
	updated, _ := model.Update(model.Init()())
	loaded := updated.(Model)
	if len(loaded.groups) != 200 {
		t.Fatalf("groups = %d, want hard cap of 200", len(loaded.groups))
	}
	// A constrained pane cannot show 200 mail rows; the section must end
	// in a dim "+N más" notice instead of silently hiding the overflow.
	sized, _ := updated.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	if view := sized.(Model).View(); !strings.Contains(view, "más") {
		t.Fatalf("view %q hides truncation", view)
	}

	client = &inboxClient{listErr: errors.New("offline")}
	model = NewModel(client)
	updated, _ = model.Update(model.Init()())
	if !strings.Contains(updated.(Model).View(), "offline") {
		t.Fatalf("view %q hides list error", updated.(Model).View())
	}
	if client.otherCalls != 0 {
		t.Fatalf("unexpected non-inbox RPC calls: %d", client.otherCalls)
	}
}

func TestModelInboxNavigationDoesNotRead(t *testing.T) {
	client := &inboxClient{}
	model := NewModel(client)
	// Distinct senders (mail-sender-groups.md merges by From address): two
	// independently selectable, collapsed sender rows, not one merged row.
	model.groups = []inboxGroup{
		{items: []core.Item{{ID: "one", Channel: core.ChannelMail, From: core.Address{ID: "one@example.com"}}}},
		{items: []core.Item{{ID: "two", Channel: core.ChannelMail, From: core.Address{ID: "two@example.com"}}}},
	}
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	model = updated.(Model)
	if model.selected != 1 {
		t.Fatalf("selection = %d, want 1", model.selected)
	}
	// The now-selected row is a collapsed sender header: Enter toggles it
	// (no RPC, no selection change), never a synchronous read.
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if client.otherCalls != 0 || updated.(Model).selected != 1 {
		t.Fatalf("Enter performed a synchronous RPC or changed selection")
	}
}
