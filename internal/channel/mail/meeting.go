package mail

import (
	"log"
	"strings"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/meeting"
)

// isCalendarType reports whether a MIME type carries an iCalendar body.
func isCalendarType(contentType string) bool {
	switch strings.ToLower(contentType) {
	case "text/calendar", "application/ics", "application/calendar":
		return true
	}
	return false
}

// setMeeting records on item the meeting its calendar parts describe
// (core.MetaMeeting), so a REQUEST, a PUBLISH and a CANCEL all reach the
// store with the message. A calendar that cannot be read is logged and
// leaves the item as it was: the message itself still syncs.
func setMeeting(item *core.Item, calendars []string) {
	var found []core.Meeting
	for _, data := range calendars {
		ms, err := meeting.ParseICS(data)
		if err != nil {
			log.Printf("mail: sync: calendar of %s: %v", item.ID, err)
			continue
		}
		found = append(found, ms...)
	}
	m, ok := meeting.Pick(found, time.Now())
	if !ok {
		return
	}
	meta, err := core.MeetingMeta(item.Meta, m, item.Timestamp)
	if err != nil {
		log.Printf("mail: sync: meeting of %s: %v", item.ID, err)
		return
	}
	item.Meta = meta
}
