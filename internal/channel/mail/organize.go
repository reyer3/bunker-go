package mail

import (
	"context"
	"errors"
	"fmt"

	"github.com/reyer3/bunker-go/internal/core"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// Organize implements core.Organizer: it can move a message to another
// folder, toggle its \Seen flag, and add/remove labels — IMAP keywords
// on a server that permits custom flags (PERMANENTFLAGS \*), X-GM-LABELS
// on Gmail.
func (a *Adapter) Organize(ctx context.Context, id string, op core.OrganizeOp) error {
	_, err := a.organize(ctx, id, op)
	return err
}

// OrganizeMove implements core.FolderMover: it performs the same
// mutation as Organize, and additionally reports the item's resulting
// address (see core.OrganizeMove) so core.Service can rekey the store
// after a move instead of leaving a stale copy behind in the folder the
// item moved out of — the bug found live on 2026-09-25 (organize --move
// Archives left INBOX still showing the moved mail as unread).
func (a *Adapter) OrganizeMove(ctx context.Context, id string, op core.OrganizeOp) (core.OrganizeMove, error) {
	return a.organize(ctx, id, op)
}

func (a *Adapter) organize(ctx context.Context, id string, op core.OrganizeOp) (core.OrganizeMove, error) {
	account, folder, uidValidity, uid, err := parseItemID(id)
	if err != nil {
		return core.OrganizeMove{}, err
	}
	if account != a.cfg.Name {
		return core.OrganizeMove{}, fmt.Errorf("mail: organize %q: not for account %q: %w", id, a.cfg.Name, core.ErrNotFound)
	}

	client, err := a.dial(ctx, a.cfg, a.passwordSource, a.tokenSource, nil)
	if err != nil {
		return core.OrganizeMove{}, fmt.Errorf("mail: organize %q: %w", id, err)
	}
	defer client.Close()

	folders, err := discoverFolders(ctx, client, a.cfg)
	if err != nil {
		return core.OrganizeMove{}, fmt.Errorf("mail: organize %q: %w", id, err)
	}
	mailbox := folders.Resolve(folder)

	mbox, err := client.Select(mailbox, nil).Wait()
	if err != nil {
		return core.OrganizeMove{}, fmt.Errorf("mail: organize %q: select %s: %w", id, mailbox, err)
	}
	if mbox.UIDValidity != uidValidity {
		return core.OrganizeMove{}, fmt.Errorf("mail: organize %q: mailbox UIDVALIDITY changed: %w", id, core.ErrNotFound)
	}

	if op.Seen != nil {
		if err := storeSeen(client, uid, *op.Seen); err != nil {
			return core.OrganizeMove{}, fmt.Errorf("mail: organize %q: set seen: %w", id, err)
		}
	}

	if len(op.AddLabels) > 0 || len(op.RemoveLabels) > 0 {
		if err := a.storeLabels(ctx, client, mbox, uid, op); err != nil {
			return core.OrganizeMove{}, fmt.Errorf("mail: organize %q: labels: %w", id, err)
		}
	}

	result := core.OrganizeMove{ID: id}
	if op.MoveTo != "" {
		target, err := folders.ResolveExisting(op.MoveTo)
		if err != nil {
			return core.OrganizeMove{}, fmt.Errorf("mail: organize %q: %w", id, err)
		}
		if target == mailbox {
			// The message is already there. A MOVE onto its own mailbox
			// would at best renumber it and, for an item that lives in
			// Gmail's \All being archived into \All, risks a duplicate, so
			// it is a no-op that keeps the id and the stored folder.
			return result, nil
		}
		moveData, err := client.Move(imap.UIDSetNum(uid), target).Wait()
		if err != nil {
			return core.OrganizeMove{}, fmt.Errorf("mail: organize %q: move to %q: %w", id, target, moveError(err))
		}
		result.Folder = target
		if destUID, ok := singleDestUID(moveData); ok {
			result.ID = fmt.Sprintf("mail:%s:%d.%d", account, moveData.UIDValidity, destUID)
		}
		// moveData.UIDValidity/DestUIDs require UIDPLUS or IMAP4rev2; a
		// server without either leaves result.ID as the (now-stale) old
		// id, since there is no way to learn the destination UID.
	}

	return result, nil
}

// singleDestUID extracts the one destination UID a single-message MOVE
// produces from the server's MOVE/COPYUID response.
func singleDestUID(data *imapclient.MoveData) (imap.UID, bool) {
	if data == nil || data.UIDValidity == 0 {
		return 0, false
	}
	uidSet, ok := data.DestUIDs.(imap.UIDSet)
	if !ok {
		return 0, false
	}
	uids, ok := uidSet.Nums()
	if !ok || len(uids) == 0 {
		return 0, false
	}
	return uids[0], true
}

// moveError makes a missing-destination-folder failure legible: bunker-go
// never auto-creates folders (the doc requires MOVE to fail clearly
// instead), so this just adds context to the server's own NO response.
func moveError(err error) error {
	var imapErr *imap.Error
	if errors.As(err, &imapErr) && imapErr.Code == imap.ResponseCodeTryCreate {
		return fmt.Errorf("destination folder does not exist and bunker-go never creates one: %w", err)
	}
	return err
}

func storeSeen(client *imapclient.Client, uid imap.UID, seen bool) error {
	op := imap.StoreFlagsDel
	if seen {
		op = imap.StoreFlagsAdd
	}
	storeFlags := &imap.StoreFlags{Op: op, Silent: true, Flags: []imap.Flag{imap.FlagSeen}}
	return client.Store(imap.UIDSetNum(uid), storeFlags, nil).Close()
}

// storeLabels applies AddLabels/RemoveLabels as Gmail X-GM-LABELS (when
// a.cfg.Gmail) or as IMAP keywords (when the server's SELECT reported
// the \* wildcard in PERMANENTFLAGS, meaning it accepts custom flags).
// Neither capability is universal, so an account without one reports
// core.ErrUnsupported with an explanation instead of silently doing
// nothing.
func (a *Adapter) storeLabels(ctx context.Context, client *imapclient.Client, mbox *imap.SelectData, uid imap.UID, op core.OrganizeOp) error {
	if a.cfg.Gmail {
		return a.storeGmailLabels(ctx, uid, labelOp{add: op.AddLabels, remove: op.RemoveLabels})
	}

	if !hasWildcardFlag(mbox.PermanentFlags) {
		return fmt.Errorf("server does not permit custom keywords (no \\* in PERMANENTFLAGS): %w", core.ErrUnsupported)
	}

	if len(op.AddLabels) > 0 {
		flags := make([]imap.Flag, len(op.AddLabels))
		for i, l := range op.AddLabels {
			flags[i] = imap.Flag(l)
		}
		storeFlags := &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: flags}
		if err := client.Store(imap.UIDSetNum(uid), storeFlags, nil).Close(); err != nil {
			return fmt.Errorf("add keywords: %w", err)
		}
	}
	if len(op.RemoveLabels) > 0 {
		flags := make([]imap.Flag, len(op.RemoveLabels))
		for i, l := range op.RemoveLabels {
			flags[i] = imap.Flag(l)
		}
		storeFlags := &imap.StoreFlags{Op: imap.StoreFlagsDel, Silent: true, Flags: flags}
		if err := client.Store(imap.UIDSetNum(uid), storeFlags, nil).Close(); err != nil {
			return fmt.Errorf("remove keywords: %w", err)
		}
	}
	return nil
}

func hasWildcardFlag(flags []imap.Flag) bool {
	for _, f := range flags {
		if f == imap.FlagWildcard {
			return true
		}
	}
	return false
}
