package core_test

import (
	"context"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

type stubAdapter struct {
	channel core.Channel
	account string
}

func (s stubAdapter) Channel() core.Channel { return s.channel }
func (s stubAdapter) Account() string       { return s.account }
func (s stubAdapter) Run(ctx context.Context, sink core.Sink) error {
	return nil
}

func TestRegistryRegisterAndGet(t *testing.T) {
	reg := core.NewRegistry()
	a := stubAdapter{channel: core.ChannelMail, account: "cl"}
	reg.Register(a)

	got, ok := reg.Get(core.ChannelMail, "cl")
	if !ok {
		t.Fatal("expected adapter to be found")
	}
	if got.Channel() != core.ChannelMail || got.Account() != "cl" {
		t.Fatalf("got wrong adapter: %+v", got)
	}

	if _, ok := reg.Get(core.ChannelMatrix, "cl"); ok {
		t.Fatal("expected no adapter for unregistered (channel, account)")
	}
}

func TestRegistryList(t *testing.T) {
	reg := core.NewRegistry()
	reg.Register(stubAdapter{channel: core.ChannelMail, account: "cl"})
	reg.Register(stubAdapter{channel: core.ChannelWhatsApp, account: "personal"})

	got := reg.List()
	if len(got) != 2 {
		t.Fatalf("List() len = %d, want 2", len(got))
	}
}
