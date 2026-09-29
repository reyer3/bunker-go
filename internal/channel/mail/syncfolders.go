package mail

import (
	"strings"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// defaultFolderPollInterval is how often Run polls every synced folder
// other than INBOX (#52). Only INBOX is IDLEd: IMAP IDLE watches the one
// selected mailbox, so watching more folders live would take one
// connection per folder, and servers (Gmail especially) cap concurrent
// connections per account. Mail filed into archives and subfolders is
// rarely urgent, while a server-side filter delivering into a subfolder
// should still show up within a couple of minutes, hence the value.
const defaultFolderPollInterval = 2 * time.Minute

// expungeSettle is how long Run waits after a live EXPUNGE in INBOX
// before looking for the message in the other synced folders (#53). A
// webmail moving or deleting many messages at once sends a burst of
// EXPUNGEs; waiting briefly handles the whole burst in one pass over the
// folders instead of one pass per message.
const expungeSettle = 250 * time.Millisecond

// excludedRoles are the special-use attributes whose mailboxes are never
// synced by default: their mail is deleted, spam or unsent, so showing it
// alongside real mail would only be noise. \Flagged marks a virtual view
// (e.g. Gmail's Starred) that only repeats messages kept elsewhere.
var excludedRoles = []string{SpecialUseTrash, SpecialUseJunk, SpecialUseDrafts, "\\Flagged"}

// excludedSpellings are the names servers give those same mailboxes
// when they do not advertise the attribute (e.g. Dovecot's plain
// "INBOX.Junk"), matched with and without the configured prefix.
var excludedSpellings = []string{"Trash", "Junk", "Spam", "Drafts"}

// syncFolder is one mailbox Run keeps in sync besides INBOX.
type syncFolder struct {
	// Mailbox is the server's name, what SELECT takes.
	Mailbox string
	// Folder is the canonical name (canonicalFolder) item ids, Meta and
	// cursors use.
	Folder string
}

// planSyncFolders chooses which mailboxes Run syncs besides INBOX (#52),
// from what LIST reported:
//
//   - Sent is always synced (K2), first.
//   - sync_folders, when set, names the rest explicitly.
//   - Otherwise, on a server with an \All mailbox (Gmail's "All Mail"),
//     just that one: every other Gmail folder is a label view of it, so
//     syncing them too would only store the same messages again.
//   - Otherwise every selectable mailbox except Trash, Junk and Drafts
//     (by attribute or by their usual unadvertised names) and anything
//     under them.
//
// exclude_folders then removes any folder (and its subfolders) from that
// choice. A configured folder the server does not have is logged as an
// error, loudly, and skipped: failing the whole account over it would
// also stop INBOX from syncing.
func (a *Adapter) planSyncFolders(folders *FolderMap) []syncFolder {
	var plan []syncFolder
	seen := make(map[string]bool)
	sent := folders.Resolve("Sent")
	add := func(mailbox string) {
		if seen[mailbox] || strings.EqualFold(mailbox, "INBOX") {
			return
		}
		seen[mailbox] = true
		if mailbox == "Sent" && mailbox != sent {
			// A mailbox literally named "Sent" that is not the account's
			// Sent (\Sent is elsewhere) would get the same canonical
			// folder, and so the same ids, as the real Sent: skip it
			// loudly rather than mix two mailboxes' UIDs.
			a.logger.Error("mail: not syncing a folder named Sent that is not the account's Sent folder",
				"channel", string(core.ChannelMail), "account", a.cfg.Name, "folder", mailbox, "sent", sent)
			return
		}
		plan = append(plan, syncFolder{Mailbox: mailbox, Folder: canonicalFolder(folders, mailbox)})
	}

	if !folders.listed || folders.exists(sent) {
		add(sent)
	}

	var excluded []string
	for _, name := range a.cfg.ExcludeFolders {
		mailbox := folders.Resolve(name)
		if strings.EqualFold(mailbox, "INBOX") {
			// INBOX is the live folder and, on Dovecot-style servers, the
			// parent of every other one: excluding it would silently drop
			// all of them.
			a.logger.Error("mail: exclude_folders cannot exclude INBOX, ignoring it",
				"channel", string(core.ChannelMail), "account", a.cfg.Name, "folder", name)
			continue
		}
		excluded = append(excluded, mailbox)
	}
	isExcluded := func(mailbox string) bool {
		for _, ex := range excluded {
			if mailbox == ex || folders.isUnder(mailbox, ex) {
				return true
			}
		}
		return false
	}

	switch {
	case len(a.cfg.SyncFolders) > 0:
		for _, name := range a.cfg.SyncFolders {
			mailbox, err := folders.ResolveExisting(name)
			if err != nil {
				a.logger.Error("mail: sync_folders names a folder the server does not have, skipping it",
					"channel", string(core.ChannelMail), "account", a.cfg.Name, "folder", name, "error", err)
				continue
			}
			if !isExcluded(mailbox) {
				add(mailbox)
			}
		}
	case folders.specialUse[SpecialUseAll] != "":
		if all := folders.specialUse[SpecialUseAll]; !isExcluded(all) {
			add(all)
		}
	default:
		var junk []string
		for _, mailbox := range folders.order {
			if folders.isJunkish(mailbox) {
				junk = append(junk, mailbox)
			}
		}
		for _, mailbox := range folders.order {
			if isExcluded(mailbox) {
				continue
			}
			skip := false
			for _, j := range junk {
				if mailbox == j || folders.isUnder(mailbox, j) {
					skip = true
					break
				}
			}
			if !skip {
				add(mailbox)
			}
		}
	}
	return plan
}

// isJunkish reports whether mailbox is a Trash, Junk or Drafts folder
// (or a \Flagged view), by attribute or by its usual unadvertised name.
func (m *FolderMap) isJunkish(mailbox string) bool {
	for _, attr := range excludedRoles {
		if m.hasAttr(mailbox, attr) {
			return true
		}
	}
	for _, name := range excludedSpellings {
		if strings.EqualFold(mailbox, name) || strings.EqualFold(mailbox, m.join(name)) {
			return true
		}
	}
	return false
}

// isUnder reports whether mailbox is a descendant of parent in the
// server's hierarchy.
func (m *FolderMap) isUnder(mailbox, parent string) bool {
	return strings.HasPrefix(mailbox, parent+string(m.separator))
}
