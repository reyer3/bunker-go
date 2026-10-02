package tui

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/reyer3/bunker-go/internal/config"
	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/kittygfx"
	"github.com/reyer3/bunker-go/internal/style"
)

// Model is the interactive shell. Its inbox is a bounded snapshot, not a
// complete conversation history.
type Model struct {
	client Client
	groups []inboxGroup
	// chats are the WhatsApp and Matrix tabs' conversation lists (see
	// chatlist.go), read ones included; groups stays the unread snapshot
	// that counts and notifications are built from.
	chats map[core.Channel][]inboxGroup
	// mailExpanded is mail-sender-groups.md's collapse/expand state for
	// the Mail section's sender rows, keyed by senderKey (the lower-
	// cased From address): true once a sender's threads have been
	// expanded. It is never touched by a poll (see the inboxLoadedMsg
	// handler in update.go), so it survives polls; it only ever resets
	// by process exit (a fresh Model on the next run), matching the
	// doc's "resets on quit."
	mailExpanded map[string]bool
	counts       map[core.Channel]map[string]int
	selected     int
	loaded       bool
	loadErr      error
	// loadedAt is when the inbox last loaded successfully, so the status
	// line can say how old the data is while the daemon is unreachable.
	loadedAt time.Time
	// adapterHealth is the daemon's last adapter health snapshot.
	adapterHealth []core.AdapterHealth
	detail        bool
	reading       bool
	readID        string
	readItem      core.Item
	readErr       error
	readToken     uint64
	// detailScroll is the index of the first visible line within the
	// plain single-item detail view's scrollable body (Subject/From/
	// Channel stay fixed above it, "Esc to inbox · q to quit" fixed
	// below), the same fitInbox-style contract the chat/thread views'
	// own chatScroll/threadScroll give a long body: j/k, arrows,
	// PgUp/PgDown, "G" and the mouse wheel scroll it; it resets to 0
	// whenever a new item is opened (see openItem).
	detailScroll   int
	width          int
	height         int
	polling        bool
	refreshPending bool
	pollToken      uint64
	composing      bool
	previewing     bool
	sending        bool
	quitConfirm    bool
	draftID        string
	composer       textarea.Model
	attachments    []string
	attaching      bool
	attachInput    string
	previewPlan    core.Plan
	replyErr       error
	replyToken     uint64
	marking        bool
	markLoading    bool
	markConfirm    bool
	markSending    bool
	markID         string
	markPlan       core.Plan
	markErr        error
	markToken      uint64
	activeTab      int
	tabSelected    [numTabs]int
	helpOpen       bool
	// helpCtx and helpScroll are the help overlay's section (the view
	// it was opened from) and scroll offset (issue #36).
	helpCtx    string
	helpScroll int
	// readUndo, flash/flashAt and drafts back issue #37: the mark-reads u
	// can undo, the status line's short notice, and the per-target drafts
	// kept for the session (see undo.go).
	readUndo   []string
	flash      string
	flashAt    time.Time
	drafts     map[string]string
	mailDrafts map[string]mailDraft
	// unreadOnOpen is the item that opening the current chat or mail
	// thread marks read, remembered for u once the mark succeeds.
	unreadOnOpen string
	// filterQuery narrows the inbox; filtering is true while it is typed
	// (issue #39, filter.go).
	filterQuery string
	filtering   bool
	// filterErr is the typed filter's parse error, shown on the filter
	// line without touching the text; filterIsQuery is true when it
	// holds an operator (issue #62, query.go).
	filterErr     error
	filterIsQuery bool
	// Daemon query results (issue #62, query.go): while queryActive, the
	// sections show queryGroups (every loaded page of queryText's
	// matches, grouped by conversation) instead of the inbox groups.
	// queryCursor resumes the next page ("" when there is none),
	// queryToken discards a stale page and queryDebounceToken a stale
	// debounce tick. queryNoPaging remembers that the daemon cannot page
	// queries, so the filter stays in memory for the session.
	queryActive        bool
	queryText          string
	queryItems         []core.Item
	queryGroups        []inboxGroup
	queryCursor        string
	queryLoading       bool
	queryErr           error
	queryToken         uint64
	queryDebounceToken uint64
	queryNoPaging      bool
	// folderLayouts is each mail account's folder prefix and separator
	// (folder.go), keyed by account name.
	folderLayouts map[string]folderLayout

	glyphs          map[core.Channel]string
	render          *lipgloss.Renderer
	now             func() time.Time
	blurred         bool
	notifyEnabled   bool
	notifyWriter    io.Writer
	tmuxPassthrough bool
	lastNotifyAt    time.Time
	pendingNotify   int

	// Inline images (issue #4, media.go): gfx is the terminal's graphics
	// support, gfxOut the locked writer image uploads go through, media
	// the per-session cache of uploaded images (shared across Model
	// copies), mediaDir where attachment bytes are cached on disk, and
	// viewer the full-size overlay (nil when closed).
	gfx      kittygfx.Mode
	gfxOut   io.Writer
	media    *mediaCache
	mediaDir string
	viewer   *imageViewer
	// mediaErr is the last video/preview failure (issue #6), shown in
	// the chat tail and the viewer; getenv overrides os.Getenv in tests.
	mediaErr error
	getenv   func(string) string

	// Chat view (K5): opening a WhatsApp/Matrix conversation sets
	// detail=true and chatMode=true instead of the plain single-item
	// detail view.
	chatMode    bool
	chatChannel core.Channel
	chatAccount string
	chatThread  string
	chatDraftID string
	// chatNewTo is the address of a chat opened from the contact picker
	// (issue #26): with no item to reply to, the draft is sent to it.
	chatNewTo string
	// picker is the "n" contact picker, nil when closed.
	picker *contactPicker
	// palette is the Ctrl+K command palette (palette.go), nil when closed.
	palette *commandPalette
	// chatName is the header's contact/group display name (K7), resolved
	// once at open time the same way an inbox row's title is (rowTitle):
	// ThreadName, then Subject, then the opening item's sender — never a
	// raw protocol identifier.
	chatName        string
	chatItems       []core.Item
	chatLoading     bool
	chatLoadErr     error
	chatToken       uint64
	chatPresence    core.Presence
	chatPresenceErr error
	chatConfirm     bool
	chatPlan        core.Plan
	chatSending     bool
	chatSendErr     error
	chatReplyToken  uint64
	// chatPreviewPending is true from the moment Enter requests a dry-run
	// preview until chatReplyPreviewMsg resolves (K10): every key is
	// ignored while it is true (mirroring chatSending's own guard), so a
	// repeated Enter typed while the round trip is still in flight can
	// never reissue the preview or bump chatReplyToken — the root cause
	// of the reported "tengo que dar como 4 enters" bug, where an
	// impatient extra Enter discarded the in-flight token before its
	// reply ever landed.
	chatPreviewPending bool
	// chatOptimistic is the K10 optimistic own bubble shown the instant
	// the user confirms a send, before the real send even returns: nil
	// when no optimistic bubble is pending. It is cleared once the
	// reloaded thread (chatSendReloadMsg) contains an item whose ID
	// matches the real send's receipt ID (chatOptimistic.id) — the
	// stored FromMe item then renders in its place, deduplicated.
	chatOptimistic *chatOptimisticMsg
	// chatAction is the edit, delete or reaction on screen (see
	// chat_actions.go); chatEditID is our message being edited in the
	// composer, with the draft set aside for it in chatEditDraft.
	chatAction      *chatAction
	chatActionToken uint64
	chatEditID      string
	chatEditDraft   string
	// emojiSel is the highlighted entry of the emoji completion list
	// (emoji.go), and emojiDismissed the draft Esc dismissed it on: the
	// list stays hidden until the draft changes again.
	// chatAttachments are the files the next chat send carries (issue
	// #5, chat_attach.go), chatTempFiles the ones pasted from the
	// clipboard (deleted once sent or dropped), chatAttachErr the last
	// attach failure, and clipboard reads pasted images (nil disables
	// Ctrl+V image paste).
	chatAttachments []string
	chatTempFiles   []string
	chatAttachErr   error
	clipboard       clipboardReader
	emojiSel        int
	emojiDismissed  string
	chatTypingAt    time.Time
	chatTypingOn    bool
	// confirmChatSend ([tui] confirm_chat_send) keeps the two-step chat
	// send for plain text; chatAutoSend marks the preview in flight as
	// one that sends itself when it comes back clean (chat_ux.go).
	confirmChatSend bool
	chatAutoSend    bool
	// chatFocus is the ID of the message a click selected (Alt+Y copies
	// it, Alt+O opens its attachment); "" selects the newest.
	chatFocus string
	// selectMode is true while the mouse is released so the terminal
	// selects text natively (F7/Alt+S).
	selectMode bool
	// lastClickKey/lastClickAt back double-click detection by the model
	// clock; copier and openFile replace the real clipboard and opener
	// in tests.
	lastClickKey string
	lastClickAt  time.Time
	copier       *textCopier
	openFile     func(argv []string) error
	// Voice notes (voice.go, voice_record.go): voiceRecordCmds are the
	// accounts' voice_record_command (keyed "channel/account"), voiceRec
	// the recording in progress and voicePlay the note being fetched or
	// played (both nil when idle). chatVoice marks chatAttachments as one
	// recorded voice note of chatVoiceDur, sent as a voice note instead
	// of a file.
	voiceRecordCmds map[string][]string
	voiceRec        *voiceRecording
	voicePlay       *voicePlayback
	chatVoice       bool
	chatVoiceDur    time.Duration
	// chatScroll is how many lines the chat body's rendered window is
	// scrolled up from the bottom (0 = pinned to the newest message,
	// bottom-aligned just above the composer — the fitInbox-style height
	// contract PgUp/mouse wheel scroll against; see chat_view.go). Measured
	// from the bottom, not the top, so prepending an older loaded page
	// never shifts what is currently on screen.
	chatScroll int

	// Mail thread view (K6): opening a mail item sets detail=true and
	// threadMode=true instead of the plain single-item detail view.
	threadMode     bool
	threadChannel  core.Channel
	threadAccount  string
	threadKey      string
	threadItems    []core.Item
	threadExpanded map[int]bool
	threadSelected int
	threadLoading  bool
	threadLoadErr  error
	threadSeenErr  error
	threadToken    uint64
	// threadSubject is K8's bold title line, resolved once at open time
	// from the opening item's own Subject (the newest item in the
	// conversation, which shares the same Subject once mail threading
	// normalizes "Re: "/"Fwd: " prefixes).
	threadSubject string
	// threadFolder is the opening item's short mail folder name
	// (folder.go), shown dimmed after the subject; "" for INBOX/Sent.
	threadFolder string
	// threadBodies/threadBodyLoading/threadBodyErr back K8's body-fetch
	// cache: mail sync stores headers only (conversation-view.md's
	// reported bug — a synced Item.Body is empty until fetched), so an
	// expanded message's full body is fetched on demand via
	// Client.Read(id, receipt=false) and cached per id so re-expanding an
	// already-fetched message never re-fetches it. Reset to fresh empty
	// maps every time a new thread is opened (see openThread).
	threadBodies      map[string]string
	threadBodyLoading map[string]bool
	threadBodyErr     error
	// threadScroll is the index of the first visible line within the
	// thread's full rendered body (the Subject title is fixed above it,
	// never scrolled). Reset to the selected item's own starting line
	// whenever the selection or its expand state changes, so a long
	// expanded body scrolls within the view (PgUp/PgDown/wheel) instead
	// of pushing the Subject or other messages off screen.
	threadScroll int

	// The full To/Cc/Subject editor r/R/f open (K6).
	mailComposing  bool
	mailAction     string
	mailTargetID   string
	mailChannel    core.Channel
	mailAccount    string
	mailThread     string
	mailTo         textinput.Model
	mailCc         textinput.Model
	mailSubject    textinput.Model
	mailFocus      int
	mailAttachInfo []core.Attachment
	mailPreviewing bool
	mailSending    bool
	mailPlan       core.Plan
	mailSendErr    error
	mailToken      uint64

	// Attachment download (mail view `d`, chat view Ctrl+D): a small overlay on top of
	// the chat/mail thread view (chatMode/threadMode stay true
	// underneath, exactly like markConfirm's overlay on the plain detail
	// view), so Esc returns to whichever view opened it.
	downloadActive       bool
	downloadItemID       string
	downloadAttachments  []core.Attachment
	downloadPicking      bool
	downloadIndex        int
	downloadPath         string
	downloadOverwrite    bool
	downloadSending      bool
	downloadErr          error
	downloadResult       core.DownloadResult
	downloadToken        uint64
	downloadDefaultDirFn func() string

	// Launch modes (issue #81, launch.go). sidebar is the compact layout
	// for a narrow herdr pane; externalOpen, when set, opens a
	// conversation somewhere else (a new herdr pane) instead of in place.
	// openID is the one conversation "bunker open" starts on: the pane
	// exists only for it, so leaving it quits. openErr is why it could
	// not be opened.
	sidebar      bool
	externalOpen func(id string) error
	openID       string
	openErr      error

	// herdr integration (issue #82, herdr.go): agentAsk asks a coding
	// agent about a conversation ("a"), asking while it runs;
	// unreadReport publishes the unread total after each poll and
	// messageNotify replaces OSC 777 notifications. The *Failed flags
	// keep a persistent failure from flashing on every poll.
	agentAsk             func(ctx context.Context, itemID string) error
	asking               bool
	unreadReport         func(n int) error
	unreadReportFailed   bool
	messageNotify        func(body string) error
	messageNotifyFailed  bool
	lastMessageNotifyAt  time.Time
	pendingMessageNotify int

	// Voice calls (issue #27, calls.go): the live calls the last poll saw,
	// whether polling has started, which ringing calls were already
	// announced, and whether a control request is in flight.
	calls        []core.Call
	callsStarted bool
	callNotified map[string]bool
	callBusy     bool

	// updateNotice is the "nueva versión" line (issue #105, see
	// update_notice.go), shown from updateNoticeAt for
	// updateNoticeDuration; updateNoticed keeps it to once per session.
	updateNotice   string
	updateNoticeAt time.Time
	updateNoticed  bool
}

// numTabs is "Todo" plus one tab per channelOrder entry.
const numTabs = 1 + 3

func NewModel(client Client, opts ...Option) Model {
	m := Model{client: client, polling: client != nil, pollToken: 1}
	for _, opt := range opts {
		opt(&m)
	}
	if m.openID != "" {
		// A single-conversation pane never shows the inbox, so it does
		// not poll it (nor notify about it: the panel that opened it
		// already does).
		m.polling = false
	}
	return m
}

// withGlyphs returns a copy of m using the given resolved channel glyphs
// (see internal/style.ResolveGlyphs) instead of the package defaults.
func (m Model) withGlyphs(glyphs map[core.Channel]string) Model {
	m.glyphs = glyphs
	return m
}

// clock returns the model's injectable clock, defaulting to time.Now so
// production code needs no wiring while tests pin a fixed time.
func (m Model) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// resolvedGlyphs returns the model's configured channel glyphs, falling
// back to the package defaults.
func (m Model) resolvedGlyphs() map[core.Channel]string {
	if m.glyphs != nil {
		return m.glyphs
	}
	return style.Glyphs
}

// renderer returns the model's lipgloss renderer, defaulting to one over
// io.Discard (which is never a *os.File, so termenv detects no color
// capability — a safe, deterministic default for anything that renders a
// Model without wiring a real terminal output, including most tests).
func (m Model) renderer() *lipgloss.Renderer {
	if m.render != nil {
		return m.render
	}
	return lipgloss.NewRenderer(io.Discard)
}

func (m Model) Init() tea.Cmd {
	if m.client == nil {
		return nil
	}
	if m.openID != "" {
		return readOpenItem(m.client, m.openID)
	}
	return loadInbox(m.client, m.pollToken)
}

// Run owns the Bubble Tea program but not the RPC connection; the caller
// closes that connection whether the terminal exits cleanly or with an
// error. It builds its lipgloss renderer from output (so color detection,
// including NO_COLOR, matches the real terminal Bubble Tea writes to) and
// loads [render.glyphs] config overrides the same way `bunker render`
// does; a missing/unreadable config keeps the package default glyphs.
func Run(client Client, input io.Reader, output io.Writer, opts ...Option) error {
	glyphs := style.Glyphs
	var notify *bool
	var confirmChatSend bool
	var folderLayouts map[string]folderLayout
	var voiceRecordCommands map[string][]string
	if cfg, err := config.LoadDefault(); err == nil {
		voiceRecordCommands = voiceRecordCommandsFromConfig(*cfg)
		glyphs = style.ResolveGlyphs(cfg.Render.Glyphs)
		notify = cfg.Tui.Notify
		confirmChatSend = cfg.Tui.ConfirmChatSend
		folderLayouts = folderLayoutsFromConfig(*cfg)
	}
	model := NewModel(client, opts...).withGlyphs(glyphs)
	model.folderLayouts = folderLayouts
	model.confirmChatSend = confirmChatSend
	model.voiceRecordCmds = voiceRecordCommands
	model.render = lipgloss.NewRenderer(output)
	output = lockOutput(output)
	model.notifyEnabled = resolveNotifyEnabled(notify, os.Getenv)
	model.notifyWriter = output
	model.tmuxPassthrough = os.Getenv("TMUX") != ""
	model.clipboard = newExecClipboard()
	model.gfx = kittygfx.Detect(os.Getenv)
	if model.gfx == kittygfx.Kitty {
		model.gfxOut = output
		model.media = newMediaCache()
		model.mediaDir = mediaCacheDir()
	}
	// The alternate screen: the panel owns the whole pane, so a resize
	// redraws from the top instead of diffing against lines the terminal
	// has already re-wrapped, and quitting restores what was there.
	_, err := tea.NewProgram(model, tea.WithInput(input), tea.WithOutput(output), tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithReportFocus(), tea.WithFilter(mouseLeakFilter())).Run()
	// Whichever way the interface ended, no recorder or player outlives it.
	killVoiceProcs()
	return err
}
