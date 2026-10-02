package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/meeting"
)

var _ core.MeetingLister = (*Store)(nil)

// now is the clock bare-link detection measures a message's age with;
// tests replace it.
var now = time.Now

// metaWithMeeting returns the Meta to store for item: its own, plus the
// meeting it carries.
//
//   - A channel that attached one (mail's calendar part) is kept as is.
//   - Otherwise a recent message with a bare call link in it gets a link
//     meeting (meeting.FromItem), for every channel.
//   - Otherwise, for mail, the stored row's meeting is carried over: mail
//     reaches the store in copies of different depth (a header-only sync,
//     a keyword refresh), and a shallow copy has neither the calendar
//     part nor the body text, so without this it would erase the meeting
//     a fuller copy found. Same rule as Upsert's body/attachments merge.
func (s *Store) metaWithMeeting(ctx context.Context, tx *sql.Tx, item core.Item) (map[string]string, error) {
	meta := make(map[string]string, len(item.Meta)+2)
	for k, v := range item.Meta {
		meta[k] = v
	}
	if meta[core.MetaMeeting] != "" {
		if _, err := strconv.ParseInt(meta[core.MetaMeetingEnd], 10, 64); err != nil {
			// A producer that set the meeting without its end: derive it.
			if m, ok := core.MeetingFromItem(item); ok {
				return core.MeetingMeta(meta, m, item.Timestamp)
			}
			delete(meta, core.MetaMeeting)
		}
		return meta, nil
	}
	delete(meta, core.MetaMeetingEnd)
	if m, ok := meeting.FromItem(item, now()); ok {
		return core.MeetingMeta(meta, m, item.Timestamp)
	}
	if item.Channel != core.ChannelMail || item.Deleted {
		return meta, nil
	}
	var old string
	err := tx.QueryRowContext(ctx, `SELECT meta_json FROM items WHERE id = ?`, item.ID).Scan(&old)
	if err == sql.ErrNoRows {
		return meta, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: read stored meta of %s: %w", item.ID, err)
	}
	var stored map[string]string
	if json.Unmarshal([]byte(old), &stored) == nil && stored[core.MetaMeeting] != "" {
		meta[core.MetaMeeting] = stored[core.MetaMeeting]
		meta[core.MetaMeetingEnd] = stored[core.MetaMeetingEnd]
	}
	return meta, nil
}

// meetingEnd is the meeting_end column value for meta: 0 without one.
func meetingEnd(meta map[string]string) int64 {
	if meta[core.MetaMeeting] == "" {
		return 0
	}
	n, err := strconv.ParseInt(meta[core.MetaMeetingEnd], 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// maxMeetingItems bounds one Meetings read.
const maxMeetingItems = 1000

// Meetings implements core.MeetingLister: the items carrying a meeting
// that ends at or after endsAfter, newest first, through idx_items_meeting.
func (s *Store) Meetings(ctx context.Context, endsAfter time.Time) ([]core.Item, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, channel, account, thread, thread_name, from_id, from_name,
			to_json, subject, '', attachments_json, unread, from_me, timestamp, meta_json,
			edited, deleted
		FROM items WHERE meeting_end >= ? AND meeting_end > 0 AND deleted = 0
		ORDER BY timestamp DESC, id LIMIT ?
	`, endsAfter.Unix(), maxMeetingItems)
	if err != nil {
		return nil, fmt.Errorf("store: list meetings: %w", err)
	}
	defer rows.Close()

	var out []core.Item
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan meeting: %w", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list meetings: %w", err)
	}
	return out, nil
}
