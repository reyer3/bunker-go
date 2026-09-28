package mail

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/reyer3/bunker-go/internal/core"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// storeLister is the read capability Run's startup reconciliation
// (T9c) needs from the Sink it's given; core.Store — what the daemon
// actually passes to Run — satisfies it structurally. A Sink that
// doesn't also implement it (most unit-test fakes for other packages)
// simply skips reconciliation, the same optional-capability pattern as
// core.Fetcher/Sender/Organizer.
type storeLister interface {
	List(ctx context.Context, filter core.Filter) ([]core.Item, error)
}

// reconcileFolder implements T9(c) (and, for folder "Sent", K2's
// "reconciliation parity"): it compares every stored item of this
// account's folder against the server, keyed by UIDVALIDITY. A stored
// item whose UID is no longer present is dropped (its message was moved
// or deleted elsewhere while the daemon wasn't running — mirroring the
// stale row kept live on 2026-09-25, mail:cl:1700000000.100); one
// still present has its \Seen state refreshed via a FETCH bounded to
// just the stored items, so a read done elsewhere before startup isn't
// left stale. If UIDVALIDITY itself changed (the mailbox was
// recreated), every stored item of this folder for this account is
// dropped outright — a UID collision under a new UIDVALIDITY could name
// a completely different message — and the normal sync that follows
// repopulates the folder from scratch. The caller must already have
// folder's actual mailbox selected on client.
func (a *Adapter) reconcileFolder(ctx context.Context, client *imapclient.Client, sink core.Sink, folder string, uidValidity uint32) error {
	lister, ok := sink.(storeLister)
	if !ok {
		return nil
	}
	stored, err := lister.List(ctx, core.Filter{Channel: core.ChannelMail, Account: a.cfg.Name})
	if err != nil {
		return fmt.Errorf("mail: reconcile: list stored items: %w", err)
	}

	type storedRef struct {
		id  string
		uid imap.UID
	}
	var refs []storedRef
	storedByID := make(map[string]core.Item, len(stored))
	for _, item := range stored {
		if item.Meta["folder"] != folder {
			continue // only this call's folder is being reconciled here
		}
		_, _, storedValidity, uid, err := parseItemID(item.ID)
		if err != nil {
			continue // foreign/malformed id, not ours to reconcile
		}
		if storedValidity != uidValidity {
			if err := sink.Delete(ctx, item.ID); err != nil && !errors.Is(err, core.ErrNotFound) {
				return fmt.Errorf("mail: reconcile: drop stale-uidvalidity item %s: %w", item.ID, err)
			}
			continue
		}
		refs = append(refs, storedRef{id: item.ID, uid: uid})
		storedByID[item.ID] = item
	}
	if len(refs) == 0 {
		return nil
	}

	searchData, err := client.UIDSearch(&imap.SearchCriteria{}, nil).Wait()
	if err != nil {
		return fmt.Errorf("mail: reconcile: uid search all: %w", err)
	}
	present := make(map[imap.UID]bool, len(searchData.AllUIDs()))
	for _, uid := range searchData.AllUIDs() {
		present[uid] = true
	}

	var remaining imap.UIDSet
	remainingIDs := make(map[imap.UID]string, len(refs))
	for _, r := range refs {
		if !present[r.uid] {
			if err := sink.Delete(ctx, r.id); err != nil && !errors.Is(err, core.ErrNotFound) {
				return fmt.Errorf("mail: reconcile: drop stale item %s: %w", r.id, err)
			}
			continue
		}
		remaining.AddNum(r.uid)
		remainingIDs[r.uid] = r.id
	}
	if len(remainingIDs) == 0 {
		return nil
	}

	messages, err := client.Fetch(remaining, &imap.FetchOptions{UID: true, Flags: true}).Collect()
	if err != nil {
		return fmt.Errorf("mail: reconcile: fetch flags: %w", err)
	}

	// T14(a), Gmail half: X-GM-LABELS never travels as an ordinary FLAGS
	// item, so it needs its own batched, bounded (just the still-present
	// stored items) raw-connection fetch. A failure here is logged and
	// never fails reconciliation: every item below just keeps whatever
	// Labels it already had.
	var gmailLabels map[imap.UID][]string
	if a.cfg.Gmail && len(messages) > 0 {
		uids := make([]imap.UID, len(messages))
		for i, msg := range messages {
			uids[i] = msg.UID
		}
		labels, err := a.fetchGmailLabelsRaw(ctx, uids)
		if err != nil {
			log.Printf("mail: reconcile: gmail X-GM-LABELS fetch for %q failed, leaving stored Labels as is: %v", a.cfg.Name, err)
		} else {
			gmailLabels = labels
		}
	}

	for _, msg := range messages {
		id, ok := remainingIDs[msg.UID]
		if !ok {
			continue
		}
		item, ok := storedByID[id]
		if !ok {
			continue
		}
		item.Unread = !hasSeenFlag(msg.Flags)
		if a.cfg.Gmail {
			if labels, ok := gmailLabels[msg.UID]; ok {
				item.Labels = labels
			}
			// else: raw fetch failed, or the server didn't answer for
			// this UID — leave item.Labels as it was already stored.
		} else {
			// T14(a), Dovecot half: read-modify-write so keywords are
			// refreshed into Labels alongside Unread, instead of the old
			// MarkRead-only call that left Labels untouched.
			item.Labels = dovecotLabelsFromFlags(msg.Flags)
		}
		if err := sink.Upsert(ctx, item); err != nil {
			return fmt.Errorf("mail: reconcile: refresh %s: %w", id, err)
		}
	}
	return nil
}
