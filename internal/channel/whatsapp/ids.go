// Package whatsapp implements the bunker-go WhatsApp channel adapter on
// top of go.mau.fi/whatsmeow, behind a narrow waClient interface so every
// behavior is testable with a fake. No code path in this package dials
// WhatsApp's servers from a test.
package whatsapp

import (
	"fmt"
	"strings"
)

const idPrefix = "whatsapp:"

// itemID builds a core.Item ID for a message: "whatsapp:<account>:<chatJID>/<msgID>".
func itemID(account, chatJID, msgID string) string {
	return fmt.Sprintf("%s%s:%s/%s", idPrefix, account, chatJID, msgID)
}

// parseItemID splits an item id produced by itemID back into its parts.
// The chat JID itself may legitimately contain no slash, so the message id
// is always the segment after the LAST slash.
func parseItemID(id string) (account, chatJID, msgID string, err error) {
	if !strings.HasPrefix(id, idPrefix) {
		return "", "", "", fmt.Errorf("whatsapp: id %q: missing %q prefix", id, idPrefix)
	}
	rest := strings.TrimPrefix(id, idPrefix)

	accountSep := strings.Index(rest, ":")
	if accountSep < 0 {
		return "", "", "", fmt.Errorf("whatsapp: id %q: missing account separator", id)
	}
	account = rest[:accountSep]
	rest = rest[accountSep+1:]

	msgSep := strings.LastIndex(rest, "/")
	if msgSep < 0 || msgSep == len(rest)-1 {
		return "", "", "", fmt.Errorf("whatsapp: id %q: missing message id", id)
	}
	chatJID = rest[:msgSep]
	msgID = rest[msgSep+1:]
	if chatJID == "" || msgID == "" {
		return "", "", "", fmt.Errorf("whatsapp: id %q: empty chat or message id", id)
	}
	return account, chatJID, msgID, nil
}
