package mail

import (
	"strings"

	"github.com/emersion/go-imap/v2"
)

// hasSeenFlag reports whether flags include \Seen.
func hasSeenFlag(flags []imap.Flag) bool {
	for _, f := range flags {
		if f == imap.FlagSeen {
			return true
		}
	}
	return false
}

// dovecotLabelsFromFlags extracts Dovecot IMAP keywords — custom flags a
// server with PERMANENTFLAGS \* accepts — from flags. It excludes IMAP
// system flags (leading \, e.g. \Seen, \Answered, \Flagged, \Deleted,
// \Draft, \Recent) and RFC 5788 server-defined keywords (leading $, e.g.
// $Forwarded, $MDNSent, $Junk, $NotJunk), neither of which is a label a
// user set through Organize or a mail client's own tagging UI.
func dovecotLabelsFromFlags(flags []imap.Flag) []string {
	var labels []string
	for _, f := range flags {
		s := string(f)
		if strings.HasPrefix(s, "\\") || strings.HasPrefix(s, "$") {
			continue
		}
		labels = append(labels, s)
	}
	return labels
}
