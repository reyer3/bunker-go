package fake_test

import (
	"context"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/channel/fake"
	"github.com/reyer3/bunker-go/internal/core"
)

func TestAdapterImplementsEveryCapability(t *testing.T) {
	var (
		_ core.Adapter         = (*fake.Adapter)(nil)
		_ core.Sender          = (*fake.Adapter)(nil)
		_ core.Organizer       = (*fake.Adapter)(nil)
		_ core.StatusPublisher = (*fake.Adapter)(nil)
		_ core.Fetcher         = (*fake.Adapter)(nil)
	)
}

type recordingSink struct {
	items map[string]core.Item
}

func newRecordingSink() *recordingSink { return &recordingSink{items: map[string]core.Item{}} }

func (s *recordingSink) Upsert(ctx context.Context, item core.Item) error {
	s.items[item.ID] = item
	return nil
}
func (s *recordingSink) MarkRead(ctx context.Context, id string, read bool) error { return nil }
func (s *recordingSink) Delete(ctx context.Context, id string) error {
	delete(s.items, id)
	return nil
}
func (s *recordingSink) Cursor(ctx context.Context, key string) (string, error) { return "", nil }
func (s *recordingSink) SetCursor(ctx context.Context, key, val string) error   { return nil }

func TestRunStreamsSeedItemsThenBlocksUntilCanceled(t *testing.T) {
	seed := core.Item{ID: "mail:demo:1", Channel: core.ChannelMail, Account: "demo", Subject: "hi"}
	a := fake.New(core.ChannelMail, "demo", seed)

	sink := newRecordingSink()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := a.Run(ctx, sink)
	if err == nil {
		t.Fatal("expected Run to return the context error once canceled")
	}
	if _, ok := sink.items[seed.ID]; !ok {
		t.Fatalf("expected seed item %s to be upserted into sink", seed.ID)
	}
}

func TestSendRecordsOutgoingAndReturnsReceipt(t *testing.T) {
	a := fake.New(core.ChannelWhatsApp, "personal")
	out := core.Outgoing{Channel: core.ChannelWhatsApp, Account: "personal", To: []string{"55199"}, Body: "hola"}

	receipt, err := a.Send(context.Background(), out)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if receipt.Channel != core.ChannelWhatsApp || receipt.ID == "" {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	sent := a.SentMessages()
	if len(sent) != 1 || sent[0].Body != "hola" {
		t.Fatalf("SentMessages = %+v, want one message with body hola", sent)
	}
}

func TestOrganizeRecordsCall(t *testing.T) {
	a := fake.New(core.ChannelMail, "cl")
	op := core.OrganizeOp{AddLabels: []string{"vip"}}

	if err := a.Organize(context.Background(), "mail:cl:1", op); err != nil {
		t.Fatalf("Organize: %v", err)
	}
	calls := a.OrganizeCalls()
	if len(calls) != 1 || calls[0].ID != "mail:cl:1" || calls[0].Op.AddLabels[0] != "vip" {
		t.Fatalf("OrganizeCalls = %+v", calls)
	}
}

func TestPostStatusRecordsAndReturnsReceipt(t *testing.T) {
	a := fake.New(core.ChannelWhatsApp, "personal")
	status := core.Status{Text: "buenos dias"}

	receipt, err := a.PostStatus(context.Background(), status)
	if err != nil {
		t.Fatalf("PostStatus: %v", err)
	}
	if receipt.ID == "" {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	statuses := a.PostedStatuses()
	if len(statuses) != 1 || statuses[0].Text != "buenos dias" {
		t.Fatalf("PostedStatuses = %+v", statuses)
	}
}

func TestFetchReturnsSeedOrSentItem(t *testing.T) {
	seed := core.Item{ID: "mail:demo:1", Channel: core.ChannelMail, Account: "demo", Body: "full body"}
	a := fake.New(core.ChannelMail, "demo", seed)

	got, err := a.Fetch(context.Background(), seed.ID)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.Body != "full body" {
		t.Fatalf("Fetch = %+v, want body 'full body'", got)
	}

	if _, err := a.Fetch(context.Background(), "mail:demo:missing"); err == nil {
		t.Fatal("expected error fetching unknown id")
	}
}
