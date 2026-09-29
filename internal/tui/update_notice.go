package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/reyer3/bunker-go/internal/core"
)

// New release notice (issue #105): the daemon checks for releases and
// reports the result with its health; the TUI mentions it once per
// session on the status line (and as a herdr notification when one is
// wired), then stays quiet so it never nags.

// updateNoticeDuration is how long the notice stays on the status line.
const updateNoticeDuration = time.Minute

// HealthReporter is the optional capability that returns adapter
// health and the update status in one call; the RPC client and the
// TUI's query client implement it.
type HealthReporter interface {
	HealthReport(ctx context.Context) (core.HealthReport, error)
}

// fetchHealthReport is best-effort like fetchHealth: a client without
// HealthReport (or a failing one) still gets the adapter health, with no
// update.
func fetchHealthReport(ctx context.Context, client Client) core.HealthReport {
	if hr, ok := client.(HealthReporter); ok {
		if report, err := hr.HealthReport(ctx); err == nil {
			return report
		}
	}
	return core.HealthReport{Adapters: fetchHealth(ctx, client)}
}

// updateNoticeText is the status line's wording for version.
func updateNoticeText(version string) string {
	return "nueva versión v" + version + " disponible · bunker update"
}

// noteUpdate starts the notice the first time a poll reports an update.
func (m Model) noteUpdate(st core.UpdateStatus) (Model, tea.Cmd) {
	if !st.Available || st.Latest == "" || m.updateNoticed {
		return m, nil
	}
	m.updateNoticed = true
	m.updateNotice = updateNoticeText(safeLine(st.Latest))
	m.updateNoticeAt = m.clock()
	if m.messageNotify == nil {
		return m, nil
	}
	notify, body := m.messageNotify, m.updateNotice
	return m, func() tea.Msg { return messageNotifiedMsg{err: notify(body)} }
}

// currentUpdateNotice is the notice while it is still due.
func (m Model) currentUpdateNotice() (string, bool) {
	if m.updateNotice == "" || m.clock().Sub(m.updateNoticeAt) > updateNoticeDuration {
		return "", false
	}
	return m.updateNotice, true
}
