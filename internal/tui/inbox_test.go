package tui

import (
	"context"
	"errors"
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
	return c.readResult, c.readErr
}

func (c *inboxClient) Reply(context.Context, string, string, []string, []string, bool) (core.Plan, core.Receipt, error) {
	c.otherCalls++
	return core.Plan{}, core.Receipt{}, nil
}

func (c *inboxClient) Organize(context.Context, string, core.OrganizeOp, bool) (core.Plan, error) {
	c.otherCalls++
	return core.Plan{}, nil
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
		client.items = append(client.items, item(strings.Repeat("x", i+1), core.ChannelMail, "a", "", time.Time{}))
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
	model.groups = []inboxGroup{
		{items: []core.Item{{ID: "one", Channel: core.ChannelMail}}},
		{items: []core.Item{{ID: "two", Channel: core.ChannelMail}}},
	}
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	model = updated.(Model)
	if model.selected != 1 {
		t.Fatalf("selection = %d, want 1", model.selected)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if client.otherCalls != 0 || updated.(Model).selected != 1 {
		t.Fatalf("Enter performed a synchronous RPC or changed selection")
	}
}
