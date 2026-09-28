package mail

import (
	"context"
	"fmt"
	"time"

	"github.com/reyer3/bunker-go/internal/core"

	"github.com/emersion/go-imap/v2"
)

var _ core.Backfiller = (*Adapter)(nil)

// Backfill implements core.Backfiller (H2: mail-history). It runs UID
// SEARCH SINCE since on folder, skips any UID the store already has an
// item for, batch-FETCHes headers/flags for the rest through the exact
// same fetchAndUpsertRange helper Run's own sync uses (so it never marks
// anything \Seen, being BODY.PEEK header-only), and upserts them. It
// never touches Run's own sync cursor (fetchAndUpsertRange alone doesn't
// set one; only the cursor-persisting wrapper fetchAndUpsert does), so a
// later Run resumes exactly where it left off regardless of how far back
// this reached. dryRun reports the count and id range without upserting
// anything.
func (a *Adapter) Backfill(ctx context.Context, store core.Store, folder string, since time.Time, dryRun bool) (core.BackfillResult, error) {
	if folder == "" {
		folder = "INBOX"
	}

	client, err := a.dial(ctx, a.cfg, a.passwordSource, a.tokenSource, nil)
	if err != nil {
		return core.BackfillResult{}, fmt.Errorf("mail: backfill %s: %w", a.cfg.Name, err)
	}
	defer client.Close()

	folders, err := discoverFolders(ctx, client, a.cfg)
	if err != nil {
		return core.BackfillResult{}, fmt.Errorf("mail: backfill %s: %w", a.cfg.Name, err)
	}
	mailbox := folders.Resolve(folder)

	mbox, err := client.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return core.BackfillResult{}, fmt.Errorf("mail: backfill %s: select %s: %w", a.cfg.Name, mailbox, err)
	}

	searchData, err := client.UIDSearch(&imap.SearchCriteria{Since: since}, nil).Wait()
	if err != nil {
		return core.BackfillResult{}, fmt.Errorf("mail: backfill %s: search: %w", a.cfg.Name, err)
	}
	uids := searchData.AllUIDs()
	if len(uids) == 0 {
		return core.BackfillResult{}, nil
	}

	var low, high imap.UID
	var missing imap.UIDSet
	for _, uid := range uids {
		if low == 0 || uid < low {
			low = uid
		}
		if uid > high {
			high = uid
		}
		id := itemID(a.cfg.Name, folder, mbox.UIDValidity, uid)
		if _, err := store.Get(ctx, id); err == nil {
			continue // already synced; never re-fetch or overwrite it
		}
		missing.AddNum(uid)
	}

	result := core.BackfillResult{
		FirstID: itemID(a.cfg.Name, folder, mbox.UIDValidity, low),
		LastID:  itemID(a.cfg.Name, folder, mbox.UIDValidity, high),
	}
	missingUIDs, ok := missing.Nums()
	if !ok || len(missingUIDs) == 0 {
		return result, nil
	}
	result.Count = len(missingUIDs)
	if dryRun {
		return result, nil
	}

	if _, err := a.fetchAndUpsertRange(ctx, client, store, folders, missing, mbox.UIDValidity, 0, nil, folder); err != nil {
		return core.BackfillResult{}, fmt.Errorf("mail: backfill %s: %w", a.cfg.Name, err)
	}
	return result, nil
}
