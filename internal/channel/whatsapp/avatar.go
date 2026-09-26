package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"

	"github.com/reyer3/bunker-go/internal/core"
)

// avatarHTTPTimeout bounds the plain HTTPS GET Avatar makes to download a
// profile/group picture once GetProfilePictureInfo has resolved its URL.
const avatarHTTPTimeout = 10 * time.Second

// httpGetURL is the production httpGet: a plain HTTPS GET with no special
// headers — WhatsApp profile/group picture URLs need none, unlike message
// media, which whatsmeow's own end-to-end-encrypted Download handles.
func httpGetURL(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, avatarHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("whatsapp: fetch profile picture: unexpected status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// Avatar implements core.AvatarProvider: it fetches thread's (a contact
// or group JID, the same value stored as Item.Thread) preview-size
// profile/group picture via whatsmeow's GetProfilePictureInfo, then
// downloads it with a plain HTTPS GET. ok is false, err nil, when the
// contact/group has no picture set or has hidden it from Alice
// (whatsmeow's ErrProfilePictureNotSet/ErrProfilePictureUnauthorized) —
// a negative-cache miss for core.Service.Avatar, never a failure. Neither
// this method nor its errors ever include the picture URL or the raw JID
// beyond what the caller already passed in as thread, per the feature

// defaultAvatarTimeout bounds one profile-picture lookup plus its
// download, well under the RPC deadline, so an unanswered lookup never
// holds a TUI avatar request until the socket times out.
const defaultAvatarTimeout = 10 * time.Second

// doc's "no avatar URLs ... in logs or error strings" constraint.
func (a *Adapter) Avatar(ctx context.Context, thread string) (core.AvatarSource, bool, error) {
	jid, err := types.ParseJID(thread)
	// types.ParseJID never errors for a bare string with no "@" — it
	// treats the whole thing as a bare server name with an empty User
	// (e.g. "not a jid" parses to JID{User:"", Server:"not a jid"}), never
	// a JID this package's callers produce as an Item.Thread (always
	// "<user>@<server>"), so treat an empty User the same as a parse
	// error instead of forwarding a meaningless lookup to
	// GetProfilePictureInfo.
	if err != nil || jid.User == "" {
		return core.AvatarSource{}, false, fmt.Errorf("whatsapp: avatar: invalid thread %q", thread)
	}

	// Channels (newsletters) never answer this lookup and broadcast lists
	// have no picture: both get the generated avatar, with no network.
	if jid.Server == types.NewsletterServer || jid.Server == types.BroadcastServer {
		return core.AvatarSource{}, false, nil
	}
	timeout := a.avatarTimeout
	if timeout <= 0 {
		timeout = defaultAvatarTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	info, err := a.cli.GetProfilePictureInfo(ctx, jid, &whatsmeow.GetProfilePictureParams{Preview: true})
	if err != nil {
		if errors.Is(err, whatsmeow.ErrProfilePictureNotSet) || errors.Is(err, whatsmeow.ErrProfilePictureUnauthorized) {
			return core.AvatarSource{}, false, nil
		}
		return core.AvatarSource{}, false, fmt.Errorf("whatsapp: avatar: %w", err)
	}
	if info == nil || info.URL == "" {
		return core.AvatarSource{}, false, nil
	}

	data, err := a.httpGet(ctx, info.URL)
	if err != nil {
		return core.AvatarSource{}, false, fmt.Errorf("whatsapp: avatar: download picture: %w", err)
	}

	return core.AvatarSource{Data: data, DisplayName: a.avatarDisplayName(ctx, jid)}, true, nil
}

// avatarDisplayName picks the text Avatar's caller (core.Service) derives
// a generated fallback's initial from: the group name for a group JID,
// else the same contact-name resolution incoming messages use, with no
// push name to fall back on (this is an on-demand lookup, not driven by
// an incoming message) — the bare JID user part when nothing else is
// known.
func (a *Adapter) avatarDisplayName(ctx context.Context, jid types.JID) string {
	if jid.Server == types.GroupServer {
		return a.groupName(ctx, jid)
	}
	_, name := a.resolveContactName(ctx, jid, "")
	return name
}
