# bunker CLI

`bunker` is one binary: a daemon that talks to channel adapters and stores
their items, and a set of client subcommands that drive it over a unix
socket. Every command accepts `--json`, which is the stable, documented
contract Claude Code parses; without it, output is compact plain text for
a human at a terminal.

The daemon must be running for every command except `render`, which falls
back to reading the store directly, and `daemon` itself.

## Sockets and paths

- RPC socket: `$BUNKER_SOCKET`, else `$XDG_RUNTIME_DIR/bunker-go.sock`,
  else `<tmp>/bunker-go.sock`.
- Config file: `$BUNKER_CONFIG_DIR/config.toml`, else
  `~/.config/bunker-go/config.toml`.
- State dir (SQLite store): `$BUNKER_STATE_DIR`, else
  `~/.local/state/bunker-go/`.

## `bunker daemon [--fake]`

Runs the daemon: opens the SQLite store, wires one adapter per configured
`[[account]]` (or three in-memory demo adapters under `--fake`, one per
channel), starts each adapter's receive loop under a supervisor, and
serves the RPC socket until it receives `SIGINT`/`SIGTERM`. A missing
config file is not an error — the daemon starts with no accounts, so
`list`/`counts` still work against an empty store.

No flags produce `--json` output; this command prints plain status lines
to stdout as it starts.

If an adapter's `Run` returns while the daemon is still live (a dropped
IMAP/WhatsApp/Matrix connection, a sync error), the supervisor restarts
it with a capped exponential backoff (1s, doubling, up to 5 minutes,
with jitter), resetting back to the base delay once a run has stayed up
for at least 2 minutes — the process itself never exits just because one
channel disconnected. Every restart, and every discarded store-write
error that used to be silently lost, is logged via `log/slog` to
**stderr** (not stdout) with `channel`/`account` attributes, so `journald`
carries them under a normal systemd unit. See `bunker health` below for
querying the resulting per-adapter state instead of grepping logs.

## `bunker` (no arguments): interactive side panel

Running `bunker` with no arguments on a TTY opens a Bubble Tea side panel
(a tmux split or popup, typically) instead of printing usage: a small
interactive client over the same RPC connection every subcommand uses. It
never starts the daemon itself — it dials the existing socket exactly like
`bunker list` does, and prints the same "cannot reach bunker daemon" hint
and exits 1 if nothing is listening. Piped/non-interactive stdin (no TTY)
keeps today's behavior: usage text on stderr and exit 2, unchanged.

It lists unread items across every channel/account, grouped by
`(Channel, Account, Thread)` (an item's own id is the group key when it
carries no thread), polls for new ones every 5 seconds with one query in
flight at a time, and lets you read and reply to an item or mark it read
— all without ever touching `internal/store` or a channel adapter
directly; it only ever calls through the same small RPC client interface
`bunker`'s other commands use.

**Layout.** The inbox is one clearly separated section per channel, in a
fixed order (Mail, WhatsApp, Matrix): each section header shows that
channel's brand-color glyph, name, and total unread count, followed by a
thin rule in the same brand color (mail blue, WhatsApp green, Matrix
green — the same accents `bunker render --tmux`/`--ansi` use, including
any `[render.glyphs]` override). A channel with more than one configured
account shows the account as a dim tag on its rows instead of splitting
into more sections. Every conversation is two lines: a bold title (the
thread name, then the subject, then the sender — never a raw Matrix room
id or bare WhatsApp JID, which are shortened and dimmed instead) with its
relative time (`HH:MM` today, "ayer" yesterday, else `dd-mmm`) and an
unread badge on the right, and a dim "Sender: body" preview of the newest
message underneath. The selected row gets a full-width highlight and a
colored left bar. An empty section still shows its header and one dim
"sin pendientes" line. The overview shows all three sections at once,
each getting a fair share of the pane's height; a section with more
conversations than fit ends in a dim "+N más" line rather than pushing
another section off screen. `?` opens a full-keymap help overlay; `Esc`
closes it. At terminal widths under ~30 columns the two-line row
collapses to one line (no preview). Setting `NO_COLOR` disables all
color, same as everywhere else in bunker.

**Mail sender groups.** The Mail section groups its conversations under
one collapsible row per sender, so a sender with many mails takes one
line until expanded: a chevron (`▸` collapsed, `▾` expanded), the
sender's display name (its newest thread's newest non-empty `From.Name`,
else the address), a dim account tag when Mail has more than one
account, the sender's newest time, and an unread badge summing every one
of that sender's loaded unread items. The sender key is the lower-cased
`From` address (never the display name), so two display names for the
same address merge; a sender with exactly one thread still gets its own
row, for consistency. Senders are ordered by their newest mail, and
threads under a sender are ordered newest first. Collapsed by default:
`Enter`, `→`, or a click on a sender row toggles it; `←` collapses it
(and, with the cursor on one of its threads, jumps up to the sender row
first, so the selection is never left pointing at a row that just
disappeared). `Enter` on a thread row still opens it, same as before.
Expanded threads render with the existing two-line row design, indented
by 2 cells. The expand state is kept per sender address across polls,
and resets when the TUI exits. WhatsApp and Matrix are unaffected: every
row there is still one conversation.

**Mouse.** The wheel moves the selection (like `j`/`k`); a click on a
conversation selects it, and a click on the already-selected row opens
it (or, for a Mail sender row, toggles it), matching `Enter`; a click on
a section header, its rule, or a "+N más" notice focuses that section,
matching `1`/`2`/`3`. The mouse never sends or marks anything by itself
— it can only select, open, toggle, or focus, the same things clicking
is allowed to do from the keyboard. Clicking is ignored entirely outside
the plain inbox (while composing, previewing, marking, or with the help
overlay open); the wheel is the one exception — while reading a
detail view or composing a reply it instead scrolls that view's long
body/draft, the same as `PgUp`/`PgDown` (below).

**Desktop notifications.** When a poll finds unread conversations that
were not part of the previous snapshot — never for the initial backlog
on first open — and the panel is not the focused terminal pane (via
`tea.FocusMsg`/`BlurMsg`; needs tmux's `focus-events on`), it emits an
OSC 777 desktop notification (`sender`/first 60 characters of the body
for one new item, or a generic "bunker"/"N new" once more than one
arrives), sanitized the same way everything else from the daemon is.
Notifications are rate-limited to at most one per 10 seconds; arrivals
inside that window are coalesced into the next one instead of being
dropped. Under tmux (`$TMUX` set) the sequence is wrapped in tmux's DCS
passthrough so it reaches the real terminal instead of being swallowed.
Opt out with `[tui] notify = false` in `config.toml` or
`BUNKER_TUI_NOTIFY=0` in the environment; either alone disables it.

**Hyperlinks.** In the detail view (`Enter` on an item), URLs in the
body and the sender's address become OSC 8 hyperlinks (`mailto:` for
mail, a `matrix.to` link for Matrix, `wa.me` for a WhatsApp contact — a
WhatsApp group has no such link and stays plain text): Ctrl/Cmd-clicking
the visible text opens it, in a terminal that supports OSC 8 (tmux needs
`terminal-features ",xterm-ghostty:hyperlinks"`, or whatever matches your
outer terminal, in `~/.tmux.conf`). The escape sequences never count
toward the panel's width/wrap math, and only ever wrap text the panel has
already run through its terminal-escape sanitizer — a message body or
sender name can never inject its own escape sequence this way.

Two safety properties hold regardless of what the daemon or a message's
own text says:

- **Opening an item never marks it read.** Reading in the panel calls
  `Read(id, markReceipt=false)`; only the explicit `m` key marks
  something read, and even that goes through a dry-run preview first
  (below). The panel also never starts, stops, or restarts the daemon,
  and never generates a reply on its own — a human reads and decides.
- **Every reply and every mark-read is preview-then-confirm.** The panel
  always asks the daemon to plan the action first (`dryRun=true`, e.g. a
  Matrix attachment policy check still happens, so a dry-run answering
  "no" is a real network round trip, not evidence of being offline) and
  shows channel/account/recipient/attachments before doing anything for
  real. There is no auto-retry on an uncertain result, and a send/organize
  already in flight blocks every key (including quit and cancel) until it
  resolves, so nothing can be sent or marked twice from one confirm.
  Editing a draft after a preview always requires a fresh preview before
  the next send.

Keys: `j`/`k`/arrows move (in the overview they walk across section
boundaries), `Enter` opens the selected item, `Esc` goes back (or closes
the help overlay, or returns from a focused section to the overview),
`1`/`2`/`3` focus the Mail/WhatsApp/Matrix section alone at full height
with scrolling, `0` returns to the overview, `Tab`/`Shift+Tab` cycle
overview → Mail → WhatsApp → Matrix → overview, `r` starts a reply to the
selected/open item, `m` marks it read (dry-run preview, then `Enter` to
confirm), `g` refreshes the inbox now, `?` opens the help overlay, `q`
quits (asks again first if a reply preview/send is in flight). A directly
opened single-item detail view (kept for parity; every current channel
instead opens its own chat/thread view below) scrolls a long body with
`j`/`k`, arrows, `PgUp`/`PgDown`, `G` (jump to the end) and the mouse
wheel, the footer keymap hint always keeps `q` visible even at a narrow
(40-column) terminal width.
Composing a reply: the draft is a real multi-line text editor (a shared
[bubbles](https://github.com/charmbracelet/bubbles) textarea), so arrows,
Home/End, word motions and paste move and edit the cursor position instead
of only ever appending at the end; `PgUp`/`PgDown` and the mouse wheel
page through a long draft the same way, since every letter (including
`j`/`k`) is literal draft text here. `Ctrl+A` adds a local file attachment
by path (spaces allowed; never a shell), `Ctrl+X` drops the most recently
added attachment, `Ctrl+S` requests the dry-run preview, and `Enter`
inserts a newline rather than sending — no key sends without that explicit
preview-then-confirm step, sending never double-fires, and an error keeps
the draft exactly as typed with no automatic retry. Terminal control
sequences in anything the daemon returns (a message body, an attachment
name, a sender's display name) are stripped before they ever reach your
terminal.

Opening a WhatsApp/Matrix item now opens its chat view instead of the
plain single-item detail: a header shows the channel glyph and the
contact/group name in bold, with live presence on the line under it. The
conversation renders as real colored chat bubbles (a dark neutral
background for incoming messages, WhatsApp's own dark green — or
Matrix's brand teal — for your own), each up to ~75% of the terminal
width, with a dim "HH:MM" at the bottom-right of every bubble; own
messages align right and others' align left, a sender name appears once
per run of consecutive messages from the same person (colored stably per
sender in a group), a day separator ("hoy"/"ayer"/"dd-mmm") renders as a
centered dim pill, and attachments show as "📎 name (size)" chips inside
the bubble. The composer is docked at the bottom in a rounded box with
the placeholder "Escribe un mensaje…", growing up to 3 lines as the draft
gains lines. A message you send for real is stored immediately as your
own (right-aligned) bubble even before the channel echoes it back (most
channels never do, for your own linked-device sends) — a fan-out
broadcast stores one such item per successful recipient; mail relies on
its own Sent-folder sync instead, so this never double-stores for mail.
Opening it
marks the conversation read with a receipt (this replaces the plain
detail view's "opening never marks read" default for these two
channels — mail keeps that default). While the chat is open the daemon
is told you are focused (a keepalive renews that lease every 20s; it
reports unfocused on `Esc`/blur/quit, and the daemon's own 60s timeout
covers a hard quit) and the header shows the other side's live presence
("en línea"/"escribiendo…"/"grabando audio…"/"últ. vez HH:MM", or
nothing for a channel without presence data). Composing: every key is
literal draft text; `Enter` requests a dry-run preview and then shows an
inline "¿Enviar a …? Enter/Esc" confirm — a second `Enter` sends for
real, `Esc` cancels back to editing with the draft kept, and typing your
own composing state is reported to the other side, throttled to at most
once every 5s and cleared after 5s idle, on send, or on leave. `Alt+Enter`
inserts a newline (plain `Enter` is reserved for send/confirm here, unlike
the mail reply composer). `Up` at the top of an empty draft loads an
older page of the conversation instead of moving the cursor. `Esc` leaves
the chat view and returns to the inbox.

Opening a mail item now opens its thread view instead of the plain
single-item detail: the Subject renders as a bold title, every message in
the conversation stacks, the newest expanded with full headers
(From/To/Date). An expanded message's full body is fetched on demand
(`Read` with no receipt, cached per message so re-expanding it never
re-fetches) — mail sync only stores headers, so a synced item's body is
otherwise empty until you open it — and quoted (`>`) lines within it
render dimmed. Older messages collapse to "sender · date · snippet",
where the snippet comes from the fetched body (or the Subject when no
body has been fetched yet), never the sender's name again; toggled with
`Enter`. Opening it marks the
thread `\Seen` immediately (Organize with no dry-run — this replaces the
plain detail view's "opening never marks read" default for mail too;
explicit `m` still exists for the dry-run-then-confirm flow elsewhere).
`j`/`k`/arrows move between messages. `r` (reply), `R` (reply-all,
excluding your own address once a sent item has synced into the thread)
and `f` (forward, starting with an empty `To` and listing the original's
attachments informationally — re-attaching them is not implemented yet)
all open the same full editor: editable `To`/`Cc`/`Subject` fields (`Tab`/
`Shift+Tab` cycle focus) plus the reply body, prefilled with a quoted
original. `Ctrl+S` requests a dry-run preview; `Enter` there sends for
real (no double send, no auto-retry), and `Esc` at any stage returns to
editing/the thread without ever sending. `Esc` from the thread view
returns to the inbox.

## `bunker list [flags]`

Lists items.

Flags: `--channel c`, `--account a`, `--unread`, `--label l`, `-q text`
(substring match over subject and body), `--limit n`.

```json
{"items": [ <core.Item as JSON, see below> ]}
```

## `bunker read <id> [--no-receipt] [--json]`

Fetches the full item, using the adapter's `Fetcher` capability if it has
one, falling back to the stored copy otherwise.

Unless `--no-receipt` is given, it also marks the item read on the
channel itself when the adapter implements `core.ReadMarker`:

- **WhatsApp**: presence `available` (so the linked device does not
  suppress the phone's own push notification while it looks "active"),
  the read receipt (blue ticks), then presence `unavailable` again.
- **Matrix**: an `m.read` receipt and the fully-read marker for the same
  event, in one call. No presence — Matrix has no equivalent concept.
- **Mail**: never marked. Mail has no `ReadMarker`; `Fetch` always uses
  IMAP `BODY.PEEK`, so reading a mail item is unaffected by `--no-receipt`
  either way.

`--no-receipt` fetches without marking anything, on every channel.

```json
{"item": { <core.Item> }}
```

## `bunker reply <id> <text|-> [--cc addr]... [--attach path]... [--dry-run] [--json]`

Replies to item `<id>`. `<text>` can be `-` to read the body from stdin.
`--dry-run` returns the `Plan` alone and never reaches the channel
adapter.

`--cc addr` (repeatable) adds a Cc recipient; each value may itself be a
comma-separated list (`--cc "a@x.cl, b@x.cl"`), and the flag may be
repeated too — every address from every occurrence is collected, each
trimmed of surrounding whitespace, with empty entries dropped. See `send`
below for what each channel does with Cc.

`--attach path` (repeatable) attaches local files to the reply, for
every channel — see `send` below for the full validation and JSON-shape
story, which `reply` shares. A reply with attachments quotes the
replied-to item exactly like a plain-text reply does; on WhatsApp this
means the first attached image (only) carries the `ContextInfo` that
quotes the original message, the same way its caption carries `<text>`
on the first image only.

```json
{"dryRun": false, "plan": { <core.Plan> }, "receipt": { <core.Receipt> }}
```

`receipt` is the zero value (`{"ID":"","Channel":"","At":"0001-01-01T00:00:00Z"}`)
when `dryRun` is `true`.

## `bunker send <channel> <account> <to> <text|-> [--cc addr]... [--subject s] [--attach path]... [--media path]... [--dry-run] [--json]`

Sends a fresh message, not tied to any existing item. Same `--dry-run` and
JSON shape as `reply`, plus new `plan.Media`/`plan.Attachments` fields
(see below).

`<to>` is a comma-separated list of recipients (`"a@x.cl, b@x.cl"`):
each entry is trimmed, empty entries are dropped, and the command fails
before touching the backend if that leaves nothing. `--cc addr`
(repeatable, each value may also be comma-separated — see `reply` above)
adds Cc recipients. Per-channel support:

- **Mail**: every `<to>` and `--cc` address gets both a header (`To`/
  `Cc`) and an SMTP `RCPT TO`, never just the first.
- **WhatsApp** and **Matrix**: exactly one recipient. More than one `<to>`
  or any `--cc` fails with an unsupported-capability error instead of
  silently sending to the first and dropping the rest.

`plan.Cc` (alongside the existing `plan.Target`, which already lists
`<to>`) carries the Cc list; it is additive and does not change any
existing JSON field, so an existing consumer that ignores unknown fields
is unaffected. It is present (`null` or an empty list) even with no `--cc`
given.

`plan.Recipients` lists every `<to>` address a send/reply reaches, one
entry each, whether the channel addressed everyone in one native call
(mail) or `core.Service` fanned out N sequential single-recipient calls
(see Fan-out below) — for reply, this is always the single original
sender. `plan.Subject` is the message subject shown for operator/approval
visibility: for `send`, `--subject` as given (empty when none); for
`reply`, the original item's subject with a computed `"Re: "` prefix
(left as-is if it already carries one, checked case-insensitively), empty
when the original item has none (WhatsApp/Matrix items never do). Neither
field changes what an adapter actually transmits.

The human (non-JSON) output shows recipients, Cc and subject on both
`--dry-run` and a real send/reply, whenever the Plan carries them:

```
[dry-run] would send via mail/cl to [alice@x.cl] cc [carol@example.org] subject "hi": hi there
```

`--attach path` (repeatable) attaches local files, for every channel.
`--media path` is kept as an exact alias of `--attach` for compatibility
(a prior slice of this feature shipped it first, and existing scripts and
scheduled sends use it); the two may also be combined in one call — every
path from both flags is attached, `--media` paths first, in the order
each flag was given.

Attachment support per channel:

- **WhatsApp**: one message per file, in the order given, with `<text>`
  as the caption of the first attachment only (WhatsApp has no single
  message that carries several attachments, and captioning every one
  would repeat the same text). The file's extension picks the message
  type:
  - image (`.png`, `.jpg`/`.jpeg`, `.webp`) → an image message, up to
    16 MB.
  - video (`.mp4`, `.3gp`/`.3gpp`) → a video message, up to 16 MB.
  - audio (`.ogg`/`.opus`, `.mp3`, `.m4a`, `.aac`) → a non-voice-note
    audio message, up to 16 MB. WhatsApp's audio message has no caption
    field at all, so `<text>` is silently dropped when the *first*
    attachment is audio (later attachments never carry a caption
    anyway).
  - everything else → a document message (`FileName`/`Title` from the
    base name, `Mimetype` content-sniffed with an extension fallback),
    up to 100 MB.
- **Mail**: any file type, as `multipart/mixed` parts of one message, up
  to 18 MB per file and 18 MB in total. Base64 inflates by ~4/3, so the
  encoded message stays under Gmail's 25 MB limit.
- **Matrix**: any MIME type, uploaded to the homeserver's media repo and
  sent as `m.image`/`m.video`/`m.audio`/`m.file` (by MIME prefix, else
  `m.file`), up to the server's own `m.upload.size` (fetched once from
  `/_matrix/media/v3/config` and cached; 50 MB when the server does not
  advertise one). In an encrypted room the file is encrypted client-side
  (mautrix's `crypto/attachment`) before upload and the event carries
  `file` (no `url`); an unencrypted room carries `url` instead. `<text>`
  is sent as a separate `m.text` event right after the media event
  (chosen over an MSC2530 caption for wider client compatibility), and a
  reply's `m.relates_to`/`m.in_reply_to` is set on the first event only.

Validation is split across two layers:

- The CLI only checks that each path exists, is a regular file (not a
  directory) and is readable. It never inspects content or enforces a
  type or size rule.
- `core.Service` inspects every file exactly once — its base name, a
  content-sniffed MIME type (falling back to the file extension when
  sniffing is inconclusive) and its size — into a `core.AttachmentInfo`,
  and validates the result against the target adapter's own
  `core.AttachmentPolicy` (`MaxBytes`, a MIME type to a byte limit, plus
  `core.AnyMIME` as the catch-all key for a type with no exact entry).
  WhatsApp's policy accepts `image/png`, `image/jpeg` and `image/webp`,
  `video/mp4` and `video/3gpp`, and `audio/ogg`, `audio/mpeg`,
  `audio/mp4` and `audio/aac`, all up to 16 MB, plus `core.AnyMIME` up to
  100 MB for everything else (sent as a document). Matrix's policy
  accepts any MIME type up to the homeserver's `m.upload.size`. This
  validation runs identically on `--dry-run` and a real send/reply,
  before `SendMedia` is ever called: only a small sniff read happens,
  never a full read or an upload.

```json
{"dryRun": false, "plan": { <core.Plan>, "Media": ["/path/to/pic.png"], "Attachments": [{"Name": "pic.png", "MIME": "image/png", "Size": 2048}] }, "receipt": { <core.Receipt> }}
```

`plan.Media` (paths, kept for compatibility) and `plan.Attachments`
(name/MIME/size, computed and validated as above) are the only new
fields added by this flag — everything else matches the existing
`reply`/`send`/`status post` shape. Both are present (as `null` or an
empty list) even for a plain send/reply with no attachments, since this
codebase does not use `omitempty` json tags elsewhere either; existing
consumers that decode into a typed struct are unaffected by an added
field.

The human (non-JSON) output of a send/reply with attachments lists each
one on its own line as `name (mime, size)`, e.g.:

```
[dry-run] would send via whatsapp/personal to [+51999999999]: mira
  pic.png (image/png, 2048 bytes)
```

## Fan-out broadcast (`send` with more than one recipient)

`<to>` may name more than one recipient (see above). Mail addresses every
recipient in one native SMTP submission (it implements
`core.MultiRecipientSender`). WhatsApp and Matrix do not: `core.Service`
instead fans the send out into N sequential single-recipient calls, one
per address, in order — `reply` never fans out, since it always targets
the single original sender.

Limits and pacing are configured per WhatsApp account (see
`docs/config.example.toml`), with built-in defaults:

- `max_broadcast_recipients` (default 10): exceeding it is an error
  *before* anything is sent — no partial broadcast.
- `broadcast_pause_min_seconds` / `broadcast_pause_max_seconds` (default
  3/8): a randomized pause between each recipient after the first.
  Matrix always uses the 10/3-8s default (not yet independently
  configurable).

One recipient's failure never stops the rest and is never hidden: every
outcome is reported. `receipt.recipients` lists `{"To", "Receipt",
"Error"}` per recipient (`Error` is empty on success); the top-level
`receipt.ID`/`Channel`/`At` mirror the *first successful* recipient, so a
JSON consumer that only reads those top-level fields keeps working
unchanged for a single-recipient send. The CLI process exits `1` if any
recipient failed, `0` only when every one succeeded.

```json
{"dryRun": false, "plan": { <core.Plan> }, "receipt": {
  "ID": "...", "Channel": "whatsapp", "At": "...",
  "recipients": [
    {"To": "+51111", "Receipt": {"ID": "...", "Channel": "whatsapp", "At": "..."}, "error": ""},
    {"To": "+51222", "Receipt": {}, "error": "whatsapp: send: +51222 is not on WhatsApp"}
  ]
}}
```

`--dry-run` on a fan-out shows the full per-recipient plan and an
estimated pause budget (`plan.FanoutPauseMin`/`FanoutPauseMax`: the
`(N-1)` inter-recipient pauses at the resolved policy's bounds — this
does *not* include each adapter's own composing/typing time, which
`core.Service` has no visibility into) without sending or sleeping
anything:

```
[dry-run] would send via whatsapp/personal to 3 recipients [+51111 +51222 +51333]: hola
  estimated pause between recipients: 6s-16s (plus each adapter's own composing/typing time)
```

A real fan-out lists each recipient's outcome:

```
send: 3 recipients
  ✓ +51111 (receipt whatsapp:personal:...)
  ✗ +51222: whatsapp: send: +51222 is not on WhatsApp
  ✓ +51333 (receipt whatsapp:personal:...)
```

## Human emulation (WhatsApp/Matrix)

Every WhatsApp send (single or fan-out, text or media) performs this
choreography around the real delivery, so a linked device that stays
"available" the whole time does not suppress the phone's own push
notifications:

1. Presence `available`.
2. Chat presence `composing`, for a duration proportional to the text at
   ~7 characters/second, clamped to 2-15s with up to ±25% jitter — or,
   for a captionless image, a flat 2-4s window instead.
3. Chat presence `paused`.
4. The real send.
5. Presence `unavailable` again — always, including on an error.

Matrix shows a typing notification (proportional to the text, same
2-15s clamp, no jitter) before each send instead; it has no presence
concept.

## `bunker organize <id> [flags]`

Mutates an item's labels, folder and read state.

Flags: `--label x` (repeatable, adds), `--unlabel x` (repeatable,
removes), `--move folder`, `--seen` / `--unseen` (mutually exclusive),
`--dry-run`.

```json
{"dryRun": false, "plan": { <core.Plan> }}
```

There is no `receipt` here: the `Organizer` port has no receipt, only a
plan.

A non-dry-run call also reconciles the stored item: `--seen`/`--unseen`
updates its Unread flag, `--label`/`--unlabel` update its Labels, and
`--move` moves it out of the folder view it left — for mail, whose IMAP
UID encodes the folder into the item id, this rekeys the stored item to
the address a fresh sync of the destination folder would itself use, so
it stops showing up (and counting) under the folder it was moved out
of.

For mail accounts, the daemon also keeps the store in sync with changes
made outside bunker-go: while idling, an externally expunged message
(moved or deleted in Roundcube/Gmail) is dropped from the store, and an
externally toggled `\Seen` flag updates Unread; at startup, every
stored INBOX item is compared against the server (`UID SEARCH`, keyed
by UIDVALIDITY) and a UID no longer present is dropped, so a stale row
left over from before the daemon last ran does not linger in `list` or
`counts`.

Item.Labels for mail is read back from the server, not just from
Organize, in every one of these paths (initial sync, IDLE, startup
reconciliation, Fetch):

- Dovecot (`.cl`-style) accounts: Labels are a message's IMAP custom
  keywords (FLAGS), excluding system flags (`\Seen`, `\Answered`, ...)
  and RFC 5788 server-defined keywords (`$Forwarded`, `$MDNSent`,
  `$Junk`, `$NotJunk`, ...), neither of which is a user label.
- Gmail accounts: Labels come from `X-GM-LABELS`, fetched in a bounded,
  batched call over a dedicated raw XOAUTH2 connection (go-imap v2 has
  no public API for this extension). Gmail's backslash system labels
  keep their name with the backslash stripped (`\Sent` → `Sent`),
  except `\Inbox`, which is dropped (redundant with the item's own
  INBOX folder). A failure on this raw fetch is logged and never fails
  the sync/reconcile/fetch it was part of: the item just keeps
  whatever Labels it already had.

The generated `Message-ID` for a sent/replied mail uses the sending
account's own address domain (the part of the configured username
after `@`), not the IMAP server's hostname — a misconfigured account
with no `@` in its username falls back to the IMAP host, the previous
behavior.

## `bunker status post <channel> <account> <text> [--media path] [--dry-run] [--json]`

Publishes a channel status/story. `<text>` can be `-` to read from stdin.
Same shape as `reply`/`send`.

## `bunker counts [--json]`

Unread item counts per channel and account.

```json
{"counts": {"mail": {"cl": 3}, "whatsapp": {"personal": 5}, "matrix": {"work": 2}}}
```

## `bunker health [--json]`

Reports every adapter's current connection health, as the daemon's
adapter supervisor tracks it (R1's restart-with-backoff policy):

- `channel`, `account` — which adapter.
- `state` — one of `connecting`, `connected`, `backoff` or `stopped`.
  `connecting` covers both the very first `Run` attempt and every
  restart's initial window; the supervisor promotes it to `connected`
  once `Run` has kept going for a couple of seconds without returning (no
  adapter-specific "I'm authenticated" signal is required). `backoff`
  means `Run` returned and the supervisor is waiting before retrying.
  `stopped` means the daemon itself is shutting down.
- `since` — RFC3339 timestamp of when the adapter entered `state`.
- `lastError` — the error that caused the most recent `backoff`
  transition, if any (cleared once the adapter reconnects).
- `restarts` — how many times this adapter's `Run` has been restarted.

```json
{"adapters": [
  {"channel":"mail","account":"cl","state":"connected","since":"2026-01-02T03:04:05Z","restarts":0},
  {"channel":"whatsapp","account":"personal","state":"backoff","since":"2026-01-02T03:05:00Z","lastError":"dial refused","restarts":3}
]}
```

Non-JSON output, one line per adapter:

```
mail/cl: connected (restarts=0)
whatsapp/personal: backoff (restarts=3) last_error="dial refused"
```

## `bunker download <id> [-n index] -o path [--force] [--json]`

Saves one attachment of a stored item to disk, so Alice (and Claude Code)
can open a file bunker-go only ever described in `list`/`read` output.

- `<id>` is the item's id, exactly as `list`/`read` print it.
- `-n index` selects which attachment when the item carries more than
  one (`0`-based; defaults to `0`).
- `-o path` is the output file — **required**, there is no default. The
  path is used literally: no shell, no `~` expansion, no directory
  auto-creation.
- `--force` allows overwriting an existing file at `path`. Without it,
  `download` refuses and exits non-zero rather than silently clobbering
  something already there.

The daemon writes the file itself (it and the CLI always run as the same
user on the same machine), through a temp file plus rename in `path`'s
own directory, at mode `0600`, capped at 100 MB by default; when the
attachment's declared size is known, the actual byte count must match it
exactly or the download is rejected and no partial file is left behind.

Mail re-fetches the MIME part by UID over IMAP (`BODY.PEEK`, so this
never marks anything `\Seen` either) — it works for any mail item
already stored, with no extra state kept around for it. WhatsApp looks up
the download descriptor (`DirectPath`/`MediaKey`/`FileSHA256`/
`FileEncSHA256`) it privately persisted when the message first arrived
and calls whatsmeow's `Download`; an item stored before this feature
existed has no descriptor and fails with a clear error ("no media key
stored; re-download from the phone") instead of a panic. Matrix looks up
the mxc:// URL (plain rooms) or the `attachment.EncryptedFile` key/iv/hash
(E2EE rooms, from the event's `file`) it privately persisted when the
`m.image`/`m.video`/`m.audio`/`m.file` event first arrived, downloads the
bytes from the homeserver's media repo and, for an encrypted room,
decrypts them in place — a hash mismatch (tampered or corrupted ciphertext)
is rejected rather than returned; an item stored before this feature
existed, or whose room key never arrived, has no descriptor and fails
with a clear error instead of a panic.

```json
{"result": {"Path": "/home/alice/manual.pdf", "Bytes": 483921, "Name": "manual.pdf", "MIME": "application/pdf"}}
```

Non-JSON output: `saved manual.pdf (application/pdf, 483921 bytes) to /home/alice/manual.pdf`.

## `bunker avatar <channel> <account> <thread> [--json]`

Prints the local PNG path for one conversation's avatar — a debugging/
scripting entry point ahead of the TUI rendering it (a later task; see
`odd/tasks/tui-avatars.md`).

- `<channel>`/`<account>` identify the adapter, exactly like `send`'s
  positionals.
- `<thread>` is the same value as the conversation's `Item.Thread` (a
  WhatsApp JID, a Matrix room id).

The daemon fetches the picture through the adapter's `AvatarProvider`
capability when it has one (WhatsApp contacts/groups, Matrix rooms/DMs),
resizes it to at most 96x96 and caches it at
`~/.cache/bunker-go/avatars` (`0700` directory, `0600` files, overridable
via `BUNKER_CACHE_DIR`) for 24h; a "no picture"/"not authorized" result is
itself cached as a negative for 24h. Fetches are on demand and
rate-limited per adapter, so a burst of `avatar` calls across many
conversations never turns into a burst of profile-picture requests.

Mail has no `AvatarProvider` — it never fetches a picture over the
network, ever — and any account without a real picture (rate-limited, no
picture, unauthorized, or the fetch itself failing) falls back to a
generated avatar: a circle in the channel's brand color (mail blue,
WhatsApp green, Matrix green) with the conversation's initial letter in
white, deterministic from (channel, display name).

WhatsApp fetches the preview-size picture via whatsmeow's
`GetProfilePictureInfo` for both contacts (`<thread>` a `@s.whatsapp.net`/
`@lid` JID) and groups (`@g.us`), then downloads it with a plain HTTPS
GET — profile pictures are not part of WhatsApp's end-to-end encrypted
media, unlike message attachments (see `download` above). No picture set
or a contact who hid theirs from Alice
(`ErrProfilePictureNotSet`/`ErrProfilePictureUnauthorized`) is a negative
cache miss, not an error.

Matrix (`<thread>` a room id) prefers the room's own `m.room.avatar`
state event; when the room has none set and it looks like a plain DM
(exactly one sync-summary hero — the same heuristic the room's
`ThreadName` fallback uses), it falls back to that member's own global
profile avatar instead. Either way the picture is downloaded through
mautrix's authenticated media endpoint (`Client.DownloadBytes`) — room
avatars are not end-to-end encrypted even in an encrypted room, so no
decryption is needed. Neither the room nor (for a DM) the hero having an
avatar set (`M_NOT_FOUND` from both lookups) is a negative cache miss,
not an error.

```json
{"result": {"Path": "/home/alice/.cache/bunker-go/avatars/3f2a....png", "Generated": false}}
```

Non-JSON output is just the path on its own line.

## `bunker thread <channel> <account> <thread> [--before RFC3339] [--limit N] [--json]`

Prints one conversation's items, oldest→newest — the query the TUI's chat
and mail thread views open a conversation with.

- `<channel>`/`<account>`/`<thread>` identify the conversation, exactly
  like `avatar`'s positionals.
- `--before RFC3339` pages backward (scroll-up pagination): returns at
  most `--limit` items strictly before that timestamp. Omitted (or the
  zero time) means "the newest window".
- `--limit N` caps how many items come back; omitted or `<= 0` uses the
  daemon's own default (currently 50).

Backed by `core.Filter.Thread` and a store index on `(channel, account,
thread, timestamp)`, so this stays fast regardless of how large the store
grows.

```json
{"items": [{"ID": "whatsapp:personal:3EB0...", "Channel": "whatsapp", "Account": "personal", "Thread": "5511999999999@s.whatsapp.net", "From": {"ID": "5511999999999@s.whatsapp.net", "Name": "Alice"}, "Body": "hola", "FromMe": false, "Unread": false, "Timestamp": "2026-09-01T12:00:00Z", "Meta": {}}]}
```

Non-JSON output is one line per item: `<id>\t<RFC3339 timestamp>\t<body>`.

## `bunker read-thread <channel> <account> <thread> [--no-receipt] [--json]`

Marks every unread, non-`FromMe` item of one conversation read in a single
call — fixing the read-on-open bug where opening a conversation marked
only its *newest* item read (`Read`/`organize` acting on a single id),
leaving older unread incoming messages stranded in the unread panel. The
TUI's chat view (K5) and mail thread view (K6) now use this same RPC on
open instead.

- `<channel>`/`<account>`/`<thread>` identify the conversation, exactly
  like `thread`'s positionals.
- `--no-receipt` still clears the local unread state (fixing the actual
  bug) but skips notifying the channel itself: no WhatsApp/Matrix read
  receipts, no mail `\Seen`. Default: receipts/`\Seen` are sent.

Backed by `core.Service.ReadThread(ctx, channel, account, thread string,
receipt bool) (int, error)`: it loads the whole conversation (paging past
`thread`'s own default window when needed), selects the unread,
non-`FromMe` items, and unless `--no-receipt`:

- WhatsApp: batches a single `MarkRead` call per (chat, sender) — grouping
  is required for a group conversation, since whatsmeow scopes one
  `MarkRead` call to one sender within a chat.
- Matrix: sends one read/fully_read marker on the *newest* selected event
  (an older marker is already covered by a newer one).
- Mail: sets `\Seen` on each selected item (`Organize` with `Seen: true`).

It then marks every selected item read in the store (`MarkThreadReadUpTo`,
up to the newest one's timestamp) and returns how many it marked. It never
touches a `FromMe` item or any other thread, and is idempotent: calling it
again on an already-read conversation marks nothing and returns 0.

```json
{"count": 3}
```

Non-JSON output is a single line: `3 item(s) marked read`.

## `bunker backfill mail <account> --since YYYY-MM-DD [--folder INBOX] [--dry-run] [--json]`

Recovers mail history a bounded initial sync never reached: it runs `UID
SEARCH SINCE <date>` on `--folder` (default `INBOX`), then batch-FETCHes
headers/flags (the exact same item-building path `Run`'s own sync uses)
for whatever UIDs the store doesn't already have, and upserts them. Only
`mail` is a supported channel today (the first positional names it
explicitly, so a future channel's own backfill support slots in the same
way later).

- `<account>` is the configured mail account name.
- `--since YYYY-MM-DD` (required) is the earliest date to search from.
- `--folder` defaults to `INBOX`.
- `--dry-run` reports the count and id range without upserting anything.
- It never marks anything `\Seen` (BODY.PEEK, like `read`/`download`) and
  never moves the account's regular sync cursor — a later `daemon` run
  resumes exactly where it left off, regardless of how far back a
  backfill reached.
- Idempotent: an id the store already has is never re-fetched or
  overwritten, so running the same `--since` twice adds nothing new the
  second time.

```json
{"dryRun": false, "result": {"Count": 2, "FirstID": "mail:cl:1700000000.13262", "LastID": "mail:cl:1700000000.13333"}}
```

Non-JSON output: `added 2 item(s), range mail:cl:...13262..mail:cl:...13333`
(`would add` instead of `added` on `--dry-run`), or `added 0 item(s);
nothing found since <date>` when the search matched nothing.

## `bunker search mail <account> [--from x] [--subject y] [--since D] [--before D] [--folder INBOX] [--limit 50] [--json]`

Runs an IMAP `UID SEARCH` for the given criteria and upserts every match
into the store (headers only, read-only — it never marks anything read),
printing them exactly like `list` does (same line format, same
`{"items": [...]}` JSON shape) — this is the escape hatch for not
knowing an item's id at all, only roughly what it should contain. Only
`mail` is a supported channel today, same as `backfill`.

- `<account>` is the configured mail account name.
- `--from` matches the `From` header; `--subject` matches `Subject`.
- `--since`/`--before` are `YYYY-MM-DD` dates, both optional.
- `--folder` defaults to `INBOX`; `--limit` defaults to 50.

```json
{"items": [{"ID": "mail:cl:1700000000.13300", "Channel": "mail", "Account": "cl", "Subject": "hi", "Unread": true, "Meta": {}}]}
```

Non-JSON output is one line per match: `<mark> <id>\t<subject>` (`*` for
unread), same as `list`.

## Presence, typing and the availability lease (K3)

No CLI subcommand exposes these — they exist for the interactive TUI's
chat view (a later task) — but they are part of the daemon's RPC
contract, over `rpc.Client`:

- `Presence(ctx, channel, account, thread string) (core.Presence, error)`
  — `core.Presence{State string; LastSeen time.Time; Typers []string}`.
  `State` is `"online"`, `"offline"`, `"typing"`, `"recording"`, or
  `"unknown"`. A channel with no live presence (mail) always answers
  `State: "unknown"`, never an error.
- `PresenceKeepalive(ctx, channel, account, thread string, focused bool)
  error` — the availability lease. **The daemon stays unavailable by
  default.** WhatsApp only delivers a contact's presence/typing while
  the user's own account is itself "available" (which also shows the user
  online to contacts), so a chat view must call this every ≤20s while it is
  open: `focused=true` grants (or renews) availability and subscribes to
  `thread`; `focused=false` — or 60s without a renewing call, or the
  daemon shutting down — revokes it. A channel without this gate
  (Matrix, mail) treats every call as a no-op success.
- `Typing(ctx, channel, account, thread string, composing bool) error` —
  a thin, validated forward to the channel's typing indicator. `thread`
  is required; a channel without typing (mail) returns an
  unsupported-capability error. **The daemon never throttles the send
  rate itself** — the caller must (at most every 5s while typing;
  `composing=false` on idle, send, or leave).

## Attachment download in the TUI's chat and mail thread views (`d` / `Ctrl+D`)

Both K5's chat view (WhatsApp/Matrix) and K6's mail thread view let
the user save an attachment straight from the conversation with `d`,
without leaving to the CLI's `bunker download`. No CLI subcommand exposes
this either — it is the interactive TUI's own `Client.Download`, the
exact `rpc.Client.Download(ctx, id string, index int, destPath string,
opts core.DownloadOptions) (core.DownloadResult, error)` signature above,
reused as-is.

- In the mail thread view, `d` acts on the currently selected stacked
  message (the same one `r`/`R`/`f` act on): it downloads its one
  attachment directly, or opens a small numbered picker for 2+.
- In the chat view the composer always has focus, so every printable
  key is draft text (a message may start with "d"). Download is on
  `Ctrl+D` instead: it reaches for the newest loaded message that
  carries an attachment (searching backward, so an attachment a few
  messages back is still reachable even if the very last message has
  none), whether or not a draft is in progress.
- The destination defaults to `~/Descargas/<attachment name>` (falling
  back to `~/Downloads` when `~/Descargas` does not exist), shown as an
  editable text field before anything is downloaded. The attachment's
  own name — content an attacker fully controls — is sanitized down to a
  single safe filename first: no path separator, no `..`, no embedded
  NUL survives into the suggested path, so a hostile attachment name can
  never escape the destination directory on its own; the user can still
  edit the field to any path they want, which is their own explicit
  choice, same as `download -o`.
- Confirming (`Enter`) a path that already exists on disk asks
  "¿Sobrescribir?" first (`Enter`/`Esc`) and only an explicit `Enter`
  there passes `Force: true` to `Download` — exactly `download`'s own
  "refuses to clobber without `--force`" rule, just interactive.
- The download itself never blocks the UI (it is a `tea.Cmd`, like every
  other RPC call here): a "Descargando..." status shows while it is in
  flight, then either "Guardado: `<path>` (`<bytes>` bytes)" or the
  daemon's own error verbatim (the size cap, a missing WhatsApp media
  key, mail's `BODY.PEEK` failure, Matrix's unsupported-capability
  error) — `Esc` closes the result or backs out of an error to retry.

## `bunker render [--tmux] [--json]`

Renders the tmux status segment: one glyph and unread count per channel,
in this fixed order:

| Channel  | Glyph |
|----------|-------|
| mail     | ✉     |
| whatsapp | 💬    |
| matrix   | ⌘     |

Plain output: `✉ 3  💬 5  ⌘ 2` (two spaces between segments).

```json
{"segments": [{"channel":"mail","glyph":"✉","unread":3}, ...], "daemonUp": true, "allConnected": true}
```

`render` never blocks tmux: it gives itself a 200ms budget. It tries the
live daemon first; if that is unreachable or too slow, it falls back to a
direct, read-only open of the store file (`daemonUp: false` in that case).
If neither works within budget, it prints a dead marker instead of
hanging:

- plain: `bunker: dead`
- json: `{"dead": true}`

When the live daemon does answer within budget, `render` also asks it for
`bunker health`'s snapshot (still inside the same 200ms budget): if any
adapter is not `connected`, plain output appends a trailing `! ` marker
(`✉ 3  💬 5  ⌘ 2 !`) and JSON's `allConnected` is `false`. This never adds
extra latency risk beyond the render budget already enforced, and it
never changes render's dead-daemon behavior: `daemonUp: false` (or the
dead marker) means health was never even asked. If the health call itself
doesn't answer in time, `allConnected` defaults to `true` (no marker)
rather than guessing.

Styles:

- default: plain `✉ 3  💬 5  ⌘ 2`.
- `--tmux`: tmux `#[fg=…]` colors with Nerd Font glyphs (mail `󰇮` #4db0ff, WhatsApp `󰖣` #25d366, Matrix `󰘨` #0dbd8b). Channels at 0 are dimmed to #a3a09e.
- `--ansi`: the same glyphs and colors as 24-bit ANSI escapes, for a Claude Code statusline or a shell prompt.
- `--hide-empty`: print nothing when no channel has unread items, so a status line can disappear.

Any styled glyph can be overridden in `config.toml`, for example with a codepoint from a locally installed icon font:

```toml
[render.glyphs]
matrix = "\U00100000"   # keys: mail, whatsapp, matrix
```

The interactive panel's desktop notifications (see above) can be turned
off in `config.toml` too, independently of the `BUNKER_TUI_NOTIFY=0`
environment opt-out:

```toml
[tui]
notify = false
```

## `bunker link whatsapp <account>`

QR-pairs a configured WhatsApp account: calls the adapter's own `Link`
directly, never through the daemon (there is no session yet for it to
serve). It renders each QR code to stdout as whatsmeow issues one, and
returns once the device is linked.

## `bunker link matrix <account> [--recovery-key|--recovery-key-stdin]`

Runs the SSO login flow for a configured Matrix account: opens a
localhost callback, prints the homeserver's SSO URL to stdout, and on
success persists the session under the account's state directory
(`$BUNKER_STATE_DIR/matrix/<account>` by default), ready for `bunker
daemon` to pick up.

With `--recovery-key` or `--recovery-key-stdin`, it then imports the
account's recovery key (`internal/channel/matrix.ImportRecoveryKeyForAccount`),
using the session and crypto store the login (or a prior one) just
wrote — it never logs in again to do this. This restores cross-signing
keys (via secret storage, SSSS) and megolm sessions from the server-side
key backup, and prints `restored cross-signing keys; restored N/M megolm
sessions from the key backup`.

- `--recovery-key` is a boolean flag. The key is **never** a command-line
  argument (it would leak into shell history and `ps`): it prompts on the
  terminal with echo disabled and refuses to run when stdin is not an
  interactive terminal, naming `--recovery-key-stdin` instead.
- `--recovery-key-stdin` reads the key from a pipe instead, for feeding it
  from an extractor without writing it to disk. Either way, all
  whitespace in the key (spaces, the spec's own visual grouping, embedded
  newlines from a wrapped extraction) is stripped before use.
- The two flags are mutually exclusive.
- Errors from this step never include the key itself.

## `bunker import-keys matrix <account> <file> [--passphrase-stdin]`

Imports an Element-style megolm key export (a file starting with
`-----BEGIN MEGOLM SESSION DATA-----`) into an already-logged-in
account's existing session and crypto store
(`internal/channel/matrix.ImportKeyExportForAccount`, via mautrix crypto's
own `OlmMachine.ImportKeys`). Prints `imported N new, M already known
(T in export) from <file>`, adding `, F failed` before the total when the
export contained sessions the machine rejected outright (bad algorithm,
mismatched session ID).

mautrix's own `ImportKeys` counts a session as "imported" whenever
storing it succeeds, even when the store already had that exact session
(storing is an idempotent upsert) -- so it cannot tell new sessions from
already-known ones on its own. `ImportKeyExportForAccount` wraps the
account's crypto store to observe the lookup `OlmMachine.StoreGroupSession`
always does before writing, and reports the honest split. This fixes a
live bug: importing a 159-session Element export reported "imported
159/159" while the crypto store's session count never moved, because all
159 sessions were already covered by the server's key backup.

The export passphrase is read the same secure way as the recovery key:
prompted on the terminal with echo disabled by default, or from a pipe
with `--passphrase-stdin`. Unlike the recovery key, the passphrase is
**not** whitespace-normalized (only a trailing line ending is trimmed): it
may legitimately contain spaces.

After either `--recovery-key`/`--recovery-key-stdin` or `import-keys`
succeeds, restart `bunker daemon` (or run it for the first time) so the
matrix adapter's `RetryUndecryptable` step — which runs once at every
daemon startup — re-fetches and decrypts items it had previously stored
undecryptable, now that the newly imported keys may cover them.

Neither `link` nor `import-keys` accepts `--json`: they are interactive,
one-shot setup steps, not scriptable data commands.

## Errors

Every command's `--json` error shape is:

```json
{"error": "human-readable message"}
```

Exit codes: `0` success, `1` an error occurred while handling the command
(unknown id, unsupported capability, an invalid flag combination such as
`--seen --unseen` together, I/O failure), `2` bad usage caught before the
command ran (missing argument, unknown flag, unknown command).

## `core.Item` JSON shape

```json
{
  "ID": "mail:cl:1234",
  "Channel": "mail",
  "Account": "cl",
  "Thread": "t1",
  "ThreadName": "Support ticket #42",
  "From": {"ID": "a@b.cl", "Name": "A"},
  "To": [{"ID": "c@d.cl", "Name": "C"}],
  "Subject": "hi",
  "Body": "body",
  "Attachments": [{"Name": "f.pdf", "MIME": "application/pdf", "Size": 10, "Ref": "ref1"}],
  "Labels": ["inbox", "vip"],
  "Unread": true,
  "FromMe": false,
  "Timestamp": "2026-01-02T03:04:05Z",
  "Meta": {"uid": "42"},
  "Edited": false,
  "Deleted": false,
  "Reactions": [{"Sender": "5511999999999@s.whatsapp.net", "Emoji": "👍"}]
}
```

`FromMe` is `true` when the account owner sent the message: WhatsApp's
`IsFromMe`, a Matrix event whose sender is the account's own user, or a
mail item whose `From` matches the account's own address (including one
synced from the Sent folder — see `thread` above). It drives the chat
view's right-aligned bubbles and is independent of `Unread` (mail's IMAP
`\Seen` flag toggles on its own).

`Edited`, `Deleted` and `Reactions` are S2's channel-agnostic WhatsApp
edit/revoke/reaction model (Matrix can adopt the same shape later, not
yet in scope). `Edited` is `true` once a channel-observed edit replaced
`Body`. `Deleted` is `true` after a revoke; the row is kept (its place in
`list`/`thread` pagination is preserved) but `Body` is cleared, and the
TUI renders "mensaje eliminado" in its place. `Reactions` lists at most
one entry per `Sender` — a newer reaction from the same sender replaces
the previous one, and an empty `Emoji` removes it — rendered as a line of
emoji under the message in the chat/thread view. Both fields are absent
of any effect for a plain, never-edited/revoked/reacted-to item
(`Edited`/`Deleted` `false`, `Reactions` empty).

## Adapter registration hook

Each configured account's channel maps to a constructor through:

```go
func RegisterAdapter(channel string, ctor func(acc config.Account) (core.Adapter, error))
```

None of the L2 channel packages (mail, whatsapp, matrix) can call this
from their own `init()`: `RegisterAdapter` lives in `cmd/bunker`, which is
`package main`, and Go refuses to import a `main` package from anywhere
else. So `cmd/bunker/wire.go` does the opposite instead — it is the one
place that imports `internal/channel/{mail,whatsapp,matrix}` directly,
registering `mail.NewAdapter`, `whatsapp.NewFromAccount` and `matrix.New`
from its own `init()`. `internal/core` still never imports an adapter
package, keeping the hexagonal boundary at the core, not at `cmd/bunker`.

`channel` matches the `channel = "..."` value used in `config.toml`
(`"mail"`, `"whatsapp"`, `"matrix"`). `ctor` receives the parsed
`config.Account` (with `Options map[string]interface{}` for
channel-specific keys) and returns a `core.Adapter`. `cmd/bunker` builds
the registry once at daemon startup by calling this constructor for every
configured account; an account whose channel has no registered
constructor is a startup error naming the missing channel and account.
