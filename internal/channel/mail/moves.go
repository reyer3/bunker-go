package mail

import (
	"context"
	"errors"
	"fmt"

	"github.com/reyer3/bunker-go/internal/core"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// resolveVanished decides, for each stored item gone from the folder it
// was synced from (a live EXPUNGE in INBOX, or a reconcile pass over any
// synced folder), whether it was moved or deleted (#53). An EXPUNGE only
// says the message left one mailbox: a webmail or phone moving it to an
// archive or subfolder looks exactly like a delete from where bunker-go
// watches. So every synced folder, INBOX first, is searched for the
// item's Message-ID; where it turns up, the item is relocated to that
// copy (relocate), and only an item found in none of them is deleted.
//
// This looks the message up by Message-ID instead of using COPYUID: the
// COPYUID of a move made by another client goes to that client, never to
// this connection, and every copy of a message keeps its Message-ID. No
// grace period is needed either: a MOVE (or COPY then EXPUNGE) puts the
// destination copy in place before the source copy is expunged, so the
// copy is already there when the EXPUNGE that started this arrives.
//
// ids are item ids; those the store no longer has (already rekeyed by
// this process's own Organize, or deleted) are skipped. The returned ids
// are the ones left undecided because a folder could not be searched:
// deleting them could drop mail that moved into that folder, so the
// caller retries them on its next pass. client must be a connection this
// may freely SELECT on (never the IDLE one); it is left with an
// arbitrary mailbox selected.
func (a *Adapter) resolveVanished(ctx context.Context, client *imapclient.Client, sink core.Sink, folders *FolderMap, plan []syncFolder, ids []string) ([]string, error) {
	getter, ok := sink.(itemGetter)
	if !ok {
		// Without reading the stored item there is no Message-ID to look
		// for: fall back to the pre-#53 behavior, a plain delete.
		for _, id := range ids {
			if err := sink.Delete(ctx, id); err != nil && !errors.Is(err, core.ErrNotFound) {
				return nil, fmt.Errorf("mail: drop vanished item %s: %w", id, err)
			}
		}
		return nil, nil
	}

	type lostItem struct {
		item  core.Item
		msgID string
	}
	var lost []lostItem
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		item, err := getter.Get(ctx, id)
		if errors.Is(err, core.ErrNotFound) {
			continue
		}
		if err != nil {
			return ids, fmt.Errorf("mail: read vanished item %s: %w", id, err)
		}
		msgID := item.Meta["message_id"]
		if msgID == "" {
			// Nothing to recognize a moved copy by.
			if err := sink.Delete(ctx, id); err != nil && !errors.Is(err, core.ErrNotFound) {
				return nil, fmt.Errorf("mail: drop vanished item %s: %w", id, err)
			}
			continue
		}
		lost = append(lost, lostItem{item: item, msgID: msgID})
	}

	undecided := func() []string {
		out := make([]string, len(lost))
		for i, l := range lost {
			out[i] = l.item.ID
		}
		return out
	}

	// INBOX first: on Gmail a message in INBOX is also in All Mail, and
	// the INBOX copy is the one kept while it is there (#52). Then Sent,
	// which planSyncFolders always puts first, for the same reason.
	order := append([]syncFolder{{Mailbox: "INBOX", Folder: "INBOX"}}, plan...)
	incomplete := false
	for _, f := range order {
		if len(lost) == 0 {
			break
		}
		mbox, err := client.Select(f.Mailbox, &imap.SelectOptions{ReadOnly: true}).Wait()
		if err != nil {
			a.logFolderError("select to look for moved mail", f.Mailbox, err)
			incomplete = true
			continue
		}
		var still []lostItem
		for _, l := range lost {
			found, err := a.findByMessageID(ctx, client, folders, f.Folder, mbox.UIDValidity, l.msgID)
			if err != nil {
				a.logFolderError("look for moved mail", f.Mailbox, err)
				incomplete = true
				still = append(still, l)
				continue
			}
			if found == nil {
				still = append(still, l)
				continue
			}
			if err := relocate(ctx, sink, l.item, *found); err != nil {
				return append(undecided(), l.item.ID), err
			}
		}
		lost = still
	}

	if incomplete {
		return undecided(), nil
	}
	for _, l := range lost {
		if err := sink.Delete(ctx, l.item.ID); err != nil && !errors.Is(err, core.ErrNotFound) {
			return nil, fmt.Errorf("mail: drop deleted item %s: %w", l.item.ID, err)
		}
	}
	return nil, nil
}

// findByMessageID looks for a message with exactly msgID in the selected
// mailbox and returns it built as an item of folder, or nil when there
// is none. SEARCH HEADER is a substring match, so every candidate's
// envelope is compared exactly before one is taken; a draft is never
// taken (Gmail's All Mail holds drafts too, with the Message-ID of the
// mail they will become).
func (a *Adapter) findByMessageID(ctx context.Context, client *imapclient.Client, folders *FolderMap, folder string, uidValidity uint32, msgID string) (*core.Item, error) {
	criteria := &imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "Message-ID", Value: msgID}}}
	data, err := client.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("mail: search Message-ID in %s: %w", folder, err)
	}
	uids := data.AllUIDs()
	if len(uids) == 0 {
		return nil, nil
	}
	var set imap.UIDSet
	set.AddNum(uids...)
	fetched, err := a.fetchItems(ctx, client, folders, set, uidValidity, folder)
	if err != nil {
		return nil, err
	}
	for _, f := range fetched {
		if f.draft || f.item.Meta["message_id"] != msgID {
			continue
		}
		item := f.item
		return &item, nil
	}
	return nil, nil
}

// relocate moves a stored item to the copy of its message found in
// another folder (#53): the item keeps what only the store knew — the
// full body and attachments a read saved — and takes everything the
// server says about the new copy, its id and folder first, then its
// flags and labels (a move keeps \Seen, so read state carries over). The
// id changes because it names the mailbox and UID (itemID), so this is an
// upsert of the new id followed by a delete of the old one: the TUI and
// CLI then simply find the item under its new id on their next load, the
// same rekey core.Service already does after a move made from bunker-go.
func relocate(ctx context.Context, sink core.Sink, old, found core.Item) error {
	item := found
	if old.Body != "" {
		item.Body = old.Body
	}
	if len(old.Attachments) > 0 {
		item.Attachments = old.Attachments
	}
	if err := sink.Upsert(ctx, item); err != nil {
		return fmt.Errorf("mail: relocate %s to %s: %w", old.ID, item.ID, err)
	}
	if item.ID == old.ID {
		return nil
	}
	if err := sink.Delete(ctx, old.ID); err != nil && !errors.Is(err, core.ErrNotFound) {
		return fmt.Errorf("mail: relocate %s to %s: drop old id: %w", old.ID, item.ID, err)
	}
	return nil
}

// allMailDedupe keeps a Gmail message stored once (#52). On Gmail every
// message is also in the \All mailbox ("All Mail"), which is synced so
// archived mail shows up, so its INBOX and Sent copies would otherwise
// each be stored twice. The INBOX or Sent copy is the one kept: an \All
// copy of a message stored from either is skipped, and an INBOX or Sent
// copy synced after an \All one replaces it. Once the message leaves
// INBOX (archived), its INBOX item vanishes and resolveVanished finds
// the \All copy by Message-ID, even though the \All sync cursor already
// went past it. It is inert on servers without \All and for any other
// folder.
type allMailDedupe struct {
	sink core.Sink
	// allFolder is the \All mailbox's canonical folder, "" when inactive.
	allFolder string
	folder    string
	byMsgID   map[string][]core.Item
}

// newAllMailDedupe prepares the dedupe for a batch of n messages synced
// into folder, listing the stored items once for the whole batch. A
// Sink that cannot List (most unit-test fakes for other packages) gets
// an inert one.
func (a *Adapter) newAllMailDedupe(ctx context.Context, sink core.Sink, folders *FolderMap, folder string, n int) *allMailDedupe {
	d := &allMailDedupe{sink: sink, folder: folder}
	all := folders.specialUse[SpecialUseAll]
	if all == "" || n == 0 {
		return d
	}
	allFolder := canonicalFolder(folders, all)
	if folder != allFolder && folder != "INBOX" && folder != "Sent" {
		return d
	}
	lister, ok := sink.(storeLister)
	if !ok {
		return d
	}
	stored, err := lister.List(ctx, core.Filter{Channel: core.ChannelMail, Account: a.cfg.Name})
	if err != nil {
		// Worst case a message is stored twice until the next pass.
		a.logFolderError("list stored items for All Mail dedupe", all, err)
		return d
	}
	d.allFolder = allFolder
	d.byMsgID = make(map[string][]core.Item)
	for _, item := range stored {
		if item.Account != a.cfg.Name {
			continue
		}
		if msgID := item.Meta["message_id"]; msgID != "" {
			d.byMsgID[msgID] = append(d.byMsgID[msgID], item)
		}
	}
	return d
}

// skip reports whether a message fetched from \All must not be stored:
// a draft, or one already stored from INBOX or Sent.
func (d *allMailDedupe) skip(f fetchedMsg) bool {
	if d.allFolder == "" || d.folder != d.allFolder {
		return false
	}
	if f.draft {
		return true
	}
	for _, stored := range d.byMsgID[f.item.Meta["message_id"]] {
		if folder := stored.Meta["folder"]; folder == "INBOX" || folder == "Sent" {
			return true
		}
	}
	return false
}

// replaced returns the stored \All copies an INBOX or Sent item replaces.
func (d *allMailDedupe) replaced(item core.Item) []core.Item {
	if d.allFolder == "" || d.folder == d.allFolder {
		return nil
	}
	var out []core.Item
	for _, stored := range d.byMsgID[item.Meta["message_id"]] {
		if stored.Meta["folder"] == d.allFolder && stored.ID != item.ID {
			out = append(out, stored)
		}
	}
	return out
}

// upsert stores item, first taking over (relocate) any \All copy it
// replaces, so a body a read saved on that copy is kept.
func (d *allMailDedupe) upsert(ctx context.Context, item core.Item) error {
	olds := d.replaced(item)
	if len(olds) == 0 {
		if err := d.sink.Upsert(ctx, item); err != nil {
			return fmt.Errorf("mail: upsert %s: %w", item.ID, err)
		}
		return nil
	}
	for _, old := range olds {
		if err := relocate(ctx, d.sink, old, item); err != nil {
			return err
		}
		if old.Body != "" {
			item.Body = old.Body
		}
		if len(old.Attachments) > 0 {
			item.Attachments = old.Attachments
		}
	}
	msgID := item.Meta["message_id"]
	kept := []core.Item{item}
	for _, stored := range d.byMsgID[msgID] {
		if stored.Meta["folder"] != d.allFolder {
			kept = append(kept, stored)
		}
	}
	d.byMsgID[msgID] = kept
	return nil
}
