package mail

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/reyer3/bunker-go/internal/core"

	"github.com/emersion/go-imap/v2"
)

// sentIDInfix marks a Sent-folder item id, distinguishing it from an
// INBOX id that happens to share the same uidvalidity/uid (a real
// possibility: UIDVALIDITY is scoped per mailbox, so INBOX and Sent each
// pick their own numbering independently).
const sentIDInfix = "sent."

// mailboxIDSep separates an escaped mailbox name from "<uidvalidity>.
// <uid>" in the id of an item that lives in any folder other than INBOX
// or Sent (#52). escapeMailbox always escapes it inside the name, so the
// last one in an id is the separator, whatever the mailbox is called.
const mailboxIDSep = "/"

// itemID builds a core.Item ID for one IMAP message. folder is the
// canonical folder name (see canonicalFolder): "INBOX", "Sent", or the
// server's own name for any other mailbox.
//
//   - INBOX: "mail:<account>:<uidvalidity>.<uid>", unchanged from before
//     K2, so every id already persisted in a live database keeps parsing
//     the same way.
//   - Sent: "mail:<account>:sent.<uidvalidity>.<uid>" (K2), also kept.
//   - any other mailbox: "mail:<account>:<escaped mailbox>/<uidvalidity>.
//     <uid>" (#52), e.g. "mail:cl:INBOX.Archive/1700000000.12". The
//     mailbox must be in the id, not only in Meta: read, mark, move and
//     download get just the id and must SELECT the mailbox it lives in,
//     and a UID is only unique within one mailbox.
func itemID(account, folder string, uidValidity uint32, uid imap.UID) string {
	switch folder {
	case "INBOX":
		return fmt.Sprintf("mail:%s:%d.%d", account, uidValidity, uid)
	case "Sent":
		return fmt.Sprintf("mail:%s:%s%d.%d", account, sentIDInfix, uidValidity, uid)
	}
	return fmt.Sprintf("mail:%s:%s%s%d.%d", account, escapeMailbox(folder), mailboxIDSep, uidValidity, uid)
}

// escapeMailbox makes a mailbox name safe inside an item id: path
// escaping turns '/' (Gmail's separator, and mailboxIDSep), spaces and
// brackets into %XX, and ':' is escaped too so nothing after the account
// can be mistaken for another "<channel>:<account>:" field. Dovecot-style
// names ("INBOX.Clients.Acme") stay readable.
func escapeMailbox(mailbox string) string {
	return strings.ReplaceAll(url.PathEscape(mailbox), ":", "%3A")
}

// parseItemID decodes a core.Item.ID produced by itemID back into its
// parts, including which folder it came from: "INBOX", "Sent" (the same
// canonical names buildItem stores in Meta["folder"]) or the server's own
// mailbox name for any other folder. Any malformed or foreign-channel id
// is reported as core.ErrNotFound, since as far as this adapter is
// concerned such an id simply isn't one of its messages.
func parseItemID(id string) (account, folder string, uidValidity uint32, uid imap.UID, err error) {
	notFound := fmt.Errorf("mail: item id %q: %w", id, core.ErrNotFound)
	parts := strings.SplitN(id, ":", 3)
	if len(parts) != 3 || parts[0] != string(core.ChannelMail) {
		return "", "", 0, 0, notFound
	}
	account = parts[1]
	rest := parts[2]

	folder = "INBOX"
	switch {
	case strings.Contains(rest, mailboxIDSep):
		i := strings.LastIndex(rest, mailboxIDSep)
		mailbox, uerr := url.PathUnescape(rest[:i])
		if uerr != nil || mailbox == "" {
			return "", "", 0, 0, notFound
		}
		folder = mailbox
		rest = rest[i+len(mailboxIDSep):]
	case strings.HasPrefix(rest, sentIDInfix):
		folder = "Sent"
		rest = strings.TrimPrefix(rest, sentIDInfix)
	}

	v, u, ok := parseValidityUID(rest)
	if !ok {
		return "", "", 0, 0, notFound
	}
	return account, folder, v, imap.UID(u), nil
}

// parseValidityUID parses exactly "<uidvalidity>.<uid>", rejecting the
// trailing text fmt.Sscanf alone would silently ignore.
func parseValidityUID(s string) (uint32, uint32, bool) {
	var v, u uint32
	var tail string
	if n, _ := fmt.Sscanf(s, "%d.%d%s", &v, &u, &tail); n != 2 {
		return 0, 0, false
	}
	return v, u, true
}

// canonicalFolder maps a server mailbox name to the folder name item ids
// and Meta["folder"] use: "INBOX" for INBOX (any case, RFC 3501 5.1),
// "Sent" for the account's Sent mailbox (so K2's ids stay stable), and
// the mailbox's own name for anything else.
func canonicalFolder(folders *FolderMap, mailbox string) string {
	if strings.EqualFold(mailbox, "INBOX") {
		return "INBOX"
	}
	if mailbox == folders.Resolve("Sent") {
		return "Sent"
	}
	return mailbox
}

// mailboxFor is canonicalFolder's inverse: the server mailbox to SELECT
// for a folder parsed out of an item id.
func mailboxFor(folders *FolderMap, folder string) string {
	switch folder {
	case "INBOX":
		return "INBOX"
	case "Sent":
		return folders.Resolve("Sent")
	}
	return folder
}
