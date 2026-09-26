package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
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
	// --tmux is accepted for forward compatibility: the default output is
	// already the compact, tmux-safe segment line documented in
	// docs/cli.md, so it does not currently change the format.
	fs.Bool("tmux", false, "tmux-friendly compact output (currently the default)")
	jsonOut := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	budgetCtx, cancel := context.WithTimeout(ctx, renderBudget)
	defer cancel()

	counts, daemonUp, err := renderCounts(budgetCtx)
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
		writeJSON(stdout, map[string]any{"segments": segments, "daemonUp": daemonUp})
		return 0
	}

	parts := make([]string, 0, len(segments))
	for _, seg := range segments {
		parts = append(parts, fmt.Sprintf("%s %d", seg.Glyph, seg.Unread))
	}
	fmt.Fprintln(stdout, strings.Join(parts, "  "))
	return 0
}

// renderCounts tries the live daemon first, then falls back to a
// read-only open of the store file directly.
func renderCounts(ctx context.Context) (map[core.Channel]map[string]int, bool, error) {
	if client, err := rpc.Dial(rpc.DefaultSocketPath()); err == nil {
		defer client.Close()
		if counts, err := client.Counts(ctx); err == nil {
			return counts, true, nil
		}
	}

	st, err := store.Open(config.StoreDBPath())
	if err != nil {
		return nil, false, fmt.Errorf("render: store fallback: %w", err)
	}
	defer st.Close()
	counts, err := st.Counts(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("render: store counts: %w", err)
	}
	return counts, false, nil
}
