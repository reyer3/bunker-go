package core_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// syncBuffer is an io.Writer safe for one goroutine to write while
// another concurrently reads String() (not strictly needed here, since
// Send runs synchronously on the test goroutine, but kept consistent
// with the other packages' logging tests).
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func captureSlogDefault(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// TestServiceSendLogsStoreSentItemErrorWithChannelAccountAttrs proves R3
// for sent_item.go:42: storeSentItem's Upsert failure (previously
// discarded with `_ = `) is logged at error level with channel/account
// attributes, and Send itself still succeeds (the send already happened;
// this is only bunker's own local cache of it).
func TestServiceSendLogsStoreSentItemErrorWithChannelAccountAttrs(t *testing.T) {
	buf := captureSlogDefault(t)
	store := newMemStore()
	store.upsertErr = errors.New("disk full")
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelWhatsApp, account: "personal"}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	out := core.Outgoing{Channel: core.ChannelWhatsApp, Account: "personal", To: []string{"5511999"}, Body: "hola"}
	if _, _, err := svc.Send(context.Background(), out, false); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "level=ERROR") {
		t.Fatalf("log output = %q, want an ERROR level entry", got)
	}
	if !strings.Contains(got, "channel=whatsapp") {
		t.Fatalf("log output = %q, want a channel=whatsapp attribute", got)
	}
	if !strings.Contains(got, "account=personal") {
		t.Fatalf("log output = %q, want an account=personal attribute", got)
	}
	if len(store.items) != 0 {
		t.Fatalf("store.items = %+v, want none stored when Upsert fails", store.items)
	}
}

// TestServiceSendKeepsEchoThatArrivedFirst covers the other order of the
// sent-item/echo race (issue #114): the channel's sync already upserted
// the echo under the receipt's id before Send returned, so the richer
// synced row stays instead of being overwritten by the optimistic one.
func TestServiceSendKeepsEchoThatArrivedFirst(t *testing.T) {
	echo := core.Item{
		ID: "sent-1", Channel: core.ChannelWhatsApp, Account: "personal",
		Thread: "5511999", ThreadName: "Sala", From: core.Address{ID: "me", Name: "Yo"},
		Body: "hola", FromMe: true,
	}
	store := newMemStore(echo)
	reg := core.NewRegistry()
	reg.Register(&spyAdapter{channel: core.ChannelWhatsApp, account: "personal"})
	svc := core.NewService(store, reg)

	out := core.Outgoing{Channel: core.ChannelWhatsApp, Account: "personal", To: []string{"5511999"}, Body: "hola"}
	if _, _, err := svc.Send(context.Background(), out, false); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(store.items) != 1 {
		t.Fatalf("store has %d items, want the one echo", len(store.items))
	}
	if got := store.items["sent-1"]; got.ThreadName != "Sala" || got.From.Name != "Yo" {
		t.Errorf("stored item = %+v, want the synced echo kept", got)
	}
}

// TestServiceReplyStoresSentItemWithOriginalThreadName: a reply's stored
// item carries the replied item's chat name, so a conversation whose
// newest message was sent from bunker is still titled by the contact's
// name instead of the bare thread id.
func TestServiceReplyStoresSentItemWithOriginalThreadName(t *testing.T) {
	item := core.Item{
		ID: "whatsapp:personal:1", Channel: core.ChannelWhatsApp, Account: "personal",
		Thread: "5511999@s.whatsapp.net", ThreadName: "Ana Ejemplo",
		From: core.Address{ID: "5511999@s.whatsapp.net", Name: "Ana Ejemplo"},
	}
	store := newMemStore(item)
	reg := core.NewRegistry()
	reg.Register(&spyAdapter{channel: core.ChannelWhatsApp, account: "personal"})
	svc := core.NewService(store, reg)

	_, receipt, err := svc.Reply(context.Background(), item.ID, "hola", nil, nil, false)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if got := store.items[receipt.ID].ThreadName; got != "Ana Ejemplo" {
		t.Errorf("stored ThreadName = %q, want %q", got, "Ana Ejemplo")
	}
}

// TestServiceSendStoresSentItemWithLastKnownThreadName: a fresh send has
// no original item, so the stored item takes the newest non-empty chat
// name already stored for the same channel/account/thread. Items of
// another thread or account never lend their name.
func TestServiceSendStoresSentItemWithLastKnownThreadName(t *testing.T) {
	thread := "5511999@s.whatsapp.net"
	tests := []struct {
		name  string
		items []core.Item
		want  string
	}{
		{
			name: "newest non-empty name wins",
			items: []core.Item{
				{ID: "a", Channel: core.ChannelWhatsApp, Account: "personal", Thread: thread, ThreadName: "Nombre viejo", Timestamp: time.Unix(10, 0)},
				{ID: "b", Channel: core.ChannelWhatsApp, Account: "personal", Thread: thread, ThreadName: "Ana Ejemplo", Timestamp: time.Unix(20, 0)},
				{ID: "c", Channel: core.ChannelWhatsApp, Account: "personal", Thread: thread, FromMe: true, Timestamp: time.Unix(30, 0)},
			},
			want: "Ana Ejemplo",
		},
		{
			name: "other threads and accounts are ignored",
			items: []core.Item{
				{ID: "a", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "other@s.whatsapp.net", ThreadName: "Otro", Timestamp: time.Unix(10, 0)},
				{ID: "b", Channel: core.ChannelWhatsApp, Account: "work", Thread: thread, ThreadName: "Trabajo", Timestamp: time.Unix(20, 0)},
			},
			want: "",
		},
		{name: "empty thread stays unnamed", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMemStore(tt.items...)
			reg := core.NewRegistry()
			reg.Register(&spyAdapter{channel: core.ChannelWhatsApp, account: "personal"})
			svc := core.NewService(store, reg)

			out := core.Outgoing{Channel: core.ChannelWhatsApp, Account: "personal", To: []string{thread}, Body: "hola"}
			_, receipt, err := svc.Send(context.Background(), out, false)
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			stored, ok := store.items[receipt.ID]
			if !ok {
				t.Fatalf("sent item %q not stored", receipt.ID)
			}
			if stored.ThreadName != tt.want {
				t.Errorf("stored ThreadName = %q, want %q", stored.ThreadName, tt.want)
			}
		})
	}
}

// TestServiceSendStoresSentItemWhenNameLookupFails: the name lookup is
// best effort; a store failure there still stores the sent item (unnamed)
// and never fails the send that already happened.
func TestServiceSendStoresSentItemWhenNameLookupFails(t *testing.T) {
	store := newMemStore()
	store.threadErr = errors.New("database is locked")
	reg := core.NewRegistry()
	reg.Register(&spyAdapter{channel: core.ChannelWhatsApp, account: "personal"})
	svc := core.NewService(store, reg)

	out := core.Outgoing{Channel: core.ChannelWhatsApp, Account: "personal", To: []string{"5511999"}, Body: "hola"}
	_, receipt, err := svc.Send(context.Background(), out, false)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if _, ok := store.items[receipt.ID]; !ok {
		t.Fatalf("sent item %q not stored after a failed name lookup", receipt.ID)
	}
}
