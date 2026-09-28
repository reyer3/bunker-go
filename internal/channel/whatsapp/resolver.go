package whatsapp

import (
	"context"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// storeNameResolver implements NameResolver against a real
// *whatsmeow.Client's device store (contacts, LID mappings) and its
// network-backed GetGroupInfo. NewFromAccount wires one in; tests wire a
// fake instead (see names_test.go).
type storeNameResolver struct {
	cli *whatsmeow.Client
}

func newStoreNameResolver(cli *whatsmeow.Client) *storeNameResolver {
	return &storeNameResolver{cli: cli}
}

func (r *storeNameResolver) ResolvePN(ctx context.Context, lid types.JID) (types.JID, error) {
	if r.cli.Store == nil || r.cli.Store.LIDs == nil {
		return types.JID{}, nil
	}
	return r.cli.Store.LIDs.GetPNForLID(ctx, lid)
}

func (r *storeNameResolver) Contact(ctx context.Context, user types.JID) (types.ContactInfo, error) {
	if r.cli.Store == nil || r.cli.Store.Contacts == nil {
		return types.ContactInfo{}, nil
	}
	return r.cli.Store.Contacts.GetContact(ctx, user)
}

func (r *storeNameResolver) GroupInfo(ctx context.Context, jid types.JID) (*types.GroupInfo, error) {
	return r.cli.GetGroupInfo(ctx, jid)
}

func (r *storeNameResolver) AllContacts(ctx context.Context) (map[types.JID]types.ContactInfo, error) {
	if r.cli.Store == nil || r.cli.Store.Contacts == nil {
		return nil, nil
	}
	return r.cli.Store.Contacts.GetAllContacts(ctx)
}

var (
	_ NameResolver     = (*storeNameResolver)(nil)
	_ ContactDirectory = (*storeNameResolver)(nil)
)
