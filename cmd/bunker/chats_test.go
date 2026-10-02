package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

func chatsBackend() *fakeBackend {
	at := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
	return &fakeBackend{conversations: []core.Conversation{
		{Last: core.Item{ID: "wa:2", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "a", ThreadName: "Ana   Pérez", Timestamp: at}, Unread: 0},
		{Last: core.Item{ID: "mx:1", Channel: core.ChannelMatrix, Account: "hs", Thread: "!r", From: core.Address{Name: "Room"}, Timestamp: at}, Unread: 3},
	}}
}

func TestCmdChatsJSON(t *testing.T) {
	backend := chatsBackend()
	code, out, _ := runCallCmd(t, backend, "chats", "--channel", "whatsapp", "--account", "personal", "--limit", "5", "--json")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	var got struct {
		Conversations []core.Conversation `json:"conversations"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout %q: %v", out, err)
	}
	if len(got.Conversations) != 1 || got.Conversations[0].Last.ID != "wa:2" || got.Conversations[0].Unread != 0 {
		t.Fatalf("conversations = %+v", got.Conversations)
	}
	want := core.ConversationFilter{Channel: core.ChannelWhatsApp, Account: "personal", Limit: 5}
	if len(backend.conversationsCalls) != 1 || backend.conversationsCalls[0] != want {
		t.Fatalf("filter = %+v, want %+v", backend.conversationsCalls, want)
	}
}

func TestCmdChatsJSONEmptyIsArray(t *testing.T) {
	code, out, _ := runCallCmd(t, &fakeBackend{}, "chats", "--json")
	if code != 0 || !strings.Contains(out, `"conversations": []`) && !strings.Contains(out, `"conversations":[]`) {
		t.Fatalf("code %d, out %q", code, out)
	}
}

func TestCmdChatsHumanOutput(t *testing.T) {
	code, out, _ := runCallCmd(t, chatsBackend(), "chats")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 ||
		lines[0] != "whatsapp/personal\tAna Pérez\t0 unread\twa:2" ||
		lines[1] != "matrix/hs\tRoom\t3 unread\tmx:1" {
		t.Fatalf("output = %q", out)
	}
}

func TestCmdChatsErrors(t *testing.T) {
	if code, _, _ := runCallCmd(t, &fakeBackend{}, "chats", "extra"); code != 2 {
		t.Fatalf("positional arg: code = %d, want 2", code)
	}
	if code, _, _ := runCallCmd(t, &fakeBackend{}, "chats", "--limit", "-1"); code != 2 {
		t.Fatalf("negative limit: code = %d, want 2", code)
	}
	backend := &fakeBackend{conversationsErr: errors.New("boom")}
	if code, _, stderr := runCallCmd(t, backend, "chats"); code == 0 || !strings.Contains(stderr, "boom") {
		t.Fatalf("backend error: code %d, stderr %q", code, stderr)
	}
}
