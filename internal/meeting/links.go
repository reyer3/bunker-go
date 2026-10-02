package meeting

import (
	"strings"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// FromText finds a bare meeting link in a message and returns it as a
// "link" meeting: no time, listed only while the message is recent (see
// core.MeetingLinkWindow). title names it (the conversation or subject);
// it falls back to the provider's name.
func FromText(title string, texts ...string) (core.Meeting, bool) {
	u := core.FindMeetingURL(texts...)
	if u == "" {
		return core.Meeting{}, false
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Reunión en " + core.MeetingProvider(u)
	}
	return core.Meeting{
		UID:     "link:" + u,
		Method:  core.MeetingMethodLink,
		Summary: title,
		URL:     u,
		Link:    true,
	}, true
}

// FromItem derives the meeting an item carries when the channel did not
// attach one: a bare link in its subject or body, if the message is
// recent at now. Revoked items and old history yield nothing, so a first
// sync of years of mail writes no meeting data.
func FromItem(item core.Item, now time.Time) (core.Meeting, bool) {
	if item.Deleted || item.Timestamp.IsZero() || now.Sub(item.Timestamp) > core.MeetingLinkWindow {
		return core.Meeting{}, false
	}
	title := item.ThreadName
	if title == "" {
		title = item.Subject
	}
	if title == "" {
		title = item.From.Name
	}
	return FromText(title, item.Subject, item.Body)
}

// Pick chooses the meeting a message stands for when its calendar holds
// several events: the soonest one that has not ended at now, else the
// first. Cancellations are kept (a CANCEL must reach the store).
func Pick(meetings []core.Meeting, now time.Time) (core.Meeting, bool) {
	if len(meetings) == 0 {
		return core.Meeting{}, false
	}
	best := -1
	for i, m := range meetings {
		if m.Rule == "" && !m.End.After(now) {
			continue
		}
		if best < 0 || m.Start.Before(meetings[best].Start) {
			best = i
		}
	}
	if best < 0 {
		best = 0
	}
	return meetings[best], true
}
