package rpc_test

import (
	"context"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

func TestClientContacts(t *testing.T) {
	client, _, _ := startTestServer(t)
	contacts, err := client.Contacts(context.Background(), core.ContactFilter{Query: "them"})
	if err != nil {
		t.Fatal(err)
	}
	if len(contacts) != 1 || contacts[0].Address != "them@x.cl" || contacts[0].Channel != core.ChannelMail {
		t.Fatalf("Contacts = %+v, want the seeded mail sender", contacts)
	}
	none, err := client.Contacts(context.Background(), core.ContactFilter{Query: "nadie"})
	if err != nil || len(none) != 0 {
		t.Fatalf("no match = %+v, %v", none, err)
	}
}

func TestClientMarkUnread(t *testing.T) {
	client, _, _ := startTestServer(t)
	// The fake mail adapter is an Organizer: the seeded item goes back to unread.
	if _, err := client.MarkUnread(context.Background(), "mail:cl:1"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.MarkUnread(context.Background(), "mail:cl:nope"); err == nil {
		t.Fatal("an unknown id should be an error")
	}
}
