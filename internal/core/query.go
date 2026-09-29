package core

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ErrInvalidQuery marks a query-language string ParseQuery rejected: an
// unknown operator, a missing value, a malformed date or an unterminated
// quote. Callers match it with errors.Is to tell a user typo apart from a
// store failure.
var ErrInvalidQuery = errors.New("invalid query")

// QueryField names what one QueryTerm matches against.
type QueryField string

// The fields a query can address. QueryText is free text, matched against
// every indexed column the way Filter.Query is; the rest are operators.
const (
	QueryText    QueryField = "text"
	QueryFrom    QueryField = "from"
	QueryTo      QueryField = "to"
	QuerySubject QueryField = "subject"
	QueryIs      QueryField = "is"
	QueryHas     QueryField = "has"
	QueryIn      QueryField = "in"
	QueryChannel QueryField = "channel"
	QueryAccount QueryField = "account"
	QueryLabel   QueryField = "label"
	QueryBefore  QueryField = "before"
	QueryAfter   QueryField = "after"
)

// queryOperators is every operator ParseQuery accepts, in the order its
// "unknown operator" error lists them.
var queryOperators = []QueryField{
	QueryFrom, QueryTo, QuerySubject, QueryIs, QueryHas, QueryIn,
	QueryChannel, QueryAccount, QueryLabel, QueryBefore, QueryAfter,
}

// QueryTerm is one condition of a Query. Every term of a Query must hold
// (they are ANDed); Negate inverts this one.
//
// Value is normalized by ParseQuery: QueryIs holds "unread" or "read",
// QueryHas holds "attachment", QueryChannel a known channel name. Phrase
// reports that Value came from a quoted string, so text fields match it
// as an exact phrase instead of word by word with a prefix. Time is set
// only for QueryBefore and QueryAfter, already resolved to an instant.
//
// The shape is flat on purpose: it crosses the RPC boundary and the MCP
// schema generator, which cannot describe a recursive type.
type QueryTerm struct {
	Field  QueryField `json:"field"`
	Value  string     `json:"value,omitempty"`
	Phrase bool       `json:"phrase,omitempty"`
	Negate bool       `json:"negate,omitempty"`
	Time   time.Time  `json:"time,omitzero"`
}

// Query is a parsed query-language string (see ParseQuery): a
// conjunction of terms. The zero Query matches everything.
type Query struct {
	Terms []QueryTerm `json:"terms,omitempty"`
}

// queryDateLayout is the absolute date format of before:/after:, the same
// YYYY-MM-DD the CLI's --since/--before flags take.
const queryDateLayout = "2006-01-02"

// ParseQuery parses s with the current time as the anchor of relative
// dates. See ParseQueryAt.
func ParseQuery(s string) (Query, error) {
	return ParseQueryAt(s, time.Now())
}

// ParseQueryAt parses the query language that `bunker list --query`,
// `bunker find` and the MCP search tool share:
//
//	from:x to:x subject:x    sender, recipients, subject (words, prefix-matched)
//	is:unread is:read        read state
//	has:attachment           at least one attachment
//	in:<folder>              mail folder (Meta["folder"]), ignoring case
//	channel:mail|whatsapp|matrix
//	account:x label:x
//	before:D after:D         D is YYYY-MM-DD or a relative 7d, 2w, 3m
//	"a phrase"  from:"Ana María"
//	-word -from:x            negate a word or an operator
//
// Anything else is free text, matched like Filter.Query. now anchors the
// relative dates and gives absolute ones their time zone, so tests (and a
// daemon in another zone than its user) get a deterministic result.
//
// An unknown operator is an error naming it rather than being searched
// for as text: silently treating a typo like "form:ana" as a word would
// return a plausible but wrong result. Only a letters-only prefix before
// ':' counts as an operator, so "10:30" is plain text; quote anything
// else with a colon ("http://x") to search for it literally.
func ParseQueryAt(s string, now time.Time) (Query, error) {
	var q Query
	p := queryParser{src: s}
	for {
		p.skipSpace()
		if p.done() {
			return q, nil
		}
		term, err := p.term(now)
		if err != nil {
			return Query{}, err
		}
		q.Terms = append(q.Terms, term)
	}
}

type queryParser struct {
	src string
	pos int
}

func (p *queryParser) done() bool { return p.pos >= len(p.src) }

func (p *queryParser) peek() rune {
	r, _ := utf8.DecodeRuneInString(p.src[p.pos:])
	return r
}

func (p *queryParser) skipSpace() {
	for !p.done() {
		r, size := utf8.DecodeRuneInString(p.src[p.pos:])
		if !unicode.IsSpace(r) {
			return
		}
		p.pos += size
	}
}

// quoted reads a "..." string starting at the opening quote. There is no
// escape syntax: a query has no need to search for a literal quote inside
// a phrase, and the store matches the phrase as text either way.
func (p *queryParser) quoted() (string, error) {
	start := p.pos
	p.pos++ // opening quote
	end := strings.IndexByte(p.src[p.pos:], '"')
	if end < 0 {
		return "", fmt.Errorf("core: query: unterminated quote at %q: %w", p.src[start:], ErrInvalidQuery)
	}
	v := p.src[p.pos : p.pos+end]
	p.pos += end + 1
	return v, nil
}

// word reads up to the next space.
func (p *queryParser) word() string {
	start := p.pos
	for !p.done() {
		r, size := utf8.DecodeRuneInString(p.src[p.pos:])
		if unicode.IsSpace(r) {
			break
		}
		p.pos += size
	}
	return p.src[start:p.pos]
}

func (p *queryParser) term(now time.Time) (QueryTerm, error) {
	negate := false
	// A "-" only negates when something follows it directly; a lone "-"
	// is text, as it is for Filter.Query.
	if p.peek() == '-' && p.pos+1 < len(p.src) {
		if r, _ := utf8.DecodeRuneInString(p.src[p.pos+1:]); !unicode.IsSpace(r) {
			negate = true
			p.pos++
		}
	}
	if p.peek() == '"' {
		v, err := p.quoted()
		if err != nil {
			return QueryTerm{}, err
		}
		return QueryTerm{Field: QueryText, Value: v, Phrase: true, Negate: negate}, nil
	}

	start := p.pos
	for !p.done() {
		r, size := utf8.DecodeRuneInString(p.src[p.pos:])
		if r == ':' && p.pos > start {
			name := p.src[start:p.pos]
			p.pos += size
			return p.operator(name, negate, now)
		}
		if r > unicode.MaxASCII || !unicode.IsLetter(r) {
			break
		}
		p.pos += size
	}
	p.pos = start
	return QueryTerm{Field: QueryText, Value: p.word(), Negate: negate}, nil
}

// operator reads name's value (a quoted string or a word) and validates
// it for that operator.
func (p *queryParser) operator(name string, negate bool, now time.Time) (QueryTerm, error) {
	field := QueryField(strings.ToLower(name))
	known := false
	for _, op := range queryOperators {
		if op == field {
			known = true
			break
		}
	}
	if !known {
		names := make([]string, len(queryOperators))
		for i, op := range queryOperators {
			names[i] = string(op) + ":"
		}
		return QueryTerm{}, fmt.Errorf("core: query: unknown operator %q (want one of %s; quote the text to search for it literally): %w",
			name+":", strings.Join(names, " "), ErrInvalidQuery)
	}

	var (
		value  string
		phrase bool
	)
	if !p.done() && p.peek() == '"' {
		v, err := p.quoted()
		if err != nil {
			return QueryTerm{}, err
		}
		value, phrase = v, true
	} else {
		value = p.word()
	}
	if strings.TrimSpace(value) == "" {
		return QueryTerm{}, fmt.Errorf("core: query: %s needs a value: %w", name+":", ErrInvalidQuery)
	}

	term := QueryTerm{Field: field, Value: value, Phrase: phrase, Negate: negate}
	switch field {
	case QueryIs:
		switch v := strings.ToLower(value); v {
		case "unread", "read":
			term.Value, term.Phrase = v, false
		default:
			return QueryTerm{}, fmt.Errorf("core: query: is:%s: want is:unread or is:read: %w", value, ErrInvalidQuery)
		}
	case QueryHas:
		if v := strings.ToLower(value); v != "attachment" && v != "attachments" {
			return QueryTerm{}, fmt.Errorf("core: query: has:%s: want has:attachment: %w", value, ErrInvalidQuery)
		}
		term.Value, term.Phrase = "attachment", false
	case QueryChannel:
		switch ch := Channel(strings.ToLower(value)); ch {
		case ChannelMail, ChannelWhatsApp, ChannelMatrix:
			term.Value, term.Phrase = string(ch), false
		default:
			return QueryTerm{}, fmt.Errorf("core: query: channel:%s: want mail, whatsapp or matrix: %w", value, ErrInvalidQuery)
		}
	case QueryBefore, QueryAfter:
		t, err := ParseQueryDate(value, now)
		if err != nil {
			return QueryTerm{}, fmt.Errorf("core: query: %s%s: %w", name+":", value, err)
		}
		term.Time, term.Phrase = t, false
	}
	return term, nil
}

// ParseQueryDate resolves a before:/after: value: an absolute YYYY-MM-DD
// (the start of that day in now's time zone) or a relative Nd, Nw or Nm
// (N days, weeks or calendar months before now). It is exported so other
// entry points that take a date (the MCP backfill and search_remote
// tools) accept exactly the same forms.
func ParseQueryDate(value string, now time.Time) (time.Time, error) {
	if t, err := time.ParseInLocation(queryDateLayout, value, now.Location()); err == nil {
		return t, nil
	}
	if n := len(value); n >= 2 {
		count, err := strconv.Atoi(value[:n-1])
		// The cap keeps AddDate far from overflow; nobody's mail predates
		// it by more than a few decades anyway.
		if err == nil && count >= 0 && count <= 100000 && value[0] != '+' {
			switch value[n-1] {
			case 'd':
				return now.AddDate(0, 0, -count), nil
			case 'w':
				return now.AddDate(0, 0, -7*count), nil
			case 'm':
				return now.AddDate(0, -count, 0), nil
			}
		}
	}
	return time.Time{}, fmt.Errorf("want YYYY-MM-DD or a relative 7d, 2w or 3m: %w", ErrInvalidQuery)
}
