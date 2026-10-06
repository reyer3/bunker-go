package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
)

// refreshChat opens a chat on client and delivers its initial load.
func refreshChat(t *testing.T, client *replyClient, items []core.Item) Model {
	t.Helper()
	model := chatReadyModel(client, "whatsapp:personal:1")
	model, cmd := openChat(model)
	updated, _ := model.Update(cmd())
	model = updated.(Model)
	updated, _ = model.Update(chatThreadLoadedMsg{token: model.chatToken, items: items})
	return updated.(Model)
}

func chatMsg(id string, at time.Time, fromMe bool, body string) core.Item {
	return core.Item{ID: id, Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t", Timestamp: at, FromMe: fromMe, Body: body}
}

// TestChatRefreshShowsIncomingMessages pins the fix for an open chat that
// never showed messages arriving after it was opened: each refresh tick
// re-reads the newest page, and a new incoming message appears and marks
// the conversation read, as reopening it would.
func TestChatRefreshShowsIncomingMessages(t *testing.T) {
	base := time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)
	first := chatMsg("whatsapp:personal:1", base, false, "hola")
	client := &replyClient{}
	model := refreshChat(t, client, []core.Item{first})
	readsAtOpen := len(client.readThreadCalls)

	client.threadItems = []core.Item{first, chatMsg("whatsapp:personal:2", base.Add(time.Minute), false, "¿estás?")}
	updated, cmd := model.Update(chatRefreshTickMsg{token: model.chatToken})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("a refresh tick on an open chat did not re-read the thread")
	}
	updated, cmd = model.Update(cmd())
	model = updated.(Model)

	if got := len(model.chatItems); got != 2 || model.chatItems[1].Body != "¿estás?" {
		t.Fatalf("chat items = %+v, want the new incoming message appended", model.chatItems)
	}
	if cmd == nil {
		t.Fatal("the refresh did not mark the new message read")
	}
	updated, cmd = model.Update(cmd())
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("marking read did not schedule the next refresh")
	}
	if got := len(client.readThreadCalls); got != readsAtOpen+1 {
		t.Fatalf("readThread calls = %d, want %d: a new incoming message in a focused chat is marked read", got, readsAtOpen+1)
	}
}

// TestChatRefreshKeepsOlderPagesAndSkipsReadWhenBlurred pins the merge:
// pages loaded by scrolling up stay, and a blurred terminal does not mark
// anything read (nobody is looking).
func TestChatRefreshKeepsOlderPagesAndSkipsReadWhenBlurred(t *testing.T) {
	base := time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)
	older := chatMsg("whatsapp:personal:0", base.Add(-time.Hour), false, "antes")
	first := chatMsg("whatsapp:personal:1", base, true, "hola")
	client := &replyClient{}
	model := refreshChat(t, client, []core.Item{older, first})
	model.blurred = true
	readsAtOpen := len(client.readThreadCalls)

	client.threadItems = []core.Item{first, chatMsg("whatsapp:personal:2", base.Add(time.Minute), false, "nuevo")}
	updated, cmd := model.Update(chatRefreshTickMsg{token: model.chatToken})
	model = updated.(Model)
	updated, _ = model.Update(cmd())
	model = updated.(Model)

	ids := make([]string, 0, len(model.chatItems))
	for _, it := range model.chatItems {
		ids = append(ids, it.ID)
	}
	want := []string{"whatsapp:personal:0", "whatsapp:personal:1", "whatsapp:personal:2"}
	if len(ids) != len(want) {
		t.Fatalf("chat ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("chat ids = %v, want %v", ids, want)
		}
	}
	if got := len(client.readThreadCalls); got != readsAtOpen {
		t.Fatalf("readThread calls = %d, want %d: a blurred chat must not mark read", got, readsAtOpen)
	}
}

// TestChatRefreshStopsAfterLeaving pins that a tick from a closed chat is
// dropped, so leaving a chat ends its refresh loop.
func TestChatRefreshStopsAfterLeaving(t *testing.T) {
	client := &replyClient{}
	model := refreshChat(t, client, []core.Item{chatMsg("whatsapp:personal:1", time.Now(), false, "hola")})
	token := model.chatToken
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if _, cmd := model.Update(chatRefreshTickMsg{token: token}); cmd != nil {
		t.Fatal("a refresh tick after leaving the chat still issued a command")
	}
}

// TestChatRefreshDropsOptimisticBubbleOnceStored pins the dedupe: once
// the refreshed page holds the stored copy of a sent message, the
// optimistic bubble goes away.
func TestChatRefreshDropsOptimisticBubbleOnceStored(t *testing.T) {
	base := time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)
	first := chatMsg("whatsapp:personal:1", base, false, "hola")
	client := &replyClient{}
	model := refreshChat(t, client, []core.Item{first})
	model.chatQueue = []chatOptimisticMsg{{conv: model.chatConvKey(), state: chatSendDone, id: "whatsapp:personal:2"}}

	client.threadItems = []core.Item{first, chatMsg("whatsapp:personal:2", base.Add(time.Minute), true, "enviado")}
	updated, cmd := model.Update(chatRefreshTickMsg{token: model.chatToken})
	model = updated.(Model)
	updated, _ = model.Update(cmd())
	model = updated.(Model)
	if len(model.chatQueue) != 0 {
		t.Fatal("the optimistic bubble stayed after the refresh stored its message")
	}
}
