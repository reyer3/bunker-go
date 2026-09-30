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
	// goneID is set for an EXPUNGE resolved to a tracked UID: the item
	// left INBOX, moved or deleted (see resolveVanished).
	goneID   string
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
		// UID-keyed item id this package uses. The message is only
		// reported gone from INBOX here; whether it was deleted or moved
		// to another synced folder is resolveVanished's call (#53).
		Expunge: func(seqNum uint32) {
			uid, ok := tracker.expunge(seqNum)
			if !ok {
				return
			}
			id := itemID(a.cfg.Name, "INBOX", uidValidity, uid)
			select {
			case updates <- pendingUpdate{goneID: id}:
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
			id := itemID(a.cfg.Name, "INBOX", uidValidity, uid)
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

	// firstConnect gates INBOX's startup reconciliation (T9c) to run only
	// once per process, on the first successful connection. Every other
	// synced folder is reconciled on every pass over it (pollFolders),
	// since that is also how a move out of it is noticed (#53).
	firstConnect := reconciled != nil && !*reconciled

	// K2/#52: sync every folder besides INBOX before selecting INBOX
	// below, never after — INBOX stays selected from here through the
	// whole IDLE loop, so this never re-selects away from it and misses a
	// live EXPUNGE/FETCH another client makes on INBOX while this
	// connection would otherwise be looking at another folder. Later
	// passes over those folders run on their own short-lived connection
	// (pollAndResolve) for the same reason. A folder that fails is
	// logged, not fatal: mail sync otherwise works fine without it.
	pending := a.pollFolders(ctx, client, sink, folders, a.planSyncFolders(folders))

	mbox, err := client.Select("INBOX", nil).Wait()
	if err != nil {
		return fmt.Errorf("mail: select INBOX for %q: %w", a.cfg.Name, err)
	}
	uidValidity = mbox.UIDValidity

	if firstConnect {
		gone, err := a.reconcileFolder(ctx, client, sink, "INBOX", "INBOX", mbox.UIDValidity, nil)
		if err != nil {
			return err
		}
		pending = append(pending, gone...)
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

	// #52: the other folders are polled, and #53: messages gone from a
	// synced folder are looked for in the others, on a second, short-lived
	// connection, unlike R4's reconcile above. Polling means SELECTing
	// every other folder: done on this connection it would leave INBOX
	// for the whole pass, so EXPUNGE and flag pushes made meanwhile would
	// be lost and the sequence-number tracker the live EXPUNGE path
	// depends on would go stale. That second connection lives only for
	// the pass, once every folderPollInterval (or right after an
	// EXPUNGE), which stays far under per-account connection caps; Fetch,
	// Organize and Send already open their own the same way.
	pollTicker := time.NewTicker(a.folderPollInterval)
	defer pollTicker.Stop()
	// settle fires expungeSettle after messages go missing, to look for
	// them in the other folders; nil (never fires) while none are pending.
	var settle <-chan time.Time
	if len(pending) > 0 {
		settle = time.After(expungeSettle)
	}

	for {
		select {
		case <-ctx.Done():
			idleCmd.Close()
			return ctx.Err()
		case <-client.Closed():
			return fmt.Errorf("mail: connection to %q closed", a.cfg.Name)
		case <-pollTicker.C:
			pending = a.pollAndResolve(ctx, sink, true, pending)
			settle = nil
		case <-settle:
			pending = a.pollAndResolve(ctx, sink, false, pending)
			settle = nil
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
			if upd.goneID != "" {
				pending = append(pending, upd.goneID)
				if settle == nil {
					settle = time.After(expungeSettle)
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

// pollFolders syncs every folder of plan once on client, which it
// leaves selected on the last one: for each, it reconciles the stored
// items against the server (refreshing \Seen and keywords, and
// collecting the ones gone from it, #53) and fetches what arrived since
// its cursor (or the bounded initial window, on a folder synced for the
// first time). It returns the ids of the stored items that went missing,
// for resolveVanished. A folder that fails is logged and skipped, so one
// broken folder never stops the others.
func (a *Adapter) pollFolders(ctx context.Context, client *imapclient.Client, sink core.Sink, folders *FolderMap, plan []syncFolder) []string {
	if len(plan) == 0 {
		return nil
	}
	var stored []core.Item
	if lister, ok := sink.(storeLister); ok {
		items, err := lister.List(ctx, core.Filter{Channel: core.ChannelMail, Account: a.cfg.Name})
		if err != nil {
			a.logFolderError("list stored items", "", err)
		} else {
			stored = items
		}
	}

	var gone []string
	for _, f := range plan {
		mbox, err := client.Select(f.Mailbox, nil).Wait()
		if err != nil {
			a.logFolderError("select", f.Mailbox, err)
			continue
		}
		if stored != nil {
			vanished, err := a.reconcileFolder(ctx, client, sink, f.Folder, f.Mailbox, mbox.UIDValidity, stored)
			gone = append(gone, vanished...)
			if err != nil {
				a.logFolderError("reconcile", f.Mailbox, err)
			}
		}
		// No live IDLE tracking is needed here (nil tracker): only INBOX
		// is IDLEd, so there is no seq→UID EXPUNGE/FETCH mapping to keep
		// for any other folder between passes.
		if _, err := a.syncFrom(ctx, client, sink, folders, mbox.UIDValidity, 0, nil, mbox.NumMessages, f.Folder); err != nil {
			a.logFolderError("sync", f.Mailbox, err)
		}
	}
	return gone
}

// pollAndResolve is one pass on its own connection (see runOnce for why
// not the IDLE one): when poll is set it re-LISTs the folders, so one
// created in webmail since is picked up, and runs pollFolders over them;
// then it hands every item gone missing (pending plus what the poll
// found) to resolveVanished. It returns the ids still undecided, to be
// retried on the next pass. A failure to connect is logged and keeps
// every pending id for the next pass: without looking, a moved message
// must not be taken for a deleted one.
func (a *Adapter) pollAndResolve(ctx context.Context, sink core.Sink, poll bool, pending []string) []string {
	if !poll && len(pending) == 0 {
		return nil
	}
	client, err := a.dial(ctx, a.cfg, a.passwordSource, a.tokenSource, nil)
	if err != nil {
		a.logFolderError("connect for folder poll", "", err)
		return pending
	}
	defer client.Close()

	folders, err := discoverFolders(ctx, client, a.cfg)
	if err != nil {
		a.logFolderError("discover folders", "", err)
		return pending
	}
	plan := a.planSyncFolders(folders)
	if poll && a.onFolderPoll != nil {
		defer a.onFolderPoll()
	}
	if poll {
		pending = append(pending, a.pollFolders(ctx, client, sink, folders, plan)...)
	}
	if len(pending) == 0 {
		return nil
	}
	kept, err := a.resolveVanished(ctx, client, sink, folders, plan, pending)
	if err != nil {
		a.logFolderError("resolve moved or deleted mail", "", err)
	}
	return kept
}

// logFolderError logs a failure in the passes over non-INBOX folders
// (#52) at error level: none of them fails the connection, so this log
// is the only place such a failure shows up.
func (a *Adapter) logFolderError(what, mailbox string, err error) {
	a.logger.Error("mail: folder sync: "+what,
		"channel", string(core.ChannelMail), "account", a.cfg.Name, "folder", mailbox, "error", err)
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

// folderCursorName is the per-folder part of a folder's sync cursor keys
// (#52). INBOX and Sent keep their pre-#52 names ("inbox", "sent") so an
// existing database resumes where it left off; any other folder is keyed
// by its exact mailbox name, case kept, since two mailboxes may differ
// only in case.
func folderCursorName(folder string) string {
	switch folder {
	case "INBOX", "Sent":
		return strings.ToLower(folder)
	}
	return "folder:" + folder
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
	cursorName := folderCursorName(folder)
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

// fetchAndUpsertRange FETCHes numSet's headers/flags/envelope (and the
// bounded body text, bodytext.go) and upserts each resulting item via
// sink, exactly as fetchAndUpsert always did, but without touching any
// sync cursor — extracted so Backfill (backfill.go, H2) can reuse the
// identical item-building/upsert logic on a UID set found by search,
// which must never move the regular Run sync cursor forward. tracker
// may be nil (Backfill has none to feed). Every synced folder goes
// through here, so mail in any of them is stored the same way (#52).
func (a *Adapter) fetchAndUpsertRange(ctx context.Context, client *imapclient.Client, sink core.Sink, folders *FolderMap, numSet imap.NumSet, uidValidity uint32, highWater imap.UID, tracker *seqTracker, folder string) (imap.UID, error) {
	fetched, err := a.fetchItems(ctx, client, folders, numSet, uidValidity, folder)
	if err != nil {
		return highWater, err
	}

	dedupe := a.newAllMailDedupe(ctx, sink, folders, folder, len(fetched))
	for _, f := range fetched {
		// R5: seqTracker.track must run before Sink.Upsert, not after.
		// waitForUpsert-style test synchronization (and, in production,
		// an EXPUNGE delivered on the imapclient handler goroutine)
		// unblocks the instant Upsert is called; if track ran afterward,
		// an EXPUNGE for this same sequence number landing in that gap
		// would resolve to "untracked" and be silently dropped instead
		// of reaching the store (the root cause of
		// TestAdapterRunObservesExpungeFromAnotherClient's flakiness).
		if tracker != nil {
			tracker.track(f.seq, f.uid)
		}
		if !dedupe.skip(f) {
			if err := dedupe.upsert(ctx, f.item); err != nil {
				return highWater, err
			}
		}
		if f.uid > highWater {
			highWater = f.uid
		}
	}

	return highWater, nil
}

// fetchedMsg is one message fetchItems read, built into an item.
type fetchedMsg struct {
	item  core.Item
	seq   uint32
	uid   imap.UID
	draft bool
}

// fetchItems FETCHes numSet in the mailbox selected on client (folder's)
// and builds each message into an item: headers, flags, envelope, the
// bounded body text and, on Gmail, its labels. It stores nothing.
func (a *Adapter) fetchItems(ctx context.Context, client *imapclient.Client, folders *FolderMap, numSet imap.NumSet, uidValidity uint32, folder string) ([]fetchedMsg, error) {
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
	a.addBodyTextSections(fetchOptions)
	messages, err := client.Fetch(numSet, fetchOptions).Collect()
	if err != nil {
		return nil, fmt.Errorf("mail: fetch %s for %q: %w", folder, a.cfg.Name, err)
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
		labels, err := a.fetchGmailLabelsRaw(ctx, mailboxFor(folders, folder), uids)
		if err != nil {
			log.Printf("mail: sync: gmail X-GM-LABELS fetch for %q failed, leaving Labels as synced: %v", a.cfg.Name, err)
		} else {
			gmailLabels = labels
		}
	}

	out := make([]fetchedMsg, 0, len(messages))
	for _, msg := range messages {
		item := a.buildItem(msg, folders, uidValidity, folder)
		a.fillBodyText(&item, msg)
		if labels, ok := gmailLabels[msg.UID]; ok {
			item.Labels = labels
		}
		out = append(out, fetchedMsg{item: item, seq: msg.SeqNum, uid: msg.UID, draft: hasFlag(msg.Flags, imap.FlagDraft)})
	}
	return out, nil
}

func hasFlag(flags []imap.Flag, want imap.Flag) bool {
	for _, f := range flags {
		if strings.EqualFold(string(f), string(want)) {
			return true
		}
	}
	return false
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
// canonical folder it was fetched from ("INBOX", "Sent" (K2), or the
// server's name for any other synced mailbox, #52): it selects the id
// scheme (itemID), Meta["folder"] (how the user sees which mailbox an
// item is in), and two rules that
// hold regardless of what the raw message headers say — a Sent item is
// always FromMe and never Unread, since it is what the user sent, not
// what the server marked \Seen.
func (a *Adapter) buildItem(msg *imapclient.FetchMessageBuffer, folders *FolderMap, uidValidity uint32, folder string) core.Item {
	id := itemID(a.cfg.Name, folder, uidValidity, msg.UID)

	var references []string
	for _, section := range msg.BodySection {
		// Only the References-only header section: the same FETCH may
		// also carry the whole header or body (Fetch, fillBodyText), whose
		// every "<...>" token would otherwise be read as a reference.
		if section.Section == nil || len(section.Section.HeaderFields) == 0 {
			continue
		}
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
