package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// "bunker herdr toggle" (issue #80) docks bunker as a left side panel in
// herdr, a terminal workspace manager for coding agents. bunker ships a
// herdr plugin (deploy/herdr) whose "sidebar" pane runs "bunker sidebar"; this
// command is that plugin's "toggle" action. It drives herdr only through
// its CLI, which prints the raw JSON response on stdout (exit 0) or a JSON
// object with an "error" key on stderr (exit 1).
//
// Known limitation: the panel docks beside the focused pane, so in a tab
// that already has several panes it is as tall as that pane, not a
// full-height column at the tab's left edge. That is a follow-up.

const (
	// herdrPluginID and herdrSidebarEntrypoint match
	// deploy/herdr/herdr-plugin.toml.
	herdrPluginID          = "bunker"
	herdrSidebarEntrypoint = "sidebar"
	herdrOpenEntrypoint    = "open"
	// herdrPaneLabel is how toggle finds its panel again: herdr has no
	// "which plugin owns this pane" query, so the pane is renamed to it.
	herdrPaneLabel = "bunker"
	// herdrSidebarRatio is the panel's share of the split. A split opens
	// 50/50, so moving the left pane's edge left by (0.5 - ratio) leaves
	// it that wide.
	herdrSidebarRatio = 0.25
	// herdrNewPane stands in for the pane id "plugin pane open" returns,
	// in a dry-run plan.
	herdrNewPane = "<new-pane>"
	// herdrTimeout bounds the whole toggle: it is bound to a key, and a
	// wedged herdr server must not leave the action hanging.
	herdrTimeout = 10 * time.Second
)

// herdrRunner runs "herdr <args...>" and returns its stdout, or an error
// carrying herdr's own message. Tests swap it for a fake.
type herdrRunner func(ctx context.Context, args ...string) ([]byte, error)

// herdrDeps are what cmdHerdr needs from the system.
type herdrDeps struct {
	run    herdrRunner
	getenv func(string) string
}

func defaultHerdrDeps() herdrDeps {
	return herdrDeps{run: execHerdrRunner(os.Getenv, exec.LookPath), getenv: os.Getenv}
}

// errHerdrNotFound is returned when no herdr binary can be located.
var errHerdrNotFound = errors.New("herdr: herdr not found (set HERDR_BIN_PATH or put herdr on PATH)")

// herdrBinary picks the herdr binary: HERDR_BIN_PATH, which herdr sets for
// plugin commands, so the action talks to the herdr that launched it even
// when several are installed; else herdr on PATH.
func herdrBinary(getenv func(string) string, lookPath func(string) (string, error)) (string, error) {
	if bin := getenv("HERDR_BIN_PATH"); bin != "" {
		return bin, nil
	}
	bin, err := lookPath("herdr")
	if err != nil {
		return "", errHerdrNotFound
	}
	return bin, nil
}

// execHerdrRunner runs the real herdr binary.
func execHerdrRunner(getenv func(string) string, lookPath func(string) (string, error)) herdrRunner {
	return func(ctx context.Context, args ...string) ([]byte, error) {
		bin, err := herdrBinary(getenv, lookPath)
		if err != nil {
			return nil, err
		}
		var stdout, stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		runErr := cmd.Run()
		if errors.Is(runErr, exec.ErrNotFound) || errors.Is(runErr, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %v", errHerdrNotFound, runErr)
		}
		return herdrResult(args, stdout.Bytes(), stderr.Bytes(), runErr)
	}
}

// herdrResult turns one herdr run into its stdout or an error, preferring
// herdr's JSON error message over a bare exit status.
func herdrResult(args []string, stdout, stderr []byte, runErr error) ([]byte, error) {
	if runErr == nil {
		if msg, ok := herdrErrorMessage(stdout); ok {
			return nil, fmt.Errorf("herdr: %s: %s", herdrVerb(args), msg)
		}
		return stdout, nil
	}
	if msg, ok := herdrErrorMessage(stderr); ok {
		return nil, fmt.Errorf("herdr: %s: %s", herdrVerb(args), msg)
	}
	if s := strings.TrimSpace(string(stderr)); s != "" {
		return nil, fmt.Errorf("herdr: %s: %v: %s", herdrVerb(args), runErr, s)
	}
	return nil, fmt.Errorf("herdr: %s: %w", herdrVerb(args), runErr)
}

// herdrErrorMessage extracts the "error" of a herdr JSON response. The
// error is a string or an object with a message (and usually a code), so
// both are read; anything else is shown raw rather than dropped.
func herdrErrorMessage(out []byte) (string, bool) {
	var resp struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &resp); err != nil || len(resp.Error) == 0 || string(resp.Error) == "null" {
		return "", false
	}
	var s string
	if json.Unmarshal(resp.Error, &s) == nil {
		return s, true
	}
	var obj struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(resp.Error, &obj) == nil && obj.Message != "" {
		if obj.Code != "" {
			return obj.Message + " (" + obj.Code + ")", true
		}
		return obj.Message, true
	}
	return string(resp.Error), true
}

// herdrVerb names a herdr command for error messages ("pane close"),
// without the ids and flags that follow it: herdr's subcommands are
// lowercase words, and its ids ("w1:p1") and flags are not.
func herdrVerb(args []string) string {
	var verb []string
	for _, a := range args {
		if a == "" || strings.Trim(a, "abcdefghijklmnopqrstuvwxyz") != "" {
			break
		}
		verb = append(verb, a)
	}
	return strings.Join(verb, " ")
}

// herdrPane is the part of a "pane list" entry toggle uses.
type herdrPane struct {
	PaneID      string `json:"pane_id"`
	WorkspaceID string `json:"workspace_id"`
	TabID       string `json:"tab_id"`
	Focused     bool   `json:"focused"`
	Label       string `json:"label"`
}

func parseHerdrPanes(out []byte) ([]herdrPane, error) {
	var resp struct {
		Result struct {
			Type  string      `json:"type"`
			Panes []herdrPane `json:"panes"`
		} `json:"result"`
	}
	// herdrResult already turns an "error" response into an error; this
	// repeats it so a runner that does not (a test fake) cannot make an
	// error look like an empty tab.
	if msg, ok := herdrErrorMessage(out); ok {
		return nil, fmt.Errorf("herdr: pane list: %s", msg)
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("herdr: pane list: decode response: %w", err)
	}
	if resp.Result.Type != "" && resp.Result.Type != "pane_list" {
		return nil, fmt.Errorf("herdr: pane list: unexpected result type %q", resp.Result.Type)
	}
	return resp.Result.Panes, nil
}

func parseHerdrOpenedPane(out []byte) (string, error) {
	var resp struct {
		Result struct {
			PluginPane struct {
				Pane struct {
					PaneID string `json:"pane_id"`
				} `json:"pane"`
			} `json:"plugin_pane"`
		} `json:"result"`
	}
	if msg, ok := herdrErrorMessage(out); ok {
		return "", fmt.Errorf("herdr: plugin pane open: %s", msg)
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return "", fmt.Errorf("herdr: plugin pane open: decode response: %w", err)
	}
	id := resp.Result.PluginPane.Pane.PaneID
	if err := validHerdrPaneID(id); err != nil {
		return "", fmt.Errorf("herdr: plugin pane open: %w", err)
	}
	return id, nil
}

// validHerdrPaneID rejects an id that could be read as a flag or is not
// shaped like a herdr id ("w1:p1") before it goes into an argv: the ids
// come from herdr's output, but a label-matched pane is only as
// trustworthy as whatever renamed it.
func validHerdrPaneID(id string) error {
	if id == "" {
		return errors.New("empty pane id")
	}
	if strings.HasPrefix(id, "-") || len(id) > 128 {
		return fmt.Errorf("invalid pane id %q", id)
	}
	for _, r := range id {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == ':' || r == '_' || r == '-'
		if !ok {
			return fmt.Errorf("invalid pane id %q", id)
		}
	}
	return nil
}

// herdrOpenItemCommand opens one conversation in a new plugin pane to
// the right of the focused one (the sidebar that asked for it) and
// focuses it. The id reaches "bunker open" as BUNKER_OPEN_ID because
// herdr runs a manifest pane's command as fixed argv.
func herdrOpenItemCommand(id string) []string {
	return []string{"plugin", "pane", "open", "--plugin", herdrPluginID, "--entrypoint", herdrOpenEntrypoint,
		"--placement", "split", "--direction", "right", "--env", openIDEnv + "=" + id, "--focus"}
}

// herdrItemOpener is what Enter does in "bunker sidebar": inside herdr
// (HERDR_ENV=1) it opens the conversation in a herdr pane; anywhere else
// it returns nil and the sidebar opens it in place.
func herdrItemOpener(getenv func(string) string, run herdrRunner) func(id string) error {
	if run == nil || getenv("HERDR_ENV") != "1" {
		return nil
	}
	return func(id string) error {
		if err := validItemID(id); err != nil {
			return fmt.Errorf("herdr: open: %w", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), herdrTimeout)
		defer cancel()
		_, err := run(ctx, herdrOpenItemCommand(id)...)
		return err
	}
}

// herdrToggle is what toggle decided and ran (or, in a dry run, would
// run). It is also the --json output.
type herdrToggle struct {
	Action   string     `json:"action"` // open, focus or close
	Tab      string     `json:"tab"`
	Pane     string     `json:"pane,omitempty"`
	Anchor   string     `json:"anchor,omitempty"` // open: the pane it docks beside
	DryRun   bool       `json:"dryRun"`
	Commands [][]string `json:"commands"`
}

// planHerdrToggle decides what toggle does from a pane list.
func planHerdrToggle(panes []herdrPane, envTab, envPane string) (herdrToggle, error) {
	var focused *herdrPane
	for i := range panes {
		if panes[i].Focused {
			focused = &panes[i]
			break
		}
	}
	tab := envTab
	if focused != nil {
		tab = focused.TabID
	}
	if tab == "" {
		return herdrToggle{}, errors.New("herdr: no focused pane and HERDR_TAB_ID is not set")
	}

	var inTab []herdrPane
	for _, p := range panes {
		if p.TabID == tab {
			inTab = append(inTab, p)
		}
	}
	for _, p := range inTab {
		if p.Label != herdrPaneLabel {
			continue
		}
		if err := validHerdrPaneID(p.PaneID); err != nil {
			return herdrToggle{}, fmt.Errorf("herdr: bunker pane: %w", err)
		}
		if p.Focused {
			return herdrToggle{Action: "close", Tab: tab, Pane: p.PaneID,
				Commands: [][]string{{"pane", "close", p.PaneID}}}, nil
		}
		return herdrToggle{Action: "focus", Tab: tab, Pane: p.PaneID,
			Commands: [][]string{{"plugin", "pane", "focus", p.PaneID}}}, nil
	}

	// No panel yet: dock it beside the focused pane, or, when herdr
	// reports none focused, the pane that ran the action, else the
	// tab's first pane.
	anchor := focused
	for i := range inTab {
		if anchor == nil && inTab[i].PaneID == envPane {
			anchor = &inTab[i]
		}
	}
	if anchor == nil && len(inTab) > 0 {
		anchor = &inTab[0]
	}
	if anchor == nil {
		return herdrToggle{}, fmt.Errorf("herdr: tab %q has no pane to dock beside", tab)
	}
	if err := validHerdrPaneID(anchor.PaneID); err != nil {
		return herdrToggle{}, fmt.Errorf("herdr: focused pane: %w", err)
	}
	return herdrToggle{Action: "open", Tab: tab, Anchor: anchor.PaneID, Commands: herdrOpenCommands(anchor.PaneID, herdrNewPane)}, nil
}

// herdrOpenCommands opens the plugin pane as a split right of anchor
// without focus, swaps it into anchor's place so it sits on the left,
// narrows it, labels it so the next toggle finds it, then focuses it.
// Every command after the first acts on newPane.
func herdrOpenCommands(anchor, newPane string) [][]string {
	amount := strconv.FormatFloat(0.5-herdrSidebarRatio, 'f', -1, 64)
	return [][]string{
		{"plugin", "pane", "open", "--plugin", herdrPluginID, "--entrypoint", herdrSidebarEntrypoint,
			"--placement", "split", "--target-pane", anchor, "--direction", "right", "--no-focus"},
		{"pane", "swap", "--source-pane", newPane, "--target-pane", anchor},
		{"pane", "resize", "--direction", "left", "--amount", amount, "--pane", newPane},
		{"pane", "rename", newPane, herdrPaneLabel},
		{"plugin", "pane", "focus", newPane},
	}
}

// runHerdrToggle lists the panes, decides, and unless dryRun runs the
// plan. Only "pane list", which is read-only, runs in a dry run.
func runHerdrToggle(ctx context.Context, deps herdrDeps, dryRun bool) (herdrToggle, error) {
	out, err := deps.run(ctx, "pane", "list")
	if err != nil {
		if !errors.Is(err, errHerdrNotFound) && deps.getenv("HERDR_ENV") != "1" {
			return herdrToggle{}, fmt.Errorf("herdr: not inside herdr and no herdr server answered: %w", err)
		}
		return herdrToggle{}, err
	}
	panes, err := parseHerdrPanes(out)
	if err != nil {
		return herdrToggle{}, err
	}
	plan, err := planHerdrToggle(panes, deps.getenv("HERDR_TAB_ID"), deps.getenv("HERDR_PANE_ID"))
	if err != nil {
		return herdrToggle{}, err
	}
	plan.DryRun = dryRun
	if dryRun {
		return plan, nil
	}
	if plan.Action != "open" {
		_, err := deps.run(ctx, plan.Commands[0]...)
		return plan, err
	}

	out, err = deps.run(ctx, plan.Commands[0]...)
	if err != nil {
		return plan, err
	}
	newPane, err := parseHerdrOpenedPane(out)
	if err != nil {
		return plan, err
	}
	plan.Pane = newPane
	plan.Commands = herdrOpenCommands(plan.Anchor, newPane)
	for _, argv := range plan.Commands[1:] {
		if _, err := deps.run(ctx, argv...); err != nil {
			// An unlabelled pane would not be found by the next toggle,
			// which would open a second one, so undo the open.
			if _, cerr := deps.run(ctx, "pane", "close", newPane); cerr != nil {
				return plan, fmt.Errorf("%w (and closing the new pane %s failed: %v)", err, newPane, cerr)
			}
			return plan, err
		}
	}
	return plan, nil
}

const herdrUsage = "usage: bunker herdr toggle [--dry-run] [--json]"

func cmdHerdr(ctx context.Context, args []string, stdout, stderr io.Writer, deps herdrDeps) int {
	if len(args) == 0 || args[0] != "toggle" {
		fmt.Fprintln(stderr, herdrUsage)
		return 2
	}
	flags := flag.NewFlagSet("herdr toggle", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dryRun := flags.Bool("dry-run", false, "print the herdr commands instead of running them")
	jsonOut := flags.Bool("json", false, "JSON output")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintln(stderr, herdrUsage)
		return 2
	}

	ctx, cancel := context.WithTimeout(ctx, herdrTimeout)
	defer cancel()
	plan, err := runHerdrToggle(ctx, deps, *dryRun)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	switch {
	case *jsonOut:
		writeJSON(stdout, plan)
	case *dryRun:
		for _, argv := range plan.Commands {
			fmt.Fprintln(stdout, "herdr "+strings.Join(argv, " "))
		}
	default:
		fmt.Fprintf(stdout, "%s %s\n", plan.Action, plan.Pane)
	}
	return 0
}
