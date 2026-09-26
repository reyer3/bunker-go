package whatsapp

import (
	"context"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// fakeWAClient is an in-memory waClient used by every test in this
// package. It never touches the network.
type fakeWAClient struct {
	mu sync.Mutex

	linked    bool
	ownJID    types.JID
	connected bool

	connectErr    error
	sendErr       error
	sendResp      whatsmeow.SendResponse
	isOnWAResults []types.IsOnWhatsAppResponse
	isOnWAErr     error
	downloadData  []byte
	downloadErr   error
	lastDownload  whatsmeow.DownloadableMessage
	uploadResp    whatsmeow.UploadResponse
	uploadErr     error
	uploadedType  whatsmeow.MediaType
	qrChan        chan whatsmeow.QRChannelItem
	qrErr         error

	profilePicInfo    *types.ProfilePictureInfo
	profilePicErr     error
	profilePicHang    bool // block until ctx is done, like an unanswered IQ
	profilePicCalls   []types.JID
	profilePicPreview []bool

	handlers   map[uint32]whatsmeow.EventHandler
	nextHandle uint32

	sent       []sentMessage
	markedRead []markReadCall

	// calls records every waClient method this fake observed, in order,
	// as a short tag (e.g. "presence:available", "chatpresence:composing",
	// "send", "markread", "presence:unavailable"), so tests can assert
	// the exact human-emulation call order (T13b/c) without inspecting
	// each typed field separately.
	calls []string
}

type sentMessage struct {
	to      types.JID
	message *waE2E.Message
}

type markReadCall struct {
	ids    []types.MessageID
	chat   types.JID
	sender types.JID
}

func newFakeWAClient() *fakeWAClient {
	return &fakeWAClient{handlers: make(map[uint32]whatsmeow.EventHandler)}
}

// newTestAdapter builds an Adapter exactly like NewAdapter, but with an
// instant sleeper and a fixed (non-jittering) rand source, so a test that
// does not care about T13's human-emulation timing never blocks on the
// real 2-15s composing/typing windows or 3-8s fan-out pause. A test that
// DOES care about timing overrides SetSleeper/SetRand01 itself after
// construction.
func newTestAdapter(account string, cli waClient, minSendInterval ...time.Duration) *Adapter {
	a := NewAdapter(account, cli, minSendInterval...)
	a.SetSleeper(func(time.Duration) {})
	a.SetRand01(func() float64 { return 0.5 })
	return a
}

func (f *fakeWAClient) IsLinked() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.linked }
func (f *fakeWAClient) OwnJID() types.JID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ownJID
}

func (f *fakeWAClient) Connect() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.connectErr != nil {
		return f.connectErr
	}
	f.connected = true
	return nil
}

func (f *fakeWAClient) Disconnect() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connected = false
}

func (f *fakeWAClient) IsConnected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected
}

func (f *fakeWAClient) IsLoggedIn() bool { return f.IsConnected() && f.IsLinked() }

func (f *fakeWAClient) AddEventHandler(handler whatsmeow.EventHandler) uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextHandle++
	f.handlers[f.nextHandle] = handler
	return f.nextHandle
}

func (f *fakeWAClient) RemoveEventHandler(id uint32) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.handlers[id]; !ok {
		return false
	}
	delete(f.handlers, id)
	return true
}

// emit dispatches evt to every registered handler, as whatsmeow would.
func (f *fakeWAClient) emit(evt any) {
	f.mu.Lock()
	handlers := make([]whatsmeow.EventHandler, 0, len(f.handlers))
	for _, h := range f.handlers {
		handlers = append(handlers, h)
	}
	f.mu.Unlock()
	for _, h := range handlers {
		h(evt)
	}
}

func (f *fakeWAClient) SendMessage(_ context.Context, to types.JID, message *waE2E.Message, _ ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "send")
	if f.sendErr != nil {
		return whatsmeow.SendResponse{}, f.sendErr
	}
	f.sent = append(f.sent, sentMessage{to: to, message: message})
	resp := f.sendResp
	if resp.Timestamp.IsZero() {
		resp.Timestamp = time.Now()
	}
	if resp.ID == "" {
		resp.ID = "FAKE-MSG-ID"
	}
	return resp, nil
}

func (f *fakeWAClient) IsOnWhatsApp(_ context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.isOnWAErr != nil {
		return nil, f.isOnWAErr
	}
	return f.isOnWAResults, nil
}

func (f *fakeWAClient) MarkRead(_ context.Context, ids []types.MessageID, _ time.Time, chat, sender types.JID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "markread")
	f.markedRead = append(f.markedRead, markReadCall{ids: ids, chat: chat, sender: sender})
	return nil
}

func (f *fakeWAClient) SendPresence(_ context.Context, state types.Presence) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "presence:"+string(state))
	return nil
}

func (f *fakeWAClient) SendChatPresence(_ context.Context, _ types.JID, state types.ChatPresence, _ types.ChatPresenceMedia) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "chatpresence:"+string(state))
	return nil
}

// callLog returns every recorded call, in order, safe for concurrent use.
func (f *fakeWAClient) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *fakeWAClient) Download(_ context.Context, msg whatsmeow.DownloadableMessage) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastDownload = msg
	return f.downloadData, f.downloadErr
}

func (f *fakeWAClient) Upload(_ context.Context, _ []byte, mediaType whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploadedType = mediaType
	return f.uploadResp, f.uploadErr
}

func (f *fakeWAClient) GetQRChannel(_ context.Context) (<-chan whatsmeow.QRChannelItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.qrErr != nil {
		return nil, f.qrErr
	}
	return f.qrChan, nil
}

func (f *fakeWAClient) GetProfilePictureInfo(ctx context.Context, jid types.JID, params *whatsmeow.GetProfilePictureParams) (*types.ProfilePictureInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.profilePicCalls = append(f.profilePicCalls, jid)
	f.profilePicPreview = append(f.profilePicPreview, params != nil && params.Preview)
	if f.profilePicHang {
		f.mu.Unlock()
		<-ctx.Done()
		f.mu.Lock()
		return nil, ctx.Err()
	}
	if f.profilePicErr != nil {
		return nil, f.profilePicErr
	}
	return f.profilePicInfo, nil
}

// ParseWebMessage is a simplified stand-in for *whatsmeow.Client's real
// method: it builds the *events.Message a test's assertions care about
// (chat, sender, ID, push name, timestamp, the raw message) directly from
// the synthetic WebMessageInfo the test constructed, without replicating
// the real client's raw-message unwrapping or own-JID lookups.
func (f *fakeWAClient) ParseWebMessage(chatJID types.JID, webMsg *waWeb.WebMessageInfo) (*events.Message, error) {
	sender := chatJID
	if p := webMsg.GetParticipant(); p != "" {
		if jid, err := types.ParseJID(p); err == nil {
			sender = jid
		}
	}
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     chatJID,
				Sender:   sender,
				IsFromMe: webMsg.GetKey().GetFromMe(),
				IsGroup:  chatJID.Server == types.GroupServer,
			},
			ID:        types.MessageID(webMsg.GetKey().GetID()),
			PushName:  webMsg.GetPushName(),
			Timestamp: time.Unix(int64(webMsg.GetMessageTimestamp()), 0),
		},
		Message: webMsg.GetMessage(),
	}, nil
}
