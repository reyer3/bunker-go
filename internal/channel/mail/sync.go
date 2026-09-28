package mail

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/reyer3/bunker-go/internal/core"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// Run implements core.Adapter: it connects, does a bounded initial sync
// of INBOX, then IDLEs for new mail until ctx is canceled, reconnecting
// with a.backoff after any error. R2: a session (one runOnce call) that
// lasted at least mailHealthySession resets the attempt count, so a
// reconnect right after a long healthy IDLE waits the base backoff again
// instead of whatever the cap had grown to; a runOnce error is logged
// (never just discarded) since it is otherwise invisible until every
// reconnect attempt is exhausted.
func (a *Adapter) Run(ctx context.Context, sink core.Sink) error {
	attempt := 0
	// reconciled guards startup reconciliation (T9c) to run only once
	// per process, on the first successful connection: a later
	// reconnect's ordinary sync already keeps the store current, and
	// re-running the stored-vs-server diff on every reconnect would be
	// wasted work for no behavior difference.
	reconciled := false
	for {
		start := a.now()
		err := a.runOnce(ctx, sink, &reconciled)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if a.now().Sub(start) >= mailHealthySession {
			attempt = 0
		}
		attempt++
		if err != nil {
			a.logger.Error("mail: runOnce failed, reconnecting",
				"channel", string(core.ChannelMail), "account", a.cfg.Name, "attempt", attempt, "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(a.backoff(attempt)):
		}
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

	// firstConnect gates both INBOX's and Sent's startup reconciliation
	// (T9c/K2) to run only once per process, on the first successful
	// connection — *reconciled is only set true once both have run,
	// below, so a Sent-folder error before that point still lets a later
	// reconnect retry reconciling both.
	firstConnect := reconciled != nil && !*reconciled

	// K2: sync the Sent folder before selecting INBOX below, never after
	// — INBOX stays selected from here through the whole IDLE loop, so
	// this never re-selects away from it and misses a live EXPUNGE/FETCH
	// another client makes on INBOX while this connection would
	// otherwise be looking at Sent. Sent itself gets no live IDLE (only
	// send.go's own APPEND is truly "live" for it); this bounded
	// pull-sync on every (re)connect, mirroring INBOX's own initial-sync
	// window, is what otherwise keeps it current (e.g. a message sent
	// from the phone or webmail). A missing/unreachable Sent mailbox is
	// logged, not fatal: mail sync otherwise works fine without it.
	if err := a.syncSentFolder(ctx, client, sink, folders, firstConnect); err != nil {
		log.Printf("mail: sync %q Sent folder: %v", a.cfg.Name, err)
	}

	mbox, err := client.Select("INBOX", nil).Wait()
	if err != nil {
		return fmt.Errorf("mail: select INBOX for %q: %w", a.cfg.Name, err)
	}
	uidValidity = mbox.UIDValidity

	if firstConnect {
		if err := a.reconcileFolder(ctx, client, sink, "INBOX", mbox.UIDValidity); err != nil {
			return err
		}
		*reconciled = true
	}

	lastUID, err := a.syncFrom(ctx, client, sink, folders, mbox.UIDValidity, 0, tracker, mbox.NumMessages, "INBOX")
	if err != nil {
		return err
	}

	idleCmd, err := client.Idle()
	if err != nil {
		return fmt.Errorf("mail: idle for %q: %w", a.cfg.Name, err)
	}

	// R4: the periodic \Seen safety-net reconcile runs on THIS SAME
	// connection, between IDLE cycles (idleCmd.Close/Wait, do the FETCH,
	// re-Idle — exactly the pattern the existsCh branch below already
	// uses to pause IDLE for a resync), rather than opening a second,
	// short-lived connection. A second connection would double this
	// account's concurrent IMAP connection count on every reconcile tick
	// (real servers, especially Gmail, cap concurrent connections per
	// account) and needs its own auth/dial/close lifecycle management
	// for what is a lightweight, bounded FETCH; briefly pausing IDLE
	// every few minutes costs nothing IDLE's own EXISTS-triggered pause
	// doesn't already cost. A reconcileTick left nil (SeenReconcileInterval
	// <= 0) simply never fires in the select below, disabling it.
	var reconcileTick <-chan time.Time
	if a.cfg.SeenReconcileInterval > 0 {
		ticker := time.NewTicker(a.cfg.SeenReconcileInterval)
		defer ticker.Stop()
		reconcileTick = ticker.C
	}

	for {
		select {
		case <-ctx.Done():
			idleCmd.Close()
			return ctx.Err()
		case <-client.Closed():
			return fmt.Errorf("mail: connection to %q closed", a.cfg.Name)
		case <-reconcileTick:
			if err := idleCmd.Close(); err != nil {
				return fmt.Errorf("mail: stop idle for %q (seen reconcile): %w", a.cfg.Name, err)
			}
			if err := idleCmd.Wait(); err != nil {
				return fmt.Errorf("mail: idle wait for %q (seen reconcile): %w", a.cfg.Name, err)
			}
			if err := a.reconcileSeenFlags(ctx, client, sink, mbox.UIDValidity); err != nil {
				log.Printf("mail: seen reconcile for %q: %v", a.cfg.Name, err)
			}
			idleCmd, err = client.Idle()
			if err != nil {
				return fmt.Errorf("mail: re-idle for %q (seen reconcile): %w", a.cfg.Name, err)
			}
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
		case numMessages := <-existsCh:
			if err := idleCmd.Close(); err != nil {
				return fmt.Errorf("mail: stop idle for %q: %w", a.cfg.Name, err)
			}
			if err := idleCmd.Wait(); err != nil {
				return fmt.Errorf("mail: idle wait for %q: %w", a.cfg.Name, err)
			}

			lastUID, err = a.syncFrom(ctx, client, sink, folders, mbox.UIDValidity, lastUID, tracker, numMessages, "INBOX")
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

// maxSeenReconcileItems bounds the periodic \Seen safety-net reconcile
// (R4) to the newest this many stored-unread INBOX items per account,
// so a large mailbox with a long unread backlog never turns one tick
// into an unbounded UID FETCH.
const maxSeenReconcileItems = 200

// reconcileSeenFlags implements R4's periodic \Seen safety net: a UID
// FETCH FLAGS bounded to the newest maxSeenReconcileItems items this
// account's INBOX has stored as unread, applying any \Seen change made
// elsewhere (Roundcube, the phone's Gmail app) that the live IDLE
// unsolicited-FETCH path (T9b) might have missed — including one made
// before this connection ever reached IDLE at all (during dial/
// discoverFolders/the initial sync, or while a previous connection was
// down and this one hadn't reconnected yet). It never touches Labels/
// keywords: that stays IDLE's and the startup reconciliation's job. A
// Sink that cannot List (most unit-test fakes for other packages) is
// silently skipped, the same optional-capability pattern storeLister
// already uses for the startup reconciliation.
func (a *Adapter) reconcileSeenFlags(ctx context.Context, client *imapclient.Client, sink core.Sink, uidValidity uint32) error {
	lister, ok := sink.(storeLister)
	if !ok {
		return nil
	}
	unread := true
	stored, err := lister.List(ctx, core.Filter{Channel: core.ChannelMail, Account: a.cfg.Name, Unread: &unread})
	if err != nil {
		return fmt.Errorf("mail: seen reconcile: list stored unread items: %w", err)
	}
	sort.Slice(stored, func(i, j int) bool { return stored[i].Timestamp.After(stored[j].Timestamp) })

	var uidSet imap.UIDSet
	idByUID := make(map[imap.UID]string)
	for _, item := range stored {
		if !item.Unread {
			continue // defensive: a Lister that ignores filter.Unread (most test fakes)
		}
		_, folder, storedValidity, uid, err := parseItemID(item.ID)
		if err != nil || folder != "INBOX" || storedValidity != uidValidity {
			continue
		}
		uidSet.AddNum(uid)
		idByUID[uid] = item.ID
		if len(idByUID) >= maxSeenReconcileItems {
			break
		}
	}
	if len(idByUID) == 0 {
		return nil
	}

	messages, err := client.Fetch(uidSet, &imap.FetchOptions{UID: true, Flags: true}).Collect()
	if err != nil {
		return fmt.Errorf("mail: seen reconcile: fetch flags: %w", err)
	}
	for _, msg := range messages {
		id, ok := idByUID[msg.UID]
		if !ok {
			continue
		}
		if err := sink.MarkRead(ctx, id, hasSeenFlag(msg.Flags)); err != nil && !errors.Is(err, core.ErrNotFound) {
			return fmt.Errorf("mail: seen reconcile: mark %s: %w", id, err)
		}
	}
	return nil
}

// cursorKey builds the Sink cursor key for one piece of this account's
// sync state.
func cursorKey(account, name string) string {
	return fmt.Sprintf("mail:%s:%s", account, name)
}

// syncFrom fetches and upserts every message after sinceUID (or, when
// sinceUID is 0, the last initialSyncLimit of the numMessages in the
// selected mailbox), returning the
// highest UID it saw so the caller can pick up from there next time.
// UIDValidity is folded into the persisted cursor so a server-side
// UIDVALIDITY change (mailbox recreated) is detected instead of silently
// mismatching UIDs; on that mismatch this resets to a fresh initial
// sync.
func (a *Adapter) syncFrom(ctx context.Context, client *imapclient.Client, sink core.Sink, folders *FolderMap, uidValidity uint32, sinceUID imap.UID, tracker *seqTracker, numMessages uint32, folder string) (imap.UID, error) {
	cursorName := strings.ToLower(folder)
	validityKey := cursorKey(a.cfg.Name, cursorName+".uidvalidity")
	lastUIDKey := cursorKey(a.cfg.Name, cursorName+".last_uid")

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

	// numMessages comes from the caller (the SELECT result or an EXISTS
	// update), never from client.Mailbox(): go-imap v2 releases
	// Select().Wait() before it stores the mailbox, so that cache can
	// still be nil here, which silently skipped the initial sync.
	if numMessages == 0 {
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
		if numMessages > a.initialSyncLimit {
			start = numMessages - a.initialSyncLimit + 1
		}
		var seqSet imap.SeqSet
		seqSet.AddRange(start, 0)
		return a.fetchAndUpsert(ctx, client, sink, folders, seqSet, uidValidity, validityKey, lastUIDKey, sinceUID, tracker, folder)
	}

	return a.fetchAndUpsert(ctx, client, sink, folders, uidSet, uidValidity, validityKey, lastUIDKey, sinceUID, tracker, folder)
}

func (a *Adapter) fetchAndUpsert(ctx context.Context, client *imapclient.Client, sink core.Sink, folders *FolderMap, numSet imap.NumSet, uidValidity uint32, validityKey, lastUIDKey string, highWater imap.UID, tracker *seqTracker, folder string) (imap.UID, error) {
	highWater, err := a.fetchAndUpsertRange(ctx, client, sink, folders, numSet, uidValidity, highWater, tracker, folder)
	if err != nil {
		return highWater, err
	}

	if err := sink.SetCursor(ctx, validityKey, fmt.Sprint(uidValidity)); err != nil {
		return highWater, fmt.Errorf("mail: persist uidvalidity cursor: %w", err)
	}
	if err := sink.SetCursor(ctx, lastUIDKey, fmt.Sprint(uint32(highWater))); err != nil {
		return highWater, fmt.Errorf("mail: persist last-uid cursor: %w", err)
	}
	return highWater, nil
}

// fetchAndUpsertRange FETCHes numSet's headers/flags/envelope and upserts
// each resulting item via sink, exactly as fetchAndUpsert always did,
// but without touching any sync cursor — extracted so Backfill
// (backfill.go, H2) can reuse the identical item-building/upsert logic
// on a UID set found by search, which must never move the regular Run
// sync cursor forward. tracker may be nil (Backfill has none to feed).
func (a *Adapter) fetchAndUpsertRange(ctx context.Context, client *imapclient.Client, sink core.Sink, folders *FolderMap, numSet imap.NumSet, uidValidity uint32, highWater imap.UID, tracker *seqTracker, folder string) (imap.UID, error) {
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
		return highWater, fmt.Errorf("mail: fetch %s for %q: %w", folder, a.cfg.Name, err)
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
		item := a.buildItem(msg, folders, uidValidity, folder)
		if labels, ok := gmailLabels[msg.UID]; ok {
			item.Labels = labels
		}
		// R5: seqTracker.track must run before Sink.Upsert, not after.
		// waitForUpsert-style test synchronization (and, in production,
		// an EXPUNGE delivered on the imapclient handler goroutine)
		// unblocks the instant Upsert is called; if track ran afterward,
		// an EXPUNGE for this same sequence number landing in that gap
		// would resolve to "untracked" and be silently dropped instead
		// of reaching the store (the root cause of
		// TestAdapterRunObservesExpungeFromAnotherClient's flakiness).
		if tracker != nil {
			tracker.track(msg.SeqNum, msg.UID)
		}
		if err := sink.Upsert(ctx, item); err != nil {
			return highWater, fmt.Errorf("mail: upsert %s: %w", item.ID, err)
		}
		if msg.UID > highWater {
			highWater = msg.UID
		}
	}

	return highWater, nil
}

// syncSentFolder syncs the Sent mailbox (K2): it selects the folder
// discovered via SPECIAL-USE \Sent, falling back to the configured
// prefix (e.g. "INBOX.Sent") when the server advertises no special-use
// attribute, reconciles previously stored Sent items against it once per
// connection (parity with INBOX's own reconcileFolder) when
// firstConnect, then imports/refreshes its most recent
// a.initialSyncLimit messages via the same bounded syncFrom INBOX uses.
// It leaves the Sent mailbox selected; the caller re-selects INBOX for
// IDLE. A SELECT failure (no Sent mailbox yet, e.g. an account that has
// never sent anything on a server that doesn't pre-create one) is
// returned for the caller to log, never fatal to the connection.
func (a *Adapter) syncSentFolder(ctx context.Context, client *imapclient.Client, sink core.Sink, folders *FolderMap, firstConnect bool) error {
	mailbox := folders.Resolve("Sent")
	mbox, err := client.Select(mailbox, nil).Wait()
	if err != nil {
		return fmt.Errorf("select %s: %w", mailbox, err)
	}

	if firstConnect {
		if err := a.reconcileFolder(ctx, client, sink, "Sent", mbox.UIDValidity); err != nil {
			return fmt.Errorf("reconcile: %w", err)
		}
	}

	// No live IDLE tracking is needed for Sent (nil tracker): unlike
	// INBOX, this mailbox is never IDLEd, so there is no seq→UID EXPUNGE/
	// FETCH mapping to maintain between calls.
	if _, err := a.syncFrom(ctx, client, sink, folders, mbox.UIDValidity, 0, nil, mbox.NumMessages, "Sent"); err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	return nil
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

// buildItem converts one FETCH response into a core.Item. folder is the
// friendly mailbox name it was fetched from ("INBOX" or "Sent", K2):
// it selects the id scheme (itemID), Meta["folder"], and two rules that
// hold regardless of what the raw message headers say — a Sent item is
// always FromMe and never Unread, since it is what the user sent, not
// what the server marked \Seen.
func (a *Adapter) buildItem(msg *imapclient.FetchMessageBuffer, folders *FolderMap, uidValidity uint32, folder string) core.Item {
	id := itemID(a.cfg.Name, folder, uidValidity, msg.UID)

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

	labels := dovecotLabelsFromFlags(msg.Flags)
	// folders is unused here (Organize's MoveTo uses it instead); kept as
	// a parameter for symmetry with the other buildItem call sites, which
	// already have a *FolderMap in hand from discoverFolders.
	_ = folders

	unread := !hasSeenFlag(msg.Flags)
	fromMe := a.cfg.Username != "" && strings.EqualFold(from.ID, a.cfg.Username)
	if folder == "Sent" {
		unread = false // K2: a message the user sent never counts as unread.
		fromMe = true  // K2: the Sent folder itself is the FromMe signal.
	}

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
		FromMe:     fromMe,
		Timestamp:  at,
		Meta: map[string]string{
			"message_id": messageID,
			"folder":     folder,
		},
	}
}

func addressFromEnvelope(addr imap.Address) core.Address {
	return core.Address{ID: addr.Addr(), Name: addr.Name}
}
