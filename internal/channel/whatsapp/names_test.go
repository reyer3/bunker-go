package whatsapp

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// fakeNameResolver is a NameResolver used by every name-resolution test
// in this package. It never touches a real whatsmeow store.
type fakeNameResolver struct {
	mu sync.Mutex

	pnForLID   map[types.JID]types.JID
	contacts   map[types.JID]types.ContactInfo
	groupInfo  map[types.JID]*types.GroupInfo
	groupCalls map[types.JID]int
}

func newFakeNameResolver() *fakeNameResolver {
	return &fakeNameResolver{
		pnForLID:   make(map[types.JID]types.JID),
		contacts:   make(map[types.JID]types.ContactInfo),
		groupInfo:  make(map[types.JID]*types.GroupInfo),
		groupCalls: make(map[types.JID]int),
	}
}

func (f *fakeNameResolver) ResolvePN(_ context.Context, lid types.JID) (types.JID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pnForLID[lid], nil
}

func (f *fakeNameResolver) Contact(_ context.Context, user types.JID) (types.ContactInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.contacts[user], nil
}

func (f *fakeNameResolver) GroupInfo(_ context.Context, jid types.JID) (*types.GroupInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.groupCalls[jid]++
	return f.groupInfo[jid], nil
}

func (f *fakeNameResolver) callsFor(jid types.JID) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.groupCalls[jid]
}

func emitTextMessage(cli *fakeWAClient, chat, sender types.JID, msgID, body, pushName string) {
	cli.emit(&events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: sender},
			ID:            types.MessageID(msgID),
			PushName:      pushName,
			Timestamp:     time.Now(),
		},
		Message: &waE2E.Message{Conversation: strPtr(body)},
	})
}

func TestNameResolutionOneToOneLIDResolvesToPNContact(t *testing.T) {
	cli := newFakeWAClient()
	cli.linked = true
	sink := newSpySink()
	names := newFakeNameResolver()

	lid := mustJID(t, "100000000000001@lid")
	pn := mustJID(t, "56912345678@s.whatsapp.net")
	names.pnForLID[lid] = pn
	names.contacts[pn] = types.ContactInfo{FullName: "Alice Doe", PushName: "alice"}

	a := newTestAdapter("personal", cli)
	a.SetNameResolver(names)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx, sink)
	waitFor(t, func() bool { return cli.IsConnected() })

	emitTextMessage(cli, lid, lid, "M1", "hola", "push-alice")
	waitFor(t, func() bool { return len(sink.items()) == 1 })

	item := sink.items()[0]
	if item.ThreadName != "Alice Doe" {
		t.Errorf("ThreadName = %q, want %q (saved FullName over PushName)", item.ThreadName, "Alice Doe")
	}
	if item.From.Name != "Alice Doe" {
		t.Errorf("From.Name = %q, want %q", item.From.Name, "Alice Doe")
	}
	if item.Thread != pn.String() {
		t.Errorf("Thread = %q, want the PN JID %q (stable identity over the LID)", item.Thread, pn.String())
	}
	wantID := itemID("personal", pn.String(), "M1")
	if item.ID != wantID {
		t.Errorf("ID = %q, want %q", item.ID, wantID)
	}
}

func TestNameResolutionFallsBackToPushNameWhenNoContact(t *testing.T) {
	cli := newFakeWAClient()
	cli.linked = true
	sink := newSpySink()
	a := newTestAdapter("personal", cli)
	a.SetNameResolver(newFakeNameResolver()) // no PN mapping, no contact known

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx, sink)
	waitFor(t, func() bool { return cli.IsConnected() })

	chat := mustJID(t, "56999999999@s.whatsapp.net")
	emitTextMessage(cli, chat, chat, "M2", "hola", "Push Only")
	waitFor(t, func() bool { return len(sink.items()) == 1 })

	item := sink.items()[0]
	if item.ThreadName != "Push Only" {
		t.Errorf("ThreadName = %q, want push name fallback %q", item.ThreadName, "Push Only")
	}
	if item.Thread != chat.String() {
		t.Errorf("Thread = %q, want unchanged %q (no LID mapping to prefer)", item.Thread, chat.String())
	}
}

func TestNameResolutionFallsBackToBareNumberWithNoPushNameOrContact(t *testing.T) {
	cli := newFakeWAClient()
	cli.linked = true
	sink := newSpySink()
	a := newTestAdapter("personal", cli)
	a.SetNameResolver(newFakeNameResolver())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx, sink)
	waitFor(t, func() bool { return cli.IsConnected() })

	chat := mustJID(t, "56988888888@s.whatsapp.net")
	emitTextMessage(cli, chat, chat, "M3", "hola", "")
	waitFor(t, func() bool { return len(sink.items()) == 1 })

	item := sink.items()[0]
	if item.ThreadName != "56988888888" {
		t.Errorf("ThreadName = %q, want the bare number %q", item.ThreadName, "56988888888")
	}
}

func TestNameResolutionGroupNameFromGetGroupInfoIsCached(t *testing.T) {
	cli := newFakeWAClient()
	cli.linked = true
	sink := newSpySink()
	names := newFakeNameResolver()

	group := mustJID(t, "111222333@g.us")
	names.groupInfo[group] = &types.GroupInfo{GroupName: types.GroupName{Name: "Familia"}}

	a := newTestAdapter("personal", cli)
	a.SetNameResolver(names)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx, sink)
	waitFor(t, func() bool { return cli.IsConnected() })

	sender := mustJID(t, "56911111111@s.whatsapp.net")
	emitTextMessage(cli, group, sender, "M4", "hola grupo", "Someone")
	waitFor(t, func() bool { return len(sink.items()) == 1 })
	emitTextMessage(cli, group, sender, "M5", "otra vez", "Someone")
	waitFor(t, func() bool { return len(sink.items()) == 2 })

	for _, item := range sink.items() {
		if item.ThreadName != "Familia" {
			t.Errorf("ThreadName = %q, want %q", item.ThreadName, "Familia")
		}
	}
	if calls := names.callsFor(group); calls != 1 {
		t.Errorf("GetGroupInfo calls = %d, want 1 (cached within TTL)", calls)
	}
}

func TestThreadKeyStableAcrossLIDAndPNForms(t *testing.T) {
	cli := newFakeWAClient()
	cli.linked = true
	sink := newSpySink()
	names := newFakeNameResolver()

	lid := mustJID(t, "100000000000001@lid")
	pn := mustJID(t, "56912345678@s.whatsapp.net")
	names.pnForLID[lid] = pn

	a := newTestAdapter("personal", cli)
	a.SetNameResolver(names)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx, sink)
	waitFor(t, func() bool { return cli.IsConnected() })

	emitTextMessage(cli, lid, lid, "M6", "primero", "")
	waitFor(t, func() bool { return len(sink.items()) == 1 })
	emitTextMessage(cli, pn, pn, "M7", "segundo", "")
	waitFor(t, func() bool { return len(sink.items()) == 2 })

	items := sink.items()
	if items[0].Thread != items[1].Thread {
		t.Fatalf("Thread differs across LID/PN forms of the same chat: %q vs %q", items[0].Thread, items[1].Thread)
	}
	if items[0].Thread != pn.String() {
		t.Errorf("Thread = %q, want the PN JID %q", items[0].Thread, pn.String())
	}
}

// TestResolveContactNameIgnoresDevicePart: contacts and LID mappings are
// stored per person, so a device JID (number:device@server) must resolve
// exactly like the bare person JID instead of falling back to the number.
func TestResolveContactNameIgnoresDevicePart(t *testing.T) {
	pn := types.NewJID("51911111111", types.DefaultUserServer)
	lid := types.NewJID("123456789", types.HiddenUserServer)
	names := newFakeNameResolver()
	names.pnForLID[lid] = pn
	names.contacts[pn] = types.ContactInfo{Found: true, FullName: "Ana Ejemplo"}
	a := newTestAdapter("personal", &fakeWAClient{linked: true})
	a.SetNameResolver(names)

	tests := []struct {
		name string
		jid  types.JID
	}{
		{"phone number device", types.JID{User: pn.User, Server: pn.Server, Device: 16}},
		{"lid device", types.JID{User: lid.User, Server: lid.Server, Device: 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolved, name := a.resolveContactName(context.Background(), tt.jid, "")
			if resolved != pn || name != "Ana Ejemplo" {
				t.Errorf("resolveContactName(%s) = (%s, %q), want (%s, %q)", tt.jid, resolved, name, pn, "Ana Ejemplo")
			}
		})
	}
}
