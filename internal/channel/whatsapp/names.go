package whatsapp

import (
	"context"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/reyer3/bunker-go/internal/core"
)

// groupNameCacheTTL bounds how long a group's resolved name is reused
// before GetGroupInfo is called again. WhatsApp group names rarely
// change, and GetGroupInfo is a network round trip; ten minutes keeps a
// burst of messages in an active group from re-fetching it on every one.
// Adjust this constant to change the cache lifetime.
const groupNameCacheTTL = 10 * time.Minute

// NameResolver is the narrow slice of whatsmeow's device store and
// client this package needs to turn a JID into a display name, behind an
// interface so every adapter test can fake it instead of driving a real
// whatsmeow.Client. A nil NameResolver on Adapter is valid: name
// resolution then falls back to the push name or bare JID user carried
// on the event itself.
type NameResolver interface {
	// ResolvePN returns the phone-number JID mapped to a @lid JID, or a
	// zero types.JID with a nil error when no mapping is known yet.
	ResolvePN(ctx context.Context, lid types.JID) (types.JID, error)
	// Contact returns the locally known contact info for user. A user
	// bunker-go has never seen returns a zero types.ContactInfo and a
	// nil error, not an error.
	Contact(ctx context.Context, user types.JID) (types.ContactInfo, error)
	// GroupInfo fetches a group's metadata, including its name, over the
	// network. Callers should cache the result; this package does, via
	// Adapter.groupName.
	GroupInfo(ctx context.Context, jid types.JID) (*types.GroupInfo, error)
}

// resolveContactName picks the JID a 1:1 thread should be keyed on and
// the name it (and its sender) should display.
//
// When jid is a @lid and the store already has its phone-number mapping,
// the returned JID is the PN, never the LID: WhatsApp routinely delivers
// events for the same conversation addressed by either JID form (LID
// first, historically PN, sometimes both across a session), and keying
// Item.Thread on whichever form happened to arrive would silently split
// one conversation into two threads. The PN is also the more stable,
// human-recognizable identity of the two.
//
// The name itself prefers, in order, a name Alice saved for the
// contact (FullName, then FirstName) over one WhatsApp only observed
// (PushName, then BusinessName) - a saved name reflects a deliberate
// choice, a push name is whatever the other side typed into their own
// profile. With no resolver, no mapping or no contact at all, it falls
// back to the event's own PushName, then the bare JID user part.
//
// A device JID (number:device@server, as a call offer or a sender carries
// it) is reduced to the person first: contacts and LID mappings are kept
// per person, so a device lookup would always miss and fall back to the
// number.
func (a *Adapter) resolveContactName(ctx context.Context, jid types.JID, pushName string) (types.JID, string) {
	jid = jid.ToNonAD()
	resolved := jid
	if a.names != nil {
		lookup := jid
		if jid.Server == types.HiddenUserServer {
			if pn, err := a.names.ResolvePN(ctx, jid); err == nil && !pn.IsEmpty() {
				resolved = pn
				lookup = pn
			}
		}
		if contact, err := a.names.Contact(ctx, lookup); err == nil {
			if name := firstNonEmpty(contact.FullName, contact.FirstName, contact.PushName, contact.BusinessName); name != "" {
				return resolved, name
			}
		}
	}
	return resolved, fallbackName(resolved, pushName)
}

// groupName resolves jid's group name via the configured NameResolver,
// caching the result for groupNameCacheTTL so a burst of messages from
// one group does not re-fetch GetGroupInfo on every message. It returns
// "" when there is no resolver, or the lookup failed or returned no name.
func (a *Adapter) groupName(ctx context.Context, jid types.JID) string {
	if a.names == nil {
		return ""
	}
	key := jid.String()

	a.mu.Lock()
	if entry, ok := a.groupNames[key]; ok && time.Now().Before(entry.expires) {
		a.mu.Unlock()
		return entry.name
	}
	a.mu.Unlock()

	name := ""
	if info, err := a.names.GroupInfo(ctx, jid); err == nil && info != nil {
		name = info.Name
	}

	a.mu.Lock()
	if a.groupNames == nil {
		a.groupNames = make(map[string]groupNameCacheEntry)
	}
	a.groupNames[key] = groupNameCacheEntry{name: name, expires: time.Now().Add(groupNameCacheTTL)}
	a.mu.Unlock()

	return name
}

type groupNameCacheEntry struct {
	name    string
	expires time.Time
}

// fallbackName is the last resort when no resolver, mapping or contact
// yields a name: the event's own push name, then the bare phone
// number/JID user part, per the T7 spec's fallback chain.
func fallbackName(jid types.JID, pushName string) string {
	if pushName != "" {
		return pushName
	}
	if jid.User != "" {
		return jid.User
	}
	return jid.String()
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// enrichItem applies name resolution to item: it sets ThreadName and
// From.Name, and, for a 1:1 chat whose thread identity moves from a LID
// to its mapped PN (see resolveContactName), rewrites Thread and ID to
// the stable PN form. chat and sender come from the *events.Message (or
// the event ParseWebMessage produced from history sync) item was built
// from; msgID and pushName likewise.
func (a *Adapter) enrichItem(ctx context.Context, item core.Item, chat, sender types.JID, msgID, pushName string) core.Item {
	var (
		threadJID  = chat
		threadName string
	)
	if chat.Server == types.GroupServer {
		threadName = a.groupName(ctx, chat)
		if threadName == "" {
			threadName = fallbackName(chat, pushName)
		}
	} else {
		threadJID, threadName = a.resolveContactName(ctx, chat, pushName)
	}
	_, senderName := a.resolveContactName(ctx, sender, pushName)

	item.ThreadName = threadName
	item.From.Name = senderName
	if threadJID != chat {
		item.Thread = threadJID.String()
		item.ID = itemID(a.account, threadJID.String(), msgID)
	}
	return item
}
