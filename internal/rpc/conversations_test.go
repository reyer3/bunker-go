package rpc_test

import (
	"context"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

func TestClientConversations(t *testing.T) {
	client, _, _ := startTestServer(t)
	ctx := context.Background()

	got, err := client.Conversations(ctx, core.ConversationFilter{Channel: core.ChannelMail})
	if err != nil {
		t.Fatalf("Conversations: %v", err)
	}
	if len(got) != 1 || got[0].Last.ID != "mail:cl:1" || got[0].Unread != 1 {
		t.Fatalf("Conversations = %+v, want the seeded mail item with 1 unread", got)
	}

	// A filter that matches nothing is an empty list, not an error.
	none, err := client.Conversations(ctx, core.ConversationFilter{Channel: core.ChannelWhatsApp, Limit: 10})
	if err != nil || len(none) != 0 {
		t.Fatalf("Conversations(whatsapp) = %+v, %v; want none", none, err)
	}
}
