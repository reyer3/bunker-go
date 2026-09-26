package core_test

import (
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
