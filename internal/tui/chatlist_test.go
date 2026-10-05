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

// chatListClient is an inboxClient that also lists conversations, per
// channel, recording every filter it was asked for.
type chatListClient struct {
	*inboxClient
	convs map[core.Channel][]core.Conversation
	err   error
	calls []core.ConversationFilter
}

func (c *chatListClient) Conversations(_ context.Context, f core.ConversationFilter) ([]core.Conversation, error) {
	c.calls = append(c.calls, f)
	if c.err != nil {
		return nil, c.err
	}
	return c.convs[f.Channel], nil
}

var chatListNow = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

func chatConv(id string, channel core.Channel, thread, name, body string, unread int, ago time.Duration) core.Conversation {
	return core.Conversation{
		Last: core.Item{
			ID: id, Channel: channel, Account: "personal", Thread: thread, ThreadName: name, Body: body,
			From: core.Address{ID: thread, Name: name}, Unread: unread > 0, Timestamp: chatListNow.Add(-ago),
		},
		Unread: unread,
	}
}

// chatListModel returns a model that has completed one poll against a
// client with: WhatsApp "Ana" (read, newest), "Beto" (2 unread, older),
// "Carla" (read, oldest); one unread Mail item.
func chatListModel(t *testing.T) (Model, *chatListClient) {
	t.Helper()
	ana := chatConv("whatsapp:personal:3", core.ChannelWhatsApp, "ana", "Ana", "nos vemos", 0, time.Minute)
	beto := chatConv("whatsapp:personal:5", core.ChannelWhatsApp, "beto", "Beto", "llamame", 2, time.Hour)
	carla := chatConv("whatsapp:personal:7", core.ChannelWhatsApp, "carla", "Carla", "gracias", 0, 48*time.Hour)
	mail := core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "t", Subject: "Factura",
		From: core.Address{ID: "x@example.com", Name: "Proveedor"}, Unread: true, Timestamp: chatListNow}
	client := &chatListClient{
		inboxClient: &inboxClient{
			items:  []core.Item{mail, beto.Last},
			counts: map[core.Channel]map[string]int{core.ChannelMail: {"cl": 1}, core.ChannelWhatsApp: {"personal": 2}},
		},
		convs: map[core.Channel][]core.Conversation{core.ChannelWhatsApp: {ana, beto, carla}},
	}
	m := NewModel(client)
	m.width, m.height = 70, 24
	m.now = func() time.Time { return chatListNow }
	next, _ := m.Update(loadInbox(client, m.pollToken)())
	return next.(Model), client
}

func viewAt(m Model, tab int) string {
	return m.switchTab(tab).View()
}

func TestWhatsAppTabListsReadConversationsNewestFirstWithBadges(t *testing.T) {
	m, client := chatListModel(t)
	view := viewAt(m, 2)
	ana, beto, carla := strings.Index(view, "Ana"), strings.Index(view, "Beto"), strings.Index(view, "Carla")
	if ana < 0 || beto < 0 || carla < 0 {
		t.Fatalf("WhatsApp tab must list all three conversations, read ones too:\n%s", view)
	}
	if !(ana < beto && beto < carla) {
		t.Fatalf("conversations out of order (Ana %d, Beto %d, Carla %d):\n%s", ana, beto, carla, view)
	}
	if !strings.Contains(view, "⬤2") {
		t.Fatalf("unread conversation lost its badge:\n%s", view)
	}
	if strings.Contains(view, "⬤0") {
		t.Fatalf("read conversations must not show a badge:\n%s", view)
	}
	if strings.Contains(view, "sin no leídos") || strings.Contains(view, "Proveedor") {
		t.Fatalf("WhatsApp tab shows unrelated rows:\n%s", view)
	}
	// Both chat channels are asked for, bounded by inboxLimit.
	if len(client.calls) != 2 || client.calls[0].Channel != core.ChannelWhatsApp || client.calls[1].Channel != core.ChannelMatrix ||
		client.calls[0].Limit != inboxLimit {
		t.Fatalf("Conversations calls = %+v", client.calls)
	}
}

func TestMailAndOverviewTabsStayUnreadOnly(t *testing.T) {
	m, _ := chatListModel(t)
	for _, tab := range []int{0, 1} {
		view := viewAt(m, tab)
		if strings.Contains(view, "Ana") || strings.Contains(view, "Carla") {
			t.Fatalf("tab %d lists a read conversation:\n%s", tab, view)
		}
	}
	if view := viewAt(m, 0); !strings.Contains(view, "Beto") {
		t.Fatalf("overview lost the unread conversation:\n%s", view)
	}
}

func TestChatListDoesNotChangeTheUnreadSnapshot(t *testing.T) {
	m, _ := chatListModel(t)
	// Notifications and the unread count are built from m.groups: it must
	// still hold unread items only.
	for _, g := range m.groups {
		if g.conversation || !g.items[0].Unread {
			t.Fatalf("unread snapshot holds a chat-list row: %+v", g)
		}
	}
	if len(m.groups) != 2 {
		t.Fatalf("unread groups = %d, want 2", len(m.groups))
	}
}

func TestEnterOnReadConversationOpensItsChat(t *testing.T) {
	m, _ := chatListModel(t)
	m = m.switchTab(2)
	if m.selected != 0 {
		t.Fatalf("selected = %d, want the newest (read) conversation", m.selected)
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(Model)
	if !got.chatMode || cmd == nil {
		t.Fatalf("Enter on a read conversation: chatMode=%v cmd=%v, want the chat view", got.chatMode, cmd != nil)
	}
	if got.chatThread != "ana" {
		t.Fatalf("opened thread %q, want ana", got.chatThread)
	}
}

func TestMarkReadKeyIgnoresReadConversationRows(t *testing.T) {
	m, client := chatListModel(t)
	m = m.switchTab(2)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	if got := next.(Model); got.marking || cmd != nil {
		t.Fatalf("m on a read conversation started a mark-read (marking=%v)", got.marking)
	}
	// The unread row still marks.
	m.selected = 1
	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	if got := next.(Model); !got.marking || cmd == nil {
		t.Fatalf("m on an unread conversation did not start a mark-read")
	}
	_ = client
}

func TestChatListSurvivesAFailedConversationsQuery(t *testing.T) {
	m, client := chatListModel(t)
	client.err = errors.New("boom")
	m, _ = m.startPollModel()
	next, _ := m.Update(loadInbox(client, m.pollToken)())
	m = next.(Model)
	if m.loadErr != nil {
		t.Fatalf("a failed chat-list query failed the poll: %v", m.loadErr)
	}
	if view := viewAt(m, 2); !strings.Contains(view, "Carla") {
		t.Fatalf("a failed refresh blanked the chat list:\n%s", view)
	}
}

func TestWithoutConversationsSupportTabsFallBackToUnread(t *testing.T) {
	client := &inboxClient{
		items: []core.Item{{ID: "whatsapp:personal:5", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "beto",
			ThreadName: "Beto", Unread: true, Timestamp: chatListNow}},
		counts: map[core.Channel]map[string]int{core.ChannelWhatsApp: {"personal": 1}},
	}
	m := NewModel(client)
	m.width, m.height = 70, 24
	m.now = func() time.Time { return chatListNow }
	next, _ := m.Update(loadInbox(client, m.pollToken)())
	view := viewAt(next.(Model), 2)
	if !strings.Contains(view, "Beto") || !strings.Contains(view, "⬤1") {
		t.Fatalf("fallback lost the unread conversation:\n%s", view)
	}
}

func TestEmptyChatListSaysSo(t *testing.T) {
	m, client := chatListModel(t)
	client.convs = nil
	m, _ = m.startPollModel()
	next, _ := m.Update(loadInbox(client, m.pollToken)())
	view := viewAt(next.(Model), 3)
	if !strings.Contains(view, "sin conversaciones") {
		t.Fatalf("empty Matrix chat list:\n%s", view)
	}
}

func TestFromMeConversationUsesThreadNameAndYouPrefix(t *testing.T) {
	last := core.Item{ThreadName: "Ana", Body: "listo", FromMe: true, From: core.Address{Name: "Yo Mismo"}}
	if title, _ := rowTitle(last); title != "Ana" {
		t.Fatalf("title = %q", title)
	}
	if got := previewLine(last, 40); got != "Tú: listo" {
		t.Fatalf("preview = %q", got)
	}
	last.ThreadName = ""
	if title, _ := rowTitle(last); strings.Contains(title, "Yo Mismo") {
		t.Fatalf("a chat is titled with our own name: %q", title)
	}
}

func TestQueryClientForwardsConversations(t *testing.T) {
	want := chatConv("whatsapp:personal:3", core.ChannelWhatsApp, "ana", "Ana", "hola", 1, time.Minute)
	inner := &chatListClient{inboxClient: &inboxClient{}, convs: map[core.Channel][]core.Conversation{core.ChannelWhatsApp: {want}}}
	qc := NewQueryClient(inner, nil).(ConversationsClient)
	got, err := qc.Conversations(context.Background(), core.ConversationFilter{Channel: core.ChannelWhatsApp, Limit: 7})
	if err != nil || len(got) != 1 || got[0].Last.ID != want.Last.ID || inner.calls[0].Limit != 7 {
		t.Fatalf("Conversations = %+v, %v (calls %+v)", got, err, inner.calls)
	}
	plain := NewQueryClient(&inboxClient{}, nil).(ConversationsClient)
	if _, err := plain.Conversations(context.Background(), core.ConversationFilter{}); !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func TestSidebarShowsChatListRows(t *testing.T) {
	m, client := chatListModel(t)
	sb := NewModel(client, WithSidebar())
	sb.width, sb.height = 32, 16
	sb.now = m.now
	next, _ := sb.Update(loadInbox(client, sb.pollToken)())
	view := viewAt(next.(Model), 2)
	if !strings.Contains(view, "Ana") || !strings.Contains(view, "Carla") || !strings.Contains(view, "⬤2") {
		t.Fatalf("sidebar WhatsApp tab:\n%s", view)
	}
}
