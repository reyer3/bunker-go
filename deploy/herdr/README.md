# bunker for herdr

A plugin for herdr, the terminal workspace manager for coding agents, that
docks bunker as a narrow panel on the left of the current tab, next to
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

The plugin adds one action, `bunker.toggle`, which runs
`bunker herdr toggle`:

- no bunker panel in the tab: opens one on the left of the focused pane
  and focuses it;
- a panel that is not focused: focuses it;
- a focused panel: closes it.

Bind it to a key in herdr's config:

```toml
[[keys.command]]
key = "prefix+b"
type = "plugin_action"
command = "bunker.toggle"
```

The panel runs `bunker sidebar`, a compact list for a narrow column:
the channels with their unread counts, then the conversations of the
selected one, a line each. `j`/`k` move, `Tab` or `1`/`2`/`3`/`0` switch
channel, `/` filters, `g` refreshes, `?` shows help and `q` quits.

Enter opens the selected conversation in a new pane to the right of the
panel (the plugin's `open` pane, which runs `bunker open`) and focuses
it; the list stays open. The item id reaches that pane through the
`BUNKER_OPEN_ID` environment variable, since herdr runs pane commands as
fixed argv. Esc in the conversation pane closes it.

`bunker herdr toggle --dry-run` prints the herdr commands it would run.
The panel docks beside the focused pane, so in a tab split into several
panes it is as tall as that pane rather than the whole tab.
