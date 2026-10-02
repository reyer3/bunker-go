package tui

// hitKind is what a mouse click on one physical line of the rendered
// inbox does. It is derived from the exact same layout the view text
// comes from (see rowUnit and overviewLines/focusedSectionLines), so a
// click always lands on what the eye sees, and never on anything else:
// in particular, a click never sends or marks anything by itself — it
// only ever selects, opens, or focuses.
type hitKind int

const (
	hitNone hitKind = iota
	// hitRow selects visibleGroups()[row]; a click on the already-
	// selected row instead opens it (like Enter).
	hitRow
	// hitFocus switches focus to tab (0 = overview, 1..3 = a channel
	// section — see Model.switchTab), from a click on a section header,
	// its rule, or a "+N más" truncation notice.
	hitFocus
	// hitMeeting joins activeMeetings()[row] (the "Reuniones" section).
	hitMeeting
)

// inboxHit is one physical line's click target.
type inboxHit struct {
	kind hitKind
	row  int
	tab  int
}

// rowUnit is one conversation's rendered lines (1 or 2, see buildRow)
// together with the click target every one of those lines shares.
type rowUnit struct {
	lines []string
	hit   inboxHit
}

func flattenRowUnits(units []rowUnit) (lines []string, hits []inboxHit) {
	for _, u := range units {
		lines = append(lines, u.lines...)
		for range u.lines {
			hits = append(hits, u.hit)
		}
	}
	return lines, hits
}

// repeat returns n copies of hit, one per physical line a single-target
// block (a section header+rule, an empty-section placeholder) occupies.
func repeatHit(hit inboxHit, n int) []inboxHit {
	hits := make([]inboxHit, n)
	for i := range hits {
		hits[i] = hit
	}
	return hits
}
