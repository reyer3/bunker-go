package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
)

func TestPollRefreshKeepsOneQueryInFlightAndDiscardsSupersededResult(t *testing.T) {
	client := &inboxClient{items: []core.Item{item("first", core.ChannelMail, "a", "", time.Time{})}}
	model := NewModel(client)
	initial := model.Init()
	if initial == nil {
		t.Fatal("initial poll missing")
	}
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	model = updated.(Model)
	if cmd != nil || client.listCalls != 0 {
		t.Fatal("refresh launched a second query while initial query is in flight")
	}
	updated, cmd = model.Update(initial())
	model = updated.(Model)
	if len(model.groups) != 0 || cmd == nil {
		t.Fatal("superseded initial result was applied or queued refresh was lost")
	}
	client.items = []core.Item{item("second", core.ChannelMail, "a", "", time.Time{})}
	updated, _ = model.Update(cmd())
	model = updated.(Model)
	if len(model.groups) != 1 || model.groups[0].items[0].ID != "second" || client.listCalls != 2 {
		t.Fatalf("fresh poll groups=%+v list calls=%d", model.groups, client.listCalls)
	}
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if cmd == nil || !updated.(Model).polling {
		t.Fatal("idle refresh did not start a poll")
	}
	updated, duplicate := updated.(Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if duplicate != nil || !updated.(Model).refreshPending {
		t.Fatal("second refresh bypassed single-flight guard")
	}
}

func TestPollTickErrorsAndReadState(t *testing.T) {
	client := &inboxClient{items: []core.Item{item("first", core.ChannelMail, "a", "", time.Time{})}}
	model := NewModel(client)
	updated, timer := model.Update(model.Init()())
	model = updated.(Model)
	if timer == nil {
		t.Fatal("completed poll did not schedule next tick")
	}
	model.detail = true
	model.readItem = core.Item{ID: "first", Body: "draft-safe detail"}
	client.listErr = errors.New("offline")
	updated, poll := model.Update(pollTickMsg{token: model.pollToken})
	model = updated.(Model)
	if poll == nil || !model.polling {
		t.Fatal("poll tick did not start next load")
	}
	updated, _ = model.Update(poll())
	model = updated.(Model)
	if len(model.groups) != 1 || model.groups[0].items[0].ID != "first" || model.readItem.Body != "draft-safe detail" || !model.detail {
		t.Fatalf("failed poll wiped visible state: %+v", model)
	}
	model.detail = false
	if !strings.Contains(model.View(), "offline") {
		t.Fatalf("poll error hidden: %q", model.View())
	}
	updated, stale := model.Update(pollTickMsg{token: model.pollToken - 1})
	if stale != nil || updated.(Model).polling {
		t.Fatal("stale timer started another poll")
	}
}

type deadlineClient struct{ inboxClient }

func (c *deadlineClient) List(ctx context.Context, filter core.Filter) ([]core.Item, error) {
	if _, ok := ctx.Deadline(); !ok {
		return nil, errors.New("list has no deadline")
	}
	return c.inboxClient.List(ctx, filter)
}

func (c *deadlineClient) Counts(ctx context.Context) (map[core.Channel]map[string]int, error) {
	if _, ok := ctx.Deadline(); !ok {
		return nil, errors.New("counts has no deadline")
	}
	return c.inboxClient.Counts(ctx)
}

func (c *deadlineClient) Read(ctx context.Context, id string, receipt bool) (core.Item, error) {
	if _, ok := ctx.Deadline(); !ok {
		return core.Item{}, errors.New("read has no deadline")
	}
	return c.inboxClient.Read(ctx, id, receipt)
}

func TestQueryCommandsHaveDeadlines(t *testing.T) {
	client := &deadlineClient{}
	msg := NewModel(client).Init()().(inboxLoadedMsg)
	if msg.listErr != nil || msg.countsErr != nil {
		t.Fatalf("inbox deadlines: list=%v counts=%v", msg.listErr, msg.countsErr)
	}
	read := readItem(client, "one", 1)().(itemReadMsg)
	if read.err != nil || client.readReceipt {
		t.Fatalf("read deadline=%v receipt=%t", read.err, client.readReceipt)
	}
}
