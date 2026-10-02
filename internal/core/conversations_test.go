package core_test

import (
	"context"
	"errors"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

// convStore is a memStore that also lists conversations, recording the
// filter the Service handed it.
type convStore struct {
	*memStore
	got  core.ConversationFilter
	out  []core.Conversation
	fail error
}

func (c *convStore) Conversations(_ context.Context, f core.ConversationFilter) ([]core.Conversation, error) {
	c.got = f
	return c.out, c.fail
}

func TestServiceConversationsDefaultsAndClampsLimit(t *testing.T) {
	st := &convStore{memStore: newMemStore()}
	svc := core.NewService(st, core.NewRegistry())

	out, err := svc.Conversations(context.Background(), core.ConversationFilter{Channel: core.ChannelWhatsApp})
	if err != nil {
		t.Fatalf("Conversations: %v", err)
	}
	if out == nil || len(out) != 0 {
		t.Fatalf("empty result = %#v, want a non-nil empty slice", out)
	}
	if st.got.Limit != core.DefaultConversationLimit || st.got.Channel != core.ChannelWhatsApp {
		t.Fatalf("filter = %+v, want default limit and channel kept", st.got)
	}

	if _, err := svc.Conversations(context.Background(), core.ConversationFilter{Limit: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	if st.got.Limit != core.MaxConversationLimit {
		t.Fatalf("limit = %d, want clamp to %d", st.got.Limit, core.MaxConversationLimit)
	}
}

func TestServiceConversationsPassesThroughStoreError(t *testing.T) {
	boom := errors.New("boom")
	svc := core.NewService(&convStore{memStore: newMemStore(), fail: boom}, core.NewRegistry())
	if _, err := svc.Conversations(context.Background(), core.ConversationFilter{}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestServiceConversationsUnsupportedStore(t *testing.T) {
	svc := core.NewService(newMemStore(), core.NewRegistry())
	if _, err := svc.Conversations(context.Background(), core.ConversationFilter{}); !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}
