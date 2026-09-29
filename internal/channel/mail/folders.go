package mail

import (
	"context"
	"fmt"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// discoverFolders LISTs every mailbox to learn the server's real
// hierarchy separator and which mailbox backs each special-use role, and
// returns a FolderMap seeded from that plus cfg's configured prefix.
func discoverFolders(_ context.Context, client *imapclient.Client, cfg AccountConfig) (*FolderMap, error) {
	entries, err := client.List("", "*", &imap.ListOptions{ReturnSpecialUse: true}).Collect()
	if err != nil {
		return nil, fmt.Errorf("mail: list mailboxes: %w", err)
	}

	sep := cfg.FolderSeparator
	for _, entry := range entries {
		if entry.Delim != 0 {
			sep = byte(entry.Delim)
			break
		}
	}

	folders := NewFolderMap(sep, cfg.FolderPrefix)
	var names []string
	for _, entry := range entries {
		if !selectable(entry) {
			// A \Noselect parent (e.g. Gmail's "[Gmail]") cannot hold
			// messages, so it must not satisfy a move's existence check.
			continue
		}
		names = append(names, entry.Mailbox)
		for _, attr := range entry.Attrs {
			folders.SetSpecialUse(string(attr), entry.Mailbox)
		}
	}
	folders.SetMailboxes(names)
	return folders, nil
}

func selectable(entry *imap.ListData) bool {
	for _, attr := range entry.Attrs {
		if attr == imap.MailboxAttrNoSelect || attr == imap.MailboxAttrNonExistent {
			return false
		}
	}
	return true
}
