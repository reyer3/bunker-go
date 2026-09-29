package mail

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

func syncOneAndGetID(t *testing.T, addr string, cfg AccountConfig) string {
	t.Helper()
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))
	ctx, cancel := context.WithCancel(context.Background())
	sink := newFakeSink()
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()
	seed := waitForUpsert(t, sink, 5*time.Second)
	cancel()
	<-done
	return seed.ID
}

func TestAdapterOrganizeSeenToggle(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", rawMessage("<a@x>", "", "S", "a@x", "r@x", "b"))

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	id := syncOneAndGetID(t, addr, cfg)

	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))
	seen := true
	if err := adapter.Organize(context.Background(), id, core.OrganizeOp{Seen: &seen}); err != nil {
		t.Fatalf("Organize() error = %v", err)
	}

	if !messageHasFlag(t, addr, "INBOX", "a@x", imap.FlagSeen) {
		t.Error("message is not \\Seen after Organize with Seen=true")
	}
}

func TestAdapterOrganizeMoveTo(t *testing.T) {
	addr, mem, _ := newMemIMAPServer(t)
	if err := mem.Create("INBOX/Archives", nil); err != nil {
		t.Fatalf("create INBOX/Archives: %v", err)
	}
	appendMessage(t, addr, "INBOX", rawMessage("<a@x>", "", "S", "a@x", "r@x", "b"))

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused", FolderPrefix: "INBOX"}
	id := syncOneAndGetID(t, addr, cfg)

	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))
	if err := adapter.Organize(context.Background(), id, core.OrganizeOp{MoveTo: "Archives"}); err != nil {
		t.Fatalf("Organize() error = %v", err)
	}

	verifyMailboxHasMessages(t, addr, "INBOX/Archives", 1)
	verifyMailboxHasMessages(t, addr, "INBOX", 0)
}

// TestAdapterOrganizeMoveReportsNewAddress covers T9(a): mail.Adapter
// implements core.FolderMover, so a move reports the item's new id
// (keyed by the destination mailbox's own UIDVALIDITY/UID, exactly how
// a fresh sync of that folder would key it) and its resolved folder
// name, instead of just a bare error a caller can't reconcile the store
// from.
func TestAdapterOrganizeMoveReportsNewAddress(t *testing.T) {
	addr, mem, _ := newMemIMAPServer(t)
	if err := mem.Create("INBOX/Archives", nil); err != nil {
		t.Fatalf("create INBOX/Archives: %v", err)
	}
	appendMessage(t, addr, "INBOX", rawMessage("<a@x>", "", "S", "a@x", "r@x", "b"))

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused", FolderPrefix: "INBOX"}
	id := syncOneAndGetID(t, addr, cfg)

	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))
	move, err := adapter.OrganizeMove(context.Background(), id, core.OrganizeOp{MoveTo: "Archives"})
	if err != nil {
		t.Fatalf("OrganizeMove() error = %v", err)
	}

	if move.ID == id {
		t.Errorf("OrganizeMove().ID = %q, want a new address distinct from %q", move.ID, id)
	}
	if move.Folder != "INBOX/Archives" {
		t.Errorf("OrganizeMove().Folder = %q, want %q", move.Folder, "INBOX/Archives")
	}

	// the new id must decode back to an account/uid pair a fresh sync of
	// the destination mailbox would itself produce.
	account, _, _, uid, err := parseItemID(move.ID)
	if err != nil {
		t.Fatalf("parseItemID(%q): %v", move.ID, err)
	}
	if account != "cl" {
		t.Errorf("new id account = %q, want cl", account)
	}
	if uid == 0 {
		t.Error("new id UID = 0, want the destination mailbox's real UID")
	}
}

// TestAdapterOrganizeMoveWithoutMoveToReturnsSameID covers the
// Seen/labels-only path: no MoveTo means no relocation, so OrganizeMove
// must report the same id and no folder change.
func TestAdapterOrganizeMoveWithoutMoveToReturnsSameID(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", rawMessage("<a@x>", "", "S", "a@x", "r@x", "b"))

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	id := syncOneAndGetID(t, addr, cfg)

	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))
	seen := true
	move, err := adapter.OrganizeMove(context.Background(), id, core.OrganizeOp{Seen: &seen})
	if err != nil {
		t.Fatalf("OrganizeMove() error = %v", err)
	}
	if move.ID != id {
		t.Errorf("OrganizeMove().ID = %q, want unchanged %q", move.ID, id)
	}
	if move.Folder != "" {
		t.Errorf("OrganizeMove().Folder = %q, want empty (no move)", move.Folder)
	}
}

func TestAdapterOrganizeMoveToMissingFolderFailsClearly(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", rawMessage("<a@x>", "", "S", "a@x", "r@x", "b"))

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused", FolderPrefix: "INBOX"}
	id := syncOneAndGetID(t, addr, cfg)

	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))
	err := adapter.Organize(context.Background(), id, core.OrganizeOp{MoveTo: "DoesNotExist"})
	if err == nil {
		t.Fatal("Organize() error = nil, want a clear failure for a missing folder")
	}
	// bunker-go must never auto-create the destination.
	verifyMailboxHasMessages(t, addr, "INBOX", 1)
}

// TestAdapterOrganizeMoveToArchiveFindsOtherSpelling covers issue #54 at
// the adapter level: asking for "Archives" on a prefixed server whose
// only archive is INBOX/Archive (no \Archive attribute; imapmemserver
// cannot advertise one) moves there instead of guessing a missing name.
func TestAdapterOrganizeMoveToArchiveFindsOtherSpelling(t *testing.T) {
	addr, mem, _ := newMemIMAPServer(t)
	if err := mem.Create("INBOX/Archive", nil); err != nil {
		t.Fatalf("create INBOX/Archive: %v", err)
	}
	appendMessage(t, addr, "INBOX", rawMessage("<a@x>", "", "S", "a@x", "r@x", "b"))

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused", FolderPrefix: "INBOX"}
	id := syncOneAndGetID(t, addr, cfg)

	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))
	move, err := adapter.OrganizeMove(context.Background(), id, core.OrganizeOp{MoveTo: "Archives"})
	if err != nil {
		t.Fatalf("OrganizeMove() error = %v", err)
	}
	if move.Folder != "INBOX/Archive" {
		t.Errorf("OrganizeMove().Folder = %q, want %q", move.Folder, "INBOX/Archive")
	}
	verifyMailboxHasMessages(t, addr, "INBOX/Archive", 1)
	verifyMailboxHasMessages(t, addr, "INBOX", 0)
}

func TestAdapterOrganizeMoveToMissingArchiveFails(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", rawMessage("<a@x>", "", "S", "a@x", "r@x", "b"))

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused", FolderPrefix: "INBOX"}
	id := syncOneAndGetID(t, addr, cfg)

	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))
	err := adapter.Organize(context.Background(), id, core.OrganizeOp{MoveTo: "Archive"})
	if err == nil {
		t.Fatal("Organize() error = nil, want a clear failure for a missing archive")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("Organize() error = %v, want it to say the folder does not exist", err)
	}
	verifyMailboxHasMessages(t, addr, "INBOX", 1)
}

func TestAdapterOrganizeLabelsAsKeywordsWhenPermitted(t *testing.T) {
	// imapmemserver always reports \* in PERMANENTFLAGS, so this
	// exercises the "server permits custom keywords" path.
	addr, _, _ := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", rawMessage("<a@x>", "", "S", "a@x", "r@x", "b"))

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	id := syncOneAndGetID(t, addr, cfg)

	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))
	err := adapter.Organize(context.Background(), id, core.OrganizeOp{AddLabels: []string{"Triage"}})
	if err != nil {
		t.Fatalf("Organize() error = %v", err)
	}

	// imapmemserver lower-cases all flags internally, so match
	// case-insensitively; a real IMAP server preserves keyword case.
	if !messageHasFlag(t, addr, "INBOX", "a@x", imap.Flag("triage")) {
		t.Error("message does not carry the Triage keyword after Organize")
	}
}

// TestAdapterOrganizeLabelAgreesWithFetchReadBack covers T14(a)'s last
// bullet: a label applied through Organize --label must be visible
// through the read path (Fetch) too, not just through core.Service's
// own store reconciliation (which this package doesn't exercise
// directly). It is a mutation-validated check of pre-existing behavior:
// Organize's storeLabels and Fetch's buildItem/dovecotLabelsFromFlags
// already agree on the wire (both read/write the same IMAP keyword);
// this test is the first to assert that agreement end to end.
func TestAdapterOrganizeLabelAgreesWithFetchReadBack(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", rawMessage("<a@x>", "", "S", "a@x", "r@x", "b"))

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	id := syncOneAndGetID(t, addr, cfg)

	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))
	if err := adapter.Organize(context.Background(), id, core.OrganizeOp{AddLabels: []string{"bunker-test"}}); err != nil {
		t.Fatalf("Organize() error = %v", err)
	}

	item, err := adapter.Fetch(context.Background(), id)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(item.Labels) != 1 || item.Labels[0] != "bunker-test" {
		t.Errorf("Fetch().Labels = %v after Organize --label bunker-test, want [\"bunker-test\"]", item.Labels)
	}
}

func messageHasFlag(t *testing.T, addr, mailbox, messageID string, want imap.Flag) bool {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial for verify: %v", err)
	}
	defer conn.Close()
	client := imapclient.New(conn, nil)
	defer client.Close()
	if err := client.Login(testIMAPUsername, testIMAPPassword).Wait(); err != nil {
		t.Fatalf("login for verify: %v", err)
	}
	if _, err := client.Select(mailbox, nil).Wait(); err != nil {
		t.Fatalf("select %s: %v", mailbox, err)
	}
	var seqSet imap.SeqSet
	seqSet.AddRange(1, 0)
	messages, err := client.Fetch(seqSet, &imap.FetchOptions{Flags: true, Envelope: true}).Collect()
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	for _, msg := range messages {
		if msg.Envelope == nil || msg.Envelope.MessageID != messageID {
			continue
		}
		for _, flag := range msg.Flags {
			if flag == want {
				return true
			}
		}
	}
	return false
}

func TestHasWildcardFlag(t *testing.T) {
	if hasWildcardFlag([]imap.Flag{imap.FlagSeen, imap.FlagAnswered}) {
		t.Error("hasWildcardFlag() = true without \\*, want false")
	}
	if !hasWildcardFlag([]imap.Flag{imap.FlagSeen, imap.FlagWildcard}) {
		t.Error("hasWildcardFlag() = false with \\*, want true")
	}
}
