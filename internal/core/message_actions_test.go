package core_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// spyMessenger implements Editor, Deleter, Reactor and
// MessageWindowLimiter, counting every call so tests can assert a dry-run
// or a rejected request never reaches it.
type spyMessenger struct {
	windows core.MessageWindows
	fail    error

	edits     []string
	deletes   []string
	reactions []string
}

func (s *spyMessenger) Channel() core.Channel                      { return core.ChannelWhatsApp }
func (s *spyMessenger) Account() string                            { return "personal" }
func (s *spyMessenger) Run(ctx context.Context, _ core.Sink) error { return nil }

func (s *spyMessenger) EditMessage(_ context.Context, item core.Item, newText string) (core.Receipt, error) {
	s.edits = append(s.edits, item.ID+"="+newText)
	if s.fail != nil {
		return core.Receipt{}, s.fail
	}
	return core.Receipt{ID: "edit-1", Channel: core.ChannelWhatsApp}, nil
}

func (s *spyMessenger) DeleteMessage(_ context.Context, item core.Item) (core.Receipt, error) {
	s.deletes = append(s.deletes, item.ID)
	if s.fail != nil {
		return core.Receipt{}, s.fail
	}
	return core.Receipt{ID: "delete-1", Channel: core.ChannelWhatsApp}, nil
}

func (s *spyMessenger) React(_ context.Context, item core.Item, emoji string) (core.Receipt, error) {
	s.reactions = append(s.reactions, item.ID+"="+emoji)
	if s.fail != nil {
		return core.Receipt{}, s.fail
	}
	return core.Receipt{ID: "react-1", Channel: core.ChannelWhatsApp}, nil
}

func (s *spyMessenger) OwnReactionSender() string { return "me@s.whatsapp.net" }

func (s *spyMessenger) MessageWindows() core.MessageWindows { return s.windows }

var actionNow = time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

func actionItems() []core.Item {
	return []core.Item{
		{ID: "whatsapp:personal:chat/MINE", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "chat", Body: "hola", FromMe: true, Timestamp: actionNow.Add(-5 * time.Minute)},
		{ID: "whatsapp:personal:chat/OLD", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "chat", Body: "viejo", FromMe: true, Timestamp: actionNow.Add(-time.Hour)},
		{ID: "whatsapp:personal:chat/THEIRS", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "chat", Body: "que tal", Timestamp: actionNow.Add(-2 * time.Minute)},
		{ID: "whatsapp:personal:chat/GONE", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "chat", FromMe: true, Deleted: true, Timestamp: actionNow},
		{ID: "whatsapp:personal:chat/PHOTO", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "chat", Body: "foto", FromMe: true, Timestamp: actionNow, Attachments: []core.Attachment{{Name: "a.jpg"}}},
	}
}

func actionService(t *testing.T, adapter core.Adapter) (*core.Service, *memStore) {
	t.Helper()
	reg := core.NewRegistry()
	reg.Register(adapter)
	store := newMemStore(actionItems()...)
	svc := core.NewService(store, reg)
	svc.SetMessageClock(func() time.Time { return actionNow })
	return svc, store
}

func TestEditMessageUpdatesStoreAfterAdapter(t *testing.T) {
	spy := &spyMessenger{windows: core.MessageWindows{Edit: 20 * time.Minute}}
	svc, store := actionService(t, spy)

	plan, receipt, err := svc.EditMessage(context.Background(), "whatsapp:personal:chat/MINE", "hola, corregido", false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != "edit" || plan.Target != "whatsapp:personal:chat/MINE" || plan.Preview != "hola, corregido" {
		t.Errorf("plan = %+v", plan)
	}
	if receipt.ID != "edit-1" || len(spy.edits) != 1 {
		t.Errorf("receipt = %+v, edits = %v", receipt, spy.edits)
	}
	got := store.items["whatsapp:personal:chat/MINE"]
	if got.Body != "hola, corregido" || !got.Edited {
		t.Errorf("stored item = %+v, want the new body marked edited", got)
	}
}

func TestMessageActionsDryRunNeverCallsAdapter(t *testing.T) {
	spy := &spyMessenger{}
	svc, store := actionService(t, spy)
	ctx := context.Background()

	if plan, _, err := svc.EditMessage(ctx, "whatsapp:personal:chat/MINE", "nuevo", true); err != nil || plan.Action != "edit" {
		t.Fatalf("edit dry-run: %+v, %v", plan, err)
	}
	if plan, _, err := svc.DeleteMessage(ctx, "whatsapp:personal:chat/MINE", true); err != nil || plan.Preview != "hola" {
		t.Fatalf("delete dry-run: %+v, %v (the preview should be the text being deleted)", plan, err)
	}
	if plan, _, err := svc.React(ctx, "whatsapp:personal:chat/THEIRS", "👍", true); err != nil || plan.Preview != "👍" {
		t.Fatalf("react dry-run: %+v, %v", plan, err)
	}
	if len(spy.edits)+len(spy.deletes)+len(spy.reactions) != 0 {
		t.Fatalf("dry-run reached the adapter: %v %v %v", spy.edits, spy.deletes, spy.reactions)
	}
	if got := store.items["whatsapp:personal:chat/MINE"]; got.Body != "hola" || got.Edited || got.Deleted {
		t.Errorf("dry-run changed the store: %+v", got)
	}
}

func TestEditAndDeleteRejectOtherPeoplesMessages(t *testing.T) {
	spy := &spyMessenger{}
	svc, _ := actionService(t, spy)
	ctx := context.Background()

	for _, dryRun := range []bool{true, false} {
		if _, _, err := svc.EditMessage(ctx, "whatsapp:personal:chat/THEIRS", "no", dryRun); !errors.Is(err, core.ErrNotOwnMessage) {
			t.Errorf("edit (dryRun=%v) err = %v, want ErrNotOwnMessage", dryRun, err)
		}
		if _, _, err := svc.DeleteMessage(ctx, "whatsapp:personal:chat/THEIRS", dryRun); !errors.Is(err, core.ErrNotOwnMessage) {
			t.Errorf("delete (dryRun=%v) err = %v, want ErrNotOwnMessage", dryRun, err)
		}
	}
	if len(spy.edits)+len(spy.deletes) != 0 {
		t.Fatalf("adapter reached: %v %v", spy.edits, spy.deletes)
	}
}

func TestReactWorksOnAnyMessageAndRemoves(t *testing.T) {
	spy := &spyMessenger{}
	svc, store := actionService(t, spy)
	ctx := context.Background()
	id := "whatsapp:personal:chat/THEIRS"

	if _, _, err := svc.React(ctx, id, "❤️", false); err != nil {
		t.Fatal(err)
	}
	if got := store.items[id].Reactions; len(got) != 1 || got[0] != (core.Reaction{Sender: "me@s.whatsapp.net", Emoji: "❤️"}) {
		t.Fatalf("reactions = %+v", got)
	}
	if _, _, err := svc.React(ctx, id, "", false); err != nil {
		t.Fatal(err)
	}
	if got := store.items[id].Reactions; len(got) != 0 {
		t.Fatalf("reactions after removal = %+v, want none", got)
	}
	if want := []string{id + "=❤️", id + "="}; len(spy.reactions) != 2 || spy.reactions[0] != want[0] || spy.reactions[1] != want[1] {
		t.Errorf("adapter reactions = %q, want %q", spy.reactions, want)
	}
}

func TestReactRejectsText(t *testing.T) {
	spy := &spyMessenger{}
	svc, _ := actionService(t, spy)
	for _, bad := range []string{"ok", "👍 👍", ":thumbsup:", "\x00"} {
		if _, _, err := svc.React(context.Background(), "whatsapp:personal:chat/THEIRS", bad, true); err == nil {
			t.Errorf("React(%q) accepted a non-emoji", bad)
		}
	}
}

func TestMessageActionsNotFoundDeletedAndUnsupported(t *testing.T) {
	ctx := context.Background()
	svc, _ := actionService(t, &spyMessenger{})
	if _, _, err := svc.EditMessage(ctx, "whatsapp:personal:chat/NOPE", "x", true); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("edit of an unknown id: %v, want ErrNotFound", err)
	}
	if _, _, err := svc.DeleteMessage(ctx, "whatsapp:personal:chat/GONE", true); err == nil {
		t.Error("deleting an already deleted message succeeded")
	}
	if _, _, err := svc.EditMessage(ctx, "whatsapp:personal:chat/PHOTO", "otra", true); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("editing a media message: %v, want ErrUnsupported", err)
	}
	if _, _, err := svc.EditMessage(ctx, "whatsapp:personal:chat/MINE", "  ", true); err == nil {
		t.Error("an empty edit was accepted")
	}

	bare, _ := actionService(t, bareAdapter{channel: core.ChannelWhatsApp, account: "personal"})
	if _, _, err := bare.EditMessage(ctx, "whatsapp:personal:chat/MINE", "x", true); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("edit: %v, want ErrUnsupported", err)
	}
	if _, _, err := bare.DeleteMessage(ctx, "whatsapp:personal:chat/MINE", true); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("delete: %v, want ErrUnsupported", err)
	}
	if _, _, err := bare.React(ctx, "whatsapp:personal:chat/MINE", "👍", true); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("react: %v, want ErrUnsupported", err)
	}
}

func TestEditAndDeleteHonorTheChannelWindow(t *testing.T) {
	spy := &spyMessenger{windows: core.MessageWindows{Edit: 20 * time.Minute, Delete: 30 * time.Minute}}
	svc, _ := actionService(t, spy)
	ctx := context.Background()

	if _, _, err := svc.EditMessage(ctx, "whatsapp:personal:chat/OLD", "tarde", true); !errors.Is(err, core.ErrWindowExpired) {
		t.Errorf("late edit dry-run: %v, want ErrWindowExpired", err)
	}
	if _, _, err := svc.DeleteMessage(ctx, "whatsapp:personal:chat/OLD", false); !errors.Is(err, core.ErrWindowExpired) {
		t.Errorf("late delete: %v, want ErrWindowExpired", err)
	}
	if len(spy.edits)+len(spy.deletes) != 0 {
		t.Fatal("a late edit or delete reached the adapter")
	}
	if _, _, err := svc.DeleteMessage(ctx, "whatsapp:personal:chat/MINE", false); err != nil {
		t.Errorf("delete within the window: %v", err)
	}
}

func TestMessageActionFailureLeavesStoreAlone(t *testing.T) {
	spy := &spyMessenger{fail: errors.New("whatsapp: not connected")}
	svc, store := actionService(t, spy)
	if _, _, err := svc.DeleteMessage(context.Background(), "whatsapp:personal:chat/MINE", false); err == nil {
		t.Fatal("a failed delete reported success")
	}
	if got := store.items["whatsapp:personal:chat/MINE"]; got.Deleted {
		t.Error("a failed delete revoked the stored item")
	}
}

func TestMessageActionsIdempotencyKey(t *testing.T) {
	spy := &spyMessenger{}
	svc, _ := actionService(t, spy)
	ctx := core.WithIdempotencyKey(context.Background(), "react-key")

	if _, _, err := svc.React(ctx, "whatsapp:personal:chat/THEIRS", "👍", false); err != nil {
		t.Fatal(err)
	}
	_, again, err := svc.React(ctx, "whatsapp:personal:chat/THEIRS", "👍", false)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Replayed || len(spy.reactions) != 1 {
		t.Fatalf("repeat: replayed=%v, adapter calls=%d, want a replay and one call", again.Replayed, len(spy.reactions))
	}
	if _, _, err := svc.React(ctx, "whatsapp:personal:chat/THEIRS", "❤️", false); err == nil {
		t.Fatal("the same key for a different reaction was accepted")
	}
}
