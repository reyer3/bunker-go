# bunker for herdr

A plugin for herdr, the terminal workspace manager for coding agents, that
docks bunker as a narrow panel on the right of the current tab, next to
your agents. Needs herdr 0.8.0 or later.

## Install

```sh
herdr plugin install reyer3/bunker-go/deploy/herdr
# or, from a checkout, to follow your local changes:
herdr plugin link <checkout>/deploy/herdr
```

- `bunker` must be on the `PATH` herdr runs plugin commands with.
- The bunker daemon must be running (`bunker daemon`, or the systemd user
  service in `deploy/systemd`): the panel is a client of it.

## Use

The plugin adds the `bunker.toggle` action, which runs
`bunker herdr toggle`:

- no bunker panel in the tab: opens one on the right of the focused pane
  and focuses it;
- a panel that is not focused: focuses it;
- a focused panel: closes it.

Bind it to a key in herdr's config:

```toml
[[keys.command]]
key = "prefix+i"
type = "plugin_action"
command = "bunker.toggle"
```

The panel runs `bunker sidebar`, a compact list for a narrow column:
the channels with their unread counts, then the conversations of the
selected one: each shows its name and unread badge, and under it the
last message wrapped onto two lines. `j`/`k` move, `Tab` or `1`/`2`/`3`/`0` switch
channel, `/` filters, `g` refreshes, `a` asks Claude (see below), `?`
shows help and `q` quits.

Enter opens the selected conversation in the tab's conversation pane,
between the panel and the pane on its left (the plugin's `open` pane,
which runs `bunker open`), and focuses it; the list stays open. There is only ever
one conversation pane per tab, labelled `bunker:chat`: Enter on the
conversation it already shows just focuses it, and Enter on another one
replaces it in the same place instead of piling up panes. A pane you
closed is opened again. The item id reaches the pane through the
`BUNKER_OPEN_ID` environment variable, since herdr runs pane commands as
fixed argv. Esc in the conversation pane closes it.

## Ask Claude

`a` in the panel (and in a conversation pane; `Alt+A` in a chat, where
`a` is text) asks a Claude Code agent running in herdr about the
selected conversation. bunker lists herdr's agents (`herdr agent list`),
keeps those whose kind is `claude`, picks the first one that is idle or
done (else the first one), sends it

```text
Usa bunker mcp: lee la conversación <id> (herramienta read o thread) y dime qué necesito saber; si hay que responder, propón una respuesta como plan sin enviarla.
```

with `herdr agent prompt <pane> <text>` and focuses it. Claude needs
bunker's MCP server configured (`bunker mcp`); it only reads, and any
reply comes back as a plan for you to send. With no Claude Code running
the panel says so; if the agent is waiting for your answer to a question
of its own, bunker focuses it and shows `Claude está esperando tu
respuesta en su panel` instead of prompting.

## Unread count in herdr's sidebar

After each poll the panel publishes its unread total as metadata on its
own pane, refreshed at least every 5s and expiring after 10s once bunker
stops:

```sh
herdr pane report-metadata $HERDR_PANE_ID --source bunker --token unread=<N> --ttl-ms 10000
```

herdr shows pane tokens in its Agent sidebar rows as `$name`, so add
`$unread` to a row in herdr's config, for example:

```toml
[ui.sidebar.agents]
rows = [
  ["state_icon", "agent", "tab"],
  [{ token = "$unread", fg = "#89b4fa", bold = true }],
]
```

A row whose tokens are all unset disappears, so the line only shows
where the count is reported. Whether herdr lists a plain plugin pane
(one without an agent) in its Agent sidebar is up to herdr.

## mailto: links

Ctrl-click on a `mailto:` link in any herdr pane opens a new bunker mail
to that address in a popup (80% of the screen) instead of the system mail
client. The plugin's `mailto` link handler runs the `bunker.mailto` action
(`bunker herdr mailto`), which opens the `compose` popup running `bunker
compose` with the URL in `BUNKER_MAILTO`. To, Cc, Subject and the body
are filled from the link (RFC 6068); the mail is sent from the first
mail account in `config.toml`. Ctrl+S previews and sends as in the TUI;
Esc, or a sent mail, closes the popup.

herdr routes a link to a handler only when it recognizes it as a link:
`mailto:` works for OSC 8 hyperlinks (the sender addresses bunker shows
in its mail views are such links, and so are those of other programs
that emit OSC 8), while herdr's plain-text detection only finds
`http(s)://` URLs, so a bare `mailto:` or address typed in a pane is not
clickable. This was checked against herdr 0.9.3; older releases may only
hand `http(s)` links to handlers.
A link with a `bcc=` recipient is refused (the editor has no Bcc field):
the action fails and its log (`herdr plugin log list`) says why.

## New-message notifications

Off by default. With

```toml
[herdr]
notify = true
```

in bunker's `config.toml`, the panel shows each new unread message as a
herdr notification, `<sender>: <text>`, with herdr's "request" sound, at
most one every 30s (a burst in between becomes one `N mensajes nuevos`).
Inside herdr this replaces the terminal notification (OSC 777) the TUI
otherwise sends.

## Notes

`bunker herdr toggle --dry-run` prints the herdr commands it would run.
The panel docks beside the focused pane, so in a tab split into several
panes it is as tall as that pane rather than the whole tab.
