// Package core defines the channel-agnostic domain model and ports for
// bunker-go. It must never import adapters or third-party network
// libraries: adapters depend on core, never the other way around.
package core

import "time"

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

// Attachment describes a file attached to an Item.
type Attachment struct {
	Name string
	MIME string
	Size int64
	Ref  string
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
	Timestamp   time.Time
	Meta        map[string]string
}
