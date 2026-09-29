package mail

import (
	"context"
	"fmt"
	"sort"

	"github.com/reyer3/bunker-go/internal/core"

	"github.com/emersion/go-imap/v2"
)

var _ core.Searcher = (*Adapter)(nil)

// defaultSearchLimit is Search's own result cap when criteria.Limit is
// <= 0, matching the CLI's documented --limit default (docs/cli.md).
const defaultSearchLimit = 50

// Search implements core.Searcher (H3: mail-history). It runs an IMAP
// UID SEARCH for criteria (Header field matches for From/Subject, plus
// Since/Before), keeps at most criteria.Limit hits (the highest UIDs —
// newest first, matching List's own ordering), fetches their
// headers/flags through the same fetchAndUpsertRange helper Backfill
// uses (never \Seen, never moving the sync cursor) and upserts them,
// then returns the stored copies in that same newest-first order.
func (a *Adapter) Search(ctx context.Context, store core.Store, criteria core.SearchCriteria) ([]core.Item, error) {
	folder := criteria.Folder
	if folder == "" {
		folder = "INBOX"
	}
	limit := criteria.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}

	client, err := a.dial(ctx, a.cfg, a.passwordSource, a.tokenSource, nil)
	if err != nil {
		return nil, fmt.Errorf("mail: search %s: %w", a.cfg.Name, err)
	}
	defer client.Close()

	folders, err := discoverFolders(ctx, client, a.cfg)
	if err != nil {
		return nil, fmt.Errorf("mail: search %s: %w", a.cfg.Name, err)
	}
	mailbox := folders.Resolve(folder)
	// Ids and Meta use the canonical folder (#52), never the friendly
	// name asked for, so "Archive" and "INBOX.Archive" name the same items
	// Run's own sync of that folder stores.
	folder = canonicalFolder(folders, mailbox)

	mbox, err := client.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return nil, fmt.Errorf("mail: search %s: select %s: %w", a.cfg.Name, mailbox, err)
	}

	imapCriteria := &imap.SearchCriteria{Since: criteria.Since, Before: criteria.Before}
	if criteria.From != "" {
		imapCriteria.Header = append(imapCriteria.Header, imap.SearchCriteriaHeaderField{Key: "From", Value: criteria.From})
	}
	if criteria.Subject != "" {
		imapCriteria.Header = append(imapCriteria.Header, imap.SearchCriteriaHeaderField{Key: "Subject", Value: criteria.Subject})
	}

	searchData, err := client.UIDSearch(imapCriteria, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("mail: search %s: %w", a.cfg.Name, err)
	}
	uids := searchData.AllUIDs()
	if len(uids) == 0 {
		return nil, nil
	}

	sort.Slice(uids, func(i, j int) bool { return uids[i] > uids[j] }) // newest (highest UID) first
	if len(uids) > limit {
		uids = uids[:limit]
	}

	var uidSet imap.UIDSet
	uidSet.AddNum(uids...)
	if _, err := a.fetchAndUpsertRange(ctx, client, store, folders, uidSet, mbox.UIDValidity, 0, nil, folder); err != nil {
		return nil, fmt.Errorf("mail: search %s: %w", a.cfg.Name, err)
	}

	items := make([]core.Item, 0, len(uids))
	for _, uid := range uids {
		id := itemID(a.cfg.Name, folder, mbox.UIDValidity, uid)
		item, err := store.Get(ctx, id)
		if err != nil {
			continue // fetchAndUpsertRange just upserted it; a Get failure here would be a store bug, not a search miss
		}
		items = append(items, item)
	}
	return items, nil
}
