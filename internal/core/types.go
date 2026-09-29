// Package core defines the channel-agnostic domain model and ports for
// bunker-go. It must never import adapters or third-party network
// libraries: adapters depend on core, never the other way around.
package core

import (
	"bytes"
	"time"
)

// Channel identifies which messaging surface an Item or Adapter belongs to.
type Channel string

const (
	ChannelMail     Channel = "mail"
	ChannelWhatsApp Channel = "whatsapp"
	ChannelMatrix   Channel = "matrix"
)

// Address identifies a participant on any channel.
type Address struct {
	ID   string
	Name string
}

// MaxThumbnailBytes bounds Attachment.Thumbnail. The previews a channel
// embeds in a message (WhatsApp's JPEGThumbnail) are a few KB; anything
// far larger is not a thumbnail, and every copy of it would ride along in
// the store row, the adapters' item caches and every RPC listing.
const MaxThumbnailBytes = 64 << 10

// Attachment describes a file attached to an Item.
type Attachment struct {
	Name string
	MIME string
	Size int64
	Ref  string
	// Thumbnail is a small preview image the message itself carries
	// (WhatsApp's embedded JPEGThumbnail), at most MaxThumbnailBytes. It
	// lets a client preview the attachment without downloading the media.
	// Empty when the channel sent none or it failed validation; omitted
	// from JSON then, so rows and listings without one are unchanged.
	Thumbnail []byte `json:",omitempty"`
}

// Equal reports whether a and b describe the same attachment. Thumbnail
// makes Attachment non-comparable with ==, so callers compare with this.
func (a Attachment) Equal(b Attachment) bool {
	return a.Name == b.Name && a.MIME == b.MIME && a.Size == b.Size && a.Ref == b.Ref &&
		bytes.Equal(a.Thumbnail, b.Thumbnail)
}

// Item is the unified representation of a message, mail thread entry, chat
// message or Matrix event across every channel.
type Item struct {
	// ID is stable and shaped "<channel>:<account>:<native>", e.g.
	// "mail:cl:1234" or "whatsapp:personal:3EB0...".
	ID          string
	Channel     Channel
	Account     string
	Thread      string
	ThreadName  string
	From        Address
	To          []Address
	Subject     string
	Body        string
	Attachments []Attachment
	Labels      []string
	Unread      bool
	// FromMe reports whether the account owner is the sender: WhatsApp's
	// IsFromMe, Matrix sender == own user, or mail whose From matches the
	// account's own address (including a message synced from Sent). It
	// drives the chat view's right-aligned bubbles and must never be
	// derived from Unread, which mail's IMAP \Seen flag can set
	// independently of who sent the message.
	FromMe    bool
	Timestamp time.Time
	Meta      map[string]string
	// Edited reports whether this item's Body was replaced by a
	// channel-observed edit (see Sink.EditItem). Channel-agnostic:
	// WhatsApp and Matrix both set it.
	Edited bool
	// Deleted reports whether this item was revoked/retracted at the
	// source (see Sink.RevokeItem): the row is kept (its place in the
	// conversation's history is preserved) but Body is cleared, and the
	// TUI shows "mensaje eliminado" in its place.
	Deleted bool
	// Reactions lists every participant's current emoji reaction to this
	// item, at most one per Sender (see Sink.SetReaction): a newer
	// reaction from the same Sender replaces the previous one instead of
	// appending.
	Reactions []Reaction
}

// Reaction is one participant's emoji reaction to an Item, channel-
// agnostic (WhatsApp and Matrix).
type Reaction struct {
	Sender string
	Emoji  string
}
