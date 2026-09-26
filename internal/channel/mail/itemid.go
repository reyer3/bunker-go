package mail

import (
	"fmt"
	"strings"

	"github.com/reyer3/bunker-go/internal/core"

	"github.com/emersion/go-imap/v2"
)

// parseItemID decodes a core.Item.ID produced by this package
// ("mail:<account>:<uidvalidity>.<uid>") back into its parts. Any
// malformed or foreign-channel id is reported as core.ErrNotFound, since
// as far as this adapter is concerned such an id simply isn't one of
// its messages.
func parseItemID(id string) (account string, uidValidity uint32, uid imap.UID, err error) {
	parts := strings.SplitN(id, ":", 3)
	if len(parts) != 3 || parts[0] != string(core.ChannelMail) {
		return "", 0, 0, fmt.Errorf("mail: item id %q: %w", id, core.ErrNotFound)
	}
	account = parts[1]

	var v, u uint32
	if _, err := fmt.Sscanf(parts[2], "%d.%d", &v, &u); err != nil {
		return "", 0, 0, fmt.Errorf("mail: item id %q: %w", id, core.ErrNotFound)
	}
	return account, v, imap.UID(u), nil
}
