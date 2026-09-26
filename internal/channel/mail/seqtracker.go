package mail

import (
	"sync"

	"github.com/emersion/go-imap/v2"
)

// seqTracker maps a mailbox's current sequence numbers to UIDs, so an
// unsolicited EXPUNGE (which carries only a sequence number) or an
// unsolicited FETCH (whose flag-update push may not carry a UID either)
// can be resolved to the UID this package keys item IDs by. It only
// knows the UIDs of messages this adapter has actually fetched in the
// current session; an EXPUNGE or FETCH for a sequence number outside
// that set is reported as untracked, which is fine — such a message was
// never upserted into the store either, so there is nothing to
// reconcile.
type seqTracker struct {
	mu    sync.Mutex
	bySeq map[uint32]imap.UID
}

func newSeqTracker() *seqTracker {
	return &seqTracker{bySeq: make(map[uint32]imap.UID)}
}

// track records that seq currently addresses uid, e.g. right after a
// FETCH response for it.
func (t *seqTracker) track(seq uint32, uid imap.UID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.bySeq[seq] = uid
}

// lookup returns the UID currently tracked at seq.
func (t *seqTracker) lookup(seq uint32) (imap.UID, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	uid, ok := t.bySeq[seq]
	return uid, ok
}

// expunge removes seq and renumbers every higher sequence number down
// by one, mirroring what EXPUNGE does to every other message in the
// mailbox, so subsequent lookups (and future expunges) stay correct.
func (t *seqTracker) expunge(seq uint32) (imap.UID, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	uid, ok := t.bySeq[seq]
	next := make(map[uint32]imap.UID, len(t.bySeq))
	for s, u := range t.bySeq {
		switch {
		case s < seq:
			next[s] = u
		case s > seq:
			next[s-1] = u
		}
	}
	t.bySeq = next
	return uid, ok
}
