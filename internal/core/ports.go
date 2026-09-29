package core

import (
	"context"
	"io"
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
	// MarkThreadReadUpTo marks every unread item of the (channel, account,
	// thread) conversation whose Timestamp is at or before upTo as read.
	// It never touches an item whose FromMe is true (marking our own sent
	// messages "read" makes no sense: only the other side's messages
	// drive our unread state) and never touches any other thread. Used
	// when a read event observed elsewhere (a WhatsApp ReadSelf/
	// MarkChatAsRead, a Matrix m.receipt/m.fully_read, mail's \Seen
	// reconciliation) reports "the conversation up to this point is
	// read" rather than naming individual item ids.
	MarkThreadReadUpTo(ctx context.Context, channel Channel, account, thread string, upTo time.Time) error
	// Delete removes item id from the store, e.g. when an adapter
	// observes it was expunged upstream or Service rekeyed it to a new
	// address after a move. It is a no-op error (ErrNotFound) when id is
	// already gone, never a hard failure.
	Delete(ctx context.Context, id string) error
	Cursor(ctx context.Context, key string) (string, error)
	SetCursor(ctx context.Context, key, val string) error
	// EditItem replaces id's stored Body with the new body and sets its
	// Edited flag (a WhatsApp/Matrix message edit). It is a no-op error
	// (ErrNotFound) when id is unknown, mirroring MarkRead/Delete.
	EditItem(ctx context.Context, id, body string) error
	// RevokeItem clears id's stored Body and sets its Deleted flag,
	// keeping the row (unlike Delete, which removes it) so the
	// conversation's history and thread pagination are undisturbed. It
	// is a no-op error (ErrNotFound) when id is unknown.
	RevokeItem(ctx context.Context, id string) error
	// SetReaction stores sender's reaction to item id: a newer reaction
	// from the same sender replaces the previous one, and an empty
	// Emoji removes it. Channel-agnostic (see core.Reaction).
	SetReaction(ctx context.Context, id string, reaction Reaction) error
}

// Fetcher is an optional capability: adapters that only sync headers/
// previews implement it to fetch a full body on demand.
type Fetcher interface {
	Fetch(ctx context.Context, id string) (Item, error)
}

// Backfiller is an optional adapter capability: it searches the channel's
// own server-side history for items on or after since (in folder, a
// channel-specific mailbox/room/whatever "INBOX" means for it) and
// upserts via store whichever of them the store doesn't already have,
// batching however its own protocol batches headers. It never marks
// anything read and never moves the adapter's regular Run sync cursor,
// so a later Run resumes exactly where it left off; dryRun reports what
// would be added without upserting anything. It exists because a bounded
// adapter (e.g. mail's initial_sync_limit) may never have synced
// messages older than its window, and Service.Fetch's per-id server
// fallback (see Service.Fetch) only recovers one id at a time.
type Backfiller interface {
	Backfill(ctx context.Context, store Store, folder string, since time.Time, dryRun bool) (BackfillResult, error)
}

// BackfillResult reports what a Backfiller.Backfill call added, or (on a
// dry-run) would add.
type BackfillResult struct {
	// Count is how many items were (or, on dry-run, would be) upserted.
	Count int
	// FirstID and LastID are the lowest- and highest-native-id items
	// Backfill found in [since, now) — including ones the store already
	// had — so a dry-run can still report a range when Count is 0. Both
	// are empty when nothing matched.
	FirstID, LastID string
}

// Searcher is an optional adapter capability: it runs a server-side
// search for criteria and upserts every match via store (headers only,
// read-only — it must never mark anything read), returning them the way
// Service.List does. It exists for the case Backfiller and Service.Fetch
// don't cover: not knowing an item's id at all, only what it should
// contain.
type Searcher interface {
	Search(ctx context.Context, store Store, criteria SearchCriteria) ([]Item, error)
}

// SearchCriteria narrows a Searcher.Search call. An adapter treats a zero
// field as "don't filter on this"; Limit <= 0 uses the adapter's own
// default.
type SearchCriteria struct {
	Folder  string
	From    string
	Subject string
	Since   time.Time
	Before  time.Time
	Limit   int
}

// AttachmentDownloader is an optional capability: an adapter that can
// fetch one attachment's raw bytes on demand implements it, so
// Service.Download can save it to disk without the item ever carrying
// the bytes (or any decryption material) itself. index is the
// attachment's position within item.Attachments. mail re-fetches the
// MIME part by UID over IMAP; WhatsApp looks up the download descriptor
// it persisted for (item.ID, index) when the message first arrived and
// calls whatsmeow's Download. The caller (Service.Download) closes the
// returned io.ReadCloser.
type AttachmentDownloader interface {
	DownloadAttachment(ctx context.Context, item Item, index int) (io.ReadCloser, error)
}

// AvatarSource is what an adapter's AvatarProvider capability returns for
// one conversation's picture: the raw bytes as fetched from the channel
// (any size/format the source serves — Service.Avatar decodes, resizes
// and re-encodes to the cached PNG shape) plus a display name Service
// uses to derive the generated fallback's initial/color when the fetch
// itself fails or the picture is later evicted.
type AvatarSource struct {
	Data        []byte
	DisplayName string
}

// AvatarProvider is an optional capability: an adapter that can fetch a
// conversation's profile/room picture (WhatsApp contacts and groups,
// Matrix rooms and DMs) implements it. thread is the same Item.Thread
// value grouping that conversation's items. ok is false with a nil err
// when the channel itself reports "no picture set" or "not authorized to
// view it" — a negative-cache miss, never an error: Service.Avatar always
// falls back to the generated avatar for that case. A non-nil err means
// the fetch attempt itself failed unexpectedly (network, decode); it is
// also never negative-cached, so the next call retries. Mail never
// implements this capability: it gets no network lookup, ever (see
// odd/tasks/tui-avatars.md's privacy decision).
type AvatarProvider interface {
	Avatar(ctx context.Context, thread string) (AvatarSource, bool, error)
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
	// Replayed is true when this receipt is a remembered one returned for
	// a repeated idempotency key (see WithIdempotencyKey): nothing was
	// sent this time, the message went out on the first call.
	Replayed bool `json:"replayed,omitempty"`
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

// ThreadReader is an optional capability: an adapter that can mark
// several items of one conversation read on the channel itself in as few
// native calls as possible implements it — WhatsApp's MarkRead batched
// per (chat, sender) as whatsmeow requires for a group, or a single
// Matrix receipt on the newest event (a room's read/fully_read markers
// already cover every older event). ids is oldest→newest.
// Service.ReadThread prefers this over calling ReadMarker.MarkRead once
// per item when the adapter implements it.
type ThreadReader interface {
	MarkThreadRead(ctx context.Context, ids []string) error
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

// Presence is one conversation's live presence state: WhatsApp contact
// online/offline plus typing/recording, or Matrix typing. State is one
// of "online", "offline", "typing", "recording", or "unknown" for a
// channel with no live presence (mail) or a thread never subscribed to.
type Presence struct {
	State    string
	LastSeen time.Time
	Typers   []string
}

// PresenceProvider is an optional capability: an adapter that can report
// a conversation's live presence (WhatsApp contact presence/typing,
// Matrix typing) implements it. A channel without it (mail) always
// reports State "unknown" through Service.Presence, never an error.
type PresenceProvider interface {
	Presence(ctx context.Context, thread string) (Presence, error)
}

// PresenceAvailabilityController is an optional, WhatsApp-specific
// capability: an adapter whose presence system requires the account to
// broadcast its own global availability before it receives others'
// presence/typing updates at all (WhatsApp only delivers a contact's
// presence while WE are "available", which also shows the user online —
// the privacy constraint odd/tasks/conversation-view.md's Decisions
// records) implements it. SetPresenceAvailable(true, thread) broadcasts
// available and subscribes to thread's presence; SetPresenceAvailable
// (false, thread) unsubscribes and broadcasts unavailable. Matrix has no
// such gate and does not implement this capability, so
// Service.PresenceKeepalive is a no-op for it.
type PresenceAvailabilityController interface {
	SetPresenceAvailable(ctx context.Context, available bool, thread string) error
}

// TypingSender is an optional capability: an adapter that can send a
// typing/composing notification for a thread implements it. Throttling
// (at most every 5s while typing; composing=false on idle, send or
// leave) is the TUI's own responsibility — Service.Typing is a thin,
// validated forward.
type TypingSender interface {
	SendTyping(ctx context.Context, thread string, composing bool) error
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

// Editor is an optional capability: an adapter that can replace the
// text of a message this account sent (a WhatsApp edit, a Matrix
// m.replace) implements it. Service only calls it for an item whose
// FromMe is true, after checking MessageWindowLimiter's edit window.
// item is the stored item, not just its id: the adapter needs what the
// store knows about it (its thread, sender and timestamp) even after a
// restart emptied its own caches.
type Editor interface {
	EditMessage(ctx context.Context, item Item, newText string) (Receipt, error)
}

// Deleter is an optional capability: an adapter that can delete a message
// this account sent for everyone in the conversation (a WhatsApp revoke,
// a Matrix redaction) implements it. Like Editor, Service only calls it
// for a FromMe item within the channel's delete window.
type Deleter interface {
	DeleteMessage(ctx context.Context, item Item) (Receipt, error)
}

// Reactor is an optional capability: an adapter that can set or remove
// this account's emoji reaction to any message implements it. An empty
// emoji removes our reaction. OwnReactionSender is the Reaction.Sender
// the adapter's inbound path records for this account's own reactions,
// so the store row Service writes after React is the same one a later
// echo (or the phone's own reaction) updates, never a second entry.
type Reactor interface {
	React(ctx context.Context, item Item, emoji string) (Receipt, error)
	OwnReactionSender() string
}

// MessageWindows is how long after a message was sent the channel still
// accepts an edit or a delete-for-everyone of it. Zero means no limit.
type MessageWindows struct {
	Edit   time.Duration
	Delete time.Duration
}

// MessageWindowLimiter is optional: an adapter whose channel only accepts
// edits or deletes within a time window (WhatsApp) implements it, so
// Service refuses a late one up front, on dry-run too, instead of sending
// something the other side silently ignores.
type MessageWindowLimiter interface {
	MessageWindows() MessageWindows
}

// Retrier is an optional capability: adapters that may store an item they
// could not fully process the first time (e.g. an encrypted event with no
// megolm session yet) implement it to retry those items once new keys
// become available, using the full Store to find what it previously
// stored (an Adapter only ever sees a Sink while running).
type Retrier interface {
	RetryUndecryptable(ctx context.Context, store Store) error
}

// Filter narrows a List query; every non-zero field applies. Thread also
// narrows a Thread query, where Channel+Account+Thread together identify
// one conversation.
type Filter struct {
	Channel Channel
	Account string
	Thread  string
	Unread  *bool
	Label   string
	// Query is literal free text over the full-text index; operators and
	// quotes in it are searched for as text (the TUI's search box and
	// `list -q`). Match is the parsed query language instead.
	Query string
	Limit int
	// Match, when set, must also hold: the parsed form of a
	// query-language string (see ParseQuery). It is a pointer so Filter
	// stays comparable with ==.
	Match *Query `json:",omitempty"`
	// Cursor resumes a PageLister.ListPage listing after the item a
	// previous page's NextCursor named. List ignores it.
	Cursor string `json:",omitempty"`
}

// Page is one page of a newest-first listing. NextCursor is empty on the
// last page; otherwise passing it back as Filter.Cursor returns the next
// one.
type Page struct {
	Items      []Item `json:"items"`
	NextCursor string `json:"next_cursor"`
}

// PageLister is an optional Store capability: cursor-paginated List
// (Service.ListPage). It is separate from Store so the many narrow
// in-memory stores tests use keep compiling; internal/store implements
// it.
type PageLister interface {
	ListPage(ctx context.Context, filter Filter) (Page, error)
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
	// Thread returns filter.Channel/Account/Thread's items, oldest→newest,
	// at most limit items strictly before the before cursor (zero means
	// "the newest window"). Backed by the store's (channel, account,
	// thread, timestamp) index, so opening a conversation stays fast
	// regardless of how large the whole store grows.
	Thread(ctx context.Context, filter Filter, before time.Time, limit int) ([]Item, error)
}
