package tui

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/reyer3/bunker-go/internal/core"
)

// sequenceQuits runs a tea.Sequence command step by step and reports
// whether its last step quits. tea.Sequence's message type is not
// exported, so its steps are read by reflection.
func sequenceQuits(t *testing.T, cmd tea.Cmd) bool {
	t.Helper()
	if cmd == nil {
		return false
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); ok {
		return true
	}
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice || v.Len() == 0 {
		return false
	}
	quits := false
	for i := 0; i < v.Len(); i++ {
		step, ok := v.Index(i).Interface().(tea.Cmd)
		if !ok || step == nil {
			continue
		}
		_, quits = step().(tea.QuitMsg)
	}
	return quits
}

func startOpen(t *testing.T, client *inboxClient, id string) Model {
	t.Helper()
	m := NewModel(client, WithOpenItem(id)).withGlyphs(nil)
	m.width, m.height = 60, 20
	if m.polling {
		t.Fatal("an open pane polls the inbox")
	}
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init returned no command")
	}
	next, _ := m.Update(cmd())
	return next.(Model)
}

func TestOpenItemStartsOnChatAndEscQuits(t *testing.T) {
	client := &inboxClient{readResult: core.Item{
		ID: "whatsapp:personal:1", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t1",
		From: core.Address{Name: "Alice"}, Unread: true,
	}}
	m := startOpen(t, client, "whatsapp:personal:1")
	if client.readID != "whatsapp:personal:1" || client.readReceipt {
		t.Fatalf("Read(%q, receipt=%v), want the open id without a receipt", client.readID, client.readReceipt)
	}
	if client.listCalls != 0 {
		t.Fatalf("list calls = %d, want 0", client.listCalls)
	}
	if !m.detail || !m.chatMode || m.chatThread != "t1" {
		t.Fatalf("detail=%v chatMode=%v thread=%q, want the chat of t1", m.detail, m.chatMode, m.chatThread)
	}
	if !strings.Contains(m.View(), "Esc cerrar") {
		t.Fatalf("chat hints do not say Esc closes the pane:\n%s", m.View())
	}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if !next.(Model).chatMode {
		t.Fatal("Esc left the chat for an inbox the pane does not have")
	}
	if !sequenceQuits(t, cmd) {
		t.Fatal("Esc in an open pane's chat did not quit")
	}
	if len(client.keepaliveCalls) == 0 || client.keepaliveCalls[len(client.keepaliveCalls)-1].focused {
		t.Fatalf("keepalive calls = %+v, want a final focused=false before quitting", client.keepaliveCalls)
	}
}

func TestOpenItemStartsOnMailThreadAndQQuits(t *testing.T) {
	client := &inboxClient{readResult: core.Item{
		ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "th",
		Subject: "Reunión", From: core.Address{ID: "alice@example.com"},
	}}
	m := startOpen(t, client, "mail:cl:1")
	if !m.detail || !m.threadMode || m.threadKey != "th" {
		t.Fatalf("detail=%v threadMode=%v key=%q, want the mail thread th", m.detail, m.threadMode, m.threadKey)
	}
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEscape}, {Type: tea.KeyRunes, Runes: []rune("q")}} {
		next, cmd := m.Update(key)
		if !next.(Model).threadMode {
			t.Fatalf("%s left the thread view", key)
		}
		if !sequenceQuits(t, cmd) {
			t.Fatalf("%s in an open pane's thread did not quit", key)
		}
	}
}

func TestOpenItemNotFoundShowsErrorAndQuitsOnQ(t *testing.T) {
	client := &inboxClient{readErr: fmt.Errorf("rpc: read: %w", core.ErrNotFound)}
	m := startOpen(t, client, "mail:cl:gone")
	if m.detail {
		t.Fatal("a missing item opened a view")
	}
	m.width = 0
	view := m.View()
	if !strings.Contains(view, "No se pudo abrir la conversación") || !strings.Contains(view, "ya no existe") {
		t.Fatalf("view = %q, want the not-found error", view)
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if cmd != nil || next.(Model).detail {
		t.Fatal("a key other than q/Esc did something on the error screen")
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if !sequenceQuits(t, cmd) {
		t.Fatal("q on the error screen did not quit")
	}
}

func TestOpenItemShowsLoadingBeforeTheItemArrives(t *testing.T) {
	m := NewModel(&inboxClient{}, WithOpenItem("  mail:cl:1 "))
	if m.openID != "mail:cl:1" {
		t.Fatalf("openID = %q, want it trimmed", m.openID)
	}
	if !strings.Contains(m.View(), "Abriendo conversación") {
		t.Fatalf("view = %q, want the loading line", m.View())
	}
}

func sidebarModel(opts ...Option) Model {
	m := NewModel(nil, append([]Option{WithSidebar()}, opts...)...).withGlyphs(nil)
	m.client = &inboxClient{}
	m.width, m.height = 32, 16
	m.loaded = true
	m.groups = []inboxGroup{{items: []core.Item{{
		ID: "whatsapp:personal:1", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t1",
		From: core.Address{Name: "Alice"}, Unread: true,
	}}}}
	return m
}

func TestSidebarEnterUsesTheExternalOpener(t *testing.T) {
	var opened []string
	m := sidebarModel(WithExternalOpener(func(id string) error {
		opened = append(opened, id)
		return nil
	}))
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if m.detail || m.chatMode {
		t.Fatal("Enter with an external opener opened the chat in place")
	}
	if cmd == nil {
		t.Fatal("Enter returned no command")
	}
	next, _ = m.Update(cmd())
	if len(opened) != 1 || opened[0] != "whatsapp:personal:1" {
		t.Fatalf("opened = %v, want the selected conversation's newest item", opened)
	}
	if _, ok := next.(Model).currentFlash(); ok {
		t.Fatal("a successful open left a flash")
	}
}

func TestSidebarOpenerErrorFlashes(t *testing.T) {
	m := sidebarModel(WithExternalOpener(func(string) error {
		return errors.New("herdr: plugin pane open: no such plugin")
	}))
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	next, _ := m.Update(cmd())
	flash, ok := next.(Model).currentFlash()
	if !ok || flash != "no se pudo abrir: plugin pane open: no such plugin" {
		t.Fatalf("flash = %q (%v), want the opener error without its prefix", flash, ok)
	}
	if !strings.Contains(next.(Model).View(), "no se pudo abrir") {
		t.Fatal("the sidebar does not show the flash")
	}
}

func TestSidebarEnterWithoutOpenerOpensInPlace(t *testing.T) {
	m := sidebarModel(WithExternalOpener(nil))
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if !m.detail || !m.chatMode {
		t.Fatal("Enter without an opener did not open the chat in place")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if m = next.(Model); m.detail || !strings.Contains(m.View(), "Alice") {
		t.Fatal("Esc did not return to the sidebar list")
	}
}

func TestSidebarKeysSwitchChannelsAndHelp(t *testing.T) {
	m := sidebarModel()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	if m = next.(Model); m.activeTab != 2 {
		t.Fatalf("activeTab = %d after 2, want 2", m.activeTab)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m = next.(Model); m.activeTab != 3 {
		t.Fatalf("activeTab = %d after Tab, want 3", m.activeTab)
	}
	if strings.Contains(m.View(), "Alice") {
		t.Fatal("the Matrix tab lists a WhatsApp conversation")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	if m = next.(Model); !m.helpOpen || m.helpCtx != "sidebar" {
		t.Fatalf("help open=%v ctx=%q, want the sidebar section", m.helpOpen, m.helpCtx)
	}
}

func TestSidebarLinesFitTheWidthAndMatchHits(t *testing.T) {
	m := sidebarModel()
	m.groups[0].items[0].From.Name = "Un nombre de contacto muy largo que no cabe"
	lines, hits := m.sidebarLinesAndHits()
	if len(lines) != len(hits) {
		t.Fatalf("%d lines but %d hits", len(lines), len(hits))
	}
	for i, line := range lines {
		if w := lipgloss.Width(line); w > m.width {
			t.Fatalf("line %d is %d cells wide, want <= %d: %q", i, w, m.width, line)
		}
	}
}
