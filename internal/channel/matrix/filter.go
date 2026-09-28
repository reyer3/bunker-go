// Package matrix implements the core.Adapter for the company Matrix
// homeserver (matrix.example.org, Synapse 1.68.0) using maunium.net/go/mautrix
// built with the goolm tag (pure-Go E2EE, no libolm/CGO).
package matrix

import "maunium.net/go/mautrix"

// SyncFilter builds the minimal /sync filter bunker-go uploads for every
// Matrix account.
//
// CRITICAL compatibility note: Synapse 1.68.0 rejects a filter whose
// presence part sets not_rooms with an HTTP 400 ("Invalid filter"), which
// is exactly what broke gomuks against matrix.example.org. This filter never
// sets Presence.NotRooms (or any other NotRooms field): presence is left
// as an empty filter part so the server applies its own default instead
// of us asking for a not_rooms-shaped restriction, and the room timeline
// is capped to a small limit with lazy-loaded members to keep /sync
// responses light.
//
// Room.Ephemeral and Room.AccountData are likewise explicit, empty
// filter parts (no Types/NotTypes restriction): read-sync needs every
// room's m.receipt ephemeral events and m.fully_read account data
// delivered, and leaving these two RoomFilter fields nil relies on
// unspecified per-homeserver "no filter for this category" default
// behavior instead of asking for them outright.
func SyncFilter() *mautrix.Filter {
	return &mautrix.Filter{
		Presence: &mautrix.FilterPart{},
		Room: &mautrix.RoomFilter{
			Timeline:    &mautrix.FilterPart{Limit: 50},
			State:       &mautrix.FilterPart{LazyLoadMembers: true},
			Ephemeral:   &mautrix.FilterPart{},
			AccountData: &mautrix.FilterPart{},
		},
	}
}
