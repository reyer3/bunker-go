package core_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

func TestChannelConstants(t *testing.T) {
	cases := map[core.Channel]string{
		core.ChannelMail:     "mail",
		core.ChannelWhatsApp: "whatsapp",
		core.ChannelMatrix:   "matrix",
	}
	for ch, want := range cases {
		if string(ch) != want {
			t.Errorf("channel constant = %q, want %q", ch, want)
		}
	}
}

func TestItemStableID(t *testing.T) {
	item := core.Item{
		ID:        "mail:cl:1234",
		Channel:   core.ChannelMail,
		Account:   "cl",
		From:      core.Address{ID: "a@b.cl", Name: "A"},
		To:        []core.Address{{ID: "c@d.cl", Name: "C"}},
		Subject:   "hi",
		Body:      "body",
		Unread:    true,
		Timestamp: time.Unix(0, 0),
		Meta:      map[string]string{"k": "v"},
	}
	if item.ID != "mail:cl:1234" {
		t.Fatalf("ID = %q, want mail:cl:1234", item.ID)
	}
	if item.Channel != core.ChannelMail || item.Account != "cl" {
		t.Fatalf("Channel/Account = %v/%v, want mail/cl", item.Channel, item.Account)
	}
}

// TestItemFromMeJSONFieldName pins the conversation-view.md contract both
// the daemon and TUI writers code against: core.Item.FromMe must marshal
// under the JSON name "FromMe" (Item carries no struct tags at all, so
// this is really pinning that no tag gets added later that would change
// the wire name without both branches noticing).
func TestItemFromMeJSONFieldName(t *testing.T) {
	item := core.Item{ID: "whatsapp:personal:1", FromMe: true}
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v, ok := decoded["FromMe"]; !ok || v != true {
		t.Fatalf("decoded JSON = %v, want a top-level \"FromMe\": true field", decoded)
	}
}

// TestPresenceFields pins the conversation-view.md contract's Presence
// shape: State/LastSeen/Typers.
func TestPresenceFields(t *testing.T) {
	now := time.Unix(100, 0)
	p := core.Presence{State: "typing", LastSeen: now, Typers: []string{"alice"}}
	if p.State != "typing" || !p.LastSeen.Equal(now) || len(p.Typers) != 1 || p.Typers[0] != "alice" {
		t.Fatalf("Presence = %+v, want State/LastSeen/Typers set", p)
	}
}
