package mail

import "strings"

// Special-use attributes as reported by IMAP LIST (RFC 6154), used to key
// FolderMap.specialUse.
const (
	SpecialUseSent   = "\\Sent"
	SpecialUseTrash  = "\\Trash"
	SpecialUseJunk   = "\\Junk"
	SpecialUseDrafts = "\\Drafts"
	SpecialUseAll    = "\\All"
)

// friendlyToSpecialUse maps the folder names bunker-go's CLI and config
// use to the special-use attribute that names the server's real mailbox
// for it, when one exists.
var friendlyToSpecialUse = map[string]string{
	"Sent":   SpecialUseSent,
	"Trash":  SpecialUseTrash,
	"Spam":   SpecialUseJunk,
	"Junk":   SpecialUseJunk,
	"Drafts": SpecialUseDrafts,
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
}

// NewFolderMap builds a FolderMap for a server's hierarchy separator and
// INBOX prefix (pass "" when the server has none, e.g. Gmail).
func NewFolderMap(separator byte, prefix string) *FolderMap {
	return &FolderMap{
		separator:  separator,
		prefix:     prefix,
		specialUse: make(map[string]string),
	}
}

// SetSpecialUse records the real mailbox name a special-use attribute
// (SpecialUseSent, ...) resolves to, as discovered from a LIST response.
func (m *FolderMap) SetSpecialUse(attr, mailbox string) {
	m.specialUse[attr] = mailbox
}

// Resolve returns the server-side mailbox name for a friendly folder
// name. "INBOX" and names already carrying the configured prefix are
// returned unchanged.
func (m *FolderMap) Resolve(friendly string) string {
	if friendly == "INBOX" {
		return friendly
	}
	if attr, ok := friendlyToSpecialUse[friendly]; ok {
		if mailbox, ok := m.specialUse[attr]; ok {
			return mailbox
		}
	}
	if m.prefix == "" {
		return friendly
	}
	prefixWithSep := m.prefix + string(m.separator)
	if strings.HasPrefix(friendly, prefixWithSep) || friendly == m.prefix {
		return friendly
	}
	return prefixWithSep + friendly
}
