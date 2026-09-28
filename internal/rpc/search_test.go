package rpc_test

import (
	"context"
	"errors"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

func TestClientSearchOverSocket(t *testing.T) {
	client, adapter := startBackfillSearchTestServer(t)
	want := []core.Item{{ID: "mail:cl:1.1", Channel: core.ChannelMail, Account: "cl", Subject: "hi"}}
	adapter.searchResult = want

	criteria := core.SearchCriteria{Folder: "INBOX", From: "alice@x", Limit: 10}
	items, err := client.Search(context.Background(), core.ChannelMail, "cl", criteria)
	if err != nil {
		t.Fatalf("Search error = %v", err)
	}
	if len(items) != 1 || items[0].ID != want[0].ID {
		t.Errorf("items = %+v, want %+v", items, want)
	}
	if adapter.gotCriteria != criteria {
		t.Errorf("adapter got criteria = %+v, want %+v", adapter.gotCriteria, criteria)
	}
}

func TestClientSearchUnsupportedReturnsErrUnsupported(t *testing.T) {
	client, _, _ := startTestServer(t) // fake.Adapter alone: no Searcher
	_, err := client.Search(context.Background(), core.ChannelMail, "cl", core.SearchCriteria{})
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("Search err = %v, want ErrUnsupported", err)
	}
}
