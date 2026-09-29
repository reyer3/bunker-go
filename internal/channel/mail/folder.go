package mail

import (
	"fmt"
	"strings"
)

// Special-use attributes as reported by IMAP LIST (RFC 6154), used to key
// FolderMap.specialUse.
const (
	SpecialUseSent    = "\\Sent"
	SpecialUseTrash   = "\\Trash"
	SpecialUseJunk    = "\\Junk"
	SpecialUseDrafts  = "\\Drafts"
	SpecialUseAll     = "\\All"
	SpecialUseArchive = "\\Archive"
)

// friendlyToSpecialUse maps the folder names bunker-go's CLI and config
// use to the special-use attribute that names the server's real mailbox
// for it, when one exists.
var friendlyToSpecialUse = map[string]string{
	"Sent":     SpecialUseSent,
	"Trash":    SpecialUseTrash,
	"Spam":     SpecialUseJunk,
	"Junk":     SpecialUseJunk,
	"Drafts":   SpecialUseDrafts,
	"Archive":  SpecialUseArchive,
	"Archives": SpecialUseArchive,
}

// unadvertisedSpellings lists, per special-use role, the names servers
// commonly give that mailbox when they do not advertise its attribute.
// Dovecot/webmail installs use either spelling ("Archive"/"Archives",
// "Junk"/"Spam"), so asking for one must still find the other.
var unadvertisedSpellings = map[string][]string{
	SpecialUseArchive: {"Archive", "Archives"},
	SpecialUseJunk:    {"Junk", "Spam"},
}

// FolderMap resolves a friendly folder name ("Archives", "Sent") to the
// mailbox path a server actually understands, honoring that server's
// hierarchy separator, its INBOX prefix (Dovecot's "INBOX." convention)
// and any special-use mailboxes discovered via LIST. It never invents a
// special-use mailbox: SetSpecialUse must be called with what the server
// reported, or Resolve falls back to prefix-joining the friendly name.
type FolderMap struct {
	separator  byte
	prefix     string
	specialUse map[string]string // attribute -> actual mailbox name

	// mailboxes holds every selectable mailbox LIST reported, once
	// SetMailboxes has been called (listed). Without a listing the map
	// can only guess, so existence checks are skipped rather than failing
	// every name.
	mailboxes map[string]struct{}
	listed    bool
}

// NewFolderMap builds a FolderMap for a server's hierarchy separator and
// INBOX prefix (pass "" when the server has none, e.g. Gmail).
func NewFolderMap(separator byte, prefix string) *FolderMap {
	return &FolderMap{
		separator:  separator,
		prefix:     prefix,
		specialUse: make(map[string]string),
		mailboxes:  make(map[string]struct{}),
	}
}

// SetSpecialUse records the real mailbox name a special-use attribute
// (SpecialUseSent, ...) resolves to, as discovered from a LIST response.
func (m *FolderMap) SetSpecialUse(attr, mailbox string) {
	m.specialUse[attr] = mailbox
}

// SetMailboxes records the full set of mailboxes the server reported via
// LIST, so Resolve can prefer a folder that exists over a guess and
// ResolveExisting can refuse one that does not.
func (m *FolderMap) SetMailboxes(names []string) {
	m.mailboxes = make(map[string]struct{}, len(names))
	for _, name := range names {
		m.mailboxes[name] = struct{}{}
	}
	m.listed = true
}

// Resolve returns the server-side mailbox name for a friendly folder
// name. "INBOX" and names already carrying the configured prefix are
// returned unchanged.
func (m *FolderMap) Resolve(friendly string) string {
	if friendly == "INBOX" {
		return friendly
	}
	attr, hasAttr := friendlyToSpecialUse[friendly]
	if hasAttr {
		if mailbox, ok := m.specialUse[attr]; ok {
			return mailbox
		}
	}
	// Many Dovecot/webmail servers keep an archive or junk folder
	// without advertising its attribute, under either spelling; a listed
	// one beats a prefix-joined guess that may not exist.
	if spellings, ok := unadvertisedSpellings[attr]; ok && hasAttr && m.listed {
		if mailbox, ok := m.findListed(friendly, spellings); ok {
			return mailbox
		}
	}
	return m.join(friendly)
}

// ResolveExisting is Resolve for a destination the caller is about to
// write into (a MOVE target): when the server's mailbox list is known and
// the resolved name is not on it, it fails up front with a clear error
// instead of a bare server NO. bunker-go never creates folders.
func (m *FolderMap) ResolveExisting(friendly string) (string, error) {
	mailbox := m.Resolve(friendly)
	if !m.listed || m.exists(mailbox) {
		return mailbox, nil
	}
	return "", fmt.Errorf("mail: folder %q (%s) does not exist on the server and bunker-go never creates one", friendly, mailbox)
}

// findListed looks for an existing mailbox under any of a role's
// spellings, trying the requested one first and each spelling with the
// configured prefix before without it (the prefixed form is where a
// Dovecot-style server keeps it).
func (m *FolderMap) findListed(friendly string, alternatives []string) (string, bool) {
	spellings := []string{friendly}
	for _, s := range alternatives {
		if s != friendly {
			spellings = append(spellings, s)
		}
	}
	for _, s := range spellings {
		for _, candidate := range []string{m.join(s), s} {
			if m.exists(candidate) {
				return candidate, true
			}
		}
	}
	return "", false
}

func (m *FolderMap) exists(mailbox string) bool {
	// INBOX is case-insensitive (RFC 3501 section 5.1) and always exists.
	if strings.EqualFold(mailbox, "INBOX") {
		return true
	}
	_, ok := m.mailboxes[mailbox]
	return ok
}

// join prefixes a friendly name with the configured INBOX prefix and
// separator, leaving names that already carry it alone.
func (m *FolderMap) join(friendly string) string {
	if m.prefix == "" {
		return friendly
	}
	prefixWithSep := m.prefix + string(m.separator)
	if strings.HasPrefix(friendly, prefixWithSep) || friendly == m.prefix {
		return friendly
	}
	return prefixWithSep + friendly
}
