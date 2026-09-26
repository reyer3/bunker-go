package matrix

import (
	"context"
	"errors"
	"fmt"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/reyer3/bunker-go/internal/core"
)

// Avatar implements core.AvatarProvider: it fetches thread's (a room ID,
// the same value stored as Item.Thread) m.room.avatar, or — when the room
// has none set and it looks like a plain DM (exactly one sync-summary
// hero, the same heuristic resolveRoomNameLocked uses for a DM's
// ThreadName) — that hero's own global profile avatar, then downloads it
// with mautrix's authenticated media DownloadBytes. ok is false, err nil,
// when neither exists (mautrix.MNotFound on both lookups) — a
// negative-cache miss for core.Service.Avatar, never a failure. Room
// avatars are not part of Matrix's end-to-end encryption even in an
// encrypted room (unlike message content/attachments), so no decryption
// is needed here.
func (a *Adapter) Avatar(ctx context.Context, thread string) (core.AvatarSource, bool, error) {
	roomID := id.RoomID(thread)

	contentURI, ok, err := a.avatarContentURI(ctx, roomID)
	if err != nil || !ok {
		return core.AvatarSource{}, false, err
	}

	data, err := a.client.DownloadBytes(ctx, contentURI)
	if err != nil {
		return core.AvatarSource{}, false, fmt.Errorf("matrix: avatar: download: %w", err)
	}

	a.mu.Lock()
	displayName := a.resolveRoomNameLocked(roomID)
	a.mu.Unlock()

	return core.AvatarSource{Data: data, DisplayName: displayName}, true, nil
}

// avatarContentURI resolves roomID's picture: the room's own m.room.avatar
// state event when set, else — for a room with exactly one sync-summary
// hero (a DM) — that hero's global avatar_url. ok is false with a nil err
// when neither is set (both mautrix.MNotFound); a non-nil err means an
// unexpected failure that must propagate, not fall back.
func (a *Adapter) avatarContentURI(ctx context.Context, roomID id.RoomID) (id.ContentURI, bool, error) {
	var content event.RoomAvatarEventContent
	err := a.client.StateEvent(ctx, roomID, event.StateRoomAvatar, "", &content)
	switch {
	case err == nil && content.URL != "":
		uri, parseErr := content.URL.Parse()
		if parseErr != nil {
			return id.ContentURI{}, false, fmt.Errorf("matrix: avatar: parse room avatar url: %w", parseErr)
		}
		return uri, true, nil
	case err != nil && !errors.Is(err, mautrix.MNotFound):
		return id.ContentURI{}, false, fmt.Errorf("matrix: avatar: room avatar state: %w", err)
	}

	// No room avatar (M_NOT_FOUND, or an empty URL): fall back to the DM
	// hero's own avatar, only for what resolveRoomNameLocked already
	// treats as a DM.
	a.mu.Lock()
	heroes := a.heroes[roomID]
	a.mu.Unlock()
	if len(heroes) != 1 {
		return id.ContentURI{}, false, nil
	}

	uri, err := a.client.GetAvatarURL(ctx, heroes[0])
	if err != nil {
		if errors.Is(err, mautrix.MNotFound) {
			return id.ContentURI{}, false, nil
		}
		return id.ContentURI{}, false, fmt.Errorf("matrix: avatar: hero avatar url: %w", err)
	}
	if uri.IsEmpty() {
		return id.ContentURI{}, false, nil
	}
	return uri, true, nil
}
