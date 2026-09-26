package whatsapp

import (
	"context"
	"errors"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// ErrNotLinked is returned by Run when no WhatsApp device is paired yet.
// Alice links one out of band with "bunker link whatsapp" (see Link).
var ErrNotLinked = errors.New("whatsapp: no device linked, run 'bunker link whatsapp' first")

// ErrLoggedOut is returned by Run when the phone unpairs the linked
// device while the adapter is running.
var ErrLoggedOut = errors.New("whatsapp: device was logged out")

// waClient is the narrow slice of *whatsmeow.Client this adapter depends
// on. Every adapter behavior is tested against a fake implementing this
// interface; nothing in this package talks to a *whatsmeow.Client
// directly outside of the realClient wrapper in register.go.
type waClient interface {
	// IsLinked reports whether a device is already stored (paired),
	// independent of whether it is currently connected.
	IsLinked() bool
	OwnJID() types.JID

	Connect() error
	Disconnect()
	IsConnected() bool
	IsLoggedIn() bool

	AddEventHandler(handler whatsmeow.EventHandler) uint32
	RemoveEventHandler(id uint32) bool

	SendMessage(ctx context.Context, to types.JID, message *waE2E.Message, extra ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error)
	IsOnWhatsApp(ctx context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error)
	MarkRead(ctx context.Context, ids []types.MessageID, timestamp time.Time, chat, sender types.JID) error

	// SendPresence and SendChatPresence drive the human-emulation
	// choreography every send and read performs (T13b/c): a linked device
	// that stays "available" suppresses the phone's own push
	// notifications, so Send/SendMedia/MarkRead bracket themselves with
	// available/unavailable, and Send/SendMedia additionally show
	// composing/paused around the real delivery.
	SendPresence(ctx context.Context, state types.Presence) error
	SendChatPresence(ctx context.Context, jid types.JID, state types.ChatPresence, media types.ChatPresenceMedia) error
	Download(ctx context.Context, msg whatsmeow.DownloadableMessage) ([]byte, error)
	Upload(ctx context.Context, plaintext []byte, appInfo whatsmeow.MediaType) (whatsmeow.UploadResponse, error)

	GetQRChannel(ctx context.Context) (<-chan whatsmeow.QRChannelItem, error)

	// GetProfilePictureInfo fetches the URL a contact or group's profile/
	// group picture can be downloaded from (see Adapter.Avatar,
	// core.AvatarProvider). It returns whatsmeow.ErrProfilePictureNotSet
	// or whatsmeow.ErrProfilePictureUnauthorized when jid has no picture
	// Alice can see — both a negative-cache miss for Avatar, never a hard
	// error.
	GetProfilePictureInfo(ctx context.Context, jid types.JID, params *whatsmeow.GetProfilePictureParams) (*types.ProfilePictureInfo, error)

	// ParseWebMessage turns one history-sync WebMessageInfo into the same
	// *events.Message shape a live message arrives as, so handleHistorySync
	// can reuse toItem and enrichItem unchanged.
	ParseWebMessage(chatJID types.JID, webMsg *waWeb.WebMessageInfo) (*events.Message, error)
}
