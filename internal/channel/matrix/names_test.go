package matrix

import (
	"context"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/reyer3/bunker-go/internal/core"
)

// runOneSync feeds one synthetic /sync response through a fresh Adapter
// and returns the first Item its message handler upserts.
func runOneSync(t *testing.T, resp *mautrix.RespSync) core.Item {
	t.Helper()
	srv, _ := newFakeHomeserver(t, []*mautrix.RespSync{resp})
	adapter := newTestAdapter(t, srv, nil)
	sink := newMemSink()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()

	var item core.Item
	select {
	case item = <-sink.upserts:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the synced message")
	}
	cancel()
	<-done
	return item
}

func TestAdapterResolvesMemberDisplayNameForFrom(t *testing.T) {
	const room = id.RoomID("!abc:matrix.example.org")
	const sender = id.UserID("@alice:matrix.example.org")
	senderStateKey := string(sender)

	memberEvt := &event.Event{
		ID:       "$member1",
		Sender:   sender,
		Type:     event.StateMember,
		StateKey: &senderStateKey,
		Content:  event.Content{Parsed: &event.MemberEventContent{Membership: event.MembershipJoin, Displayname: "Alice Cooper"}},
	}
	msgEvt := &event.Event{
		ID:        "$evt1",
		Sender:    sender,
		Type:      event.EventMessage,
		Timestamp: 1700000000000,
		Content:   event.Content{Parsed: &event.MessageEventContent{MsgType: event.MsgText, Body: "hola"}},
	}
	resp := &mautrix.RespSync{
		NextBatch: "s1",
		Rooms: mautrix.RespSyncRooms{
			Join: map[id.RoomID]*mautrix.SyncJoinedRoom{
				room: {
					State:    mautrix.SyncEventsList{Events: []*event.Event{memberEvt}},
					Timeline: mautrix.SyncTimeline{SyncEventsList: mautrix.SyncEventsList{Events: []*event.Event{msgEvt}}},
				},
			},
		},
	}

	item := runOneSync(t, resp)
	if item.From.ID != string(sender) {
		t.Errorf("From.ID = %q, want %q", item.From.ID, sender)
	}
	if item.From.Name != "Alice Cooper" {
		t.Errorf("From.Name = %q, want %q (resolved from m.room.member displayname)", item.From.Name, "Alice Cooper")
	}
}

func TestAdapterFromNameFallsBackToMXIDWithoutAMemberEvent(t *testing.T) {
	const room = id.RoomID("!abc:matrix.example.org")
	const sender = id.UserID("@alice:matrix.example.org")

	msgEvt := &event.Event{
		ID:        "$evt1",
		Sender:    sender,
		Type:      event.EventMessage,
		Timestamp: 1700000000000,
		Content:   event.Content{Parsed: &event.MessageEventContent{MsgType: event.MsgText, Body: "hola"}},
	}
	resp := &mautrix.RespSync{
		NextBatch: "s1",
		Rooms: mautrix.RespSyncRooms{
			Join: map[id.RoomID]*mautrix.SyncJoinedRoom{
				room: {Timeline: mautrix.SyncTimeline{SyncEventsList: mautrix.SyncEventsList{Events: []*event.Event{msgEvt}}}},
			},
		},
	}

	item := runOneSync(t, resp)
	if item.From.Name != string(sender) {
		t.Errorf("From.Name = %q, want the raw MXID %q (no displayname known)", item.From.Name, sender)
	}
}

func TestAdapterRoomNameFallsBackToCanonicalAliasWithoutMRoomName(t *testing.T) {
	const room = id.RoomID("!abc:matrix.example.org")
	const sender = id.UserID("@alice:matrix.example.org")
	emptyStateKey := ""

	aliasEvt := &event.Event{
		ID:       "$alias1",
		Sender:   sender,
		Type:     event.StateCanonicalAlias,
		StateKey: &emptyStateKey,
		Content:  event.Content{Parsed: &event.CanonicalAliasEventContent{Alias: "#team:matrix.example.org"}},
	}
	msgEvt := &event.Event{
		ID:        "$evt1",
		Sender:    sender,
		Type:      event.EventMessage,
		Timestamp: 1700000000000,
		Content:   event.Content{Parsed: &event.MessageEventContent{MsgType: event.MsgText, Body: "hola"}},
	}
	resp := &mautrix.RespSync{
		NextBatch: "s1",
		Rooms: mautrix.RespSyncRooms{
			Join: map[id.RoomID]*mautrix.SyncJoinedRoom{
				room: {
					State:    mautrix.SyncEventsList{Events: []*event.Event{aliasEvt}},
					Timeline: mautrix.SyncTimeline{SyncEventsList: mautrix.SyncEventsList{Events: []*event.Event{msgEvt}}},
				},
			},
		},
	}

	item := runOneSync(t, resp)
	if item.ThreadName != "#team:matrix.example.org" {
		t.Errorf("ThreadName = %q, want the canonical alias", item.ThreadName)
	}
}

func TestAdapterExplicitRoomNameWinsOverCanonicalAlias(t *testing.T) {
	const room = id.RoomID("!abc:matrix.example.org")
	const sender = id.UserID("@alice:matrix.example.org")
	emptyStateKey := ""

	nameEvt := &event.Event{
		ID:       "$name1",
		Sender:   sender,
		Type:     event.StateRoomName,
		StateKey: &emptyStateKey,
		Content:  event.Content{Parsed: &event.RoomNameEventContent{Name: "Team Room"}},
	}
	aliasEvt := &event.Event{
		ID:       "$alias1",
		Sender:   sender,
		Type:     event.StateCanonicalAlias,
		StateKey: &emptyStateKey,
		Content:  event.Content{Parsed: &event.CanonicalAliasEventContent{Alias: "#team:matrix.example.org"}},
	}
	msgEvt := &event.Event{
		ID:        "$evt1",
		Sender:    sender,
		Type:      event.EventMessage,
		Timestamp: 1700000000000,
		Content:   event.Content{Parsed: &event.MessageEventContent{MsgType: event.MsgText, Body: "hola"}},
	}
	resp := &mautrix.RespSync{
		NextBatch: "s1",
		Rooms: mautrix.RespSyncRooms{
			Join: map[id.RoomID]*mautrix.SyncJoinedRoom{
				room: {
					State:    mautrix.SyncEventsList{Events: []*event.Event{nameEvt, aliasEvt}},
					Timeline: mautrix.SyncTimeline{SyncEventsList: mautrix.SyncEventsList{Events: []*event.Event{msgEvt}}},
				},
			},
		},
	}

	item := runOneSync(t, resp)
	if item.ThreadName != "Team Room" {
		t.Errorf("ThreadName = %q, want the explicit m.room.name to win over the canonical alias", item.ThreadName)
	}
}

func TestAdapterRoomNameFallsBackToDMHeroesDisplayNames(t *testing.T) {
	const room = id.RoomID("!dm:matrix.example.org")
	const sender = id.UserID("@bob:matrix.example.org")
	const heroID = id.UserID("@bob:matrix.example.org")
	heroStateKey := string(heroID)

	heroMemberEvt := &event.Event{
		ID:       "$member1",
		Sender:   heroID,
		Type:     event.StateMember,
		StateKey: &heroStateKey,
		Content:  event.Content{Parsed: &event.MemberEventContent{Membership: event.MembershipJoin, Displayname: "Bob Esponja"}},
	}
	msgEvt := &event.Event{
		ID:        "$evt1",
		Sender:    sender,
		Type:      event.EventMessage,
		Timestamp: 1700000000000,
		Content:   event.Content{Parsed: &event.MessageEventContent{MsgType: event.MsgText, Body: "hola"}},
	}
	resp := &mautrix.RespSync{
		NextBatch: "s1",
		Rooms: mautrix.RespSyncRooms{
			Join: map[id.RoomID]*mautrix.SyncJoinedRoom{
				room: {
					Summary:  mautrix.LazyLoadSummary{Heroes: []id.UserID{heroID}},
					State:    mautrix.SyncEventsList{Events: []*event.Event{heroMemberEvt}},
					Timeline: mautrix.SyncTimeline{SyncEventsList: mautrix.SyncEventsList{Events: []*event.Event{msgEvt}}},
				},
			},
		},
	}

	item := runOneSync(t, resp)
	if item.ThreadName != "Bob Esponja" {
		t.Errorf("ThreadName = %q, want the DM hero's resolved display name", item.ThreadName)
	}
}

func TestAdapterRoomNameHeroFallsBackToMXIDWithoutDisplayName(t *testing.T) {
	const room = id.RoomID("!dm:matrix.example.org")
	const sender = id.UserID("@bob:matrix.example.org")
	const heroID = id.UserID("@bob:matrix.example.org")

	msgEvt := &event.Event{
		ID:        "$evt1",
		Sender:    sender,
		Type:      event.EventMessage,
		Timestamp: 1700000000000,
		Content:   event.Content{Parsed: &event.MessageEventContent{MsgType: event.MsgText, Body: "hola"}},
	}
	resp := &mautrix.RespSync{
		NextBatch: "s1",
		Rooms: mautrix.RespSyncRooms{
			Join: map[id.RoomID]*mautrix.SyncJoinedRoom{
				room: {
					Summary:  mautrix.LazyLoadSummary{Heroes: []id.UserID{heroID}},
					Timeline: mautrix.SyncTimeline{SyncEventsList: mautrix.SyncEventsList{Events: []*event.Event{msgEvt}}},
				},
			},
		},
	}

	item := runOneSync(t, resp)
	if item.ThreadName != string(heroID) {
		t.Errorf("ThreadName = %q, want the hero's raw MXID (no displayname known)", item.ThreadName)
	}
}
