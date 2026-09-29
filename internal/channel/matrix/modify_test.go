package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/reyer3/bunker-go/internal/core"
)

func ownItem(evtID id.EventID) core.Item {
	return core.Item{ID: itemID("work", relRoom, evtID), Channel: core.ChannelMatrix, Account: "work", Thread: relRoom.String(), From: core.Address{ID: relSelf.String()}, FromMe: true, Body: "hola"}
}

func sentContent(t *testing.T, state *fakeState, i int) (string, map[string]any) {
	t.Helper()
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.sentEvents) <= i {
		t.Fatalf("sent %d events, want at least %d", len(state.sentEvents), i+1)
	}
	var content map[string]any
	if err := json.Unmarshal(state.sentEvents[i].body, &content); err != nil {
		t.Fatalf("decode sent content: %v", err)
	}
	return state.sentEvents[i].path, content
}

func redactions(state *fakeState) []string {
	state.mu.Lock()
	defer state.mu.Unlock()
	return append([]string(nil), state.redactions...)
}

func TestEditMessageSendsReplace(t *testing.T) {
	srv, state := newFakeHomeserver(t, nil)
	a := newTestAdapter(t, srv, nil)

	receipt, err := a.EditMessage(context.Background(), ownItem("$m1"), "hola, corregido")
	if err != nil {
		t.Fatal(err)
	}
	path, content := sentContent(t, state, 0)
	if !strings.Contains(path, "/send/m.room.message/") {
		t.Errorf("path = %q, want an m.room.message", path)
	}
	newContent, _ := content["m.new_content"].(map[string]any)
	rel, _ := content["m.relates_to"].(map[string]any)
	if content["body"] != "* hola, corregido" || newContent["body"] != "hola, corregido" || rel["rel_type"] != "m.replace" || rel["event_id"] != "$m1" {
		t.Errorf("edit content = %v", content)
	}
	if want := itemID("work", relRoom, "$sent1"); receipt.ID != want {
		t.Errorf("receipt = %q, want %q", receipt.ID, want)
	}

	// Our own echo applies the same body to the same item, once.
	sink := newMemSink()
	sink.items[ownItem("$m1").ID] = ownItem("$m1")
	a.remember(ownItem("$m1"))
	echo := editMsg("$sent1", relSelf, "$m1", "hola, corregido")
	echo.RoomID = relRoom
	if !a.applyRelation(context.Background(), sink, echo) {
		t.Fatal("the edit echo was not recognized as a relation")
	}
	if got := sink.get(t, "$m1"); got.Body != "hola, corregido" || !got.Edited {
		t.Errorf("after echo = %+v", got)
	}
	sink.assertNoItems(t, "$sent1")
}

func TestDeleteMessageRedactsOwnEvent(t *testing.T) {
	srv, state := newFakeHomeserver(t, nil)
	a := newTestAdapter(t, srv, nil)
	if _, err := a.DeleteMessage(context.Background(), ownItem("$m1")); err != nil {
		t.Fatal(err)
	}
	if got := redactions(state); len(got) != 1 || !strings.Contains(got[0], "/redact/$m1/") {
		t.Errorf("redactions = %v, want one of $m1", got)
	}
	if _, err := a.DeleteMessage(context.Background(), core.Item{ID: itemID("work", relRoom, "$b1")}); !errors.Is(err, core.ErrNotOwnMessage) {
		t.Errorf("deleting someone else's event: %v, want ErrNotOwnMessage", err)
	}
}

// TestReactEchoDoesNotDoubleApply sends a reaction, then feeds its own
// sync echo back: the mapping already holds it, so it stays one live
// reaction and one stored reaction.
func TestReactEchoDoesNotDoubleApply(t *testing.T) {
	srv, state := newFakeHomeserver(t, nil)
	a := newTestAdapter(t, srv, nil)
	sink := newMemSink()
	a.sink = sink
	target := core.Item{ID: itemID("work", relRoom, "$b1"), From: core.Address{ID: relBob.String()}}
	sink.items[target.ID] = target

	if _, err := a.React(context.Background(), target, "👍"); err != nil {
		t.Fatal(err)
	}
	// What core.Service.React writes once the adapter succeeds.
	if err := sink.SetReaction(context.Background(), target.ID, core.Reaction{Sender: a.OwnReactionSender(), Emoji: "👍"}); err != nil {
		t.Fatal(err)
	}
	path, content := sentContent(t, state, 0)
	rel, _ := content["m.relates_to"].(map[string]any)
	if !strings.Contains(path, "/send/m.reaction/") || rel["rel_type"] != "m.annotation" || rel["event_id"] != "$b1" || rel["key"] != "👍" {
		t.Errorf("reaction = %s %v", path, content)
	}

	echo := reactionEvt("$sent1", relSelf, "$b1", "👍")
	echo.RoomID = relRoom
	a.applyRelation(context.Background(), sink, echo)

	slot := reactionSlot{target: target.ID, user: relSelf.String()}
	a.mu.Lock()
	live := a.liveReactions[slot]
	a.mu.Unlock()
	if len(live) != 1 {
		t.Errorf("live reactions = %v, want exactly one", live)
	}
	if got := sink.get(t, "$b1").Reactions; len(got) != 1 || got[0].Emoji != "👍" {
		t.Errorf("stored reactions = %+v, want our one 👍", got)
	}
}

func TestReactReplacesThenRemovesOwnReaction(t *testing.T) {
	srv, state := newFakeHomeserver(t, nil)
	a := newTestAdapter(t, srv, nil)
	sink := newMemSink()
	a.sink = sink
	target := core.Item{ID: itemID("work", relRoom, "$b1"), From: core.Address{ID: relBob.String()}, Body: "hola"}
	sink.items[target.ID] = target
	ctx := context.Background()

	if _, err := a.React(ctx, target, "👍"); err != nil {
		t.Fatal(err)
	}
	if r, err := a.React(ctx, target, "👍"); err != nil || r.ID != itemID("work", relRoom, "$sent1") {
		t.Fatalf("same emoji again = %+v, %v; want the existing reaction, nothing sent", r, err)
	}
	if _, err := a.React(ctx, target, "❤️"); err != nil {
		t.Fatal(err)
	}
	if got := redactions(state); len(got) != 1 || !strings.Contains(got[0], "/redact/$sent1/") {
		t.Fatalf("redactions = %v, want the 👍 reaction redacted before ❤️", got)
	}
	if _, err := a.React(ctx, target, ""); err != nil {
		t.Fatal(err)
	}
	if got := redactions(state); len(got) != 2 || !strings.Contains(got[1], "/redact/$sent2/") {
		t.Fatalf("redactions = %v, want the ❤️ reaction redacted", got)
	}
	state.mu.Lock()
	sent := len(state.sentEvents)
	state.mu.Unlock()
	if sent != 2 {
		t.Errorf("sent %d reactions, want 2 (👍 once, ❤️ once)", sent)
	}

	// The echo of our redaction finds the tombstone: the message is not
	// mistaken for redacted.
	echo := redactionEvt("$redaction2", relSelf, "$sent2")
	echo.RoomID = relRoom
	a.applyRelation(ctx, sink, echo)
	if got := sink.get(t, "$b1"); got.Deleted || got.Body != "hola" {
		t.Errorf("message after our reaction's redaction echo = %+v", got)
	}

	if _, err := a.React(ctx, target, ""); err == nil {
		t.Error("removing a reaction we no longer have succeeded")
	}
}

// TestReactRemovalAfterRestart removes a reaction made before a restart:
// the fresh adapter finds it through the persisted records.
func TestReactRemovalAfterRestart(t *testing.T) {
	sink := newMemSink()
	target := core.Item{ID: itemID("work", relRoom, "$b1"), From: core.Address{ID: relBob.String()}}
	sink.items[target.ID] = target

	srv1, _ := newFakeHomeserver(t, nil)
	first := newTestAdapter(t, srv1, nil)
	first.sink = sink
	if _, err := first.React(context.Background(), target, "😮"); err != nil {
		t.Fatal(err)
	}

	srv2, state2 := newFakeHomeserver(t, nil)
	second := newTestAdapter(t, srv2, nil)
	second.sink = sink
	if _, err := second.React(context.Background(), target, ""); err != nil {
		t.Fatal(err)
	}
	if got := redactions(state2); len(got) != 1 || !strings.Contains(got[0], "/redact/$sent1/") {
		t.Errorf("redactions = %v, want the reaction from before the restart", got)
	}
}

// TestModifyInEncryptedRoom checks an edit and a reaction in an
// encrypted room go out as m.room.encrypted and decrypt to the right
// relation.
func TestModifyInEncryptedRoom(t *testing.T) {
	senderMach := newTestOlmMachine(t, relSelf)
	receiverMach := newTestOlmMachine(t, relBob)
	ogs, err := crypto.NewOutboundGroupSession(relRoom, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ogs.Shared = true
	share := ogs.ShareContent().Parsed.(*event.RoomKeyEventContent)
	identity := senderMach.OwnIdentity()
	igs, err := crypto.NewInboundGroupSession(identity.IdentityKey, identity.SigningKey, relRoom, share.SessionKey, 0, 0, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := receiverMach.CryptoStore.PutGroupSession(context.Background(), igs); err != nil {
		t.Fatal(err)
	}

	srv, state := newFakeHomeserver(t, nil)
	a := newTestAdapter(t, srv, &machineCryptoHelper{mach: senderMach, outbound: ogs})
	if err := a.client.StateStore.SetEncryptionEvent(context.Background(), relRoom, &event.EncryptionEventContent{Algorithm: id.AlgorithmMegolmV1}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.EditMessage(context.Background(), ownItem("$m1"), "secreto corregido"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.React(context.Background(), core.Item{ID: itemID("work", relRoom, "$b1")}, "🔒"); err != nil {
		t.Fatal(err)
	}

	for i, want := range []event.Type{event.EventMessage, event.EventReaction} {
		path, _ := sentContent(t, state, i)
		if !strings.Contains(path, "/send/m.room.encrypted/") {
			t.Fatalf("event %d path = %q, want it encrypted", i, path)
		}
		state.mu.Lock()
		var enc event.EncryptedEventContent
		err := json.Unmarshal(state.sentEvents[i].body, &enc)
		state.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		decrypted, err := receiverMach.DecryptMegolmEvent(context.Background(), &event.Event{
			ID: id.EventID("$x"), Sender: relSelf, RoomID: relRoom, Type: event.EventEncrypted, Content: event.Content{Parsed: &enc},
		})
		if err != nil {
			t.Fatalf("decrypt event %d: %v", i, err)
		}
		if decrypted.Type != want {
			t.Errorf("event %d decrypted type = %s, want %s", i, decrypted.Type.Type, want.Type)
		}
		raw := string(decrypted.Content.VeryRaw)
		switch want {
		case event.EventMessage:
			if !strings.Contains(raw, `"m.replace"`) || !strings.Contains(raw, "secreto corregido") {
				t.Errorf("decrypted edit = %s", raw)
			}
		case event.EventReaction:
			if !strings.Contains(raw, `"m.annotation"`) || !strings.Contains(raw, "🔒") {
				t.Errorf("decrypted reaction = %s", raw)
			}
		}
	}
}
