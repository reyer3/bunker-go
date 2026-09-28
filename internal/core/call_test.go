package core_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// callerAdapter is a core.Caller spy.
type callerAdapter struct {
	channel  core.Channel
	account  string
	canErr   error
	placed   []string
	controls []core.CallAction
	active   []core.Call
}

func (c *callerAdapter) Channel() core.Channel                      { return c.channel }
func (c *callerAdapter) Account() string                            { return c.account }
func (c *callerAdapter) Run(ctx context.Context, _ core.Sink) error { <-ctx.Done(); return nil }
func (c *callerAdapter) CanCall() error                             { return c.canErr }
func (c *callerAdapter) ActiveCalls() []core.Call                   { return c.active }
func (c *callerAdapter) PlaceCall(_ context.Context, to string) (core.Call, error) {
	c.placed = append(c.placed, to)
	return core.Call{ID: "C1", Channel: c.channel, Account: c.account, Peer: to, State: core.CallStateCalling}, nil
}
func (c *callerAdapter) ControlCall(_ context.Context, id string, action core.CallAction) (core.Call, error) {
	c.controls = append(c.controls, action)
	return core.Call{ID: id, State: core.CallStateEnded}, nil
}

func newCallService(adapters ...core.Adapter) *core.Service {
	reg := core.NewRegistry()
	for _, a := range adapters {
		reg.Register(a)
	}
	return core.NewService(newMemStore(), reg)
}

func TestServicePlaceCallDryRunNeverCallsAdapter(t *testing.T) {
	spy := &callerAdapter{channel: core.ChannelWhatsApp, account: "personal"}
	svc := newCallService(spy)
	plan, call, err := svc.PlaceCall(context.Background(), core.ChannelWhatsApp, "personal", "+51999", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(spy.placed) != 0 || call.ID != "" || plan.Action != "call" || plan.Target != "+51999" {
		t.Fatalf("dry-run placed=%v call=%+v plan=%+v", spy.placed, call, plan)
	}
	if _, call, err = svc.PlaceCall(context.Background(), core.ChannelWhatsApp, "personal", "+51999", false); err != nil || call.ID != "C1" {
		t.Fatalf("PlaceCall = %+v, %v", call, err)
	}
}

func TestServicePlaceCallUnsupported(t *testing.T) {
	disabled := &callerAdapter{channel: core.ChannelWhatsApp, account: "personal", canErr: core.ErrUnsupported}
	mail := &spyAdapter{channel: core.ChannelMail, account: "cl"}
	svc := newCallService(disabled, mail)
	if _, _, err := svc.PlaceCall(context.Background(), core.ChannelWhatsApp, "personal", "+51999", true); !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("disabled caller dry-run = %v, want ErrUnsupported", err)
	}
	if _, _, err := svc.PlaceCall(context.Background(), core.ChannelMail, "cl", "x@y.cl", true); !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("mail call = %v, want ErrUnsupported", err)
	}
	if _, _, err := svc.PlaceCall(context.Background(), core.ChannelWhatsApp, "personal", "  ", true); err == nil {
		t.Fatal("empty recipient accepted")
	}
}

func TestServiceControlCallFindsOwner(t *testing.T) {
	other := &callerAdapter{channel: core.ChannelWhatsApp, account: "work"}
	owner := &callerAdapter{channel: core.ChannelWhatsApp, account: "personal", active: []core.Call{
		{ID: "IN1", Channel: core.ChannelWhatsApp, Account: "personal", Peer: "519@s.whatsapp.net", State: core.CallStateRinging},
	}}
	svc := newCallService(other, owner)

	plan, call, err := svc.ControlCall(context.Background(), "IN1", core.CallAnswer, true)
	if err != nil || len(owner.controls) != 0 || call.State != core.CallStateRinging || plan.Account != "personal" {
		t.Fatalf("dry-run: plan=%+v call=%+v err=%v controls=%v", plan, call, err, owner.controls)
	}
	if _, _, err := svc.ControlCall(context.Background(), "IN1", core.CallAnswer, false); err != nil || len(owner.controls) != 1 {
		t.Fatalf("answer err=%v controls=%v", err, owner.controls)
	}
	if _, _, err := svc.ControlCall(context.Background(), "nope", core.CallHangup, false); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("unknown call = %v, want ErrNotFound", err)
	}
	if _, _, err := svc.ControlCall(context.Background(), "IN1", "mute", false); err == nil {
		t.Fatal("unknown action accepted")
	}
}

func TestServiceCallsSortedAcrossAccounts(t *testing.T) {
	now := time.Now()
	a := &callerAdapter{channel: core.ChannelWhatsApp, account: "a", active: []core.Call{{ID: "late", StartedAt: now}}}
	b := &callerAdapter{channel: core.ChannelWhatsApp, account: "b", active: []core.Call{{ID: "early", StartedAt: now.Add(-time.Minute)}}}
	calls, err := newCallService(a, b).Calls(context.Background())
	if err != nil || len(calls) != 2 || calls[0].ID != "early" {
		t.Fatalf("Calls = %+v, %v", calls, err)
	}
}
