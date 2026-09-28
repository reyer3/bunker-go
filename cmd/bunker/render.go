package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/reyer3/bunker-go/internal/config"
	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/rpc"
	"github.com/reyer3/bunker-go/internal/store"
)

// channelGlyphs are the single-glyph markers each channel's render segment
// uses, documented in docs/cli.md: ✉ mail, 💬 WhatsApp, ⌘ Matrix.
var channelGlyphs = map[core.Channel]string{
	core.ChannelMail:     "✉",
	core.ChannelWhatsApp: "💬",
	core.ChannelMatrix:   "⌘",
}

// channelOrder fixes the segment order so the tmux status line does not
// jitter between renders.
var channelOrder = []core.Channel{core.ChannelMail, core.ChannelWhatsApp, core.ChannelMatrix}

// renderBudget is render's hard time budget: it must never make tmux wait
// noticeably for a dead or slow daemon.
const renderBudget = 200 * time.Millisecond

type renderSegment struct {
	Channel core.Channel `json:"channel"`
	Glyph   string       `json:"glyph"`
	Unread  int          `json:"unread"`
}

// cmdRender renders the compact "✉ 3  💬 5  ⌘ 2" tmux segment. It talks to
// the daemon when reachable, falls back to a direct read-only store open
// when it is not, and prints a dead marker if neither works, always
// within renderBudget.
func cmdRender(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("render", flag.ContinueOnError)
	fs.SetOutput(stderr)
	tmux := fs.Bool("tmux", false, "tmux #[fg] colors with Nerd Font glyphs")
	ansi := fs.Bool("ansi", false, "24-bit ANSI colors with Nerd Font glyphs (e.g. a Claude Code statusline)")
	hideEmpty := fs.Bool("hide-empty", false, "print nothing when no channel has unread items")
	jsonOut := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	budgetCtx, cancel := context.WithTimeout(ctx, renderBudget)
	defer cancel()

	counts, daemonUp, allConnected, calls, err := renderCounts(budgetCtx)
	if err != nil {
		if *jsonOut {
			writeJSON(stdout, map[string]any{"dead": true})
		} else {
			fmt.Fprintln(stdout, "bunker: dead")
		}
		return 0
	}

	segments := make([]renderSegment, 0, len(channelOrder))
	for _, ch := range channelOrder {
		total := 0
		for _, n := range counts[ch] {
			total += n
		}
		segments = append(segments, renderSegment{Channel: ch, Glyph: channelGlyphs[ch], Unread: total})
	}

	if *jsonOut {
		if calls == nil {
			calls = []core.Call{}
		}
		writeJSON(stdout, map[string]any{"segments": segments, "daemonUp": daemonUp, "allConnected": allConnected, "calls": calls})
		return 0
	}

	style := renderPlain
	switch {
	case *tmux:
		style = renderTmux
	case *ansi:
		style = renderANSI
	}
	var overrides map[string]string
	if cfg, err := config.LoadDefault(); err == nil {
		overrides = cfg.Render.Glyphs
	}
	callSeg := formatCallSegment(calls, style, time.Now())
	line := formatRender(segments, style, *hideEmpty && callSeg == "", resolveGlyphs(overrides))
	if callSeg != "" {
		// Issue #15: a ringing or live call leads the segment, so it is
		// the first thing seen in the status line.
		if line != "" {
			line = callSeg + "  " + line
		} else {
			line = callSeg
		}
	}
	if line != "" {
		// R4: a "!" marker warns that at least one adapter is not
		// connected, but only when the daemon actually answered within
		// renderBudget (daemonUp) -- render stays exactly as before both
		// when the daemon is down (handled above, before this point is
		// ever reached) and when the health call itself didn't answer in
		// time (allConnected defaults to true in that case, see
		// renderCounts).
		if daemonUp && !allConnected {
			line += " " + notConnectedMarker
		}
		fmt.Fprintln(stdout, line)
	}
	return 0
}

// notConnectedMarker is the render segment suffix R4 appends when the
// live daemon reports at least one adapter not in the "connected" state,
// documented in docs/cli.md.
const notConnectedMarker = "!"

// renderCounts tries the live daemon first, then falls back to a
// read-only open of the store file directly. allConnected is only
// meaningful when daemonUp is true; it defaults to true (no warning)
// whenever health isn't known one way or the other -- the daemon being
// down, or its health call itself not answering within ctx's remaining
// budget -- so a slow/partial health check never invents a false alarm.
func renderCounts(ctx context.Context) (counts map[core.Channel]map[string]int, daemonUp, allConnected bool, calls []core.Call, err error) {
	allConnected = true
	if client, dialErr := rpc.Dial(rpc.DefaultSocketPath()); dialErr == nil {
		defer client.Close()
		if counts, err = client.Counts(ctx); err == nil {
			// Live calls only exist in the daemon; like health, a slow or
			// failed answer just leaves them out of the segment.
			calls, _ = client.Calls(ctx)
			if adapters, healthErr := client.Health(ctx); healthErr == nil {
				for _, a := range adapters {
					if a.State != core.AdapterConnected {
						allConnected = false
						break
					}
				}
			}
			return counts, true, allConnected, calls, nil
		}
	}

	st, err := store.Open(config.StoreDBPath())
	if err != nil {
		return nil, false, true, nil, fmt.Errorf("render: store fallback: %w", err)
	}
	defer st.Close()
	counts, err = st.Counts(ctx)
	if err != nil {
		return nil, false, true, nil, fmt.Errorf("render: store counts: %w", err)
	}
	return counts, false, true, nil, nil
}
