package tui

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"

	"github.com/reyer3/bunker-go/internal/core"
)

// "bunker compose" (deploy/herdr's mailto link handler) starts the TUI on
// the new mail editor, prefilled from a mailto: URL, and quits when the
// editor closes: the pane or popup was opened for that one message.

// maxMailtoLen caps a mailto URL. It arrives from a clicked link and is
// passed through herdr's command line, so an absurd size is refused
// rather than carried around.
const maxMailtoLen = 8192

// MailDraft is a new message prefilled from a mailto: URL. To and Cc are
// comma-separated address lists, as the editor's fields hold them.
type MailDraft struct {
	To      string
	Cc      string
	Subject string
	Body    string
}

// ParseMailto parses a mailto: URL (RFC 6068): the addresses before "?"
// plus the to, cc, subject and body header fields. Values are
// percent-decoded and "+" stays literal. Body line breaks (%0D%0A) become
// "\n". Other header fields are ignored, except bcc: the editor has no
// Bcc field, and a recipient bunker cannot honor is an error rather than
// silently dropped. A control character anywhere but the body is refused,
// so a crafted link cannot inject header lines.
func ParseMailto(raw string) (MailDraft, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) < len("mailto:") || !strings.EqualFold(raw[:len("mailto:")], "mailto:") {
		return MailDraft{}, fmt.Errorf("tui: %q is not a mailto: URL", raw)
	}
	if len(raw) > maxMailtoLen {
		return MailDraft{}, fmt.Errorf("tui: mailto URL too long (%d bytes)", len(raw))
	}
	rest := raw[len("mailto:"):]
	addrPart, query, _ := strings.Cut(rest, "?")

	var to, cc []string
	var d MailDraft
	var haveSubject, haveBody bool
	addTo := func(list *[]string, field, value string) error {
		for _, a := range strings.Split(value, ",") {
			a = strings.TrimSpace(a)
			if a == "" {
				continue
			}
			if err := checkHeaderValue(field, a); err != nil {
				return err
			}
			*list = append(*list, a)
		}
		return nil
	}

	addrs, err := url.PathUnescape(addrPart)
	if err != nil {
		return MailDraft{}, fmt.Errorf("tui: mailto: invalid address %q: %w", addrPart, err)
	}
	if err := addTo(&to, "to", addrs); err != nil {
		return MailDraft{}, err
	}
	if query != "" {
		for _, pair := range strings.Split(query, "&") {
			if pair == "" {
				continue
			}
			rawName, rawValue, _ := strings.Cut(pair, "=")
			name, err := url.PathUnescape(rawName)
			if err != nil {
				return MailDraft{}, fmt.Errorf("tui: mailto: invalid field name %q: %w", rawName, err)
			}
			value, err := url.PathUnescape(rawValue)
			if err != nil {
				return MailDraft{}, fmt.Errorf("tui: mailto: invalid %s value: %w", name, err)
			}
			switch strings.ToLower(name) {
			case "to":
				err = addTo(&to, "to", value)
			case "cc":
				err = addTo(&cc, "cc", value)
			case "bcc":
				if strings.TrimSpace(value) != "" {
					err = errors.New("tui: mailto: bcc recipients are not supported by the editor")
				}
			case "subject":
				if !haveSubject {
					haveSubject = true
					d.Subject = value
					err = checkHeaderValue("subject", value)
				}
			case "body":
				if !haveBody {
					haveBody = true
					d.Body = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
				}
			}
			if err != nil {
				return MailDraft{}, err
			}
		}
	}
	d.To = strings.Join(to, ", ")
	d.Cc = strings.Join(cc, ", ")
	return d, nil
}

// checkHeaderValue refuses control characters in a header-bound value.
func checkHeaderValue(field, value string) error {
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("tui: mailto: %s contains a control character", field)
		}
	}
	return nil
}

// composeDraft is the message a "bunker compose" Model starts on.
type composeDraft struct {
	account string
	draft   MailDraft
}

// WithMailDraft starts on the new mail editor for account, prefilled with
// d, and makes closing the editor (Esc, or a successful send) quit.
func WithMailDraft(account string, d MailDraft) Option {
	return func(m *Model) { m.compose = &composeDraft{account: account, draft: d} }
}

// composeOnly reports whether this Model is a "bunker compose" pane.
func (m Model) composeOnly() bool { return m.compose != nil }

// startMailDraft opens the editor on the start draft. Run calls it once
// the model has its real renderer, so the editor's cursor style binds to
// the terminal's; it is a no-op without WithMailDraft.
func (m Model) startMailDraft() Model {
	if m.compose == nil {
		return m
	}
	d := m.compose.draft
	m = m.openNewMail(core.Contact{Channel: core.ChannelMail, Account: m.compose.account, Address: d.To})
	m.mailCc.SetValue(d.Cc)
	m.mailSubject.SetValue(d.Subject)
	m.composer.SetValue(d.Body)
	switch {
	case d.To == "":
		m.mailFocus = 0
	case d.Subject == "":
		m.mailFocus = 2
	default:
		m.mailFocus = 3
	}
	return m.withMailFocusApplied()
}
