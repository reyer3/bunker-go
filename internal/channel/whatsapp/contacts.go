package whatsapp

import (
	"context"
	"fmt"

	"go.mau.fi/whatsmeow/types"

	"github.com/reyer3/bunker-go/internal/core"
)

// ContactDirectory is the slice of whatsmeow's device store that lists
// every known contact, behind an interface so tests fake it.
type ContactDirectory interface {
	AllContacts(ctx context.Context) (map[types.JID]types.ContactInfo, error)
}

// SetContactDirectory configures where Contacts reads the address book.
// Without one, Contacts reports core.ErrUnsupported and core.Service
// falls back to the conversations it has stored.
func (a *Adapter) SetContactDirectory(d ContactDirectory) { a.directory = d }

var _ core.ContactLister = (*Adapter)(nil)

// Contacts lists the account's WhatsApp contacts: every phone-number JID
// the device store knows a name for. The address is the JID itself, so
// sending needs no IsOnWhatsApp lookup, and it doubles as the 1:1
// thread id (threads are keyed on the phone-number JID, see
// resolveContactName).
func (a *Adapter) Contacts(ctx context.Context) ([]core.Contact, error) {
	if a.directory == nil {
		return nil, fmt.Errorf("whatsapp: contacts: no contact store: %w", core.ErrUnsupported)
	}
	all, err := a.directory.AllContacts(ctx)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: contacts: %w", err)
	}
	out := make([]core.Contact, 0, len(all))
	for jid, info := range all {
		if jid.Server != types.DefaultUserServer {
			continue
		}
		name := firstNonEmpty(info.FullName, info.FirstName, info.PushName, info.BusinessName)
		if name == "" {
			continue
		}
		out = append(out, core.Contact{
			Channel: core.ChannelWhatsApp,
			Account: a.account,
			Name:    name,
			Address: jid.String(),
			Thread:  jid.String(),
		})
	}
	return out, nil
}
