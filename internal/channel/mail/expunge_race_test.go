package mail

import (
	"context"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

// orderingProbeSink wraps a fakeSink and, on the Upsert call for the
// message at seq, records whether tracker already resolves seq at that
// exact moment (trackedDuringUpsert). This is the root cause behind
// TestAdapterRunObservesExpungeFromAnotherClient's flakiness (R5):
// waitForUpsert/sink.upserts unblocks a test's second IMAP client the
// instant Sink.Upsert is called, racing it against fetchAndUpsert's own
// tracker.track call for that same message. If track loses that race
// (runs after Upsert instead of before), an EXPUNGE for that sequence
// number landing in the gap resolves to "untracked" (seqTracker.expunge
// returns ok=false) and is silently dropped instead of reaching the
// store — exactly the symptom the flaky test observes under load.
type orderingProbeSink struct {
	*fakeSink
	tracker             *seqTracker
	seq                 uint32
	trackedDuringUpsert bool
}

func (s *orderingProbeSink) Upsert(ctx context.Context, item core.Item) error {
	if _, ok := s.tracker.lookup(s.seq); ok {
		s.trackedDuringUpsert = true
	}
	return s.fakeSink.Upsert(ctx, item)
}

// TestFetchAndUpsertTracksSeqBeforeUpsertingIt pins the root-cause fix
// deterministically (no network race, no sleep): by the time
// fetchAndUpsert calls Sink.Upsert for a message, seqTracker.track for
// that same message's sequence number must have already run, so an
// EXPUNGE observed concurrently (in the real adapter, delivered on a
// different goroutine) can always resolve it.
func TestFetchAndUpsertTracksSeqBeforeUpsertingIt(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", rawMessage("<a@x>", "", "S", "a@x", "r@x", "b"))

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))
	ctx := context.Background()

	client, err := testDialInsecure(addr)(ctx, cfg, nil, nil, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	mbox, err := client.Select("INBOX", nil).Wait()
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	folders, err := discoverFolders(ctx, client, cfg)
	if err != nil {
		t.Fatalf("discover folders: %v", err)
	}

	tracker := newSeqTracker()
	probe := &orderingProbeSink{fakeSink: newFakeSink(), tracker: tracker, seq: 1}
	if _, err := adapter.syncFrom(ctx, client, probe, folders, mbox.UIDValidity, 0, tracker, mbox.NumMessages, "INBOX"); err != nil {
		t.Fatalf("syncFrom() error = %v", err)
	}

	if len(probe.fakeSink.upserts) != 1 {
		t.Fatalf("precondition: upserts = %d, want 1", len(probe.fakeSink.upserts))
	}
	if !probe.trackedDuringUpsert {
		t.Fatal("seqTracker.track(1, ...) had not run yet when Sink.Upsert was called for seq 1 — " +
			"an EXPUNGE arriving in that window would be silently dropped instead of reaching the store")
	}
}
