package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// Connection status (issue #35): the inbox says when the daemon is
// unreachable (the poll keeps retrying every pollInterval and the query
// client redials, so it recovers on its own) and how old the data on
// screen is meanwhile, and names every account whose adapter is not
// connected, from the daemon's health.

// HealthClient is the optional capability the status line reads adapter
// health through; the RPC client and the TUI's query client implement it.
type HealthClient interface {
	Health(ctx context.Context) ([]core.AdapterHealth, error)
}

// fetchHealth is best-effort: a client without health, or a failed call,
// just shows no adapter warnings.
func fetchHealth(ctx context.Context, client Client) []core.AdapterHealth {
	hc, ok := client.(HealthClient)
	if !ok {
		return nil
	}
	health, err := hc.Health(ctx)
	if err != nil {
		return nil
	}
	return health
}

// statusLine is the line above the inbox, if there is anything to say.
func (m Model) statusLine() (string, bool) {
	if m.loadErr != nil {
		if !isDaemonDown(m.loadErr) {
			return "Error: " + humanError(m.loadErr), true
		}
		line := "⚠ sin conexión con el daemon · reintentando"
		if !m.loadedAt.IsZero() {
			line += " · datos de " + formatAge(m.clock().Sub(m.loadedAt))
		}
		return line + " · inícialo con: systemctl --user start bunker", true
	}
	if flash, ok := m.currentFlash(); ok {
		return flash, true
	}
	var down []string
	for _, h := range m.adapterHealth {
		var state string
		switch h.State {
		case core.AdapterBackoff:
			state = "reconectando"
		case core.AdapterStopped:
			state = "detenido"
		default:
			continue
		}
		down = append(down, fmt.Sprintf("%s/%s %s", channelLabel(h.Channel), safeLine(h.Account), state))
	}
	var parts []string
	if m.queryActive {
		parts = append(parts, m.queryStatus())
	}
	if len(down) > 0 {
		parts = append(parts, "⚠ "+strings.Join(down, " · "))
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, " · "), true
}

// channelLabel is a channel's display name.
func channelLabel(ch core.Channel) string {
	if name, ok := channelNames[ch]; ok {
		return name
	}
	return string(ch)
}

// formatAge renders how long ago something happened, coarsely.
func formatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("hace %ds", max(1, int(d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("hace %d min", int(d.Minutes()))
	default:
		return fmt.Sprintf("hace %d h", int(d.Hours()))
	}
}
