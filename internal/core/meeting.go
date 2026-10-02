package core

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Meeting is a calendar invitation (a mail's text/calendar part or .ics
// attachment) or, as a lighter fallback, a bare video-call link found in
// a message body. It travels on the item that carried it (Item.Meta,
// MetaMeeting), so it survives restarts without its own table, and
// Service.Meetings lists the upcoming ones.
type Meeting struct {
	// UID identifies the event across the updates and cancellations of
	// one invitation. A link meeting uses "link:" + URL.
	UID string `json:"uid"`
	// Method is the iCalendar METHOD (REQUEST, PUBLISH, CANCEL), or
	// MeetingMethodLink for a bare link.
	Method  string `json:"method,omitempty"`
	Summary string `json:"summary,omitempty"`
	// Start and End are zero for a link meeting. For an all-day event
	// they are midnight in the event's zone and End is exclusive.
	Start  time.Time `json:"start,omitzero"`
	End    time.Time `json:"end,omitzero"`
	AllDay bool      `json:"all_day,omitempty"`
	// TZ is the IANA zone Start was written in ("" for UTC or a fixed
	// offset); a recurring event repeats on its wall clock.
	TZ        string `json:"tz,omitempty"`
	Organizer string `json:"organizer,omitempty"`
	Location  string `json:"location,omitempty"`
	// URL is the join link ("" when the invitation carries none).
	URL      string `json:"url,omitempty"`
	Sequence int    `json:"sequence,omitempty"`
	// Cancelled is set by METHOD:CANCEL or STATUS:CANCELLED.
	Cancelled bool `json:"cancelled,omitempty"`
	// Link marks a meeting without a time, found as a bare link.
	Link bool `json:"link,omitempty"`
	// Rule is the RRULE of a recurring event and ExDates the occurrences
	// removed from it. Only DAILY, WEEKLY (with BYDAY), MONTHLY (same day
	// of the month) and YEARLY rules are expanded; Recurring is then set
	// on the UpcomingMeeting naming the next occurrence.
	Rule    string      `json:"rule,omitempty"`
	ExDates []time.Time `json:"exdates,omitempty"`
}

const (
	// MetaMeeting is the Item.Meta key holding a Meeting as JSON.
	MetaMeeting = "meeting"
	// MetaMeetingEnd is the Meta key with the Unix second after which the
	// meeting no longer matters: the store indexes it so listing the
	// upcoming ones never scans every item.
	MetaMeetingEnd = "meeting_end"
	// MeetingMethodLink is Meeting.Method of a bare link.
	MeetingMethodLink = "LINK"
	// MeetingLinkWindow is how long a bare link stays listed after the
	// message that carried it: a link without a time is only worth a
	// shortcut while the call it announces is probably still on.
	MeetingLinkWindow = 24 * time.Hour
	// DefaultMeetingDays is the look-ahead of Service.Meetings.
	DefaultMeetingDays = 7
	// meetingLookback is how far behind now the store is asked for
	// meetings, so an update or cancellation of a running or just ended
	// event is still seen when picking the latest version of its UID.
	meetingLookback = 14 * 24 * time.Hour
	// maxMeetingRows bounds one listing.
	maxMeetingRows = 500
)

// farFuture is the end of a recurrence without UNTIL or COUNT.
var farFuture = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)

// MeetingFilter narrows Service.Meetings. Days <= 0 uses
// DefaultMeetingDays; Limit <= 0 returns everything in the window.
type MeetingFilter struct {
	Days  int `json:"days,omitempty"`
	Limit int `json:"limit,omitempty"`
}

// UpcomingMeeting is a meeting due soon or in progress, with the item it
// came from. For a recurring event Start and End are the next occurrence.
type UpcomingMeeting struct {
	Meeting
	ItemID    string  `json:"item_id"`
	Channel   Channel `json:"channel"`
	Account   string  `json:"account"`
	Recurring bool    `json:"recurring,omitempty"`
}

// InProgress reports whether the meeting has started and not ended at now.
func (u UpcomingMeeting) InProgress(now time.Time) bool {
	return !u.Link && !u.Start.After(now) && u.End.After(now)
}

// MeetingLister is an optional Store capability: the items carrying a
// meeting that ends at or after endsAfter (newest first, bounded).
// internal/store implements it.
type MeetingLister interface {
	Meetings(ctx context.Context, endsAfter time.Time) ([]Item, error)
}

// MeetingFromItem decodes the meeting an item carries.
func MeetingFromItem(item Item) (Meeting, bool) {
	raw := item.Meta[MetaMeeting]
	if raw == "" {
		return Meeting{}, false
	}
	var m Meeting
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return Meeting{}, false
	}
	return m, true
}

// MeetingMeta returns meta with m stored in it (a copy when meta is nil).
// at is when the carrying message was sent: a link meeting expires
// MeetingLinkWindow after it.
func MeetingMeta(meta map[string]string, m Meeting, at time.Time) (map[string]string, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("core: marshal meeting: %w", err)
	}
	if meta == nil {
		meta = map[string]string{}
	}
	meta[MetaMeeting] = string(raw)
	meta[MetaMeetingEnd] = strconv.FormatInt(m.relevantUntil(at).Unix(), 10)
	return meta, nil
}

// relevantUntil is when the meeting stops mattering at all.
func (m Meeting) relevantUntil(at time.Time) time.Time {
	if m.Link {
		return at.Add(MeetingLinkWindow)
	}
	if m.Rule == "" {
		return m.End
	}
	if r := parseRule(m.Rule); r.supported && r.until.IsZero() && r.count == 0 {
		return farFuture
	}
	last := m.End
	unbounded := true
	m.occurrences(func(start, end time.Time) bool {
		last = end
		return true
	}, &unbounded)
	if unbounded {
		return farFuture
	}
	return last
}

// Meetings lists the upcoming meetings: those starting within the next
// filter.Days days plus any in progress, soonest first, then the bare
// links of the last day, newest first. An invitation updated or cancelled
// by a later mail counts once, by its latest version; a cancelled or
// ended one is left out. It reads the store only.
func (s *Service) Meetings(ctx context.Context, filter MeetingFilter) ([]UpcomingMeeting, error) {
	days := filter.Days
	if days <= 0 {
		days = DefaultMeetingDays
	}
	lister, ok := s.store.(MeetingLister)
	if !ok {
		return nil, fmt.Errorf("core: store cannot list meetings: %w", ErrUnsupported)
	}
	now := s.queryClock()
	items, err := lister.Meetings(ctx, now.Add(-meetingLookback))
	if err != nil {
		return nil, err
	}
	return upcomingMeetings(items, now, days, filter.Limit), nil
}

// upcomingMeetings is Service.Meetings over already loaded items.
func upcomingMeetings(items []Item, now time.Time, days, limit int) []UpcomingMeeting {
	type version struct {
		item Item
		m    Meeting
	}
	latest := map[string]version{}
	for _, item := range items {
		m, ok := MeetingFromItem(item)
		if !ok || item.Deleted {
			continue
		}
		key := m.UID
		if key == "" {
			key = item.ID
		}
		cur, seen := latest[key]
		if seen && (cur.m.Sequence > m.Sequence ||
			(cur.m.Sequence == m.Sequence && cur.item.Timestamp.After(item.Timestamp))) {
			continue
		}
		latest[key] = version{item: item, m: m}
	}

	horizon := now.Add(time.Duration(days) * 24 * time.Hour)
	out := []UpcomingMeeting{}
	for _, v := range latest {
		m := v.m
		if m.Cancelled {
			continue
		}
		up := UpcomingMeeting{Meeting: m, ItemID: v.item.ID, Channel: v.item.Channel, Account: v.item.Account}
		if m.Link {
			if now.Sub(v.item.Timestamp) > MeetingLinkWindow {
				continue
			}
			out = append(out, up)
			continue
		}
		start, end, found := m.nextOccurrence(now)
		if !found || start.After(horizon) {
			continue
		}
		up.Recurring = m.Rule != ""
		up.Start, up.End = start, end
		out = append(out, up)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Link != b.Link {
			return !a.Link
		}
		if a.Link {
			return a.ItemID > b.ItemID
		}
		if !a.Start.Equal(b.Start) {
			return a.Start.Before(b.Start)
		}
		return a.ItemID < b.ItemID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	if len(out) > maxMeetingRows {
		out = out[:maxMeetingRows]
	}
	return out
}

// nextOccurrence is the first occurrence that has not ended at now.
func (m Meeting) nextOccurrence(now time.Time) (start, end time.Time, ok bool) {
	m.occurrences(func(s, e time.Time) bool {
		if e.After(now) {
			start, end, ok = s, e, true
			return false
		}
		return true
	}, nil)
	return start, end, ok
}

// maxOccurrenceSteps bounds an expansion so a malformed rule can never
// spin: a daily event started years ago is still far below it.
const maxOccurrenceSteps = 20000

// occurrences calls yield with every occurrence in order until it returns
// false, the rule ends (UNTIL, COUNT) or the step bound is hit. A meeting
// without a supported rule has exactly one. When unbounded is non-nil it
// is cleared if the series ended by itself.
func (m Meeting) occurrences(yield func(start, end time.Time) bool, unbounded *bool) {
	dur := m.End.Sub(m.Start)
	if m.Rule == "" {
		yield(m.Start, m.End)
		return
	}
	rule := parseRule(m.Rule)
	if !rule.supported {
		if unbounded != nil {
			*unbounded = false
		}
		yield(m.Start, m.End)
		return
	}
	loc := m.Start.Location()
	if m.TZ != "" {
		if l, err := time.LoadLocation(m.TZ); err == nil {
			loc = l
		}
	}
	base := m.Start.In(loc)
	excluded := map[int64]bool{}
	for _, x := range m.ExDates {
		excluded[x.Unix()] = true
	}

	count := 0
	emit := func(s time.Time) (stop bool) {
		if s.Before(base) {
			return false
		}
		if !rule.until.IsZero() && s.After(rule.until) {
			if unbounded != nil {
				*unbounded = false
			}
			return true
		}
		count++
		if rule.count > 0 && count > rule.count {
			if unbounded != nil {
				*unbounded = false
			}
			return true
		}
		if excluded[s.Unix()] {
			return false
		}
		return !yield(s, s.Add(dur))
	}

	for step := 0; step < maxOccurrenceSteps; step++ {
		switch rule.freq {
		case "DAILY":
			if emit(base.AddDate(0, 0, step*rule.interval)) {
				return
			}
		case "WEEKLY":
			if len(rule.byDay) == 0 {
				if emit(base.AddDate(0, 0, 7*step*rule.interval)) {
					return
				}
				continue
			}
			// Monday-based week of the first occurrence, then every
			// interval-th week; BYDAY days in week order.
			weekStart := base.AddDate(0, 0, -((int(base.Weekday()) + 6) % 7))
			for _, wd := range rule.byDay {
				day := weekStart.AddDate(0, 0, 7*step*rule.interval+(int(wd)+6)%7)
				if emit(day) {
					return
				}
			}
		case "MONTHLY":
			s := base.AddDate(0, step*rule.interval, 0)
			if s.Day() != base.Day() {
				continue // e.g. the 31st in a 30-day month: skipped
			}
			if emit(s) {
				return
			}
		case "YEARLY":
			s := base.AddDate(step*rule.interval, 0, 0)
			if s.Day() != base.Day() {
				continue
			}
			if emit(s) {
				return
			}
		}
	}
}

// meetingRule is the subset of an RRULE occurrences expands.
type meetingRule struct {
	freq      string
	interval  int
	count     int
	until     time.Time
	byDay     []time.Weekday
	supported bool
}

var ruleWeekdays = map[string]time.Weekday{
	"SU": time.Sunday, "MO": time.Monday, "TU": time.Tuesday, "WE": time.Wednesday,
	"TH": time.Thursday, "FR": time.Friday, "SA": time.Saturday,
}

func parseRule(rule string) meetingRule {
	r := meetingRule{interval: 1, supported: true}
	for _, part := range strings.Split(rule, ";") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		switch strings.ToUpper(strings.TrimSpace(k)) {
		case "FREQ":
			r.freq = strings.ToUpper(v)
		case "INTERVAL":
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				r.interval = n
			}
		case "COUNT":
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				r.count = n
			}
		case "UNTIL":
			if t, ok := parseRuleTime(v); ok {
				r.until = t
			}
		case "BYDAY":
			for _, d := range strings.Split(v, ",") {
				wd, ok := ruleWeekdays[strings.ToUpper(d)]
				if !ok {
					r.supported = false // "2TU", "-1FR": nth weekday
					continue
				}
				r.byDay = append(r.byDay, wd)
			}
		case "WKST", "BYSECOND", "BYMINUTE", "BYHOUR":
		default:
			// BYMONTHDAY, BYMONTH, BYSETPOS...: not expanded.
			r.supported = false
		}
	}
	switch r.freq {
	case "DAILY", "MONTHLY", "YEARLY":
		if len(r.byDay) > 0 {
			r.supported = false
		}
	case "WEEKLY":
		sort.Slice(r.byDay, func(i, j int) bool {
			return (int(r.byDay[i])+6)%7 < (int(r.byDay[j])+6)%7
		})
	default:
		r.supported = false
	}
	return r
}

func parseRuleTime(v string) (time.Time, bool) {
	for _, layout := range []string{"20060102T150405Z", "20060102T150405", "20060102"} {
		if t, err := time.Parse(layout, v); err == nil {
			if layout == "20060102" {
				t = t.Add(24*time.Hour - time.Second)
			}
			return t, true
		}
	}
	return time.Time{}, false
}
