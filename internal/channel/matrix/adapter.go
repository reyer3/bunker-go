package matrix

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/reyer3/bunker-go/internal/config"
	"github.com/reyer3/bunker-go/internal/core"
)

// Adapter is the core.Adapter for one Matrix account. It also implements
// core.Fetcher, core.Sender and core.Organizer.
type Adapter struct {
	account string
	client  *mautrix.Client
	crypto  mautrix.CryptoHelper // nil when the account has no E2EE support wired

	// cryptoInitOnce guards a.crypto.Init: it may run once from Run's
	// sync loop or once from RetryUndecryptable (the daemon calls the
	// latter before starting the former), whichever happens first.
	cryptoInitOnce sync.Once
	cryptoInitErr  error

	mu             sync.Mutex
	items          map[string]core.Item
	roomNames      map[id.RoomID]string               // explicit m.room.name
	canonicalAlias map[id.RoomID]string               // m.room.canonical_alias
	heroes         map[id.RoomID][]id.UserID          // DM heroes, from sync summary
	memberNames    map[id.RoomID]map[id.UserID]string // m.room.member displayname, per room
	unread         map[id.RoomID]int
	// typers is the current m.typing user_ids list per room (K3),
	// excluding this account's own user id. m.typing always carries the
	// full current set, never an add/remove delta, so each event simply
	// replaces the room's entry.
	typers map[id.RoomID][]string

	// sleep drives the typing-notification wait Send performs before
	// delivering (T13d). It defaults to the real time.Sleep; tests
	// override it via SetSleeper so a send test never sleeps for real
	// (T13f).
	sleep func(time.Duration)

	// mediaConfigOnce guards the single GetMediaConfig fetch AttachmentPolicy
	// makes (see media.go, T16b): mediaMaxUploadBytes is set exactly once,
	// to the homeserver's advertised m.upload.size or, when that fetch
	// fails, defaultMaxUploadBytes.
	mediaConfigOnce     sync.Once
	mediaMaxUploadBytes int64

	// sink is set once, at the top of Run, so DownloadAttachment (called
	// independently, e.g. over RPC while Run is still active in the
	// daemon) can read the same core.Sink messageHandler/encryptedHandler
	// already persist media descriptors through (M1). It is nil until Run
	// has been called at least once.
	sink core.Sink
}

var (
	_ core.Adapter              = (*Adapter)(nil)
	_ core.Fetcher              = (*Adapter)(nil)
	_ core.Sender               = (*Adapter)(nil)
	_ core.MediaSender          = (*Adapter)(nil)
	_ core.AttachmentDownloader = (*Adapter)(nil)
	_ core.Organizer            = (*Adapter)(nil)
	_ core.Retrier              = (*Adapter)(nil)
	_ core.ReadMarker           = (*Adapter)(nil)
	_ core.PresenceProvider     = (*Adapter)(nil)
	_ core.TypingSender         = (*Adapter)(nil)
)

// SetSleeper overrides how Send waits during its typing-notification
// choreography. Tests inject a fake that records durations without
// blocking.
func (a *Adapter) SetSleeper(sleep func(time.Duration)) { a.sleep = sleep }

// newAdapter wires an Adapter around an already-configured mautrix client.
// It is the shared constructor for New (production: session and crypto
// helper loaded from disk) and for tests (an httptest-backed client,
// optionally with a mautrix.CryptoHelper implementation standing in for
// cryptohelper.CryptoHelper so decrypt/encrypt can be exercised without a
// live homeserver's device/key-sharing endpoints).
func newAdapter(account string, client *mautrix.Client, cryptoHelper mautrix.CryptoHelper) *Adapter {
	if client.StateStore == nil {
		// Falls back to an in-memory store when no crypto helper supplied
		// one (cryptohelper.NewCryptoHelper installs a modernc-backed,
		// persisted StateStore itself when given a *dbutil.Database).
		client.StateStore = mautrix.NewMemoryStateStore()
	}
	// mautrix.Client.SendMessageEvent consults Crypto+StateStore itself to
	// decide whether to encrypt outgoing events, so wiring this is enough
	// to get "encrypt automatically when the room is encrypted" for free.
	if cryptoHelper != nil {
		client.Crypto = cryptoHelper
	}

	syncer := mautrix.NewDefaultSyncer()
	syncer.FilterJSON = SyncFilter()
	client.Syncer = syncer

	return &Adapter{
		account:        account,
		client:         client,
		crypto:         cryptoHelper,
		items:          make(map[string]core.Item),
		roomNames:      make(map[id.RoomID]string),
		canonicalAlias: make(map[id.RoomID]string),
		heroes:         make(map[id.RoomID][]id.UserID),
		memberNames:    make(map[id.RoomID]map[id.UserID]string),
		unread:         make(map[id.RoomID]int),
		typers:         make(map[id.RoomID][]string),
		sleep:          time.Sleep,
	}
}

// New builds the Matrix adapter for a configured account, loading the
// session saved by a prior SSOLogin and wiring a cryptohelper-managed,
// modernc-backed (pure Go, no CGO) crypto store. It never dials the
// homeserver itself; Run does that once the daemon actually starts
// syncing.
func New(acc config.Account) (core.Adapter, error) {
	stateDir := StateDirFor(acc)

	session, ok, err := LoadSession(stateDir)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("matrix: account %q has no saved session; run the SSO login flow first: %w", acc.Name, core.ErrUnsupported)
	}

	client, err := mautrix.NewClient(session.HomeserverURL, id.UserID(session.UserID), session.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("matrix: create client: %w", err)
	}
	client.DeviceID = id.DeviceID(session.DeviceID)

	db, err := OpenCryptoDatabase(filepath.Join(stateDir, "crypto.db"))
	if err != nil {
		return nil, err
	}
	pickleKey, err := loadOrCreatePickleKey(stateDir)
	if err != nil {
		return nil, err
	}
	helper, err := cryptohelper.NewCryptoHelper(client, pickleKey, db)
	if err != nil {
		return nil, fmt.Errorf("matrix: set up crypto helper: %w", err)
	}

	return newAdapter(acc.Name, client, helper), nil
}

// StateDirFor returns the directory an account's session, crypto store and
// pickle key are kept in: its "state_dir" option if set, else
// <config.StateDir()>/matrix/<account name>.
func StateDirFor(acc config.Account) string {
	if dir, ok := acc.Options["state_dir"].(string); ok && dir != "" {
		return dir
	}
	return filepath.Join(config.StateDir(), "matrix", acc.Name)
}

func homeserverFor(acc config.Account) (string, error) {
	hs, ok := acc.Options["homeserver"].(string)
	if !ok || hs == "" {
		return "", fmt.Errorf("matrix: account %q is missing the \"homeserver\" option", acc.Name)
	}
	return hs, nil
}

// LoginAccount runs SSOLogin against the account's configured homeserver
// and persists the resulting Session to its state directory, ready for a
// later New(acc).
func LoginAccount(ctx context.Context, acc config.Account, out io.Writer) (Session, error) {
	hs, err := homeserverFor(acc)
	if err != nil {
		return Session{}, err
	}
	session, err := SSOLogin(ctx, hs, out)
	if err != nil {
		return Session{}, err
	}
	if err := SaveSession(StateDirFor(acc), session); err != nil {
		return Session{}, err
	}
	return session, nil
}

const pickleKeyFileName = "pickle.key"

// loadOrCreatePickleKey returns the 32-byte key cryptohelper uses to
// pickle (encrypt at rest) the olm/megolm sessions in the crypto store,
// generating and persisting one with 0600 permissions on first use.
func loadOrCreatePickleKey(dir string) ([]byte, error) {
	path := filepath.Join(dir, pickleKeyFileName)
	data, err := os.ReadFile(path)
	if err == nil {
		return data, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("matrix: read pickle key: %w", err)
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("matrix: generate pickle key: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("matrix: create state dir %s: %w", dir, err)
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, fmt.Errorf("matrix: write pickle key: %w", err)
	}
	return key, nil
}

func (a *Adapter) Channel() core.Channel { return core.ChannelMatrix }

func (a *Adapter) Account() string { return a.account }

// Run drives the /sync loop: it uploads (or resumes) the CRITICAL minimal
// filter from SyncFilter, persists the filter ID and next-batch cursor
// through sink so a daemon restart resumes instead of re-syncing from
// scratch, maps m.room.message and (decrypted or undecryptable)
// m.room.encrypted events into Items, and upserts them through sink.
func (a *Adapter) Run(ctx context.Context, sink core.Sink) error {
	a.mu.Lock()
	a.sink = sink
	a.mu.Unlock()

	if err := a.initCrypto(ctx); err != nil {
		return err
	}

	a.client.Store = &sinkSyncStore{sink: sink, account: a.account}

	syncer, ok := a.client.Syncer.(*mautrix.DefaultSyncer)
	if !ok {
		return fmt.Errorf("matrix: unexpected syncer type %T", a.client.Syncer)
	}

	syncer.OnSync(a.captureUnreadCounts)
	syncer.OnEventType(event.StateRoomName, a.handleRoomName)
	syncer.OnEventType(event.StateCanonicalAlias, a.handleCanonicalAlias)
	syncer.OnEventType(event.StateMember, a.handleMemberEvent)
	syncer.OnEventType(event.StateEncryption, a.handleEncryptionState)
	syncer.OnEventType(event.EventMessage, a.messageHandler(sink))
	syncer.OnEventType(event.EventEncrypted, a.encryptedHandler(sink))
	syncer.OnEventType(event.EphemeralEventTyping, a.handleTyping)
	syncer.OnEventType(event.EphemeralEventReceipt, a.receiptHandler(sink))
	syncer.OnEventType(event.AccountDataFullyRead, a.fullyReadHandler(sink))

	if err := a.client.SyncWithContext(ctx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("matrix: sync: %w", err)
	}
	return nil
}

// sinkSyncStore persists the mautrix sync filter ID and next-batch token
// through a core.Sink's cursor storage, so they survive daemon restarts
// instead of living only in mautrix's in-memory MemorySyncStore.
type sinkSyncStore struct {
	sink    core.Sink
	account string
}

func (s *sinkSyncStore) cursorKey(name string) string {
	return fmt.Sprintf("matrix:%s:%s", s.account, name)
}

func (s *sinkSyncStore) SaveFilterID(ctx context.Context, _ id.UserID, filterID string) error {
	return s.sink.SetCursor(ctx, s.cursorKey("filter_id"), filterID)
}

func (s *sinkSyncStore) LoadFilterID(ctx context.Context, _ id.UserID) (string, error) {
	return s.sink.Cursor(ctx, s.cursorKey("filter_id"))
}

func (s *sinkSyncStore) SaveNextBatch(ctx context.Context, _ id.UserID, nextBatch string) error {
	return s.sink.SetCursor(ctx, s.cursorKey("since"), nextBatch)
}

func (s *sinkSyncStore) LoadNextBatch(ctx context.Context, _ id.UserID) (string, error) {
	return s.sink.Cursor(ctx, s.cursorKey("since"))
}

var _ mautrix.SyncStore = (*sinkSyncStore)(nil)

func (a *Adapter) captureUnreadCounts(_ context.Context, resp *mautrix.RespSync, _ string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for roomID, room := range resp.Rooms.Join {
		if room.UnreadNotifications != nil {
			a.unread[roomID] = room.UnreadNotifications.NotificationCount
		}
		if len(room.Summary.Heroes) > 0 {
			a.heroes[roomID] = room.Summary.Heroes
		}
	}
	return true
}

func (a *Adapter) handleRoomName(_ context.Context, evt *event.Event) {
	name := evt.Content.AsRoomName().Name
	if name == "" {
		return
	}
	a.mu.Lock()
	a.roomNames[evt.RoomID] = name
	a.mu.Unlock()
}

// handleCanonicalAlias tracks m.room.canonical_alias, the room-name
// fallback used when a room has no explicit m.room.name.
func (a *Adapter) handleCanonicalAlias(_ context.Context, evt *event.Event) {
	alias := evt.Content.AsCanonicalAlias().Alias
	if alias == "" {
		return
	}
	a.mu.Lock()
	a.canonicalAlias[evt.RoomID] = alias.String()
	a.mu.Unlock()
}

// handleMemberEvent tracks m.room.member displayname per room, used to
// resolve a sender's From.Name and a DM room's hero-based ThreadName.
func (a *Adapter) handleMemberEvent(_ context.Context, evt *event.Event) {
	content := evt.Content.AsMember()
	if content.Displayname == "" || evt.StateKey == nil {
		return
	}
	userID := id.UserID(*evt.StateKey)
	a.mu.Lock()
	if a.memberNames[evt.RoomID] == nil {
		a.memberNames[evt.RoomID] = make(map[id.UserID]string)
	}
	a.memberNames[evt.RoomID][userID] = content.Displayname
	a.mu.Unlock()
}

func (a *Adapter) handleEncryptionState(ctx context.Context, evt *event.Event) {
	content := evt.Content.AsEncryption()
	if err := a.client.StateStore.SetEncryptionEvent(ctx, evt.RoomID, content); err != nil {
		log.Printf("matrix: record encryption state for %s: %v", evt.RoomID, err)
	}
}

func (a *Adapter) messageHandler(sink core.Sink) mautrix.EventHandler {
	return func(ctx context.Context, evt *event.Event) {
		item := a.toItem(evt)
		a.remember(item)
		a.persistMediaDescriptor(ctx, sink, item, evt.Content.AsMessage())
		if err := sink.Upsert(ctx, item); err != nil {
			log.Printf("matrix: upsert %s: %v", item.ID, err)
		}
	}
}

// encryptedHandler decrypts m.room.encrypted events when a crypto helper
// is wired. A session that cannot be decrypted (not yet shared, or no
// crypto helper at all) is never dropped or allowed to crash the sync
// loop: it is stored as an Item with Meta["undecryptable"]="true" instead.
func (a *Adapter) encryptedHandler(sink core.Sink) mautrix.EventHandler {
	return func(ctx context.Context, evt *event.Event) {
		item := a.toItem(evt)
		var decryptedContent *event.MessageEventContent
		if a.crypto != nil {
			if decrypted, err := a.crypto.Decrypt(ctx, evt); err == nil {
				item = a.toItem(decrypted)
				decryptedContent = decrypted.Content.AsMessage()
			}
		}
		a.remember(item)
		a.persistMediaDescriptor(ctx, sink, item, decryptedContent)
		if err := sink.Upsert(ctx, item); err != nil {
			log.Printf("matrix: upsert %s: %v", item.ID, err)
		}
	}
}

func (a *Adapter) toItem(evt *event.Event) core.Item {
	a.mu.Lock()
	roomName := a.resolveRoomNameLocked(evt.RoomID)
	fromName := a.displayNameForLocked(evt.RoomID, evt.Sender)
	unreadCount := a.unread[evt.RoomID]
	a.mu.Unlock()

	item := core.Item{
		ID:         itemID(a.account, evt.RoomID, evt.ID),
		Channel:    core.ChannelMatrix,
		Account:    a.account,
		Thread:     evt.RoomID.String(),
		ThreadName: roomName,
		From:       core.Address{ID: evt.Sender.String(), Name: fromName},
		Timestamp:  time.UnixMilli(evt.Timestamp),
		Unread:     unreadCount > 0 && evt.Sender != a.client.UserID,
		FromMe:     evt.Sender == a.client.UserID,
		Meta:       map[string]string{},
	}

	switch evt.Type {
	case event.EventEncrypted:
		item.Meta["undecryptable"] = "true"
	default:
		content := evt.Content.AsMessage()
		item.Body = content.Body
		if att, ok := attachmentFromContent(content); ok {
			item.Attachments = append(item.Attachments, att)
		}
	}
	return item
}

// displayNameForLocked resolves userID's display name in roomID from the
// m.room.member cache, falling back to the raw MXID when no displayname
// is known yet (a fresh room, or a member the server never sent under
// lazy-loading). Callers must hold a.mu.
func (a *Adapter) displayNameForLocked(roomID id.RoomID, userID id.UserID) string {
	if name, ok := a.memberNames[roomID][userID]; ok && name != "" {
		return name
	}
	return userID.String()
}

// resolveRoomNameLocked picks ThreadName in order: the explicit
// m.room.name, else the canonical alias, else DM heroes' resolved
// display names (joined), else the raw room ID. Callers must hold a.mu.
func (a *Adapter) resolveRoomNameLocked(roomID id.RoomID) string {
	if name := a.roomNames[roomID]; name != "" {
		return name
	}
	if alias := a.canonicalAlias[roomID]; alias != "" {
		return alias
	}
	if heroes := a.heroes[roomID]; len(heroes) > 0 {
		names := make([]string, len(heroes))
		for i, hero := range heroes {
			names[i] = a.displayNameForLocked(roomID, hero)
		}
		return strings.Join(names, ", ")
	}
	return roomID.String()
}

func (a *Adapter) remember(item core.Item) {
	a.mu.Lock()
	a.items[item.ID] = item
	a.mu.Unlock()
}

// cachedItem returns the item last observed for itemIDStr, without the
// core.ErrNotFound wrapping Fetch applies (callers here treat "unknown"
// as just another reason to fall back, not an error to report).
func (a *Adapter) cachedItem(itemIDStr string) (core.Item, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	item, ok := a.items[itemIDStr]
	return item, ok
}

// referencedEventTimestamp resolves the timestamp to mark a thread read
// up to for whichever event a receipt or m.fully_read points to: the
// referenced event's own Timestamp when this adapter has seen it (the
// precise "read up to this specific message" instant), else fallback —
// the receipt's own ts for m.receipt, or "now" for m.fully_read, which
// carries no timestamp of its own at all.
func (a *Adapter) referencedEventTimestamp(roomID id.RoomID, eventID id.EventID, fallback time.Time) time.Time {
	if cached, ok := a.cachedItem(itemID(a.account, roomID, eventID)); ok && !cached.Timestamp.IsZero() {
		return cached.Timestamp
	}
	return fallback
}

// receiptHandler processes m.receipt ephemeral events (own m.read/
// m.read.private receipts from other devices/clients, e.g. Element):
// only entries for THIS account's own user id ever affect our unread
// state ("own user only" — another participant's receipt into the same
// room says nothing about what the user has read) and mark their room's
// thread read up to the referenced event's timestamp.
func (a *Adapter) receiptHandler(sink core.Sink) mautrix.EventHandler {
	return func(ctx context.Context, evt *event.Event) {
		ownUser := a.client.UserID
		for eventID, receipts := range *evt.Content.AsReceipt() {
			for _, receiptType := range []event.ReceiptType{event.ReceiptTypeRead, event.ReceiptTypeReadPrivate} {
				receipt, ok := receipts[receiptType][ownUser]
				if !ok {
					continue
				}
				upTo := a.referencedEventTimestamp(evt.RoomID, eventID, receipt.Timestamp)
				if err := sink.MarkThreadReadUpTo(ctx, core.ChannelMatrix, a.account, evt.RoomID.String(), upTo); err != nil {
					log.Printf("matrix: mark thread %s read up to %v: %v", evt.RoomID, upTo, err)
				}
			}
		}
	}
}

// fullyReadHandler processes the m.fully_read room account data event
// (the "read marker" other clients, e.g. Element, advance): it marks the
// room's thread read up to the referenced event's timestamp, the same
// way receiptHandler does. m.fully_read is inherently per-account (it is
// account data, never shared with other room members), so there is no
// "own user only" check to apply here.
func (a *Adapter) fullyReadHandler(sink core.Sink) mautrix.EventHandler {
	return func(ctx context.Context, evt *event.Event) {
		eventID := evt.Content.AsFullyRead().EventID
		if eventID == "" {
			return
		}
		upTo := a.referencedEventTimestamp(evt.RoomID, eventID, time.Now())
		if err := sink.MarkThreadReadUpTo(ctx, core.ChannelMatrix, a.account, evt.RoomID.String(), upTo); err != nil {
			log.Printf("matrix: mark thread %s read up to %v (fully_read): %v", evt.RoomID, upTo, err)
		}
	}
}

// Fetch returns the item as last observed by Run; the Matrix adapter
// never re-fetches from the homeserver, it only serves what it already
// synced.
func (a *Adapter) Fetch(_ context.Context, itemIDStr string) (core.Item, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	item, ok := a.items[itemIDStr]
	if !ok {
		return core.Item{}, fmt.Errorf("matrix: fetch %s: %w", itemIDStr, core.ErrNotFound)
	}
	return item, nil
}

// Send delivers text to a room (out.Thread, falling back to out.To[0]:
// either a room ID or a #alias:server), setting m.relates_to/m.in_reply_to
// when out.ReplyTo names the item being replied to. mautrix.Client
// encrypts automatically when the target room is encrypted.
func (a *Adapter) Send(ctx context.Context, out core.Outgoing) (core.Receipt, error) {
	roomID, err := a.resolveRoom(ctx, out)
	if err != nil {
		return core.Receipt{}, err
	}

	content := &event.MessageEventContent{MsgType: event.MsgText, Body: out.Body}
	if out.ReplyTo != "" {
		_, _, replyEventID, err := parseItemID(out.ReplyTo)
		if err != nil {
			return core.Receipt{}, fmt.Errorf("matrix: reply target: %w", err)
		}
		content.RelatesTo = &event.RelatesTo{InReplyTo: &event.InReplyTo{EventID: replyEventID}}
	}

	// Typing notification before delivering (T13d): proportional to the
	// text, no presence (Matrix has none here).
	duration := typingDuration(len(out.Body))
	if _, err := a.client.UserTyping(ctx, roomID, true, duration); err != nil {
		return core.Receipt{}, fmt.Errorf("matrix: send to %s: typing on: %w", roomID, err)
	}
	a.sleep(duration)
	if _, err := a.client.UserTyping(ctx, roomID, false, 0); err != nil {
		return core.Receipt{}, fmt.Errorf("matrix: send to %s: typing off: %w", roomID, err)
	}

	resp, err := a.client.SendMessageEvent(ctx, roomID, event.EventMessage, content)
	if err != nil {
		return core.Receipt{}, fmt.Errorf("matrix: send to %s: %w", roomID, err)
	}
	return core.Receipt{ID: resp.EventID.String(), Channel: core.ChannelMatrix, At: time.Now()}, nil
}

func (a *Adapter) resolveRoom(ctx context.Context, out core.Outgoing) (id.RoomID, error) {
	if len(out.To) > 1 || len(out.Cc) > 0 {
		return "", fmt.Errorf("matrix: send: only one room is supported, got %d To and %d Cc: %w", len(out.To), len(out.Cc), core.ErrUnsupported)
	}
	target := out.Thread
	if target == "" && len(out.To) > 0 {
		target = out.To[0]
	}
	if target == "" {
		return "", fmt.Errorf("matrix: send: no room or thread given")
	}
	if strings.HasPrefix(target, "#") {
		resp, err := a.client.ResolveAlias(ctx, id.RoomAlias(target))
		if err != nil {
			return "", fmt.Errorf("matrix: resolve alias %s: %w", target, err)
		}
		return resp.RoomID, nil
	}
	return id.RoomID(target), nil
}

// Organize supports only read-state changes (op.Seen): Matrix has no
// folders and bunker-go's labels do not map onto it. Any label/folder
// mutation is rejected with core.ErrUnsupported instead of being silently
// dropped.
func (a *Adapter) Organize(ctx context.Context, itemIDStr string, op core.OrganizeOp) error {
	if len(op.AddLabels) > 0 || len(op.RemoveLabels) > 0 || op.MoveTo != "" {
		return fmt.Errorf("matrix: labels and folders are not supported: %w", core.ErrUnsupported)
	}
	if op.Seen == nil || !*op.Seen {
		return nil
	}
	_, roomID, eventID, err := parseItemID(itemIDStr)
	if err != nil {
		return err
	}
	marker := &mautrix.ReqSetReadMarkers{Read: eventID, FullyRead: eventID}
	if err := a.client.SetReadMarkers(ctx, roomID, marker); err != nil {
		return fmt.Errorf("matrix: set read marker for %s: %w", itemIDStr, err)
	}
	return nil
}

// initCrypto runs a.crypto.Init exactly once, whether it is Run's sync
// loop or RetryUndecryptable that gets there first: cryptohelper.Init is
// not meant to run twice on the same client (it re-registers syncer
// handlers and re-loads the olm account).
func (a *Adapter) initCrypto(ctx context.Context) error {
	if a.crypto == nil {
		return nil
	}
	a.cryptoInitOnce.Do(func() {
		a.cryptoInitErr = a.crypto.Init(ctx)
	})
	if a.cryptoInitErr != nil {
		return fmt.Errorf("matrix: init crypto: %w", a.cryptoInitErr)
	}
	return nil
}

// RetryUndecryptable re-fetches and attempts to decrypt every item this
// account previously stored with Meta["undecryptable"]="true" (room and
// event IDs are recovered from the item ID itself; the original
// ciphertext was never persisted). It is meant to run once at daemon
// startup and again right after a recovery-key or key-export import, so
// history that was undecryptable before now gets a second chance once
// megolm sessions become available. An item that still cannot be
// decrypted (homeserver unreachable, event gone, or genuinely no session
// yet) is left exactly as it was: never dropped, never overwritten with
// an empty body.
func (a *Adapter) RetryUndecryptable(ctx context.Context, store core.Store) error {
	if a.crypto == nil {
		return nil // no E2EE wired for this account: nothing to retry
	}
	if err := a.initCrypto(ctx); err != nil {
		return err
	}

	items, err := store.List(ctx, core.Filter{Channel: core.ChannelMatrix, Account: a.account})
	if err != nil {
		return fmt.Errorf("matrix: list items to retry: %w", err)
	}

	for _, item := range items {
		if item.Meta["undecryptable"] != "true" {
			continue
		}
		if err := a.retryOne(ctx, store, item.ID); err != nil {
			log.Printf("matrix: retry undecryptable %s: %v", item.ID, err)
		}
	}
	return nil
}

// retryOne re-fetches and, if possible, decrypts one previously
// undecryptable item, upserting it in place on success. Any failure to
// re-fetch or decrypt is left for a later retry, never an error the
// caller must stop for.
func (a *Adapter) retryOne(ctx context.Context, store core.Store, itemIDStr string) error {
	_, roomID, eventID, err := parseItemID(itemIDStr)
	if err != nil {
		return fmt.Errorf("parse item id: %w", err)
	}

	evt, err := a.client.GetEvent(ctx, roomID, eventID)
	if err != nil {
		return fmt.Errorf("refetch event: %w", err)
	}
	evt.RoomID = roomID
	if err := evt.Content.ParseRaw(evt.Type); err != nil {
		return fmt.Errorf("parse refetched event content: %w", err)
	}
	if evt.Type != event.EventEncrypted {
		return nil // no longer an encrypted event; leave it be
	}

	decrypted, err := a.crypto.Decrypt(ctx, evt)
	if err != nil {
		return nil // still no session: try again on the next retry, not an error
	}

	item := a.toItem(decrypted)
	a.remember(item)
	if err := store.Upsert(ctx, item); err != nil {
		return fmt.Errorf("upsert retried item: %w", err)
	}
	return nil
}

// itemID builds the stable "matrix:<account>:<roomID>/<eventID>" id.
func itemID(account string, roomID id.RoomID, eventID id.EventID) string {
	return fmt.Sprintf("matrix:%s:%s/%s", account, roomID, eventID)
}

// parseItemID reverses itemID.
func parseItemID(raw string) (account string, roomID id.RoomID, eventID id.EventID, err error) {
	rest, ok := strings.CutPrefix(raw, "matrix:")
	if !ok {
		return "", "", "", fmt.Errorf("matrix: item id %q missing the matrix: prefix", raw)
	}
	account, rest, ok = strings.Cut(rest, ":")
	if !ok {
		return "", "", "", fmt.Errorf("matrix: malformed item id %q", raw)
	}
	roomPart, eventPart, ok := strings.Cut(rest, "/")
	if !ok {
		return "", "", "", fmt.Errorf("matrix: malformed item id %q", raw)
	}
	return account, id.RoomID(roomPart), id.EventID(eventPart), nil
}
