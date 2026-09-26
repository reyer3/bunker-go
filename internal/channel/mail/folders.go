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
	for _, entry := range entries {
		for _, attr := range entry.Attrs {
			folders.SetSpecialUse(string(attr), entry.Mailbox)
		}
	}
	return folders, nil
}
