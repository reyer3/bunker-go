package mail

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// TestAdapterRunSyncsSentFolderAsFromMe covers K2: a message appended to
// the Sent mailbox — discovered here via FolderMap's prefix fallback
// (INBOX/Sent: imapmemserver always reports '/' as its hierarchy
// delimiter and never advertises SPECIAL-USE, so this exercises the
// fallback path; a real Dovecot/Gmail server's SPECIAL-USE \Sent is
// covered at the unit level by folder_test.go's TestFolderMapResolve)
// — is imported as a FromMe item that is never unread, with a Thread
// key that matches the received side via References, exactly like a
// real reply would need for the chat/mail thread view to group them
// together.
func TestAdapterRunSyncsSentFolderAsFromMe(t *testing.T) {
	addr, mem, _ := newMemIMAPServer(t)
	if err := mem.Create("INBOX/Sent", nil); err != nil {
		t.Fatalf("create INBOX/Sent: %v", err)
	}

	appendMessage(t, addr, "INBOX", rawMessage(
		"<msg1@example.org>", "", "Meet recording",
		"Bob <bob@example.org>", "alice@example.org", "original body",
	))
	appendMessage(t, addr, "INBOX/Sent", rawMessage(
		"<reply1@example.org>", "<msg1@example.org>", "Re: Meet recording",
		"Alice <alice@example.org>", "bob@example.org", "reply body",
	))

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused", FolderPrefix: "INBOX", FolderSeparator: '/', Username: "alice@example.org"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := newFakeSink()
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()

	first := waitForUpsert(t, sink, 5*time.Second)
	second := waitForUpsert(t, sink, 5*time.Second)
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}

	var inbox, sent *core.Item
	for _, it := range []core.Item{first, second} {
		switch it.Meta["folder"] {
		case "INBOX":
			inbox = &it
		case "Sent":
			sent = &it
		}
	}
	if inbox == nil {
		t.Fatalf("never saw the INBOX item; got %+v and %+v", first, second)
	}
	if sent == nil {
		t.Fatalf("never saw the Sent item; got %+v and %+v", first, second)
	}

	if !sent.FromMe {
		t.Errorf("Sent item FromMe = false, want true")
	}
	if sent.Unread {
		t.Errorf("Sent item Unread = true, want false: a Sent item must never count as unread")
	}
	if inbox.FromMe {
		t.Errorf("INBOX item FromMe = true, want false (From bob@example.org)")
	}
	if sent.Thread != inbox.Thread {
		t.Errorf("Sent item Thread = %q, INBOX item Thread = %q, want them equal (References ties them together)", sent.Thread, inbox.Thread)
	}
	if sent.ID == inbox.ID {
		t.Errorf("Sent item ID must not collide with the INBOX item's id")
	}
}

// TestAdapterRunReconcilesStaleSentItemOnStartup covers K2's
// "reconciliation parity" requirement: a stored Sent item whose UID no
// longer exists on the server must be dropped on startup, exactly like
// TestAdapterRunDropsStaleStoredItemOnStartup already covers for INBOX.
func TestAdapterRunReconcilesStaleSentItemOnStartup(t *testing.T) {
	addr, mem, _ := newMemIMAPServer(t)
	if err := mem.Create("INBOX/Sent", nil); err != nil {
		t.Fatalf("create INBOX/Sent: %v", err)
	}
	appendMessage(t, addr, "INBOX/Sent", rawMessage("<keep@x>", "", "Keep", "a@x", "r@x", "b"))
	sentUIDValidity := mailboxUIDValidity(t, addr, "INBOX/Sent")

	staleID := fmt.Sprintf("mail:cl:sent.%d.999", sentUIDValidity)
	cfg := AccountConfig{Name: "cl", IMAPHost: "unused", FolderPrefix: "INBOX", FolderSeparator: '/'}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))

	sink := newFakeSink()
	sink.items[staleID] = core.Item{
		ID: staleID, Channel: core.ChannelMail, Account: "cl",
		FromMe: true, Meta: map[string]string{"folder": "Sent"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()

	waitForUpsert(t, sink, 5*time.Second) // "Keep" from the Sent sync

	pollUntil(5*time.Second, func() bool {
		_, err := sink.getItem(staleID)
		return err != nil
	})
	if _, err := sink.getItem(staleID); err == nil {
		t.Errorf("stale Sent item %s is still stored after Run started", staleID)
	}

	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
}
