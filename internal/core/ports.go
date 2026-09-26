package core

import (
	"context"
	"time"
)

// Adapter is the mandatory capability every channel plugin implements: a
// long-running loop that streams Items into a Sink until ctx is canceled.
type Adapter interface {
	Channel() Channel
	Account() string
	Run(ctx context.Context, sink Sink) error
}

// Sink is how an Adapter persists what it observes. The store implements
// it; core never implements it itself.
type Sink interface {
	Upsert(ctx context.Context, item Item) error
	MarkRead(ctx context.Context, id string, read bool) error
	// Delete removes item id from the store, e.g. when an adapter
	// observes it was expunged upstream or Service rekeyed it to a new
	// address after a move. It is a no-op error (ErrNotFound) when id is
	// already gone, never a hard failure.
	Delete(ctx context.Context, id string) error
	Cursor(ctx context.Context, key string) (string, error)
	SetCursor(ctx context.Context, key, val string) error
}

// Fetcher is an optional capability: adapters that only sync headers/
// previews implement it to fetch a full body on demand.
type Fetcher interface {
	Fetch(ctx context.Context, id string) (Item, error)
}

// Outgoing is a message to send or reply with, channel-agnostic.
type Outgoing struct {
	Channel Channel
	Account string
	To      []string
	// Cc lists additional recipients that receive a copy. A channel that
	// cannot address more than one recipient (WhatsApp, Matrix) must
	// reject any non-empty Cc with ErrUnsupported instead of silently
	// dropping it.
	Cc          []string
	Thread      string
	ReplyTo     string // item id being replied to, empty for a fresh send
	Subject     string
	Body        string
	Attachments []string // local file paths
}

// Receipt confirms a write op actually reached the channel.
type Receipt struct {
	ID      string
	Channel Channel
	At      time.Time
	// Recipients lists this send/reply's outcome for every fan-out
	// recipient (see MultiRecipientSender and Service.Send/Reply): one
	// entry per Outgoing.To address when the adapter needed N sequential
	// single-recipient calls. Empty for a single native call. ID/Channel/
	// At above mirror the first successful entry, so a JSON consumer that
	// only reads the top-level fields keeps working unchanged.
	Recipients []RecipientResult `json:"recipients,omitempty"`
}

// RecipientResult is one recipient's outcome within a fan-out send/reply.
// Error is the message text, not an error value, so it survives the JSON
// boundary; it is empty on success.
type RecipientResult struct {
	To      string
	Receipt Receipt
	Error   string `json:"error,omitempty"`
}

// Sender is an optional capability: adapters that can send/reply implement
// it.
type Sender interface {
	Send(ctx context.Context, out Outgoing) (Receipt, error)
}

// MultiRecipientSender is an optional, stronger marker capability: an
// adapter that can natively address more than one Outgoing.To recipient
// in a single outbound call (mail: one SMTP submission RCPTs everyone)
// implements it. Its method is never called; its mere presence tells
// Service.Send/Reply to make one call carrying every recipient instead of
// fanning out len(Outgoing.To) > 1 into N sequential single-recipient
// calls the way an adapter with only Sender/MediaSender (WhatsApp,
// Matrix) requires (see T13a).
type MultiRecipientSender interface {
	Sender
	NativeMultiRecipient()
}

// FanoutPolicy describes the limits and pacing Service.Send/Reply must
// honor when it fans out len(Outgoing.To) > 1 into N sequential
// single-recipient calls for an adapter without MultiRecipientSender.
// MaxRecipients caps how many recipients one broadcast may address
// (exceeding it is an error before anything is sent); PauseMin/PauseMax
// bound the randomized pause Service waits between recipients. A zero
// field falls back to Service's own default (10 recipients, 3-8s).
type FanoutPolicy struct {
	MaxRecipients      int
	PauseMin, PauseMax time.Duration
}

// FanoutConfigurer is optional: an adapter that wants non-default,
// per-account fan-out limits (see FanoutPolicy) implements it. An adapter
// that does not implement it gets Service's built-in defaults.
type FanoutConfigurer interface {
	FanoutPolicy() FanoutPolicy
}

// ReadMarker is an optional capability: an adapter that can mark an item
// read on the channel itself when Alice actually reads it (WhatsApp
// blue ticks, a Matrix m.read receipt) implements it. Mail deliberately
// never implements it: IMAP BODY.PEEK means Fetch alone must never mark
// anything \Seen, so Service.Read simply skips the mark-read step for any
// adapter that doesn't implement this — never an error.
type ReadMarker interface {
	MarkRead(ctx context.Context, id string) error
}

// AttachmentInfo is what Service computes once for each local file path in
// Outgoing.Attachments before validating and sending it: its base name,
// content-sniffed MIME type (falling back to the file extension when
// sniffing is inconclusive) and size in bytes. It never carries the
// file's bytes.
type AttachmentInfo struct {
	Name string
	MIME string
	Size int64
}

// AttachmentPolicy is what a MediaSender declares it accepts. MaxBytes
// maps an exact MIME type to the largest size in bytes that type may
// have; a MIME type absent from MaxBytes is rejected. Service validates
// every attachment against this policy before any adapter method runs —
// on dry-run sends/replies too — so a channel-specific rule (e.g.
// WhatsApp images capped at 16 MB) lives with the adapter that enforces
// it, not in the CLI.
type AttachmentPolicy struct {
	MaxBytes map[string]int64
	// MaxTotalBytes caps the sum of all attachment sizes in one message
	// (e.g. a mail server's message size limit). Zero means no total cap.
	MaxTotalBytes int64
}

// AnyMIME is the MaxBytes key a policy uses as the limit for every MIME
// type that has no exact entry (mail accepts any file type).
const AnyMIME = "*/*"

// MediaSender is an optional, stronger alternative to Sender for adapters
// that can attach local files (Outgoing.Attachments) to an outgoing
// message. Service requires it instead of plain Sender whenever
// Outgoing.Attachments is non-empty, so a channel that cannot send media
// reports ErrUnsupported instead of silently sending text-only and
// dropping the attachments. AttachmentPolicy lets Service validate every
// attachment against the adapter's own rules before ever calling
// SendMedia.
type MediaSender interface {
	Sender
	SendMedia(ctx context.Context, out Outgoing) (Receipt, error)
	AttachmentPolicy() AttachmentPolicy
}

// OrganizeOp describes a mutation to an Item's organization: labels,
// folder and read state.
type OrganizeOp struct {
	AddLabels    []string
	RemoveLabels []string
	MoveTo       string
	Seen         *bool
}

// Organizer is an optional capability: adapters that support labels/
// folders/read-state implement it.
type Organizer interface {
	Organize(ctx context.Context, id string, op OrganizeOp) error
}

// OrganizeMove is the extra fact Service needs to reconcile the store
// after an Organize whose MoveTo can relocate an item to a new address
// (mail's IMAP UID encodes the destination folder into the item ID). ID
// is the item's address after the op; it equals the id Organize was
// called with when the address didn't change. Folder is the new
// Item.Meta["folder"] value, or "" to leave Meta unchanged.
type OrganizeMove struct {
	ID     string
	Folder string
}

// FolderMover is an optional, stronger alternative to Organizer for
// adapters whose MoveTo can relocate an item to a new address. Service
// prefers it over plain Organizer so it can rekey the stored item to
// match, instead of leaving a stale copy behind in the folder it moved
// out of. OrganizeMove performs the same mutation as Organizer.Organize
// (a caller uses one or the other, never both for the same call).
type FolderMover interface {
	Organizer
	OrganizeMove(ctx context.Context, id string, op OrganizeOp) (OrganizeMove, error)
}

// Status is a channel status/story post (e.g. WhatsApp status).
type Status struct {
	Text       string
	Media      string // local file path, optional
	Background string
}

// StatusPublisher is an optional capability: adapters that can publish a
// status/story implement it.
type StatusPublisher interface {
	PostStatus(ctx context.Context, status Status) (Receipt, error)
}

// Retrier is an optional capability: adapters that may store an item they
// could not fully process the first time (e.g. an encrypted event with no
// megolm session yet) implement it to retry those items once new keys
// become available, using the full Store to find what it previously
// stored (an Adapter only ever sees a Sink while running).
type Retrier interface {
	RetryUndecryptable(ctx context.Context, store Store) error
}

// Filter narrows a List query.
type Filter struct {
	Channel Channel
	Account string
	Unread  *bool
	Label   string
	Query   string
	Limit   int
}

// Plan describes what a write op would do (or did). dryRun calls return it
// alone; real calls return it alongside the Receipt.
type Plan struct {
	Action  string
	Channel Channel
	Account string
	Target  string
	// Cc lists the Cc recipients of a send/reply, kept separate from
	// Target (which lists To) so a JSON consumer that only reads Target
	// keeps working unchanged. Empty for actions that carry no Cc.
	Cc []string
	// Subject is the message subject shown for operator/approval
	// visibility: for send, out.Subject as given (empty when none); for
	// reply, the original item's subject with a "Re: " prefix computed by
	// Service (empty when the original item carries no subject, e.g.
	// WhatsApp/Matrix). It never changes what an adapter actually
	// transmits.
	Subject string
	Preview string
	// Media lists the local attachment paths a send/reply would deliver
	// (or did). Empty for actions that carry no attachment. Kept
	// alongside Attachments for compatibility.
	Media []string
	// Attachments lists name/MIME/size for each attachment in Media,
	// computed and validated against the adapter's AttachmentPolicy
	// before any upload. Empty for actions with no attachments.
	Attachments []AttachmentInfo
	// Recipients lists every To address this send/reply reaches, in
	// order — one entry whether the adapter addressed everyone with a
	// single native call (mail) or Service fanned out N sequential
	// single-recipient calls (WhatsApp, Matrix). The CLI's human output
	// iterates this instead of Target for "to [...]" display (T13g).
	Recipients []string
	// FanoutPauseMin/Max is the total inter-recipient pause budget for a
	// fan-out send (len(Recipients) > 1 on an adapter without
	// MultiRecipientSender): (len(Recipients)-1) pauses, each within the
	// adapter's configured [PauseMin,PauseMax] (or Service's 3-8s
	// default). Zero for a native multi-recipient or single-recipient
	// send. It does not include each adapter's own per-message
	// composing/typing time, which core has no visibility into.
	FanoutPauseMin time.Duration
	FanoutPauseMax time.Duration
}

// Store is the read/write persistence port the Service depends on. It
// embeds Sink (what adapters write through) plus the read queries the
// Service needs to serve list/get/counts. internal/store implements it.
type Store interface {
	Sink
	Get(ctx context.Context, id string) (Item, error)
	List(ctx context.Context, filter Filter) ([]Item, error)
	Counts(ctx context.Context) (map[Channel]map[string]int, error)
}
