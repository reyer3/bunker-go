package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/reyer3/bunker-go/internal/core"
)

// awaitingCandidate is a conversation whose newest item is the user's
// and old enough, before the group and self-chat rules apply. others is
// how many distinct people besides the user wrote in it (Matrix only:
// the other channels decide groups from the thread id).
type awaitingCandidate struct {
	id, channel, thread, fromID string
	others                      int
}

// AwaitingReply implements core.AwaitingLister.
//
// The aggregate over idx_items_conversation finds each threaded
// conversation's newest timestamp (cut to those before q.Before, index
// only), then the join reads just that newest row through
// idx_items_thread; NOT EXISTS keeps the highest id when several rows
// share the newest timestamp, the same tie-break the inbox uses, so a
// conversation is judged by exactly one row. Only light columns are read
// here; the group and self-chat rules live in core and run in Go, and
// full items are loaded for the survivors only.
func (s *Store) AwaitingReply(ctx context.Context, q core.AwaitingQuery) ([]core.Item, error) {
	conds := []string{"thread <> ''"}
	args := []any{}
	if q.Channel != "" {
		conds = append(conds, "channel = ?")
		args = append(args, string(q.Channel))
	}
	if q.Account != "" {
		conds = append(conds, "account = ?")
		args = append(args, q.Account)
	}
	args = append(args, q.Before.UnixNano())
	query := `
		SELECT i.id, i.channel, i.thread, i.from_id,
			CASE WHEN i.channel = ? THEN (
				SELECT COUNT(DISTINCT k.from_id) FROM items k
				WHERE k.channel = i.channel AND k.account = i.account AND k.thread = i.thread AND k.from_me = 0
			) ELSE 0 END
		FROM (
			SELECT channel, account, thread, MAX(timestamp) AS newest
			FROM items WHERE ` + strings.Join(conds, " AND ") + `
			GROUP BY channel, account, thread
			HAVING MAX(timestamp) < ?
		) g
		JOIN items i ON i.channel = g.channel AND i.account = g.account AND i.thread = g.thread AND i.timestamp = g.newest
		WHERE i.from_me = 1 AND i.deleted = 0 AND NOT EXISTS (
			SELECT 1 FROM items j
			WHERE j.channel = g.channel AND j.account = g.account AND j.thread = g.thread
				AND j.timestamp = g.newest AND j.id > i.id
		)
		ORDER BY g.newest DESC, i.id DESC`
	candidates, err := s.awaitingCandidates(ctx, query, append([]any{string(core.ChannelMatrix)}, args...))
	if err != nil {
		return nil, err
	}

	var out []core.Item
	for _, c := range candidates {
		if q.Limit > 0 && len(out) >= q.Limit {
			break
		}
		ch := core.Channel(c.channel)
		if core.IsSelfChat(ch, c.thread, c.fromID) || (!q.Groups && core.IsGroupConversation(ch, c.thread, c.others)) {
			continue
		}
		item, err := scanItem(s.db.QueryRowContext(ctx, conversationItemSQL+` WHERE id = ?`, c.id))
		if err != nil {
			return nil, fmt.Errorf("store: awaiting item %s: %w", c.id, err)
		}
		if item.ThreadName == "" {
			if item.ThreadName, err = s.lastThreadName(ctx, string(item.Channel), item.Account, item.Thread); err != nil {
				return nil, err
			}
		}
		out = append(out, item)
	}
	return out, nil
}

// awaitingCandidates runs the candidate query and collects its rows,
// closing the cursor before the caller reads more (the pool may hold a
// single connection).
func (s *Store) awaitingCandidates(ctx context.Context, query string, args []any) ([]awaitingCandidate, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: awaiting: %w", err)
	}
	defer rows.Close()
	var out []awaitingCandidate
	for rows.Next() {
		var c awaitingCandidate
		if err := rows.Scan(&c.id, &c.channel, &c.thread, &c.fromID, &c.others); err != nil {
			return nil, fmt.Errorf("store: awaiting scan: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: awaiting rows: %w", err)
	}
	return out, nil
}
