package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/reyer3/bunker-go/internal/core"
)

// contactsUsage is shown on a malformed contacts command.
const contactsUsage = "usage: bunker contacts [query] [--channel c] [--account a] [--limit n] [--json]"

// cmdContacts lists contacts matching an optional query.
func cmdContacts(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("contacts", stderr)
	channel := fs.String("channel", "", "only this channel")
	account := fs.String("account", "", "only this account")
	limit := fs.Int("limit", 0, "at most this many (default 50)")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) > 1 {
		fmt.Fprintln(stderr, contactsUsage)
		return 2
	}
	filter := core.ContactFilter{Channel: core.Channel(*channel), Account: *account, Limit: *limit}
	if len(positionals) == 1 {
		filter.Query = positionals[0]
	}
	contacts, err := backend.Contacts(ctx, filter)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	if *jsonOut {
		if contacts == nil {
			contacts = []core.Contact{}
		}
		writeJSON(stdout, map[string]any{"contacts": contacts})
		return 0
	}
	if len(contacts) == 0 {
		fmt.Fprintln(stdout, "no contacts found")
		return 0
	}
	for _, c := range contacts {
		fmt.Fprintf(stdout, "%s/%s\t%s\t%s\n", c.Channel, c.Account, c.Name, c.Address)
	}
	return 0
}

// looksLikeName reports whether a recipient is a contact name rather
// than an address: it has a letter, and none of the marks every address
// form carries ("@" in emails, JIDs and Matrix user ids; a leading "!" or
// "#" in Matrix room ids and aliases). Phone numbers have no letters.
func looksLikeName(to string) bool {
	to = strings.TrimSpace(to)
	if to == "" || strings.Contains(to, "@") || strings.HasPrefix(to, "!") || strings.HasPrefix(to, "#") {
		return false
	}
	return strings.IndexFunc(to, unicode.IsLetter) >= 0
}

// resolveRecipient turns a contact name into that contact's address on
// channel/account, and passes an address through unchanged. A name that
// matches no contact, or several, is an error listing the candidates:
// a message or call never goes to a guessed recipient. It only reads the
// daemon's contacts, so it is safe under --dry-run.
func resolveRecipient(ctx context.Context, backend Backend, channel core.Channel, account, to string, stderr io.Writer, quiet bool) (string, error) {
	if !looksLikeName(to) {
		return to, nil
	}
	candidates, err := backend.Contacts(ctx, core.ContactFilter{Channel: channel, Account: account, Query: to, Limit: 100})
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", to, err)
	}
	c, err := core.ResolveContact(candidates, to)
	if err != nil {
		return "", err
	}
	if !quiet {
		fmt.Fprintf(stderr, "%s → %s <%s>\n", to, c.Name, c.Address)
	}
	return c.Address, nil
}
