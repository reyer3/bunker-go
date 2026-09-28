package core

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// Contact is someone (or a group or room) a message or a call can be
// addressed to.
type Contact struct {
	Channel Channel `json:"channel"`
	Account string  `json:"account"`
	Name    string  `json:"name"`
	// Address is what send and call take as the recipient: a JID on
	// WhatsApp, a room id on Matrix, an email address on mail.
	Address string `json:"address"`
	// Thread is the existing conversation with this contact, empty when
	// there is none yet.
	Thread string `json:"thread,omitempty"`
}

// ContactLister is an optional capability: adapters with an address book
// of their own (WhatsApp's contact store) implement it. Service.Contacts
// adds everyone the store already has a conversation with, so a channel
// without it still lists the people it talks to.
type ContactLister interface {
	Contacts(ctx context.Context) ([]Contact, error)
}

// ContactFilter narrows Service.Contacts. Query matches the name or the
// address, ignoring case and accents.
type ContactFilter struct {
	Channel Channel `json:"channel,omitempty"`
	Account string  `json:"account,omitempty"`
	Query   string  `json:"query,omitempty"`
	Limit   int     `json:"limit,omitempty"`
}

const (
	defaultContactLimit = 50
	// contactScanLimit bounds how many stored items Contacts scans for
	// conversations: enough to cover every recent correspondent without
	// reading a large store end to end.
	contactScanLimit = 5000
)

// Contacts lists the address books of the adapters that have one, merged
// with the store's conversations, deduplicated by address and ranked by
// how well the name matches filter.Query.
func (s *Service) Contacts(ctx context.Context, filter ContactFilter) ([]Contact, error) {
	byKey := map[string]*Contact{}
	var order []string
	add := func(c Contact) {
		if c.Address == "" {
			return
		}
		key := string(c.Channel) + "\x00" + c.Account + "\x00" + strings.ToLower(c.Address)
		if have, ok := byKey[key]; ok {
			if have.Name == "" {
				have.Name = c.Name
			}
			if have.Thread == "" {
				have.Thread = c.Thread
			}
			return
		}
		byKey[key] = &c
		order = append(order, key)
	}

	for _, adapter := range s.registry.List() {
		if filter.Channel != "" && adapter.Channel() != filter.Channel {
			continue
		}
		if filter.Account != "" && adapter.Account() != filter.Account {
			continue
		}
		lister, ok := adapter.(ContactLister)
		if !ok {
			continue
		}
		list, err := lister.Contacts(ctx)
		if err != nil {
			if errors.Is(err, ErrUnsupported) {
				continue
			}
			return nil, fmt.Errorf("core: contacts %s/%s: %w", adapter.Channel(), adapter.Account(), err)
		}
		for _, c := range list {
			c.Channel, c.Account = adapter.Channel(), adapter.Account()
			add(c)
		}
	}

	items, err := s.store.List(ctx, Filter{Channel: filter.Channel, Account: filter.Account, Limit: contactScanLimit})
	if err != nil {
		return nil, fmt.Errorf("core: contacts: store list: %w", err)
	}
	for _, it := range items {
		if c, ok := conversationContact(it); ok {
			add(c)
		}
	}

	query := foldContact(filter.Query)
	type ranked struct {
		c    Contact
		rank int
	}
	var out []ranked
	for _, key := range order {
		c := *byKey[key]
		rank := contactRank(c, query)
		if rank < 0 {
			continue
		}
		out = append(out, ranked{c, rank})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].rank != out[j].rank {
			return out[i].rank < out[j].rank
		}
		return foldContact(out[i].c.Name) < foldContact(out[j].c.Name)
	})
	limit := filter.Limit
	if limit <= 0 {
		limit = defaultContactLimit
	}
	contacts := make([]Contact, 0, min(limit, len(out)))
	for i := 0; i < len(out) && i < limit; i++ {
		contacts = append(contacts, out[i].c)
	}
	return contacts, nil
}

// conversationContact derives a contact from a stored item: the chat or
// room for chat channels, the sender for mail. Mail the account itself
// sent says nothing about who the correspondent is, so it is skipped.
func conversationContact(it Item) (Contact, bool) {
	switch it.Channel {
	case ChannelMail:
		if it.FromMe || it.From.ID == "" {
			return Contact{}, false
		}
		return Contact{Channel: it.Channel, Account: it.Account, Name: it.From.Name, Address: it.From.ID}, true
	default:
		if it.Thread == "" {
			return Contact{}, false
		}
		name := it.ThreadName
		if name == "" && !it.FromMe {
			name = it.From.Name
		}
		return Contact{Channel: it.Channel, Account: it.Account, Name: name, Address: it.Thread, Thread: it.Thread}, true
	}
}

// contactRank orders matches: 0 exact name, 1 name prefix, 2 word
// prefix, 3 anywhere in the name or address. -1 means no match. An empty
// query matches everyone equally.
func contactRank(c Contact, query string) int {
	if query == "" {
		return 0
	}
	name := foldContact(c.Name)
	switch {
	case name == query:
		return 0
	case strings.HasPrefix(name, query):
		return 1
	case strings.Contains(" "+name, " "+query):
		return 2
	case strings.Contains(name, query) || strings.Contains(foldContact(c.Address), query):
		return 3
	}
	return -1
}

// ResolveContact picks the one contact name refers to among candidates:
// an exact name match when there is exactly one, else the only partial
// match. None, or several, is an error naming the candidates, so a
// message or call never goes to a guessed recipient.
func ResolveContact(candidates []Contact, name string) (Contact, error) {
	query := foldContact(name)
	var exact, partial []Contact
	for _, c := range candidates {
		switch contactRank(c, query) {
		case 0:
			exact = append(exact, c)
		case 1, 2, 3:
			partial = append(partial, c)
		}
	}
	switch {
	case len(exact) == 1:
		return exact[0], nil
	case len(exact) > 1:
		return Contact{}, ambiguousContact(name, exact)
	case len(partial) == 1:
		return partial[0], nil
	case len(partial) > 1:
		return Contact{}, ambiguousContact(name, partial)
	}
	return Contact{}, fmt.Errorf("core: no contact matches %q", name)
}

func ambiguousContact(name string, matches []Contact) error {
	const shown = 5
	var b strings.Builder
	for i, c := range matches {
		if i == shown {
			fmt.Fprintf(&b, "; and %d more", len(matches)-shown)
			break
		}
		if i > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "%s <%s>", c.Name, c.Address)
	}
	return fmt.Errorf("core: %q matches %d contacts, use one of their addresses: %s", name, len(matches), b.String())
}

// foldContact lowercases s and strips the accents Spanish names carry,
// so "jose" finds "José".
func foldContact(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch r {
		case 'á', 'à', 'ä', 'â':
			r = 'a'
		case 'é', 'è', 'ë', 'ê':
			r = 'e'
		case 'í', 'ì', 'ï', 'î':
			r = 'i'
		case 'ó', 'ò', 'ö', 'ô':
			r = 'o'
		case 'ú', 'ù', 'ü', 'û':
			r = 'u'
		case 'ñ':
			r = 'n'
		case 'ç':
			r = 'c'
		}
		if unicode.IsSpace(r) {
			r = ' '
		}
		b.WriteRune(r)
	}
	return b.String()
}

// FoldSearch normalizes text for matching the way contacts do: lower
// case, Spanish accents stripped, whitespace collapsed to spaces. The TUI
// inbox filter uses it so "jose" finds "José" there too.
func FoldSearch(s string) string { return foldContact(s) }
