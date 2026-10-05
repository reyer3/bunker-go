package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/reyer3/bunker-go/internal/core"
)

// convGroup is one conversation found by the aggregate pass: its key, the
// newest timestamp (UnixNano) and its unread total. single is the item id
// of a threadless item, which is a conversation of its own.
type convGroup struct {
	channel, account, thread, single string
	newest                           int64
	unread                           int
}

// Conversations implements core.ConversationLister: one row per
// (channel, account, thread) with its newest item and unread count,
// newest conversation first. An item without a thread is a conversation
// of its own (as the inbox groups it), never merged with other
// threadless items.
//
// It stays cheap on a large store: an index-only aggregate over
// idx_items_conversation finds each conversation's newest timestamp and
// unread total (cut to the limit in SQL), then only the surviving
// conversations' newest items are read through idx_items_thread. Labels
// and reactions are not loaded: a chat list needs neither.
func (s *Store) Conversations(ctx context.Context, filter core.ConversationFilter) ([]core.Conversation, error) {
	var (
		conds []string
		args  []any
	)
	if filter.Channel != "" {
		conds = append(conds, "channel = ?")
		args = append(args, string(filter.Channel))
	}
	if filter.Account != "" {
		conds = append(conds, "account = ?")
		args = append(args, filter.Account)
	}
	where := func(extra string) string {
		all := append([]string{extra}, conds...)
		return " WHERE " + strings.Join(all, " AND ")
	}
	limit := ""
	limitArgs := append([]any(nil), args...)
	if filter.Limit > 0 {
		limit = " LIMIT ?"
		limitArgs = append(limitArgs, filter.Limit)
	}

	threaded, err := s.convGroups(ctx, `
		SELECT channel, account, thread, MAX(timestamp), SUM(unread), ''
		FROM items`+where("thread <> ''")+`
		GROUP BY channel, account, thread
		ORDER BY MAX(timestamp) DESC`+limit, limitArgs)
	if err != nil {
		return nil, err
	}
	threadless, err := s.convGroups(ctx, `
		SELECT channel, account, '', timestamp, unread, id
		FROM items`+where("thread = ''")+`
		ORDER BY timestamp DESC, id DESC`+limit, limitArgs)
	if err != nil {
		return nil, err
	}

	groups := append(threaded, threadless...)
	sort.SliceStable(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		if a.newest != b.newest {
			return a.newest > b.newest
		}
		if a.channel != b.channel {
			return a.channel < b.channel
		}
		if a.account != b.account {
			return a.account < b.account
		}
		return a.thread+a.single < b.thread+b.single
	})
	if filter.Limit > 0 && len(groups) > filter.Limit {
		groups = groups[:filter.Limit]
	}

	out := make([]core.Conversation, 0, len(groups))
	for _, g := range groups {
		var row *sql.Row
		if g.single != "" {
			row = s.db.QueryRowContext(ctx, conversationItemSQL+` WHERE id = ?`, g.single)
		} else {
			// Same tie-break as list(): newest timestamp, then highest id.
			row = s.db.QueryRowContext(ctx, conversationItemSQL+`
				WHERE channel = ? AND account = ? AND thread = ? AND timestamp = ?
				ORDER BY id DESC LIMIT 1`, g.channel, g.account, g.thread, g.newest)
		}
		last, err := scanItem(row)
		if err != nil {
			return nil, fmt.Errorf("store: conversations last item of %s/%s/%s: %w", g.channel, g.account, g.thread, err)
		}
		if last.ThreadName == "" && g.single == "" {
			name, err := s.lastThreadName(ctx, g.channel, g.account, g.thread, false)
			if err != nil {
				return nil, err
			}
			last.ThreadName = name
		}
		out = append(out, core.Conversation{Last: last, Unread: g.unread})
	}
	return out, nil
}

// lastThreadName is the newest non-empty thread_name of one thread, or ""
// when it was never named. The chat list titles a row from its newest
// item, and an item sent from bunker may carry no name (rows stored
// before sent items kept it, or a thread whose name was never learned);
// falling back here keeps the contact's name instead of the bare number.
// idx_items_thread walks the thread newest-first, so the scan stops at
// the first named item (no id tie-break: it would add a temp sort).
//
// usable also skips the names core.UsableThreadName rejects (the thread
// id, or just a phone number), for callers that would rather keep what
// they have than take another bare number.
func (s *Store) lastThreadName(ctx context.Context, channel, account, thread string, usable bool) (string, error) {
	query := `
		SELECT thread_name FROM items
		WHERE channel = ? AND account = ? AND thread = ? AND thread_name <> ''`
	if usable {
		// Mirrors core.UsableThreadName: not the thread id, and some
		// character other than a digit, '+', space or '-'.
		query += ` AND thread_name <> thread AND thread_name GLOB '*[^0-9+ -]*'`
	}
	var name string
	err := s.db.QueryRowContext(ctx, query+`
		ORDER BY timestamp DESC LIMIT 1`, channel, account, thread).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: conversations name of %s/%s/%s: %w", channel, account, thread, err)
	}
	return name, nil
}

// convGroups runs one aggregate query and collects its rows (closing the
// cursor before the caller issues more reads, as the pool may hold a
// single connection).
func (s *Store) convGroups(ctx context.Context, query string, args []any) ([]convGroup, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: conversations: %w", err)
	}
	defer rows.Close()
	var groups []convGroup
	for rows.Next() {
		var g convGroup
		if err := rows.Scan(&g.channel, &g.account, &g.thread, &g.newest, &g.unread, &g.single); err != nil {
			return nil, fmt.Errorf("store: conversations scan: %w", err)
		}
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: conversations rows: %w", err)
	}
	return groups, nil
}

const conversationItemSQL = `
	SELECT id, channel, account, thread, thread_name, from_id, from_name,
		to_json, subject, body, attachments_json, unread, from_me, timestamp, meta_json,
		edited, deleted
	FROM items`
