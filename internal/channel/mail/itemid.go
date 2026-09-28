package mail

import (
	"fmt"
	"strings"

	"github.com/reyer3/bunker-go/internal/core"

	"github.com/emersion/go-imap/v2"
)

// sentIDInfix marks a Sent-folder item id, distinguishing it from an
// INBOX id that happens to share the same uidvalidity/uid (a real
// possibility: UIDVALIDITY is scoped per mailbox, so INBOX and Sent each
// pick their own numbering independently).
const sentIDInfix = "sent."

// itemID builds a core.Item ID for one IMAP message: "mail:<account>:
// <uidvalidity>.<uid>" for folder "INBOX" (unchanged from before K2, so
// every id already persisted in a live database keeps parsing the same
// way), or "mail:<account>:sent.<uidvalidity>.<uid>" for folder "Sent".
func itemID(account, folder string, uidValidity uint32, uid imap.UID) string {
	if folder == "Sent" {
		return fmt.Sprintf("mail:%s:%s%d.%d", account, sentIDInfix, uidValidity, uid)
	}
	return fmt.Sprintf("mail:%s:%d.%d", account, uidValidity, uid)
}

// parseItemID decodes a core.Item.ID produced by itemID back into its
// parts, including which mailbox it came from (folder is "INBOX" or
// "Sent", the same friendly name buildItem stores in Meta["folder"]).
// Any malformed or foreign-channel id is reported as core.ErrNotFound,
// since as far as this adapter is concerned such an id simply isn't one
// of its messages.
func parseItemID(id string) (account, folder string, uidValidity uint32, uid imap.UID, err error) {
	parts := strings.SplitN(id, ":", 3)
	if len(parts) != 3 || parts[0] != string(core.ChannelMail) {
		return "", "", 0, 0, fmt.Errorf("mail: item id %q: %w", id, core.ErrNotFound)
	}
	account = parts[1]
	rest := parts[2]

	folder = "INBOX"
	if strings.HasPrefix(rest, sentIDInfix) {
		folder = "Sent"
		rest = strings.TrimPrefix(rest, sentIDInfix)
	}

	var v, u uint32
	if _, err := fmt.Sscanf(rest, "%d.%d", &v, &u); err != nil {
		return "", "", 0, 0, fmt.Errorf("mail: item id %q: %w", id, core.ErrNotFound)
	}
	return account, folder, v, imap.UID(u), nil
}
