package core_test

import (
	"context"
	"errors"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

// htmlAdapter is a spyAdapter that, like mail, can deliver HTML.
type htmlAdapter struct{ spyAdapter }

func (h *htmlAdapter) SendsHTML() {}

var _ core.HTMLSender = (*htmlAdapter)(nil)

func TestServiceSendHTMLReachesAdapterAndPlan(t *testing.T) {
	reg := core.NewRegistry()
	spy := &htmlAdapter{spyAdapter{channel: core.ChannelMail, account: "cl"}}
	reg.Register(spy)
	svc := core.NewService(newMemStore(), reg)

	out := core.Outgoing{Channel: core.ChannelMail, Account: "cl", To: []string{"a@x.cl"}, Subject: "s", Body: "<p>hi</p>", HTML: true}
	plan, _, err := svc.Send(context.Background(), out, false)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !spy.lastOutgoing.HTML || spy.lastOutgoing.Body != "<p>hi</p>" {
		t.Errorf("adapter got %+v, want the HTML body flagged", spy.lastOutgoing)
	}
	if !plan.HTML {
		t.Error("plan.HTML = false, want true")
	}
}

func TestServiceSendHTMLDryRunSendsNothing(t *testing.T) {
	reg := core.NewRegistry()
	spy := &htmlAdapter{spyAdapter{channel: core.ChannelMail, account: "cl"}}
	reg.Register(spy)
	svc := core.NewService(newMemStore(), reg)

	out := core.Outgoing{Channel: core.ChannelMail, Account: "cl", To: []string{"a@x.cl"}, Body: "<p>hi</p>", HTML: true}
	plan, _, err := svc.Send(context.Background(), out, true)
	if err != nil {
		t.Fatalf("Send dry-run: %v", err)
	}
	if spy.sendCalls != 0 || !plan.HTML {
		t.Errorf("sendCalls = %d, plan.HTML = %v; want a plan marked HTML and no send", spy.sendCalls, plan.HTML)
	}
}

func TestServiceSendHTMLUnsupportedAdapterFailsLoudly(t *testing.T) {
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelWhatsApp, account: "wa"}
	reg.Register(spy)
	svc := core.NewService(newMemStore(), reg)

	out := core.Outgoing{Channel: core.ChannelWhatsApp, Account: "wa", To: []string{"+51111"}, Body: "<p>hi</p>", HTML: true}
	for _, dryRun := range []bool{true, false} {
		_, _, err := svc.Send(context.Background(), out, dryRun)
		if !errors.Is(err, core.ErrUnsupported) {
			t.Errorf("dryRun=%v: err = %v, want ErrUnsupported", dryRun, err)
		}
	}
	if spy.sendCalls != 0 {
		t.Errorf("adapter sent %d messages despite the refusal", spy.sendCalls)
	}
}

func TestServiceReplyHTML(t *testing.T) {
	item := core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "t1", From: core.Address{ID: "them@x.cl"}, Subject: "hi"}
	reg := core.NewRegistry()
	spy := &htmlAdapter{spyAdapter{channel: core.ChannelMail, account: "cl"}}
	reg.Register(spy)
	svc := core.NewService(newMemStore(item), reg)

	plan, _, err := svc.Reply(core.WithHTML(context.Background()), item.ID, "<p>re</p>", nil, nil, nil, false)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if !spy.lastOutgoing.HTML || !plan.HTML {
		t.Errorf("lastOutgoing.HTML = %v, plan.HTML = %v; want both true", spy.lastOutgoing.HTML, plan.HTML)
	}

	// Without WithHTML the reply is plain, exactly as before.
	if _, _, err := svc.Reply(context.Background(), item.ID, "plain", nil, nil, nil, false); err != nil {
		t.Fatalf("plain Reply: %v", err)
	}
	if spy.lastOutgoing.HTML {
		t.Error("a reply without WithHTML went out as HTML")
	}
}

func TestServiceReplyHTMLUnsupportedAdapterFailsLoudly(t *testing.T) {
	item := core.Item{ID: "whatsapp:wa:1", Channel: core.ChannelWhatsApp, Account: "wa", Thread: "t1", From: core.Address{ID: "+51111"}}
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelWhatsApp, account: "wa"}
	reg.Register(spy)
	svc := core.NewService(newMemStore(item), reg)

	_, _, err := svc.Reply(core.WithHTML(context.Background()), item.ID, "<p>x</p>", nil, nil, nil, true)
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
	if spy.sendCalls != 0 {
		t.Error("adapter was called")
	}
}
