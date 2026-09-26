package mail

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/reyer3/bunker-go/internal/core"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// Run implements core.Adapter: it connects, does a bounded initial sync
// of INBOX, then IDLEs for new mail until ctx is canceled, reconnecting
// with a.backoff after any error.
func (a *Adapter) Run(ctx context.Context, sink core.Sink) error {
	attempt := 0
	// reconciled guards startup reconciliation (T9c) to run only once
	// per process, on the first successful connection: a later
	// reconnect's ordinary sync already keeps the store current, and
	// re-running the stored-vs-server diff on every reconnect would be
	// wasted work for no behavior difference.
	reconciled := false
	for {
		err := a.runOnce(ctx, sink, &reconciled)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		attempt++
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(a.backoff(attempt)):
		}
		_ = err // surfaced only via backoff/retry; Run itself only returns on ctx cancellation
	}
}

// pendingUpdate is one live reconciliation fact (T9b) forwarded from an
// imapclient.UnilateralDataHandler callback — which runs in an arbitrary
// goroutine per its docs — to runOnce's own goroutine, so sink writes
// never race syncFrom's.
type pendingUpdate struct {
	deleteID string // set for an EXPUNGE resolved to a tracked UID
	markID   string // set for a flag FETCH resolved to a tracked UID
	markSeen bool
	// labels is this FETCH's Dovecot keywords (T14a), applied to the
	// store alongside markSeen. It is only meaningful for a non-Gmail
	// account: Gmail labels never travel as ordinary FLAGS, so applying
	// an always-empty computed value here would wipe out Labels the
	// account's X-GM-LABELS read-back had already set.
	labels []string
}

// itemGetter is the read capability the IDLE FETCH keyword
// reconciliation (T14a) needs from its Sink to merge a flags-only
// update into the stored item without clobbering its other fields;
// core.Store satisfies it structurally. A Sink that doesn't (most unit-
// test fakes for other packages) simply skips the Labels merge, the
// same optional-capability pattern as storeLister.
type itemGetter interface {
	Get(ctx context.Context, id string) (core.Item, error)
}

// runOnce is one connect-sync-idle cycle. It returns nil only when ctx is
// canceled; any other return is a connection problem the caller retries.
// *reconciled is set once startup reconciliation (T9c) has run.
func (a *Adapter) runOnce(ctx context.Context, sink core.Sink, reconciled *bool) error {
	existsCh := make(chan uint32, 8)
	updates := make(chan pendingUpdate, 32)
	tracker := newSeqTracker()

	// uidValidity is set once Select succeeds, below; Expunge/Fetch only
	// ever fire afterward (while selected/IDLEing), so the closures below
	// always read it after that assignment.
	var uidValidity uint32

	handler := &imapclient.UnilateralDataHandler{
		Mailbox: func(data *imapclient.UnilateralDataMailbox) {
			if data.NumMessages != nil {
				select {
				case existsCh <- *data.NumMessages:
				default:
				}
			}
		},
		// Expunge implements T9b's live reconciliation for moves/deletes
		// made elsewhere (Roundcube, Gmail): the server only reports a
		// sequence number, so it is resolved through tracker back to the
		// UID-keyed item id this package uses.
		Expunge: func(seqNum uint32) {
			uid, ok := tracker.expunge(seqNum)
			if !ok {
				return
			}
			id := fmt.Sprintf("mail:%s:%d.%d", a.cfg.Name, uidValidity, uid)
			select {
			case updates <- pendingUpdate{deleteID: id}:
			default:
			}
		},
		// Fetch implements T9b's live reconciliation for \Seen changes and
		// T14(a)'s for Dovecot keyword (label) changes made elsewhere: an
		// unsolicited FETCH push reports the current flags for a sequence
		// number, resolved the same way.
		Fetch: func(data *imapclient.FetchMessageData) {
			buf, err := data.Collect()
			if err != nil {
				return
			}
			uid, ok := tracker.lookup(buf.SeqNum)
			if !ok {
				return
			}
			id := fmt.Sprintf("mail:%s:%d.%d", a.cfg.Name, uidValidity, uid)
			select {
			case updates <- pendingUpdate{
				markID:   id,
				markSeen: hasSeenFlag(buf.Flags),
				labels:   dovecotLabelsFromFlags(buf.Flags),
			}:
			default:
			}
		},
	}

	client, err := a.dial(ctx, a.cfg, a.passwordSource, a.tokenSource, handler)
	if err != nil {
		return err
	}
	defer client.Close()

	folders, err := discoverFolders(ctx, client, a.cfg)
	if err != nil {
		return fmt.Errorf("mail: discover folders for %q: %w", a.cfg.Name, err)
	}

	mbox, err := client.Select("INBOX", nil).Wait()
	if err != nil {
		return fmt.Errorf("mail: select INBOX for %q: %w", a.cfg.Name, err)
	}
	uidValidity = mbox.UIDValidity

	if reconciled != nil && !*reconciled {
		if err := a.reconcileStartup(ctx, client, sink, mbox.UIDValidity); err != nil {
			return err
		}
		*reconciled = true
	}

	lastUID, err := a.syncFrom(ctx, client, sink, folders, mbox.UIDValidity, 0, tracker)
	if err != nil {
		return err
	}

	idleCmd, err := client.Idle()
	if err != nil {
		return fmt.Errorf("mail: idle for %q: %w", a.cfg.Name, err)
	}

	for {
		select {
		case <-ctx.Done():
			idleCmd.Close()
			return ctx.Err()
		case <-client.Closed():
			return fmt.Errorf("mail: connection to %q closed", a.cfg.Name)
		case upd := <-updates:
			if upd.deleteID != "" {
				if err := sink.Delete(ctx, upd.deleteID); err != nil && !errors.Is(err, core.ErrNotFound) {
					return fmt.Errorf("mail: reconcile expunge %s: %w", upd.deleteID, err)
				}
				continue
			}
			markErr := sink.MarkRead(ctx, upd.markID, upd.markSeen)
			if markErr != nil && !errors.Is(markErr, core.ErrNotFound) {
				return fmt.Errorf("mail: reconcile flags %s: %w", upd.markID, markErr)
			}
			// T14(a): reconcile Dovecot keywords too, read-modify-write so
			// every other field of the stored item is left untouched. Skip
			// entirely for Gmail (see pendingUpdate.labels) and when the
			// item is already gone (markErr) or the Sink can't be read back
			// (most unit-test fakes for other packages).
			if markErr == nil && !a.cfg.Gmail {
				if getter, ok := sink.(itemGetter); ok {
					if item, gerr := getter.Get(ctx, upd.markID); gerr == nil {
						item.Labels = upd.labels
						if err := sink.Upsert(ctx, item); err != nil {
							return fmt.Errorf("mail: reconcile keywords %s: %w", upd.markID, err)
						}
					}
				}
			}
		case <-existsCh:
			if err := idleCmd.Close(); err != nil {
				return fmt.Errorf("mail: stop idle for %q: %w", a.cfg.Name, err)
			}
			if err := idleCmd.Wait(); err != nil {
				return fmt.Errorf("mail: idle wait for %q: %w", a.cfg.Name, err)
			}

			lastUID, err = a.syncFrom(ctx, client, sink, folders, mbox.UIDValidity, lastUID, tracker)
			if err != nil {
				return err
			}

			idleCmd, err = client.Idle()
			if err != nil {
				return fmt.Errorf("mail: re-idle for %q: %w", a.cfg.Name, err)
			}
		}
	}
}

// cursorKey builds the Sink cursor key for one piece of this account's
// sync state.
func cursorKey(account, name string) string {
	return fmt.Sprintf("mail:%s:%s", account, name)
}

// syncFrom fetches and upserts every message after sinceUID (or, when
// sinceUID is 0, the last initialSyncLimit messages), returning the
// highest UID it saw so the caller can pick up from there next time.
// UIDValidity is folded into the persisted cursor so a server-side
// UIDVALIDITY change (mailbox recreated) is detected instead of silently
// mismatching UIDs; on that mismatch this resets to a fresh initial
// sync.
func (a *Adapter) syncFrom(ctx context.Context, client *imapclient.Client, sink core.Sink, folders *FolderMap, uidValidity uint32, sinceUID imap.UID, tracker *seqTracker) (imap.UID, error) {
	validityKey := cursorKey(a.cfg.Name, "inbox.uidvalidity")
	lastUIDKey := cursorKey(a.cfg.Name, "inbox.last_uid")

	if sinceUID == 0 {
		stored, err := sink.Cursor(ctx, validityKey)
		if err != nil {
			return 0, fmt.Errorf("mail: read uidvalidity cursor: %w", err)
		}
		if stored == fmt.Sprint(uidValidity) {
			if raw, err := sink.Cursor(ctx, lastUIDKey); err == nil && raw != "" {
				var uid uint32
				fmt.Sscanf(raw, "%d", &uid)
				sinceUID = imap.UID(uid)
			}
		}
	}

	mbox := client.Mailbox()
	if mbox == nil || mbox.NumMessages == 0 {
		if err := sink.SetCursor(ctx, validityKey, fmt.Sprint(uidValidity)); err != nil {
			return sinceUID, fmt.Errorf("mail: persist uidvalidity cursor: %w", err)
		}
		return sinceUID, nil
	}

	var uidSet imap.UIDSet
	if sinceUID > 0 {
		uidSet.AddRange(sinceUID+1, 0)
	} else {
		start := uint32(1)
		if mbox.NumMessages > a.initialSyncLimit {
			start = mbox.NumMessages - a.initialSyncLimit + 1
		}
		var seqSet imap.SeqSet
		seqSet.AddRange(start, 0)
		return a.fetchAndUpsert(ctx, client, sink, folders, seqSet, uidValidity, validityKey, lastUIDKey, sinceUID, tracker)
	}

	return a.fetchAndUpsert(ctx, client, sink, folders, uidSet, uidValidity, validityKey, lastUIDKey, sinceUID, tracker)
}

func (a *Adapter) fetchAndUpsert(ctx context.Context, client *imapclient.Client, sink core.Sink, folders *FolderMap, numSet imap.NumSet, uidValidity uint32, validityKey, lastUIDKey string, highWater imap.UID, tracker *seqTracker) (imap.UID, error) {
	fetchOptions := &imap.FetchOptions{
		UID:      true,
		Flags:    true,
		Envelope: true,
		BodySection: []*imap.FetchItemBodySection{{
			Specifier:    imap.PartSpecifierHeader,
			HeaderFields: []string{"References"},
			Peek:         true,
		}},
	}
	messages, err := client.Fetch(numSet, fetchOptions).Collect()
	if err != nil {
		return highWater, fmt.Errorf("mail: fetch inbox for %q: %w", a.cfg.Name, err)
	}

	// T14(a): Gmail's X-GM-LABELS never travels as an ordinary FLAGS
	// item, so buildItem alone can't populate Labels for a Gmail
	// account; batch-fetch it once for this whole sync window over the
	// raw connection. A failure here is logged and never fails the
	// sync itself — every item just keeps whatever Labels it already
	// had (nil, for a message synced for the first time).
	var gmailLabels map[imap.UID][]string
	if a.cfg.Gmail && len(messages) > 0 {
		uids := make([]imap.UID, len(messages))
		for i, msg := range messages {
			uids[i] = msg.UID
		}
		labels, err := a.fetchGmailLabelsRaw(ctx, uids)
		if err != nil {
			log.Printf("mail: sync: gmail X-GM-LABELS fetch for %q failed, leaving Labels as synced: %v", a.cfg.Name, err)
		} else {
			gmailLabels = labels
		}
	}

	for _, msg := range messages {
		item := a.buildItem(msg, folders, uidValidity)
		if labels, ok := gmailLabels[msg.UID]; ok {
			item.Labels = labels
		}
		if err := sink.Upsert(ctx, item); err != nil {
			return highWater, fmt.Errorf("mail: upsert %s: %w", item.ID, err)
		}
		if tracker != nil {
			tracker.track(msg.SeqNum, msg.UID)
		}
		if msg.UID > highWater {
			highWater = msg.UID
		}
	}

	if err := sink.SetCursor(ctx, validityKey, fmt.Sprint(uidValidity)); err != nil {
		return highWater, fmt.Errorf("mail: persist uidvalidity cursor: %w", err)
	}
	if err := sink.SetCursor(ctx, lastUIDKey, fmt.Sprint(uint32(highWater))); err != nil {
		return highWater, fmt.Errorf("mail: persist last-uid cursor: %w", err)
	}
	return highWater, nil
}

// referencesHeaderRe pulls every "<...>" message id token out of a raw
// (possibly folded) References header value.
var referencesHeaderRe = regexp.MustCompile(`<[^<>]+>`)

func parseReferences(raw []byte) []string {
	matches := referencesHeaderRe.FindAllString(string(raw), -1)
	out := make([]string, len(matches))
	for i, m := range matches {
		out[i] = stripAngle(m)
	}
	return out
}

func stripAngle(s string) string {
	return strings.TrimSuffix(strings.TrimPrefix(s, "<"), ">")
}

// buildItem converts one FETCH response into a core.Item.
func (a *Adapter) buildItem(msg *imapclient.FetchMessageBuffer, folders *FolderMap, uidValidity uint32) core.Item {
	id := fmt.Sprintf("mail:%s:%d.%d", a.cfg.Name, uidValidity, msg.UID)

	var references []string
	for _, section := range msg.BodySection {
		references = parseReferences(section.Bytes)
	}

	var subject, messageID string
	var inReplyTo string
	var from core.Address
	var to []core.Address
	var at time.Time
	if msg.Envelope != nil {
		subject = msg.Envelope.Subject
		messageID = msg.Envelope.MessageID
		if len(msg.Envelope.InReplyTo) > 0 {
			inReplyTo = msg.Envelope.InReplyTo[0]
		}
		if len(msg.Envelope.From) > 0 {
			from = addressFromEnvelope(msg.Envelope.From[0])
		}
		for _, addr := range msg.Envelope.To {
			to = append(to, addressFromEnvelope(addr))
		}
		at = msg.Envelope.Date
	}

	unread := !hasSeenFlag(msg.Flags)
	labels := dovecotLabelsFromFlags(msg.Flags)
	// folders is unused for INBOX (Run only syncs INBOX, which never
	// carries its own folder label); Organize's MoveTo uses it instead.
	_ = folders

	return core.Item{
		ID:         id,
		Channel:    core.ChannelMail,
		Account:    a.cfg.Name,
		Thread:     ThreadID(messageID, inReplyTo, references),
		ThreadName: ThreadName(subject),
		From:       from,
		To:         to,
		Subject:    subject,
		Labels:     labels,
		Unread:     unread,
		Timestamp:  at,
		Meta: map[string]string{
			"message_id": messageID,
			"folder":     "INBOX",
		},
	}
}

func addressFromEnvelope(addr imap.Address) core.Address {
	return core.Address{ID: addr.Addr(), Name: addr.Name}
}
