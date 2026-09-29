package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/reyer3/bunker-go/internal/tui"
)

// herdr side-panel integration (issue #82): "a" asks a Claude Code agent
// running in herdr about a conversation, the sidebar publishes its unread
// total as pane metadata herdr's agent sidebar can show, and new messages
// can become herdr notifications. All of it goes through the herdr CLI
// (herdrRunner), like "bunker herdr toggle".

const (
	// herdrClaudeKind is the "agent" label herdr gives Claude Code.
	herdrClaudeKind = "claude"
	// herdrMetadataSource names bunker's pane metadata reports. herdr
	// allows [A-Za-z0-9:._-], at most 80 characters.
	herdrMetadataSource = "bunker"
	// herdrUnreadToken is the metadata token the unread total goes in; a
	// herdr agent sidebar row shows it as "$unread".
	herdrUnreadToken = "unread"
	// herdrUnreadTTL makes the reported count expire on its own when the
	// sidebar stops (closed pane, crash): two polls without a report.
	herdrUnreadTTL = 2 * tui.PollInterval
	// herdrReportTimeout bounds one metadata or notification call: they
	// run after every poll and must not pile up behind a wedged herdr.
	herdrReportTimeout = 3 * time.Second
	// herdrNotifyTitle is every notification's title; the body names the
	// sender.
	herdrNotifyTitle = "bunker"
)

// errNoClaudeAgent is what "a" says when herdr runs no Claude Code.
var errNoClaudeAgent = errors.New("herdr: no hay ningún Claude Code en herdr · inícialo en un panel de herdr y vuelve a pulsar a")

// herdrAgent is the part of an "agent list" entry bunker uses; herdr
// sends more fields, which are ignored.
type herdrAgent struct {
	PaneID string `json:"pane_id"`
	Name   string `json:"name"`
	Agent  string `json:"agent"`
	Status string `json:"agent_status"`
}

// parseHerdrAgents reads "herdr agent list". herdr's schema (v0.8) is
// {"result":{"type":"agent_list","agents":[...]}}; a bare "result" array
// is read too, so a differently shaped response still works rather than
// reading as "no agents".
func parseHerdrAgents(out []byte) ([]herdrAgent, error) {
	if msg, ok := herdrErrorMessage(out); ok {
		return nil, fmt.Errorf("herdr: agent list: %s", msg)
	}
	var resp struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &resp); err != nil {
		return nil, fmt.Errorf("herdr: agent list: decode response: %w", err)
	}
	var list []herdrAgent
	if err := json.Unmarshal(resp.Result, &list); err == nil {
		return list, nil
	}
	var obj struct {
		Type   string       `json:"type"`
		Agents []herdrAgent `json:"agents"`
	}
	if err := json.Unmarshal(resp.Result, &obj); err != nil {
		return nil, fmt.Errorf("herdr: agent list: decode response: %w", err)
	}
	if obj.Type != "" && obj.Type != "agent_list" {
		return nil, fmt.Errorf("herdr: agent list: unexpected result type %q", obj.Type)
	}
	return obj.Agents, nil
}

// pickClaudeAgent chooses which Claude Code gets the question: the first
// (in herdr's order) that is idle or done, i.e. ready for input, so a
// question does not land in the middle of another task; else the first
// one, whatever it is doing (herdr queues a prompt to a working agent,
// and a blocked one is reported as such).
func pickClaudeAgent(agents []herdrAgent) (herdrAgent, error) {
	var claude []herdrAgent
	for _, a := range agents {
		if strings.EqualFold(strings.TrimSpace(a.Agent), herdrClaudeKind) {
			claude = append(claude, a)
		}
	}
	if len(claude) == 0 {
		return herdrAgent{}, errNoClaudeAgent
	}
	for _, a := range claude {
		if a.Status == "idle" || a.Status == "done" {
			return a, nil
		}
	}
	return claude[0], nil
}

// herdrClaudePrompt is what Claude is asked. It names the item and the
// bunker MCP tools that read it, and asks for a reply only as a plan, so
// nothing is sent without the user.
func herdrClaudePrompt(itemID string) string {
	return "Usa bunker mcp: lee la conversación " + itemID +
		" (herramienta read o thread) y dime qué necesito saber; si hay que responder, propón una respuesta como plan sin enviarla."
}

// isHerdrAgentBlocked reports whether err is herdr refusing a prompt
// because the agent waits for an answer (error code agent_blocked).
func isHerdrAgentBlocked(err error) bool {
	return err != nil && strings.Contains(err.Error(), "agent_blocked")
}

// askHerdrClaude prompts a Claude Code agent about itemID and focuses it,
// so the answer is in view. A blocked agent is focused without a prompt
// (herdr would refuse it) and reported as tui.ErrAgentBlocked.
func askHerdrClaude(ctx context.Context, run herdrRunner, itemID string) error {
	if err := validItemID(itemID); err != nil {
		return fmt.Errorf("herdr: ask: %w", err)
	}
	out, err := run(ctx, "agent", "list")
	if err != nil {
		return err
	}
	agents, err := parseHerdrAgents(out)
	if err != nil {
		return err
	}
	agent, err := pickClaudeAgent(agents)
	if err != nil {
		return err
	}
	// The pane id, not the name, is the target: it is always unique,
	// and it is checked before going into an argv.
	if err := validHerdrPaneID(agent.PaneID); err != nil {
		return fmt.Errorf("herdr: claude agent: %w", err)
	}
	if agent.Status == "blocked" {
		// Focusing it shows the question it waits on; a failure there
		// matters less than saying why nothing was asked.
		_, _ = run(ctx, "agent", "focus", agent.PaneID)
		return fmt.Errorf("herdr: agent prompt %s: %w", agent.PaneID, tui.ErrAgentBlocked)
	}
	if _, err := run(ctx, "agent", "prompt", agent.PaneID, herdrClaudePrompt(itemID)); err != nil {
		if isHerdrAgentBlocked(err) {
			_, _ = run(ctx, "agent", "focus", agent.PaneID)
			return fmt.Errorf("%w: %w", err, tui.ErrAgentBlocked)
		}
		return err
	}
	_, err = run(ctx, "agent", "focus", agent.PaneID)
	return err
}

// herdrAgentAsker is what "a" does in "bunker sidebar" and "bunker open":
// inside herdr (HERDR_ENV=1) it asks Claude; anywhere else it is nil and
// the key and its hints stay out.
func herdrAgentAsker(getenv func(string) string, run herdrRunner) func(ctx context.Context, itemID string) error {
	if run == nil || getenv("HERDR_ENV") != "1" {
		return nil
	}
	return func(ctx context.Context, itemID string) error {
		return askHerdrClaude(ctx, run, itemID)
	}
}

// herdrUnreadReporter publishes the sidebar's unread total on its own
// pane. It reports when the count changes, and otherwise only once half
// the TTL has gone by, so the token neither expires while bunker runs
// nor costs a herdr process on every refresh ("g" polls at once).
type herdrUnreadReporter struct {
	run  herdrRunner
	pane string
	ttl  time.Duration
	now  func() time.Time

	mu       sync.Mutex
	reported bool
	last     int
	lastAt   time.Time
}

// herdrUnreadCommand is the argv that sets the token.
func herdrUnreadCommand(pane string, n int, ttl time.Duration) []string {
	return []string{"pane", "report-metadata", pane, "--source", herdrMetadataSource,
		"--token", herdrUnreadToken + "=" + strconv.Itoa(n), "--ttl-ms", strconv.FormatInt(ttl.Milliseconds(), 10)}
}

func (r *herdrUnreadReporter) report(n int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if r.reported && n == r.last && now.Sub(r.lastAt) < r.ttl/2 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), herdrReportTimeout)
	defer cancel()
	if _, err := r.run(ctx, herdrUnreadCommand(r.pane, n, r.ttl)...); err != nil {
		// Not recorded as reported, so the next poll retries.
		return err
	}
	r.reported, r.last, r.lastAt = true, n, now
	return nil
}

// herdrUnreadReporterFor is the sidebar's unread reporter: only inside
// herdr, and only with a HERDR_PANE_ID fit for an argv.
func herdrUnreadReporterFor(getenv func(string) string, run herdrRunner) func(int) error {
	if run == nil || getenv("HERDR_ENV") != "1" || validHerdrPaneID(getenv("HERDR_PANE_ID")) != nil {
		return nil
	}
	r := &herdrUnreadReporter{run: run, pane: getenv("HERDR_PANE_ID"), ttl: herdrUnreadTTL, now: time.Now}
	return r.report
}

// herdrNotifyCommand is the argv of one notification. herdr's parser only
// knows "--body" followed by a separate value (it rejects "--body=…") and
// takes that next argument verbatim, so a body starting with "-" is safe.
func herdrNotifyCommand(body string) []string {
	return []string{"notification", "show", herdrNotifyTitle, "--body", body, "--sound", "request"}
}

// herdrMessageNotifier shows new-message notifications in herdr, when
// enabled ([herdr] notify = true) and inside herdr; else nil. The TUI
// decides when (and how often) to call it.
func herdrMessageNotifier(getenv func(string) string, run herdrRunner, enabled bool) func(string) error {
	if !enabled || run == nil || getenv("HERDR_ENV") != "1" {
		return nil
	}
	return func(body string) error {
		ctx, cancel := context.WithTimeout(context.Background(), herdrReportTimeout)
		defer cancel()
		_, err := run(ctx, herdrNotifyCommand(body)...)
		return err
	}
}
