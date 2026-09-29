package tui

import (
	"strings"

	"github.com/mattn/go-runewidth"
	"github.com/reyer3/bunker-go/internal/config"
	"github.com/reyer3/bunker-go/internal/core"
)

// Mail folder display: a mail item carries its mailbox in
// Meta["folder"] ("INBOX", "Sent", or the server's own name such as
// "INBOX.Archive" or "[Gmail]/All Mail"). Once the / query can reach
// the whole store (issue #62) a row may come from any folder, so rows and
// the thread header name the folder, in the short form a person would
// call it rather than the server's full path.

// folderTagMax caps the folder tag's width on a row: it is context, not
// the row's subject, and must never squeeze the title out.
const folderTagMax = 14

// folderMinTitle is the title width a row keeps before it gives up its
// folder tag, so a narrow pane still reads as a list of conversations.
const folderMinTitle = 12

// folderLayout is one mail account's hierarchy: the INBOX prefix a
// Dovecot-style server puts before every folder and its separator (the
// account's folder_prefix/folder_sep options).
type folderLayout struct {
	prefix string
	sep    byte
}

// gmailFolderNames translates Gmail's special folders, which are English
// whatever the account's language, into the Spanish of the rest of the UI.
var gmailFolderNames = map[string]string{
	"All Mail":  "Todos",
	"Sent Mail": "Enviados",
	"Starred":   "Destacados",
	"Important": "Importantes",
	"Drafts":    "Borradores",
	"Spam":      "Spam",
	"Trash":     "Papelera",
	"Bin":       "Papelera",
}

// folderLayoutsFromConfig reads every mail account's folder_prefix and
// folder_sep, the same options the mail adapter builds its FolderMap
// from, so the TUI strips exactly the prefix the server uses.
func folderLayoutsFromConfig(cfg config.Config) map[string]folderLayout {
	out := map[string]folderLayout{}
	for _, acc := range cfg.Accounts {
		if acc.Channel != string(core.ChannelMail) {
			continue
		}
		var layout folderLayout
		layout.prefix, _ = acc.Options["folder_prefix"].(string)
		if sep, _ := acc.Options["folder_sep"].(string); sep != "" {
			layout.sep = sep[0]
		}
		out[acc.Name] = layout
	}
	return out
}

// folderDisplayName is folder's short name, or "" for INBOX and Sent:
// INBOX is where mail is expected to be, and Sent items already read as
// sent, so naming either would only add noise. The configured prefix
// and separator are stripped ("INBOX.Clientes.Acme" is
// "Clientes/Acme"); without a layout (an account missing from the
// config) the common "INBOX." / "INBOX/" prefix is recognized anyway.
func folderDisplayName(folder string, layout folderLayout) string {
	f := strings.TrimSpace(folder)
	if f == "" || strings.EqualFold(f, "INBOX") || f == "Sent" {
		return ""
	}
	for _, root := range []string{"[Gmail]/", "[Google Mail]/"} {
		if rest, ok := strings.CutPrefix(f, root); ok {
			if name, ok := gmailFolderNames[rest]; ok {
				return name
			}
			return rest
		}
	}
	if layout.prefix == "" && layout.sep == 0 && len(f) > len("INBOX.") && strings.EqualFold(f[:len("INBOX")], "INBOX") {
		if c := f[len("INBOX")]; c == '.' || c == '/' {
			layout = folderLayout{prefix: f[:len("INBOX")], sep: c}
		}
	}
	if layout.prefix != "" && layout.sep != 0 {
		p := layout.prefix + string(layout.sep)
		if len(f) > len(p) && strings.EqualFold(f[:len(p)], p) {
			f = f[len(p):]
		}
	}
	// "/" reads as a path to anyone; a "." hierarchy does not, and a
	// folder name may itself contain dots on a "/" server, so only the
	// account's own separator is rewritten.
	if layout.sep != 0 && layout.sep != '/' {
		f = strings.ReplaceAll(f, string(layout.sep), "/")
	}
	return f
}

// folderTag is the short, sanitized folder label for item, "" when it
// is not mail or is in INBOX/Sent.
func (m Model) folderTag(item core.Item) string {
	if item.Channel != core.ChannelMail {
		return ""
	}
	name := folderDisplayName(item.Meta["folder"], m.folderLayouts[item.Account])
	if name == "" {
		return ""
	}
	return runewidth.Truncate(safeLine(name), folderTagMax, "…")
}
