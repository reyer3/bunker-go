# bunker CLI

`bunker` is one binary: a daemon that talks to channel adapters and stores
their items, and a set of client subcommands that drive it over a unix
socket. Every command that produces data accepts `--json`, which is the
stable, documented contract Claude Code parses; without it, output is
compact plain text for a human at a terminal.

## `--json` coverage

| Command | `--json` |
|---|---|
| `list`, `find`, `read`, `thread`, `search`, `counts`, `health`, `contacts`, `chats`, `meetings`, `calls`, `avatar`, `download` | yes: data |
| `reply`, `send`, `edit`, `delete`, `react`, `organize`, `status post`, `call`, `call answer\|reject\|hangup`, `backfill` | yes: `{"dryRun", "plan", ...}` with the [`core.Plan`/`core.Receipt` shape](#coreplan-and-corereceipt-json-shape) |
| `unread`, `read-thread`, `meetings join` | yes: the result of the change |
| `version`, `update`, `render`, `herdr toggle` | yes |
| `import-keys matrix` | yes: `{"new", "already_known", "failed", "total"}` |
| `daemon`, `mcp`, `bunker` (no arguments), `sidebar`, `open`, `app`, `link`, `help` | exempt: interactive, long-running or setup commands with no data result |

Every JSON error is `{"error": "..."}` (see [Errors](#errors)).

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
channel; the WhatsApp and Matrix ones also hold two already-read
conversations, so their chat-list tabs have something to show), starts each adapter's receive loop under a supervisor, and
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

## `bunker version [--json]`

Prints this binary's version (also `bunker --version`); it needs no
daemon. A release build carries the version, short commit and build date
goreleaser stamps in; a source build (`make dev`, `go build`, `go
install`) falls back to the build info the Go toolchain embeds: the
module version (`dev` for a plain checkout, a tag or a pseudo-version
for `go install`) and the VCS revision and time.

```json
{"version":"0.12.0","commit":"abc1234","date":"2026-09-29T00:00:00Z","go":"go1.26.8","os":"linux","arch":"amd64","release":true}
```

`release` is false for `dev`, pseudo-version and `+dirty` builds: they
never look for updates and `bunker update` refuses to replace them.

## `bunker update [--dry-run] [--yes] [--json]`

Replaces this binary with the latest GitHub release; it needs no daemon.

1. Asks the GitHub API for the latest release and stops, exit 0, when
   this binary is already that version or newer (a pre-release is never
   installed over a stable release).
2. Picks the `bunker_<version>_<os>_<arch>.tar.gz` asset for this
   `GOOS`/`GOARCH` and downloads it and `checksums.txt` (at most 150 MiB
   and 64 KiB; 5 minutes per download, 10 for the whole command).
3. Verifies the archive's SHA-256 against its `checksums.txt` entry. A
   mismatch, or no entry for the asset, is an error and nothing changes.
4. Extracts `bunker` from the archive into a temp file in the directory
   of the running binary (`os.Executable()`, symlinks resolved, so a
   link is followed to the real file), mode 0755, renames the current
   binary to `bunker.old` (for a manual rollback) and the new one into
   its place. Both renames stay in one directory, so each is atomic.
5. Restarts the daemon when `systemctl --user is-active bunker` says the
   user service is running (what `make dev` does); otherwise it says to
   restart `bunker daemon`. A failing restart is an error (exit 1), with
   the new binary already in place.

It asks `Proceed? [y/N]` on a terminal unless `--yes`; with stdin not a
terminal and no `--yes` it refuses rather than guess. It refuses a `dev`
or pseudo-version build (update those the way they were built: `git pull
&& make dev`) and a binary whose directory it cannot write to, and says
why.

`--dry-run` prints the plan (version from → to, the asset URL, the
target path and where the old binary goes) and never touches the
network: it reads the release the daemon cached at
`$BUNKER_STATE_DIR/update.json`, and fails when there is none yet.

```
update bunker 0.12.0 → 0.13.0
  download  https://github.com/reyer3/bunker-go/releases/download/v0.13.0/bunker_0.13.0_linux_amd64.tar.gz
  verify    sha256 against https://github.com/reyer3/bunker-go/releases/download/v0.13.0/checksums.txt
  replace   /home/me/.local/bin/bunker (keeping the current one as /home/me/.local/bin/bunker.old)
  restart   bunker.service if it is active
```

With `--json`, `--dry-run` prints `{"dry_run":true,"plan":{...}}` and an
update prints `{"updated":true,"plan":{...},"service_restarted":true}`,
where `plan` has `from`, `to`, `asset`, `asset_url`, `checksums_url`,
`target` and `backup`; an up-to-date binary prints
`{"up_to_date":true,"version":"0.13.0","latest_version":"0.13.0"}`.

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
message underneath. A message with no text previews as a short label
instead: `🎤 Nota de voz 0:12`, `📷 Foto`, `🎥 Video`, `Sticker`, `🎵 Audio`,
`📎 <nombre>` for other files, `🚫 Mensaje eliminado` and `📞 Llamada`. A
collapsed Mail sender previews its newest thread's subject (and the start
of its body once fetched). The selected row gets a full-width highlight and a
colored left bar. An empty section still shows its header and one dim
"sin pendientes" line. The overview shows all three sections at once,
each getting a fair share of the pane's height; a section with more
conversations than fit ends in a dim "+N más" line rather than pushing
another section off screen.

**Chat list (WhatsApp and Matrix tabs).** Mail and the overview (`Todo`)
list unread conversations only: reading one makes it disappear (inbox
zero). The WhatsApp and Matrix tabs (`2`, `3`, and the same tabs in
`bunker sidebar`) work like a messaging app instead: they list the most
recent conversations (up to 200 per channel), read or not, one row each,
ordered by their newest message. A conversation with unread messages
carries its `⬤N` badge and a bold title; a fully read one has no badge and
a regular-weight title, and the preview line shows the newest message
(`Tú: ...` when it is ours). `Enter`, a click, or the sidebar's opener
opens a read conversation exactly as an unread one. `m` (mark read) does
nothing on a fully read row. The section header's number is still the
unread total, and notifications still come from the unread list only. An
empty list shows "sin conversaciones". The list comes from
[`bunker chats`](#bunker-chats---channel-c---account-a---limit-n---json); against
an older daemon without it, these tabs keep listing unread items only.

**Reuniones.** Under the conversation list (inbox and sidebar) a compact
"Reuniones" section lists the next meetings from calendar invitations and
recent call links, as listed by
[`bunker meetings`](#bunker-meetings---days-n---json): `📅 10:30 Revisión
semanal · en 25 min`, with `ahora` while it is on, `en N min` or `en N h`
within the day, then `hoy`, `mañana`, `jue 8`, `15 oct`; a bare link shows
`🔗 Equipo · enlace`. A click on a row, or `J` (the first meeting with a
link; also "Unirse a la próxima reunión" in the palette), opens its link
with `xdg-open` (or `$BUNKER_OPEN_URL`) and says "Abriendo reunión…" on the
status line; a meeting without a link, or an opener that is missing, says so
there instead. The section shows at most 4 rows (the rest as `+N más`),
takes at most a third of the pane and only what still fits, so a channel
section or the footer never leaves the screen, and it is hidden while
there is nothing to show. It refreshes with the 5-second poll; against an
older daemon without `meetings` it stays hidden.

Every view ends in a one-line key hint in the same notation (`key
label`, joined by ` · `). When it does not fit the width, the
lowest-priority keys are dropped instead of wrapping, and how to leave
and how to get help always stay.

`?` (or `F1`, which also works in a chat, where `?` is text) opens the
help overlay on the section for the current view: inbox, chat, mail
thread, mail editor, contact picker or command palette. It scrolls
with `j`/`k`, the arrows, `PgUp`/`PgDn` or the wheel. `Esc`, `?` or `F1`
close it. At
terminal widths under ~30 columns the two-line row
collapses to one line (no preview). Setting `NO_COLOR` disables all
color, same as everywhere else in bunker.

**Command palette.** `Ctrl+K` opens a list of what the current view can
do, each command with its key on the right; `F2` opens it too, and is
the only way from a chat, where the composer always has focus and
`Ctrl+K` deletes to the end of the line (the reply composer, the mail
editor, the `/` filter and the contact picker never open it).
- **Picking:** typing filters by prefix, word, substring or letters in
  order, ignoring case and accents (`leido` finds "Marcar leído").
  `↑`/`↓` (or `Ctrl+P`/`Ctrl+N`) move, `Enter` runs, `Esc` (or `Ctrl+K`/
  `F2` again) closes.
- **Commands:** only the current view's: in the inbox and the sidebar
  "Nuevo mensaje", "Buscar / filtrar", "Refrescar", "Ir a
  Todo/Mail/WhatsApp/Matrix", "Marcar leído", "Deshacer leído",
  "Responder", "Ayuda", "Salir"; in a mail thread "Responder", "Responder
  a todos", "Reenviar", "Descargar adjunto"; in a chat "Editar", "Borrar"
  and "Reaccionar al último mensaje" and "Descargar último adjunto"; and
  "Preguntar a Claude" only inside herdr. One that cannot run right now
  is dimmed with the reason (`Deshacer leído · nada que deshacer`) and
  `Enter` ignores it.
- **Same path as the key:** running a command presses its key, so every
  send, mark-read, edit, delete or reaction still goes through its
  dry-run preview and explicit confirm.
- **Recent conversations:** listed under "Recientes" (the loaded inbox,
  newest first, up to 8, filtered by the same text); `Enter` opens one
  as `Enter` on its inbox row does, leaving the conversation on screen
  first.
- **Shortcuts:** from the inbox and the side panel it also lists
  "Buscar contacto" (`@`) and "Llamar" (`c`, dimmed with the reason when
  the selected row cannot be called).
- **`@` contacts:** a query starting with `@` lists contacts instead (the
  same `bunker contacts` lookup as `n`); `Enter` starts a new message to
  one, exactly like picking it with `n`.
- A `bunker open` pane lists no recent conversations or contacts: it
  exists for its one conversation.

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
confirm; on WhatsApp this sends the other side a read receipt), `u`
undoes the last mark-read, including opening a chat or mail thread (see
`bunker unread`), `n` starts a new conversation and `@` searches a contact
(see below), `c` calls the selected WhatsApp conversation (see Voice calls),
`g`
refreshes the inbox now, `?` opens the help overlay, `q`
quits (asks again first if a reply preview/send is in flight). A directly
opened single-item detail view (kept for parity; every current channel
instead opens its own chat/thread view below) scrolls a long body with
`j`/`k`, arrows, `PgUp`/`PgDown`, `G` (jump to the end) and the mouse
wheel, the footer keymap hint always keeps `q` visible even at a narrow
(40-column) terminal width.

`/` filters the inbox: typing narrows it to conversations whose name,
sender, subject or text contains the query, ignoring case and accents.
`Enter` keeps the filter and returns to the list, and `Esc` clears it. A
section the filter empties says `sin coincidencias`. Text with a query
operator (`from:`, `is:unread`, `in:Archive`, `before:7d`, ... the
grammar of `bunker list --query`) searches the whole store instead: the
daemon answers it through `list_page` on `Enter` or after a short pause
in typing, its results replace the inbox (Mail rows are listed directly,
not under sender headers) until `Esc`, and moving past the last result
loads the next page. The status line shows the active query and how many
conversations it found; a typo such as `foo:` shows `consulta inválida:
operador desconocido "foo:"` on the filter line and keeps the text. A
daemon without `list_page` keeps the in-memory filter and says so.

A mail row from a folder other than INBOX shows the folder dimmed beside
its time, and the mail thread header shows it after the subject, in a
short form: the account's `folder_prefix`/`folder_sep` are stripped
(`INBOX.Clientes.Acme` reads `Clientes/Acme`) and Gmail's special folders
are named in Spanish (`[Gmail]/All Mail` reads `Todos`). An opened chat says
`cargando mensajes…` while it loads and `sin mensajes todavía` when there
is no history yet.

Drafts are kept for the session. Leaving a chat, or closing the reply
composer or the mail editor with `Esc`, keeps the unsent text (the status
line says `borrador guardado`), and reopening the same conversation or
reply restores it. A successful send forgets it.

New conversation (`n`): a contact picker over `bunker contacts`.
- **Picking:** typing filters by name or address, ignoring accents.
  `↑`/`↓` (or `Tab`) choose, and `Enter` or a click opens the contact.
  `Esc` cancels.
- **Chat contacts (WhatsApp, Matrix):** the chat view opens, with an empty
  history if you have never talked. The first message is a fresh send to
  the contact, still previewed and confirmed like a reply.
- **Mail contacts:** the full editor opens with the address in To and the
  cursor on the subject.

Contact search (`@`): the same picker, headed `Buscar contacto`, from the
inbox list and the side panel (not inside a chat, thread, detail view,
editor, filter or palette, where `@` is text). On the WhatsApp or Matrix
tab it is scoped to that channel (`Buscar contacto en WhatsApp`, the
daemon's `contacts` lookup with `channel` set); from the overview and Mail
it lists every channel, like `n`, which always does. `Alt+C` on a
highlighted WhatsApp contact opens its chat and starts the call preview
(see Voice calls); on any other contact the picker says why it cannot be
called and stays open.

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
literal draft text; `Enter` (or `Ctrl+S`, the send key of every other
composer) sends a plain text message with **one** keypress: the panel
still runs the dry-run first (so the plan is validated like any other
send) and sends only if it comes back clean; a plan error is shown and
nothing is sent. Everything else keeps the explicit flow — the dry-run
preview and an inline "¿Enviar a …? ↵ enviar · Esc cancelar" confirm that
a second `Enter` or `Ctrl+S` answers — namely attachments, voice notes, a
new conversation started from the contact picker, edits, calls and every
mail composer. `[tui] confirm_chat_send = true` in the config restores the
two-step flow for plain text too (see `docs/config.example.toml`). Input
is ignored while the preview or the send is in flight, so a fast double
`Enter` sends once. `Esc` cancels back to editing with the draft kept, and typing your
own composing state is reported to the other side, throttled to at most
once every 5s and cleared after 5s idle, on send, or on leave. `Alt+Enter`
inserts a newline (plain `Enter` is reserved for send/confirm here, unlike
the mail reply composer). `Up` at the top of an empty draft loads an
older page of the conversation instead of moving the cursor. `Esc` leaves
the chat view and returns to the inbox.

**Selecting and copying text.** The panel captures the mouse, which is
what keeps the terminal from selecting text. Three ways around it:
`F7` (or `Alt+S`) enters the *selection mode*: the mouse goes back to the
terminal, a "Modo selección: selecciona con el ratón · Esc/F7 volver"
line replaces the key hints, and `Esc` or `F7` capture it again. Many
terminals also select with `Shift`+drag while the mouse is captured, no
mode needed. And `Alt+Y` copies a whole message to the clipboard: in a
chat, the message a click selected (the bubble is highlighted) or, with
none selected, the newest; in a mail thread, the selected message's body;
in the plain detail view, its body. The clipboard is written through
`wl-copy` (Wayland), `xclip` or `xsel` (X11) when one is installed, else
as an OSC 52 sequence, which the terminal turns into a clipboard write
(it works over SSH and in tmux with `set-clipboard on`; some terminals
need it enabled). The status line says "Copiado", or why it failed.

**Opening attachments.** A double click (two presses on the same
attachment line within 400 ms) in a chat bubble or in an expanded mail
message downloads the file into the media cache and opens it with
`xdg-open`; `BUNKER_OPEN_FILE` replaces the command (the path is appended
as the last argument). `Alt+O` does the same from the keyboard for the
selected chat message's first file (or the newest message that has one)
and for the selected mail message's first attachment. A missing opener is
an error shown on screen, before anything is downloaded. Voice notes keep
their single click to play; on an image thumbnail the first click still
opens the viewer and the second opens the file; on a video the first
click plays it as before. All of these are also in the command palette.

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

Lists items from the local store, newest first (items sharing a
timestamp are ordered by id, descending, so the order is total).

Flags: `--channel c`, `--account a`, `--unread`, `--label l`, `-q text`,
`--query q`, `--cursor c`, `--limit n` (default 0, no limit).

`--query` takes the query language below. `--cursor` resumes after the
page a previous call returned: pass it that call's `next_cursor`. The
cursor is opaque (base64 of the last item's timestamp and id), stable
under ties, and a malformed one is an error rather than a restart from
the top. Every flag combines with every other (AND).

`-q` is a literal full-text search over subject, sender name and address,
recipients, body, attachment names and thread name, ignoring case and
accents (`jose` finds `José`). Every word must match; the last one also
matches as a prefix (`factu` finds `factura`). Operators and quotes in the
text are searched for literally. A query with no letters or digits (e.g.
`%`) falls back to a literal substring match over subject and body.
Results stay newest first. Mail sync stores the first
`index_body_max_kb` (default 64 KiB) of each message's text, so mail is
found by its body too; attachment names are indexed once the message has
been read (`read`/`download` fetch it whole and keep its body and
attachments in the store). With `index_body_max_kb = 0` sync stores
headers only and a mail's body becomes searchable only after it is read.

```json
{"items": [ <core.Item as JSON, see below> ], "next_cursor": "eyJ0Ijo..."}
```

`next_cursor` is always present and is `""` on the last page (with no
`--limit`, every match is one page). Non-JSON output is one line per
item (`<mark> <id>\t<subject>`, `*` for unread); when there is a next
page, `next_cursor: <c>` goes to stderr so stdout stays one item per
line.

### Query language (`--query`, `bunker find`, the MCP `search` tool)

Terms are separated by spaces and must all match. Operator names ignore
case.

| Term | Matches |
|---|---|
| `word` | free text over subject, sender, recipients, body, attachment and thread names, like `-q` (prefix-matched, ignoring case and accents) |
| `"a phrase"` | those words together, in order (no prefix) |
| `from:x` | the sender's name or address (`from:ana`, `from:ana@example.com`, `from:"Ana María"`) |
| `to:x` | a recipient's name or address |
| `subject:x` | the subject only |
| `is:unread`, `is:read` | read state |
| `has:attachment` | at least one attachment |
| `in:<folder>` | mail folder (`Meta.folder`: `INBOX`, `Sent`, or the mailbox name), ignoring ASCII case, so `in:inbox` works; quote names with spaces |
| `channel:mail\|whatsapp\|matrix` | one channel |
| `account:x` | one account (exact name) |
| `label:x` | items carrying that label, ignoring ASCII case |
| `after:D` | on or after `D` |
| `before:D` | strictly before `D` |
| `-term` | negates any word, phrase or operator: `-spam`, `-"no reply"`, `-in:inbox`, `-from:bot` |

`D` is either `YYYY-MM-DD` (the start of that day in the daemon's local
time) or relative to now: `7d` (days), `2w` (weeks), `3m` (calendar
months). `after:7d` is "in the last seven days"; `before:3m` is "older
than three months".

Negation keeps items the field does not apply to: `-in:inbox` also
returns chats (which have no folder).

Errors are loud, never a silently widened search: an unknown operator
(`foo:bar`, or a typo like `form:ana`) names the operator; an operator
with no value, an invalid `is:`/`has:`/`channel:` value, a malformed date
and an unterminated quote are errors too. Only a letters-only prefix
before `:` is an operator, so `10:30` is plain text; quote anything else
containing a colon (`"http://x"`) to search for it. Values are always
bound as parameters and quoted for the full-text index, so quotes, `%`,
`_` and FTS5 syntax (`OR`, `NEAR`, `*`, `{col}:`) are searched for as
text.

Examples:

```sh
bunker list --query 'from:ana is:unread has:attachment after:2w' --json
bunker list --query 'in:inbox -label:newsletter "orden de compra"' --limit 20 --json
bunker list --query 'channel:whatsapp before:2026-01-01' --limit 20 --cursor eyJ0Ijo... --json
```

## `bunker find <query> [--cursor c] [--limit 50] [--json]`

A shortcut for `bunker list --query`: the positionals are joined with
spaces into the query, so `bunker find from:ana factura` needs no
quoting (shell-quote phrases: `bunker find '"orden de compra"'`). It
takes the same flags as `list` and prints the same output, but
`--limit` defaults to 50 and a query is required. It searches the local
store only; `bunker search` is the different command that asks the mail
server.

## `bunker read <id> [--mark-read] [--json]`

Fetches the full item, using the adapter's `Fetcher` capability if it has
one, falling back to the stored copy otherwise. **By default it has no
side effect**: nothing is marked read, locally or on the channel (this
changed in the release that introduced `--mark-read`; before it, `read`
marked the item read unless `--no-receipt` was given). Reading is safe to
script and to hand to an agent.

With `--mark-read` it also marks the item read on the channel itself when
the adapter implements `core.ReadMarker`, and in the local store:

- **WhatsApp**: presence `available` (so the linked device does not
  suppress the phone's own push notification while it looks "active"),
  the read receipt (blue ticks), then presence `unavailable` again.
- **Matrix**: an `m.read` receipt and the fully-read marker for the same
  event, in one call. No presence — Matrix has no equivalent concept.
- **Mail**: never marked. Mail has no `ReadMarker`; `Fetch` always uses
  IMAP `BODY.PEEK`, so reading a mail item is unaffected by `--mark-read`
  either way.

`--no-receipt` is a deprecated no-op kept so old scripts keep working
(`read` no longer marks anything by default); combining it with
`--mark-read` is a usage error (exit 2). The MCP `read` tool and the
TUI's item fetches never mark anything read; the TUI marks through its
own explicit actions (opening a conversation, `read-thread`).

```json
{"item": { <core.Item> }}
```

## `bunker reply <id> <text|-> [--cc addr]... [--attach path]... [--idempotency-key k] [--dry-run] [--json]`

`bunker reply <id> --voice <file.ogg>` replies with a voice note instead
(no text; see [Voice notes](#voice-notes-notas-de-voz)).

Replies to item `<id>`. `<text>` can be `-` to read the body from stdin.
`--dry-run` returns the `Plan` alone and never reaches the channel
adapter.

`--idempotency-key k` makes a retry safe: the daemon sends at most once
per key. A repeat of a key that already sent returns the first call's
plan and receipt, with `receipt.replayed: true`, and sends nothing; a
repeat while the first call is still sending waits for it and shares its
result; a failed send is forgotten, so the same key can retry it. Keys
live in the daemon's memory for 24 hours (at most 1024 of them, least
recently used dropped first) and are lost when it restarts. Reusing a key
for a different message is an error. `send` takes the same flag.

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

`receipt` is the zero value (`{"id":"","channel":"","at":"0001-01-01T00:00:00Z"}`)
when `dryRun` is `true`.

## `bunker send <channel> <account> <to> <text|-> [--cc addr]... [--subject s] [--attach path]... [--media path]... [--idempotency-key k] [--dry-run] [--json]`

`bunker send <channel> <account> <to> --voice <file.ogg>` sends an Ogg
Opus file as a voice note (no text; see
[Voice notes](#voice-notes-notas-de-voz)).

Sends a fresh message, not tied to any existing item. Same `--dry-run`,
`--idempotency-key` and JSON shape as `reply`, plus new
`plan.Media`/`plan.Attachments` fields (see below). A broadcast is
remembered as a whole, including recipients that failed, so retrying
those needs a new key.

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

`plan.cc` (alongside `plan.target`, which lists `<to>`) carries the Cc
list; it is omitted when there is no `--cc`.

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
{"dryRun": false, "plan": { <core.Plan>, "media": ["/path/to/pic.png"], "attachments": [{"name": "pic.png", "mime": "image/png", "size": 2048}] }, "receipt": { <core.Receipt> }}
```

`plan.media` (paths, kept for compatibility) and `plan.attachments`
(name/mime/size, computed and validated as above) are the only fields
added by this flag — everything else matches the `reply`/`send`/`status
post` shape. Both are omitted for a plain send/reply with no
attachments.

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
outcome is reported. `receipt.recipients` lists `{"to", "receipt",
"error"}` per recipient (`error` is omitted on success); the top-level
`receipt.id`/`channel`/`at` mirror the *first successful* recipient, so a
JSON consumer that only reads those top-level fields keeps working
unchanged for a single-recipient send. The CLI process exits `1` if any
recipient failed, `0` only when every one succeeded.

```json
{"dryRun": false, "plan": { <core.Plan> }, "receipt": {
  "id": "...", "channel": "whatsapp", "at": "...",
  "recipients": [
    {"to": "+51111", "receipt": {"id": "...", "channel": "whatsapp", "at": "..."}},
    {"to": "+51222", "receipt": {"id": "", "channel": "", "at": "0001-01-01T00:00:00Z"}, "error": "whatsapp: send: +51222 is not on WhatsApp"}
  ]
}}
```

`--dry-run` on a fan-out shows the full per-recipient plan and an
estimated pause budget (`plan.fanout_pause_min`/`fanout_pause_max`, in
nanoseconds in JSON: the
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

For mail, `--move Archive` (or `Archives`) goes to the server's
`\Archive` folder or a listed Archive/Archives folder; on Gmail, which
has neither, it moves the message to `[Gmail]/All Mail` (`\All`), which
archives it by dropping the INBOX label, and a message already in that
mailbox is left as is. A server with no archive folder and no `\All`
fails the move instead of creating one.

For mail accounts, the daemon also keeps the store in sync with changes
made outside bunker-go: while idling, an externally toggled `\Seen` flag
updates Unread, and a message expunged from a synced folder (moved or
deleted in Roundcube/Gmail, noticed live in INBOX and on the next poll
elsewhere, or at startup for anything that happened while the daemon
was down) is looked up by Message-ID in every synced folder. Found
there, the item moves with it: it is rekeyed to the new folder's id and
keeps its stored body and read state. Only a message gone from every
synced folder (deleted, or moved to Trash/Junk or an excluded folder) is
dropped from the store, so a stale row does not linger in `list` or
`counts`.

### Mail folders and item ids

The daemon syncs INBOX (watched live with IDLE), Sent, and by default
every other folder except Trash, Junk/Spam and Drafts; on Gmail, All
Mail instead of the label folders, storing a message that is also in
INBOX or Sent only once, as that copy. Folders other than INBOX are
polled every 2 minutes. `sync_folders` and `exclude_folders` in the
account config change the set (see `docs/config.example.toml`).

The item id names the mailbox, so `read`, `organize` and `download`
act on the right one: `mail:<account>:<uidvalidity>.<uid>` for INBOX,
`mail:<account>:sent.<uidvalidity>.<uid>` for Sent, and
`mail:<account>:<mailbox>/<uidvalidity>.<uid>` for any other folder,
with the mailbox name URL-path-escaped (e.g.
`mail:cl:INBOX.Archive/1700000000.12`,
`mail:example:%5BGmail%5D%2FAll%20Mail/5.33`). `Meta.folder` is
`INBOX`, `Sent`, or the server's mailbox name, which is how the item
shows which folder it is in.

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

## `bunker call <channel> <account> <to> [--dry-run] [--json]`

Places a voice call. Only WhatsApp implements it (`core.Caller`), and only
on an account with `calls = true` (see `docs/config.example.toml`); any
other account fails with `core: capability not supported`, dry-run
included. `<to>` is a `+E164` phone number or a JID. The command returns as
soon as the offer is on the wire: audio runs inside the daemon, on the
machine it runs on, and starts only once the peer answers and media flows,
so nothing is recorded while the call is still ringing. The daemon holds
one call at a time.

```
$ bunker call whatsapp personal +51999999999 --dry-run
[dry-run] would call via whatsapp/personal: +51999999999
$ bunker call whatsapp personal +51999999999
call ok: 3EB0C4... whatsapp/personal outgoing +51999999999@s.whatsapp.net calling
```

`--json` returns `{"dryRun", "plan", "call"}`, where `call` is:

```json
{"ID": "3EB0C4...", "Channel": "whatsapp", "Account": "personal",
 "Peer": "51999999999@s.whatsapp.net", "PeerName": "Ana",
 "Direction": "outgoing", "State": "active",
 "StartedAt": "2026-09-28T10:00:00Z", "ConnectedAt": "2026-09-28T10:00:07Z",
 "EndedAt": "0001-01-01T00:00:00Z", "EndReason": ""}
```

`audio_error` (omitted while empty) says why the call has no sound, e.g.
`"pacat terminó (exit status 1): Connection refused"` or
`"no llega audio del otro lado (sin medios)"`; see
[Troubleshooting call audio](#troubleshooting-call-audio).

`Direction` is `incoming` or `outgoing`; `State` moves `ringing`/`calling`
→ `connecting` → `active` → `ended`. `StartedAt` is when the call started
ringing, `ConnectedAt` when media started flowing (zero until then) and
`EndedAt` when it ended (zero while live). The call's duration counts from
`ConnectedAt`, so ringing time is not included.

## `bunker call answer|reject|hangup <call-id|latest> [--dry-run] [--json]`

Answers or rejects a ringing incoming call, or hangs up any live call.
`<call-id>` comes from `bunker calls`; whichever account owns it acts, so
no channel/account is needed. `latest` stands for the newest ringing
incoming call (`answer`/`reject`) or the newest live call (`hangup`),
and is an error when there is none; it is what tmux key bindings use. An unknown id is `not_found`. `--dry-run`
returns the plan and the call's current state without changing it.

Incoming calls are never answered on their own. Each one is also written
to its conversation as an item, so it shows in `list`, `counts` and the
tmux segment: `📞 Llamada entrante` (unread) while ringing, then
`📞 Llamada perdida` (still unread) if nobody answered, or
`📞 Llamada finalizada (m:ss)` once an answered call ends. An outgoing call
nobody answered becomes `📞 Llamada sin respuesta`. Its `Meta`
carries `wa_call_id`, `wa_call` (direction) and `wa_state`.

## `bunker calls [--json]`

Lists every live call, oldest first (`no active calls` when there are
none). A connected call also shows how long it has been connected:

```
3EB0C4... whatsapp/wa outgoing Ana (51999999999@s.whatsapp.net) active 2:35
```
 `--json` returns `{"calls": [...]}` with the shape above.

## `bunker call audio-test [--account A] [--seconds N] [--json]`

Checks this machine's call audio without WhatsApp: it plays a 1 s 440 Hz
tone through the account's `call_playback_command` while recording `N`
seconds (default 3, 1 to 30) from its `call_capture_command`, then reports
the commands used, whether each was found and started, any exit error and
stderr tail, and the microphone level (peak and RMS, as a fraction of full
scale and in dBFS). `--account` is optional when there is a single WhatsApp
account. The call gains are applied, so the level is what a call would send.

It is local only: it runs in the CLI process (it reads `config.toml`, it
does not need the daemon) and never touches the network or WhatsApp, so
it has no `--dry-run`. Exit status is 1 when a helper is missing or died,
or the mic heard digital silence.

```
$ bunker call audio-test --seconds 2
capture : started, recorded 2.0 s of 2 s
  command: parec --raw --format=s16le --rate=16000 --channels=1 --latency-msec=60
playback: started, played a 1 s 440 Hz tone
  command: pacat --playback --raw --format=s16le --rate=16000 --channels=1 --latency-msec=60
mic level: peak 0.1204 (-18.4 dBFS), rms 0.0310 (-30.2 dBFS)
hint: playback accepted a 1 s 440 Hz tone; if you did not hear it, ...
result: ok
```

`--json` returns `{"account", "capture", "playback", "seconds_wanted",
"seconds_recorded", "mic_peak", "mic_rms", "mic_peak_dbfs", "mic_rms_dbfs",
"hints", "ok"}`, where `capture`/`playback` are `{"command", "disabled",
"found", "started", "error", "stderr"}`. A peak of 0 (-120 dBFS) means the
microphone is muted or the wrong source is selected.

### Troubleshooting call audio

When a call connects but nobody hears anything ("las llamadas no se
escuchan"), go in this order:

1. `bunker call audio-test`: proves the speaker and microphone commands
   work outside any call. A missing `parec`/`pacat` is also refused up
   front: `bunker call` and `bunker call answer` fail with
   `whatsapp: call audio: parec not found in PATH; install pulseaudio-utils
   ...` instead of starting a silent call (an incoming call stays ringing,
   so it can still be rejected).
2. `bunker calls` (or the TUI banner) during the call: a non-empty
   `audio_error` / `[sin audio: ...]` names the problem. A helper that dies
   mid-call is reported with its exit status and stderr; an `audio_error`
   of `no llega audio del otro lado (sin medios)` means the call was
   answered but no media packet arrived from the peer within 10 s (a
   network, firewall/UDP or relay problem, not local audio). It clears by
   itself if media shows up later.
3. The daemon log (its stderr: the terminal running `bunker daemon`, or
   your service manager's journal), lines tagged `component=call`. Look for
   `meowcaller: first RTP decoded from relay, inbound audio flowing`
   (the peer's media reached you), `meowcaller: first RTP sent to relay,
   outbound media flowing` (yours left), `call audio helper started`,
   `call audio helper failed` (with the exit status and stderr) and
   `meowcaller: failed to write ... audio`. meowcaller's own warnings and
   errors always reach the log; its debug output does not.

| What you see | Meaning |
| --- | --- |
| `audio-test` fails or `parec not found` | local audio tools; fix `call_capture_command` / `call_playback_command` |
| `audio-test` ok, `first RTP decoded` missing, `sin medios` | the peer's audio never arrived: network/relay |
| `first RTP decoded` present, `audio_error` empty, still silent | output device/volume (`pactl list short sinks`) |
| `audio-test` mic peak 0 | muted or wrong input source |

### Voice calls in the TUI

The side panel drives the same calls (`calls`, `call`, `call answer|reject|hangup`
over the socket), still opt-in per account with `calls = true`. It polls
`calls` every 3 s (every second while one is live, so the duration counts),
and only when the daemon connection supports them. Video calls and screen
sharing are not part of it. Audio still runs on the daemon's machine.

- **Place:** `Alt+C` in a WhatsApp chat (a chat's composer takes every plain
  key, hence Alt, like `Alt+E`/`Alt+X`). It asks the daemon for a dry-run plan,
  shows `¿Llamar a «Nombre»? ↵ llamar · Esc cancelar`, and places the call only
  after `↵`. A group, a non-WhatsApp chat, a call already live or an account
  without `calls = true` is an error on the chat's last line
  (`las llamadas no están activadas en esta cuenta · añade calls = true …`),
  never a silent no-op.
- **From the list:** `c` on a selected WhatsApp 1:1 row of the inbox or the
  side panel opens that conversation and starts the same `Alt+C` action
  (dry-run plan, `↵` confirms, `Esc` cancels); nothing else is placed by
  it. A Mail or Matrix row, a group, a connection without calls or a call
  already live flashes the reason on the list (`no se puede llamar: …`)
  and opens nothing. Inside herdr the side panel hands conversations to
  another pane, so `c` there only flashes to press `Alt+C` in the
  conversation pane. `Alt+C` also works on a contact in the `n`/`@`
  picker. The palette lists `Llamar` (`c`) and `Buscar contacto` (`@`).
- **Incoming:** a banner replaces the last line of every view:
  `📞 Llamada entrante de Ana · a contestar · x rechazar`. `a` answers and `x`
  rejects. A desktop notification (`Llamada entrante de Ana`) fires once per
  call, through the same path as new messages (herdr's notifier when present,
  else OSC 777), without the focus and rate limits messages have.
- **Active:** the banner becomes `📞 En llamada con Ana · 1:35 · h colgar`
  (`Llamando a…` / `Conectando con…` before media flows). `h` hangs up.
- **Keys where you type:** in a chat, the reply/mail editors, the filter and
  the palette plain keys are text, so the banner shows `Alt+A`, `Alt+X` and
  `Alt+H` there. The keys act only while they apply: `a`/`x` (and `Alt+A`/`Alt+X`)
  during a ringing call take precedence over "preguntar a Claude" and, in a
  chat, "eliminar último mensaje"; `h` does nothing without a live call.
- **Palette:** `Llamar` (in a chat) and, while they apply, `Contestar llamada`,
  `Rechazar llamada` and `Colgar llamada`.

## `bunker unread <id> [--json]`

Puts an item back in the unread inbox.
- **Mail:** the server's `\Seen` flag is cleared too.
- **WhatsApp and Matrix:** they cannot mark a message unread, so only
  bunker's store changes. The command says so (`unread in bunker only`,
  `"local_only": true`). The read receipt the other side already got is
  not taken back.

The TUI's `u` key uses this to undo the last mark-read.

## `bunker edit <id> <text|-> [--idempotency-key k] [--dry-run] [--json]`

Replaces the text of one of your own messages; everyone in the chat sees
the edit. `-` reads the text from stdin.
- **WhatsApp:** only within 20 minutes of sending (whatsmeow's
  `EditWindow`); a later edit fails before anything is sent
  (`the channel no longer allows this change`), on `--dry-run` too. The
  edit goes out with the same typing emulation and pacing as a send.
- **Matrix:** an `m.replace` edit, encrypted in encrypted rooms. No time
  limit.
- **Mail:** unsupported.

Messages with attachments cannot be edited (the caption edit is not
implemented), and someone else's message is refused. On success the
stored item carries the new body and `Edited: true`.

```
[dry-run] would edit whatsapp:personal:<chat>/<msg> via whatsapp/personal to: new text
edit ok: whatsapp:personal:<chat>/<msg> (receipt whatsapp:personal:<chat>/<edit>)
```

`--json` returns `{"dryRun", "plan", "receipt"}` like `send`, with
`plan.action` `"edit"`, `plan.target` the item id and `plan.preview` the
new text.

## `bunker delete <id> [--yes] [--idempotency-key k] [--dry-run] [--json]`

Deletes one of your own messages for everyone. It plans first (the
plan's `preview` is the text about to disappear), then, on a terminal,
asks `Delete for everyone ...? [y/N]`. Without a terminal (a script, a
pipe) it refuses unless `--yes` is given, since nothing can be asked.
- **WhatsApp:** a revoke, only within two days of sending.
- **Matrix:** a redaction of the event. No time limit.
- **Mail:** unsupported.

The stored item is kept with an empty body and `Deleted: true`, as when
the other side deletes a message. `--json` has `plan.action` `"delete"`.

## `bunker react <id> <emoji|--remove> [--idempotency-key k] [--dry-run] [--json]`

Sets your reaction to any message (yours or anyone's) to one emoji,
replacing your previous reaction on it; `--remove` takes it away. Text
that is not a single emoji is refused.
- **WhatsApp:** a reaction message; removing sends an empty one, which
  is how WhatsApp withdraws it. Paced like a send, with a short pause
  instead of typing.
- **Matrix:** an `m.annotation`, encrypted in encrypted rooms. Matrix
  allows several reactions per person; bunker keeps one, so a new
  reaction first redacts your previous ones on that message, and
  `--remove` redacts them. Removing when bunker knows of no reaction of
  yours fails.
- **Mail:** unsupported.

`--json` has `plan.action` `"react"` and `plan.preview` the emoji (empty
for a removal). The stored item's `Reactions` gets (or loses) your entry.

## `bunker contacts [query] [--channel c] [--account a] [--limit n] [--json]`

Lists who you can write to or call, merged across accounts:

- **WhatsApp:** the account's contact store (the saved name, else the push
  name). Only people with a phone-number JID are listed.
- **Every channel:** the conversations already in the store: WhatsApp
  chats and groups, Matrix rooms, and mail senders (never the account's
  own address).

Entries are deduplicated by address. `query` matches the name or the
address, ignoring case and accents (`jose` finds `José`), ranking exact
names first, then prefixes. The default limit is 50.

```
whatsapp/wa	Ana Díaz	51999999999@s.whatsapp.net
mail/cl	Ana Soto	ana@example.cl
```

`--json` returns `{"contacts": [{"channel", "account", "name", "address",
"thread"}]}`. `address` is what `send` and `call` take. `thread` is the
existing conversation, empty when there is none yet.

### Names in `send` and `call`

`bunker send` and `bunker call` also take a contact's name where they take
an address:

```sh
bunker call whatsapp wa "Ana Díaz" --dry-run
bunker send whatsapp wa jose "hola"
```

- **What counts as a name:** a recipient with a letter and none of the
  marks addresses carry (`@`, or a leading `!` or `#`). A phone number is
  never treated as a name.
- **Where it resolves:** only among that channel/account's contacts. An
  exact name wins, otherwise a single partial match.
- **Failures:** no match, or several, is an error listing the candidates
  (`Ana Díaz <…>; Ana Soto <…>`), so nothing is sent to a guessed
  recipient.
- **Output:** the resolution is printed to stderr (`jose → José Pérez
  <…>`), except with `--json`.
- **Dry run:** resolving only reads the daemon's contacts, so it works
  under `--dry-run`.

`bunker call` also accepts a contact's phone-number JID directly. A group
cannot be called.

## `bunker chats [--channel c] [--account a] [--limit n] [--json]`

Lists conversations the way a messaging app does: one per
`(channel, account, thread)` (an item without a thread is its own
conversation), ordered by their newest item, newest first, **including
fully read ones**, each with its unread count. `--channel` and `--account`
narrow it (`bunker chats --channel whatsapp`); `--limit` defaults to 50 and
is capped at 500. It only reads the daemon's store: nothing reaches a
channel. This is what the TUI's WhatsApp and Matrix tabs show.

```
whatsapp/personal	Ana Díaz	0 unread	whatsapp:personal:3
matrix/work	Equipo	3 unread	matrix:work:9
```

Columns: `channel/account`, name (the thread name, else the sender), unread
count, and the id of the conversation's newest item (what `read`, `reply`
and `open` take).

`--json` returns `{"conversations": [{"last": <item>, "unread": N}]}`,
where `last` is the full [item](#bunker-list-flags) (without labels or
reactions) of the conversation's newest message and `unread` is how many of
the conversation's items are unread (0 for a read one).

## `bunker meetings [--days N] [--json]`

Lists the upcoming meetings, soonest first: those that start within the
next `--days` days (default 7) plus any in progress. It only reads the
daemon's store; nothing reaches a channel. The TUI and `bunker sidebar`
show the same list in a "Reuniones" section under the conversations.

```
2026-10-05 10:30	Revisión semanal	Meet	mail:cl:1234
ahora (hasta 11:00)	Demo de producto	Zoom	mail:cl:1301
enlace	Demo equipo	Jitsi	whatsapp:personal:3EB0A1
```

Columns: when (local time; `ahora (hasta HH:MM)` while in progress;
`enlace` for a bare link, which has no time), the title, the provider
(`Meet`, `Zoom`, `Teams`, `Webex`, `Jitsi`, or `sin enlace` when the
invitation carries no join link) and the id of the item that carried it
(what `meetings join` takes).

`--json` returns `{"meetings": [...]}`. Each meeting is the stored meeting
plus where it came from:

```json
{"uid": "abc-1@example.com", "method": "REQUEST", "summary": "Revisión semanal",
 "start": "2026-10-05T14:30:00Z", "end": "2026-10-05T15:30:00Z", "tz": "America/Lima",
 "organizer": "Ana <ana@example.com>", "location": "...", "url": "https://meet.google.com/abc-defg-hij",
 "sequence": 1, "item_id": "mail:cl:1234", "channel": "mail", "account": "cl"}
```

`all_day`, `link` (a bare link, no `start`/`end`) and `recurring` (the
`start`/`end` are the next occurrence of a repeating event) appear when
true.

### Where meetings come from

- **Calendar invitations (mail).** A `text/calendar` part or an `.ics`
  attachment with `METHOD:REQUEST` or `PUBLISH` (a calendar without a
  method counts as `PUBLISH`), whether or not you accepted it. Replies
  (`METHOD:REPLY`) and the other attendee-to-attendee methods are ignored.
  Read from the bounded body text mail sync already fetches
  (`index_body_max_kb`, 64 KiB by default; with `index_body_max_kb = 0`
  a meeting is only found when the message is read), and again when a
  message is read in full.
- **Updates and cancellations.** Meetings are deduplicated by the event's
  `UID`: the version with the highest `SEQUENCE` wins (a later message when
  equal). `METHOD:CANCEL` or `STATUS:CANCELLED` leaves the meeting out.
- **The join link** is, in this order: `X-GOOGLE-CONFERENCE`,
  `X-MICROSOFT-SKYPETEAMSMEETINGURL`, `X-MICROSOFT-ONLINEMEETINGCONFLINK`;
  then a Google Meet, Zoom, Teams, Webex or Jitsi link found in `LOCATION`,
  `URL`, `DESCRIPTION` or any other `X-` property (Outlook Safe Links and
  Google redirect wrappers are unwrapped); then a `LOCATION` that is itself
  a web address. Links are matched by host and path, so a Teams
  "meeting options" link is not a join link.
- **Times** accept UTC (`Z`), `TZID` (IANA names, Outlook's Windows names,
  or the invitation's own `VTIMEZONE` as a fixed offset), floating times
  (read in the daemon's zone), all-day dates, and `DTEND` or `DURATION`.
- **Recurring events** (`RRULE`) are expanded for `DAILY`, `WEEKLY` (with
  `BYDAY`), `MONTHLY` (same day of the month) and `YEARLY` rules, with
  `INTERVAL`, `COUNT`, `UNTIL` and `EXDATE`. Other rules (`BYSETPOS`,
  "second Tuesday", ...) show only their first occurrence. A single edited
  instance (`RECURRENCE-ID`) is its own meeting and does not remove the
  series' occurrence on that day.
- **Bare links.** A Meet, Zoom, Teams, Webex or Jitsi link in the subject
  or body of any message (mail, WhatsApp or Matrix) sent in the last 24
  hours is listed as a "link" meeting without a time, after the timed
  ones; older messages are never scanned. A link repeated in several
  messages counts once. The 24 hours are measured from the message's date
  and applied both when it is stored and when the list is built.

The meeting is stored with the item (`Meta["meeting"]`, and
`Meta["meeting_end"]` as an indexed `items.meeting_end` column added by
schema migration 5). Items stored before that migration gain a meeting only
when their mail is synced or read again.

## `bunker meetings join <item-id|next> [--dry-run] [--json]`

Opens a meeting's join link with the desktop opener **on this machine**
(not the daemon's): `xdg-open` on Linux, `open` on macOS, or the command in
`$BUNKER_OPEN_URL`, which receives the URL as its last argument (for
example `BUNKER_OPEN_URL="firefox --new-window"`). `next` joins the first
upcoming meeting that has a link. `--dry-run` prints the URL and the
command and opens nothing:

```
$ bunker meetings join mail:cl:1234 --dry-run
[dry-run] would open https://meet.google.com/abc-defg-hij with: xdg-open https://meet.google.com/abc-defg-hij
```

`--json` returns `{"dryRun", "id", "summary", "url", "command"}`. Only
`http` and `https` links are ever opened. An item that carries no meeting,
a meeting without a link, or a missing opener is an error (exit 1), never
a silent no-op.

In the TUI the same link opens with a click on a row of the "Reuniones"
section, `J` (the next meeting with a link) or the palette's "Unirse a la
próxima reunión"; a meeting without a link says so on the status line.

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

- `update_available`, `latest_version` — the daemon's cached release
  check (see below); `latest_version` is only present when
  `update_available` is true.

```json
{"adapters": [
  {"channel":"mail","account":"cl","state":"connected","since":"2026-01-02T03:04:05Z","restarts":0},
  {"channel":"whatsapp","account":"personal","state":"backoff","since":"2026-01-02T03:05:00Z","lastError":"dial refused","restarts":3}
], "update_available": true, "latest_version": "0.13.0"}
```

Non-JSON output, one line per adapter, then one line when there is an
update:

```
mail/cl: connected (restarts=0)
whatsapp/personal: backoff (restarts=3) last_error="dial refused"
bunker: new version v0.13.0 available, run 'bunker update'
```

**New releases.** A release build of the daemon asks the GitHub API for
the latest release (`GET /repos/reyer3/bunker-go/releases/latest`, the
only request it makes for this, with nothing about the user) two
minutes after it starts and then every 24 hours, and keeps the answer
in `$BUNKER_STATE_DIR/update.json`, so a restarted daemon knows before
its next check. A failed check is logged at debug level and keeps the
previous answer; it never affects the daemon. A pre-release is never
offered over a stable release. A `dev` or pseudo-version build (see
`bunker version`) and `bunker daemon --fake` never check, and
`[update] check = false` in config.toml turns the check off. The health
RPC method carries `update_available` and `latest_version` next to
`adapters`, so older clients simply ignore them. The TUI (inbox and
`bunker sidebar`) shows `nueva versión vX disponible · bunker update` on
its status line for a minute, once per session; inside herdr with
`[herdr] notify = true` it is also a herdr notification. The MCP
`health` tool reports the same two fields.

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
headers/flags and the bounded body text (the exact same item-building
path `Run`'s own sync uses)
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
into the store (headers and bounded body text, like sync; read-only — it
never marks anything read),
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

## Inline images in the TUI's chat view (kitty graphics)

When the TUI runs directly in a terminal that speaks the kitty graphics
protocol (Ghostty, kitty), an image attachment in a WhatsApp or Matrix
chat shows as a thumbnail inside its bubble, instead of the
`📎 name (bytes)` row.

- **Opening full size:** `Ctrl+O` opens the newest image, and a click on
  a thumbnail opens that one. `←`/`→` browse the conversation's images;
  `Esc` or a click closes the viewer and returns to the chat.
- **Embedded thumbnails:** a WhatsApp image, video, sticker or document
  usually carries its own small preview (the attachment's `Thumbnail`).
  The chat draws that first, with no download and no `ffmpeg`, and a
  document with one shows it above its `📎 name (bytes)` line. `Ctrl+O`
  on an image or video still downloads the full media for the viewer.
- **Fetching:** without an embedded thumbnail, or when it does not
  decode, thumbnails go through the same `Client.Download` as
  `Ctrl+D`. Each attachment is downloaded once, up to 25 MB, into
  `$XDG_CACHE_HOME/bunker-go/media`, which is created private, and reused
  after that. JPEG, PNG, GIF and still WebP images are supported. One
  that cannot be decoded keeps its text row.
- **Drawing:** images use kitty graphics unicode placeholders. Each image
  is uploaded once and then drawn in ordinary text cells, so it scrolls
  and redraws with the rest of the chat.
- **Detection:** `TERM_PROGRAM=ghostty`, `TERM=xterm-ghostty`,
  `TERM=xterm-kitty` or `KITTY_WINDOW_ID` enable images. They stay off
  inside tmux (`$TMUX`), which does not forward the protocol by default.
  `BUNKER_GRAPHICS=kitty` or `BUNKER_GRAPHICS=none` overrides detection.
  With images off, the TUI renders exactly as before.
## Videos in the TUI's chat view

- **Thumbnail:** with kitty graphics on, a video attachment shows a frame
  in its bubble with a `▶ name · clic para reproducir` line. The frame is
  the message's embedded thumbnail when it has one; otherwise it is
  grabbed with `ffmpeg` as an external process. The video is downloaded
  once, up to 64 MB, into the same media cache as images. When `ffmpeg`
  is missing or fails, the video keeps its text row.
- **Playing:** a click on the thumbnail plays the video, and so does
  `Enter` on a video in the full-size viewer. The TUI suspends while the
  player runs and comes back when it exits.
  - The player is `mpv`. With kitty graphics on it draws inside the
    terminal (`--vo=kitty`); otherwise it opens its own window.
  - `BUNKER_VIDEO_PLAYER="vlc --play-and-exit"` swaps in another player;
    the file path is appended as the last argument.
  - A missing player is reported in the chat.
- **Without graphics:** inside tmux or a plain terminal, `Ctrl+O` plays
  the conversation's newest video in `mpv`'s own window.

## Voice notes (notas de voz)

Voice notes work on WhatsApp and Matrix, end to end: they show up as
voice notes when received, play from the TUI, can be recorded from the
TUI and can be sent from the CLI. Everything that touches audio is an
external process; bunker links no audio library.

- **Receiving:** a WhatsApp push-to-talk message and a Matrix `m.audio`
  event carrying `org.matrix.msc3245.voice` become an attachment with
  `"voice": true`, its length in `"duration"` (whole seconds, omitted when
  unknown) and, when the sender's app included one, `"waveform"` (base64 of
  one 0-100 byte per bar). Older stored messages simply lack those keys. A
  plain audio file, or any message from before this feature, stays an
  ordinary attachment.
- **Showing:** the chat bubble reads `🎤 Nota de voz · 0:12`, with a
  crude `▁▃▇█` waveform under it when there is one.
- **Playing in the TUI:** `Alt+P` plays the chat's newest voice note, and
  a click on a voice bubble plays that one. The note is downloaded once
  into the media cache and played by `mpv --no-video --really-quiet
  <file>` in the background (the chat stays usable), with
  `▶ reproduciendo… · Esc detener` below the composer. `Esc` stops it
  (and only then leaves the chat), as does `Alt+P` again.
  `BUNKER_AUDIO_PLAYER="ffplay -nodisp -autoexit"` swaps in another
  player; the file path is appended as the last argument. A missing
  player is an error in the chat that names what to install.
- **Recording in the TUI:** in a WhatsApp or Matrix chat with an empty
  composer and no attachments, `Alt+V` starts recording and the composer
  becomes `● Grabando 0:07 · ↵ enviar · Esc cancelar`. `Enter` stops the
  recorder gracefully (SIGINT, so the Ogg file is finalized), then shows
  the usual preview, `¿Enviar nota de voz (0:07) a …? ↵ enviar · Esc
  cancelar`, and `Enter` sends it. `Esc` while recording, or at the
  preview, discards the note and deletes its temp file (private, 0600,
  also removed once sent or when you leave the chat). Recordings stop by
  themselves at 5 minutes and go to the preview. A recorder that is not
  installed, or that exits early, is reported in the chat with its stderr
  tail; nothing is silently skipped. `Ctrl+K`/`F2` lists *Grabar nota de
  voz* and *Reproducir nota de voz*, with why one is unavailable.
- **The recorder:** by default
  `ffmpeg -hide_banner -loglevel error -f pulse -i default -ac 1 -ar 48000
  -c:a libopus -b:a 24k -application voip -t 300 -y {output}` (PulseAudio,
  also served by PipeWire). Set `voice_record_command` on the account to
  use another tool (see `docs/config.example.toml`): a list is the exact
  command, a string runs under `sh -c` (for pipelines such as
  `pw-record` into `opusenc`), and `{output}` stands for the file, which
  must end up as Ogg Opus. The recorder runs in its own process group and
  is stopped with SIGINT sent to the whole group; in a pipeline make the
  encoder ignore it (`(trap '' INT; exec opusenc ...)`, as the example
  config does) so it finishes when its input closes.

Sending (CLI):

```
bunker send whatsapp personal "Ana" --voice nota.ogg [--dry-run] [--json]
bunker reply whatsapp:personal:3EB0… --voice nota.ogg [--dry-run] [--json]
```

`--voice` takes one **Ogg Opus** file (`.ogg`/`.opus`) and needs no text:
a voice note cannot carry a caption, goes to a single recipient, and
cannot be combined with `--attach`/`--media`. Any other format is
refused with a hint, for example
`ffmpeg -i in.wav -c:a libopus -b:a 24k out.ogg`. Other channels (mail)
answer `ErrUnsupported` rather than sending the file as a plain
attachment. `--dry-run` never touches the network; the plan has
`"voice": true` and its attachment `"voice": true, "duration_ms": 12000`
(human output: `voice note nota.ogg (audio/ogg; codecs=opus, 9120 bytes,
0:12)`). Length comes from the file itself (the last Ogg page's granule
position, minus the pre-skip, at 48 kHz).

- **WhatsApp:** an `AudioMessage` with `PTT` set, mimetype
  `audio/ogg; codecs=opus` and `seconds`; it shows "recording audio" while
  it waits and follows the usual send pacing. No waveform is sent: it
  would need an Opus decoder and a made-up one would be a lie, so clients
  draw a flat placeholder.
- **Matrix:** an `m.audio` with `org.matrix.msc3245.voice` and
  `org.matrix.msc1767.audio` `{duration}` in milliseconds, uploaded
  encrypted in an encrypted room. No waveform either.
- **MCP:** the `send` and `reply` tools take text only, so voice notes are
  CLI/TUI only.

Required external tools: `mpv` (or `BUNKER_AUDIO_PLAYER`) to play, and
`ffmpeg` (or `voice_record_command`) to record. Sending an existing
Ogg Opus file needs neither.

## Editing, deleting and reacting in the TUI's chat view

The chat view has no message selection (the composer always has focus
and plain keys are draft text), so these keys act on the latest message:

| Key | Action |
|-----|--------|
| `Alt+E` | put your last text message in the composer; `Ctrl+S` (or ↵) previews the edit, ↵ confirms, `Esc` cancels and gives your draft back |
| `Alt+X` | delete your last message for everyone, after a confirm |
| `Alt++` (or `Alt+=`) | react to the last message received: `1`-`6` pick 👍 ❤️ 😂 😮 😢 🙏, `0` removes your reaction, then ↵ confirms |

Each builds the same dry-run plan as `bunker edit`/`delete`/`react`
first, and the same channel rules apply (WhatsApp's 20-minute edit
window, no media edits). The conversation reloads afterwards.

## Emoji completion in the TUI's chat composer

Typing `:` followed by at least two letters at the end of a chat draft
lists up to six matching emoji just above the composer, for example
`:risa` → 😂 and `:thumbs` → 👍. Shortcodes and English and Spanish
keywords all match, with or without accents (`:corazon`, `:corazón`).

- **Keys:** `Tab`/`Shift+Tab` move through the list and `Enter` inserts
  the highlighted emoji. That `Enter` never sends the message. `Esc`
  hides the list until the draft changes, and a second `Esc` leaves the
  chat as usual.
- **When it opens:** only at the draft's end, and only when the `:`
  starts the draft or follows a space. Times (`12:30`) and URLs
  (`http://`) never open it.

## Attachments in the TUI's chat composer

A chat message (WhatsApp/Matrix) can carry files, sent through the same
previewed and confirmed `Reply` as the text:

- **Drag and drop:** dropping files on the terminal pastes their paths.
  When a paste consists only of absolute paths to existing files, the
  files are attached instead of typed. Plain paths, `file://` URIs, quoted
  paths and backslash-escaped spaces all work, one or several at a time.
  Any other paste goes into the draft as text.
- **Clipboard image (`Ctrl+V`):** attaches the clipboard's image, such as
  a screenshot. It reads the clipboard with `wl-paste` on Wayland or
  `xclip` on X11. A missing tool, a missing graphical session or a
  clipboard with no image shows a clear error in the chat; nothing is
  silently skipped. The image is saved to a private (0600) temp file,
  which is deleted once sent, removed, or when you leave the chat.
- **Chips:** attachments show as chips below the composer.
  `Backspace` on an empty draft removes the last one.
- **Empty text:** a message may carry only attachments.

## `bunker mcp [--allow-send]`

Serves bunker to AI agents (Claude Code, Zed's agent panel, any MCP host)
as an [MCP](https://modelcontextprotocol.io) server on stdio. Every tool
call is one daemon RPC on a fresh connection, so the server survives
daemon restarts. A dead daemon is a tool error, except for `health`,
which answers `daemon_up: false` with a hint to run `bunker daemon`.

| Tool | What it does |
|---|---|
| `counts` | unread counts per channel and account |
| `list` | newest items with a 200-character snippet (`channel`, `account`, `unread`, `limit` ≤ 100, default 20, `cursor`); returns `items` and `next_cursor` |
| `search` | the [query language](#query-language---query-bunker-find-the-mcp-search-tool) over the local store (`query`, `limit` ≤ 100, `cursor`); returns `items` (same shape as `list`) and `next_cursor`; reads only the store, never marks anything read |
| `search_remote` | `bunker search mail`: an IMAP search on the mail server (`account`, `from`, `subject`, `since`, `before`, `folder` = `INBOX`, `limit` ≤ 100); returns `items` |
| `backfill` | `bunker backfill mail`: fetch older mail into the store (`account`, `since`, `folder` = `INBOX`, `dry_run`); returns `dry_run`, `count`, `first_id`, `last_id` |
| `read` | one item with its body (capped at 20,000 characters); **never marks it read** |
| `attachment` | the text of one attachment (`id`, `index` from 0), downloaded like `bunker download` into a temp dir that is removed afterwards; see below; **never marks anything read** |
| `thread` | a conversation's newest messages, oldest first |
| `contacts` | the same matches as `bunker contacts` |
| `calls` | live voice calls |
| `health` | whether the daemon is up and, per account, `channel`, `account`, `state`, `since`, `last_error`, `restarts` and `last_item` (the newest stored item's time); plus `update_available` and `latest_version` from the daemon's release check |
| `send` | a new message; `to` takes an address or a contact name, resolved like the CLI |
| `reply` | a reply to an item |
| `edit` | edit one of the user's own messages (`id`, `text`), like `bunker edit` |
| `delete` | delete one of the user's own messages for everyone (`id`), like `bunker delete` |
| `react` | react to any message (`id`, `emoji`; an empty `emoji` removes ours), like `bunker react` |
| `mark_read` | mark an item read (`id`); on WhatsApp and Matrix this sends the sender a read receipt |
| `mark_unread` | put an item back in the unread inbox (`id`); on WhatsApp and Matrix only bunker changes |
| `archive` | move a mail to the archive (`id`), like `bunker organize --move Archive`; mail only |
| `move` | move a mail to another folder (`id`, `folder`); mail only |
| `label` | add or remove labels on a mail (`id`, `add`, `remove`); mail only |

Reads are annotated read-only; `send`, `reply`, `edit`, `delete`,
`react` and the organize tools are annotated destructive. `edit`,
`delete` and `react` are gated exactly like `send`: a plan unless the
call confirms and the server runs with `--allow-send`, and a confirmed
retry is never applied twice.

`attachment` lets an agent read an invoice, a contract or a spreadsheet.
It returns `id`, `index`, `name`, `mime`, `size`, `format`, `has_text`,
`text`, `truncated` and `note`:
- `text/plain`, CSV, Markdown, JSON and XML come back as they are, decoded
  to UTF-8 (the declared charset, else a byte order mark, else UTF-8,
  else Windows-1252). HTML goes through the same HTML-to-text conversion
  as mail bodies.
- PDF needs poppler's `pdftotext` on `PATH`, run as an external process
  with a 30-second timeout. Without it the tool fails with a hint to
  install `poppler-utils`. A PDF with no text layer (a scan) answers
  `has_text: false` with a note.
- `.docx`, `.xlsx` and `.odt` are read from their XML (`word/document.xml`;
  every sheet with its shared strings, one line per row, cells separated
  by tabs; `content.xml`).
- Images, audio and video are not downloaded: the answer is their name,
  MIME type and size, `format: "media"`, `has_text: false` and a note
  that there is no text.
- Any other type (e.g. a legacy `.doc`) is an error, and so is an index
  out of range.
- The text is capped at 100 KB; a longer one is cut and says
  `truncated: true`.

`search_remote` and `backfill` sit in between, and are annotated
neither read-only nor destructive (idempotent, open-world):
- They contact the mail server, so they need the account online and
  take as long as IMAP does.
- On the server they only read: nothing is sent, moved, deleted or
  marked `\Seen` (`BODY.PEEK`), and the regular sync position does not
  move.
- They write to the local store. `search_remote` upserts every match
  (headers, flags and bounded body text, like sync), so an item already
  stored is refreshed from the server. `backfill` adds only mail the store
  does not have; `dry_run` counts without storing.
- Afterwards the mail is visible to `list` and `search`.

Their `since`/`before` take the query language's dates: `YYYY-MM-DD`
or a relative `7d`, `2w`, `3m`. Both are mail only. `backfill` takes a
date, not a message count: the daemon's backfill searches the server
by date (`UID SEARCH SINCE`).

Agents are told to prefer `search` and only reach for `search_remote`
or `backfill` when the mail is not stored yet.

**Sending is opt-in twice:**
- `send` and `reply` always build the dry-run plan first. By default they
  return only that plan.
- A real send needs the server started with `--allow-send` **and** the
  call to pass `confirm: true`. A confirm on a plans-only server returns
  the plan with an error saying nothing was sent.
- The daemon's WhatsApp pacing and fan-out limits still apply. There is no
  bulk tool: one message per call.
- A confirmed send has its own 5-minute timeout (reads keep 60 seconds),
  because pacing can take a while. It carries an idempotency key derived
  from the plan (channel, account, recipients or replied-to item, subject,
  text and attachment paths), so retrying a confirm that timed out never
  sends twice: the retry waits for, or replays, the first send's receipt
  (`receipt.replayed: true`). The same text to the same person again
  within 24 hours is answered the same way; change the text to send it
  anew.

**The organize tools follow the same rules.** `mark_read`,
`mark_unread`, `archive`, `move` and `label` go through the same
`Organize` and `MarkUnread` paths as `bunker organize` and
`bunker unread`. They are outbound on some channels (a WhatsApp or
Matrix read receipt), so by default they return only a plan:

```json
{"done": false, "plan": {"action": "mark_read", "id": "...", "channel": "whatsapp", "account": "personal",
  "change": "mark read and send a read receipt", "notifies_sender": true}}
```

`notifies_sender` says whether the other side is told; `local_only`
says only bunker's store changes (marking a chat message unread). They
apply the change, and answer `done: true`, only with `--allow-send` and
`confirm: true`. Folders and labels on WhatsApp or Matrix fail at plan
time, and every channel or daemon error is a tool error.

Claude Code:

```sh
claude mcp add bunker -- bunker mcp                # plans only
claude mcp add bunker -- bunker mcp --allow-send   # can send and organize after confirm
```

Zed (`settings.json`; check Zed's docs if the key has changed):

```json
"context_servers": {
  "bunker": { "source": "custom", "command": "bunker", "args": ["mcp"] }
}
```

## `bunker app [--dry-run]`

Opens the TUI in a terminal window of its own, so images and videos draw
even when you normally live in tmux (the TUI turns them off inside tmux).
It uses the first terminal found of `ghostty` and `kitty`, both of which
speak kitty graphics:

```sh
ghostty --title=bunker --class=dev.bunker.app -e /path/to/bunker
kitty --title bunker --class dev.bunker.app /path/to/bunker
```

- **Override:** `[app] command` in the config replaces the whole command
  line (see `docs/config.example.toml`).
- **Environment:** `TMUX` and `TMUX_PANE` are dropped, so running it from
  a tmux pane still opens a window with images.
- **Errors:** no supported terminal, a configured command not on `PATH`,
  or an unreachable daemon is an error and no window opens (a TUI without
  the daemon would close at once). Besides stderr, every failure (also a
  broken config or a window that fails to start) shows a desktop
  notification via `notify-send` when it is installed, and appends a
  timestamped line to `$XDG_STATE_HOME/bunker/app.log` (default
  `~/.local/state/bunker/app.log`), so a launch from the desktop entry
  never fails silently. `--dry-run` only prints to stderr.
- **`--dry-run`:** prints the command line instead of opening the window.
- **Desktop entry:** `make install` installs
  `deploy/desktop/bunker.desktop` (and its icon) under
  `$(PREFIX)/share`, running `bunker app`. Its `StartupWMClass` is
  `dev.bunker.app`, the window's class, so window-manager rules can
  place or focus the window. Each launch opens a new window.

## `bunker sidebar`

The TUI laid out for a narrow pane of about 28 to 45 columns: the herdr
plugin's `sidebar` pane runs it (see `deploy/herdr`). It is the same
program as bare `bunker`, with the same needs: a TTY (without one it
prints `error: bunker sidebar needs a terminal` and exits 2) and a
running daemon (else the same "cannot reach bunker daemon" hint, exit 1).

- **Layout:** a short channel list (Todo, Mail, WhatsApp, Matrix) with
  each one's unread count, the active one marked `▌`; then the
  conversations of that channel, two lines each: channel glyph (a chevron
  on a Mail sender row), name and unread badge, the name truncated so the
  badge always shows, and under it a dim preview of the last message
  (`Ana: ...` in a group, `Tú: ...` when it is ours, just the text in a
  1:1 chat, the newest subject on a collapsed Mail sender; the same labels
  as the full TUI for voice notes, photos and files), truncated to the
  pane. In a pane too short for two lines per row (under 4 row lines) the
  previews are dropped and rows are one line; then a one-line key hint.
- **Keys:** `j`/`k` and the arrows move, `Tab`/`⇧Tab` or `1`/`2`/`3`/`0`
  switch channel, `/` filters, `@` searches a contact (scoped to the focused
  channel), `c` explains to call with `Alt+C` in the conversation pane,
  `J` joins the next meeting (see Reuniones above), `g` refreshes, `Ctrl+K` or `F2` opens the
  command palette, `?` or `F1` shows help, `q` quits. The other inbox
  keys work as in the full TUI.
- **Enter:** inside herdr (`HERDR_ENV=1`) it shows the conversation in
  the tab's single conversation pane, to the right of the list, and
  focuses it; the list stays where it was. bunker keeps one such pane per
  tab, labelled `bunker:chat` (herdr cannot say which plugin owns a pane,
  so, like the panel's `bunker` label, the pane is renamed). It lists the
  panes (`herdr pane list`) and:

  - no conversation pane in the tab: opens one and labels it:

    ```sh
    herdr plugin pane open --plugin bunker --entrypoint open --placement split --direction right --env BUNKER_OPEN_ID=<id> --focus
    herdr pane rename <new-pane> bunker:chat
    ```

  - it already shows `<id>`: `herdr plugin pane focus <pane>` and nothing
    else;
  - it shows another conversation (or one this sidebar process did not
    open): the new pane is opened by splitting it and the old one is
    closed, so the new conversation takes the same slot:

    ```sh
    herdr plugin pane open … --target-pane <old-pane> … --env BUNKER_OPEN_ID=<id> --focus
    herdr pane rename <new-pane> bunker:chat
    herdr pane close <old-pane>
    ```

  Which item a pane shows is remembered in the sidebar process (ids can
  hold characters a label should not), so a conversation pane closed by
  hand is simply opened again. The panel (`bunker` label) and the pane
  running the sidebar are never closed or reused. herdr is found the
  same way as for `bunker herdr toggle` (`HERDR_BIN_PATH`, else `herdr`
  on `PATH`), the call has a 10s timeout, and an id starting with `-` or
  holding a control character is never passed on. A failure shows on the
  status line (`no se pudo abrir: …`); a failed relabel closes the pane
  it just opened. Outside herdr, Enter opens the conversation in place,
  as the full TUI does, and Esc goes back to the list.
- **`a`, ask Claude:** inside herdr only (the key and its `a Claude` hint
  are absent elsewhere). It runs `herdr agent list`, keeps the agents
  whose `agent` kind is `claude`, and picks the first that is `idle` or
  `done` (ready for input), else the first one. Then, with its pane id as
  the target:

  ```sh
  herdr agent prompt <pane> "Usa bunker mcp: lee la conversación <id> (herramienta read o thread) y dime qué necesito saber; si hay que responder, propón una respuesta como plan sin enviarla."
  herdr agent focus <pane>
  ```

  The item id is checked as for Enter, and a pane id that is not shaped
  like a herdr id is never passed on; the whole ask has a 15s timeout.
  The status line shows `preguntando a Claude…`, then `enviado a Claude`.
  No Claude Code in herdr shows `no se pudo preguntar a Claude: no hay
  ningún Claude Code en herdr · …`. An agent that is `blocked` (or a
  prompt herdr refuses with `agent_blocked`) is focused without a prompt
  and shows `Claude está esperando tu respuesta en su panel`.
- **Unread count:** inside herdr, with a valid `HERDR_PANE_ID`, after each
  poll the panel reports its unread total (the "Todo" count) on its own
  pane, so herdr's Agent sidebar rows can show it as `$unread`:

  ```sh
  herdr pane report-metadata <HERDR_PANE_ID> --source bunker --token unread=<N> --ttl-ms 10000
  ```

  The TTL is two poll intervals, so the count disappears soon after the
  panel stops. It is reported when it changes, else at most once per
  half TTL. A failure shows once on the status line
  (`no se pudo avisar a herdr de los no leídos: …`), and again only after
  a report has succeeded in between.
- **Notifications:** with `[herdr] notify = true` in `config.toml` (off by
  default), inside herdr each new unread message (detected as for the
  OSC 777 notifications) is shown with

  ```sh
  herdr notification show bunker --body "<sender>: <text>" --sound request
  ```

  at most one every 30s; messages arriving in between are coalesced into
  the next one as `N mensajes nuevos`. The text is sanitized and cut to
  60 cells. It replaces the OSC 777 notification and, unlike it, does not
  depend on the pane being unfocused. A failure shows once, like the
  unread count's.

## `bunker open [<id>]`

Starts the TUI directly on the conversation of item `<id>`: a WhatsApp
or Matrix item opens its chat view, a mail item its mail thread view.
With no argument it reads the id from `BUNKER_OPEN_ID`; that is how the
herdr plugin's `open` pane gets it, since herdr runs a manifest pane's
command as fixed argv and passes the id with `--env`.

- **Leaving:** the pane exists for that one conversation, so Esc (and `q`
  in the mail thread view, where it is not typed text) quits instead of
  going back to an inbox. Leaving a chat still reports the presence and
  typing as gone before exiting.
- **No inbox:** it does not poll the inbox or send new-message
  notifications (the sidebar that opened it already does).
- **Ask Claude:** inside herdr, `a` (in the mail thread and plain detail
  views) or `Alt+A` (in a chat, where `a` is typed text) asks Claude
  about this item, exactly as `a` in `bunker sidebar`; the result shows
  above the key hints.
- **Errors:** no id or more than one argument prints usage and exits 2;
  an id starting with `-` or holding a control character exits 2; no TTY
  prints `error: bunker open needs a terminal` and exits 2; an
  unreachable daemon prints the same hint as bare `bunker` and exits 1.
  An id the daemon does not know shows
  `No se pudo abrir la conversación: …` in the pane, and `q` or Esc quits.

## `bunker herdr toggle [--dry-run] [--json]`

Docks bunker as a narrow panel on the left of the current herdr tab. It
is the `bunker.toggle` action of the herdr plugin in `deploy/herdr` (see
its README to install it and bind a key), and needs herdr 0.8.0 or later.
It talks to herdr only through the `herdr` CLI: `HERDR_BIN_PATH` when
set (herdr sets it for plugin commands), else `herdr` on `PATH`.

It lists the panes, takes the tab of the focused pane (or `HERDR_TAB_ID`
when herdr reports none focused), and looks there for a pane labelled
`bunker`:

- **Focused:** closes it (`herdr pane close <pane>`).
- **Not focused:** focuses it (`herdr plugin pane focus <pane>`).
- **None:** opens the plugin's `sidebar` pane and docks it:

  ```sh
  herdr plugin pane open --plugin bunker --entrypoint sidebar --placement split --target-pane <focused> --direction right --no-focus
  herdr pane swap --source-pane <new-pane> --target-pane <focused>
  herdr pane resize --direction left --amount 0.25 --pane <new-pane>
  herdr pane rename <new-pane> bunker
  herdr plugin pane focus <new-pane>
  ```

  The split opens right of the focused pane without focus, the swap moves
  it to the left, and the resize leaves it a quarter of the split. The
  label is how the next toggle finds it. If a step after the open fails,
  the new pane is closed again so a later toggle does not open a second
  one.

- **Limitation:** the panel docks beside the focused pane, so in a tab
  split into several panes it is as tall as that pane, not a full-height
  column at the tab's edge.
- **`--dry-run`:** runs only the read-only `herdr pane list` and prints
  the herdr commands it would run, one per line, with `<new-pane>` for the
  id the open would return. Nothing changes.
- **Errors:** herdr not found, no herdr server answering (outside herdr),
  an error response from herdr (its message is shown), or a pane id that
  does not look like a herdr id (such as one starting with `-`), which is
  never passed on as an argument. Everything is bounded by a 10s timeout.

Plain output is the action and pane (`open w1:p5`, `focus w1:p3`,
`close w1:p3`). JSON:

```json
{"action":"open","tab":"w1:t1","pane":"w1:p5","anchor":"w1:p1","dryRun":false,"commands":[["plugin","pane","open","--plugin","bunker", ...], ...]}
```

`pane` is empty in an open dry run; `anchor` (the pane it docks beside)
is only set for `open`.

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
{"segments": [{"channel":"mail","glyph":"✉","unread":3}, ...], "daemonUp": true, "allConnected": true, "calls": []}
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

**Calls:** when the daemon reports a call, it leads the segment. JSON's
`calls` carries the same objects as `bunker calls --json`.

| Situation | Segment |
|---|---|
| Incoming call ringing | `📞 Ana` (bold and blinking with `--tmux`) |
| Call connected | `📞 Ana 2:35`, with its connected time |
| Your own call still ringing | `📞 → Ana` |

`--hide-empty` never hides a call. The caller's name is stripped of `#`
and control characters before it reaches the status line. Calls come
only from the live daemon, never from the store fallback.

A ready-to-paste `~/.tmux.conf` snippet. `latest` picks the newest
ringing call for `answer`/`reject` and the newest live call for `hangup`
(see `bunker call`):

```tmux
set -g status-interval 2
set -g status-right '#(bunker render --tmux) '
bind-key a run-shell 'bunker call answer latest'
bind-key R run-shell 'bunker call reject latest'
bind-key H run-shell 'bunker call hangup latest'
bind-key m display-popup -E -w 80% -h 80% bunker
```

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

## `bunker import-keys matrix <account> <file> [--passphrase-stdin] [--json]`

Imports an Element-style megolm key export (a file starting with
`-----BEGIN MEGOLM SESSION DATA-----`) into an already-logged-in
account's existing session and crypto store
(`internal/channel/matrix.ImportKeyExportForAccount`, via mautrix crypto's
own `OlmMachine.ImportKeys`). Prints `imported N new, M already known
(T in export) from <file>`, adding `, F failed` before the total when the
export contained sessions the machine rejected outright (bad algorithm,
mismatched session ID). With `--json` it prints
`{"new": 3, "already_known": 7, "failed": 0, "total": 10}` (errors use the
usual `{"error": ...}`).

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

`link` does not accept `--json`: it is an interactive, one-shot setup step
(a QR code or SSO URL for a person), not a scriptable data command.
`import-keys` does, so a script can drive it with `--passphrase-stdin`.

## Errors

Every command's `--json` error shape is:

```json
{"error": "human-readable message"}
```

Exit codes: `0` success, `1` an error occurred while handling the command
(unknown id, unsupported capability, an invalid flag combination such as
`--seen --unseen` together, I/O failure), `2` bad usage caught before the
command ran (missing argument, unknown flag, unknown command).

## `core.Plan` and `core.Receipt` JSON shape

Every outbound command returns a `plan` (what was, or with `--dry-run`
would be, done) and a `receipt` (what the channel confirmed; the zero
value on a dry run). Keys are lower snake_case, like the rest of the API;
before this release they were the Go field names (`Action`, `Recipients`,
`ID`, ...).

```json
{
  "plan": {
    "action": "send", "channel": "mail", "account": "cl",
    "target": "alice@x.cl", "cc": ["carol@example.org"],
    "subject": "hi", "preview": "hi there",
    "media": ["/path/a.pdf"],
    "attachments": [{"name": "a.pdf", "mime": "application/pdf", "size": 2048}],
    "recipients": ["alice@x.cl"],
    "fanout_pause_min": 3000000000, "fanout_pause_max": 8000000000
  },
  "receipt": {
    "id": "...", "channel": "mail", "at": "2026-09-30T12:00:00Z",
    "recipients": [{"to": "alice@x.cl", "receipt": {"id": "...", "channel": "mail", "at": "..."}, "error": "..."}],
    "replayed": false
  }
}
```

`plan.action`, `channel`, `account`, `target` and `preview`, and
`receipt.id`, `channel` and `at`, are always present. The rest is omitted
when empty: `cc`, `subject`, `media`, `attachments`, `recipients`, the
two `fanout_pause_*` fields (integer nanoseconds), `voice` (true for a
voice note, whose attachment also carries `"voice": true` and
`"duration_ms"`), `receipt.recipients` (fan-out only), `receipt.replayed` (true for an idempotent replay) and a
recipient's `error`. The envelope keys (`dryRun`, `call`, `result`) are
unchanged.

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

`Edited`, `Deleted` and `Reactions` are the channel-agnostic
edit/revoke/reaction model shared by WhatsApp and Matrix. `Edited` is
`true` once a channel-observed edit replaced `Body` (on Matrix, an
`m.replace` edit by the original sender; edits by anyone else are
ignored). `Deleted` is `true` after a revoke (on Matrix, a redaction of
the message); the row is kept (its place in `list`/`thread` pagination
is preserved) but `Body` is cleared, and the TUI renders "mensaje
eliminado" in its place. `Reactions` lists at most one entry per
`Sender` — a newer reaction from the same sender replaces the previous
one, and an empty `Emoji` removes it — rendered as a line of emoji under
the message in the chat/thread view. Matrix lets one user add several
reactions to the same message: the newest is shown, and redacting it
falls back to that user's previous one if it was seen since the daemon
started (otherwise the user's reaction is simply removed). Edits,
reactions and redactions never appear as items of their own. Both fields
are absent of any effect for a plain, never-edited/revoked/reacted-to
item (`Edited`/`Deleted` `false`, `Reactions` empty).

A voice note's attachment also has `voice` (true), `duration` (seconds)
and `waveform` (base64, one 0-100 byte per bar), each omitted when empty.

An attachment may also carry `Thumbnail`: the small preview image
(base64 JPEG or PNG, at most 64 KB) a WhatsApp image, video, sticker or
document message embeds, which the TUI draws instead of downloading the
media. It is absent when the message had none or it was not a valid
image (the daemon logs and drops those).

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
