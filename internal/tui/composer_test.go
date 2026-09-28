package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestComposerSupportsRealCursorMovement pins down the difference K4 makes:
// the old draftBody string only ever appended to the end, so any key that
// moved the cursor (Home/Right/Left/arrows) was a no-op. With the shared
// bubbles/textarea composer, moving the cursor and inserting mid-string
// must actually work.
func TestComposerSupportsRealCursorMovement(t *testing.T) {
	client := &replyClient{}
	model := readyModel(client, "mail:a:1")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = updated.(Model)

	model = typeRunes(model, "hello")
	if got := model.composer.Value(); got != "hello" {
		t.Fatalf("draft after typing = %q, want %q", got, "hello")
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyHome})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRight})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRight})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
	model = updated.(Model)

	if got := model.composer.Value(); got != "heXllo" {
		t.Fatalf("draft after cursor move + insert = %q, want %q", got, "heXllo")
	}
}

// TestComposerCursorIsStaticForDeterministicGoldens guards against a
// regression where the shared composer's cursor blinks on a tea.Tick: a
// blinking cursor would make teatest golden captures nondeterministic
// depending on scheduler timing (see the tui-ghostty doc's discipline of
// pinning the clock and color profile for goldens).
func TestComposerCursorIsStaticForDeterministicGoldens(t *testing.T) {
	ta := newComposer(40, nil)
	if mode := ta.Cursor.Mode(); mode.String() != "static" {
		t.Fatalf("composer cursor mode = %s, want static", mode)
	}
}

// longDraft builds a multi-line draft taller than composerHeight, so
// PgUp/PgDown/wheel have real room to scroll within the reply composer
// (K4's writeCompose), matching bunker-tui.md's "detail/compose views
// ... no vertical scrolling for long bodies" gap.
func longDraft(lines int) string {
	rows := make([]string, lines)
	for i := range rows {
		rows[i] = "linea de borrador"
	}
	out := rows[0]
	for _, r := range rows[1:] {
		out += "\n" + r
	}
	return out
}

// TestComposePgDownScrollsLongDraftThenPgUpBack pins PgUp/PgDown as an
// explicit page-scroll for a long draft: every other key already reaches
// the shared bubbles/textarea composer as literal draft text (including
// "j"/"k", which move the cursor there, not the view), so PgUp/PgDown are
// the only new bindings this view needs — matching the parent's "in
// compose, text-input keys win" instruction.
func TestComposePgDownScrollsLongDraftThenPgUpBack(t *testing.T) {
	client := &replyClient{}
	model := readyModel(client, "mail:a:1")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = updated.(Model)
	model.composer.SetValue(longDraft(30))
	// SetValue's InsertString leaves the cursor at the LAST line (there is
	// no public "go to input begin" method); walk it back to line 0
	// deterministically before asserting PgDown's effect.
	for i := 0; i < 30; i++ {
		model.composer.CursorUp()
	}
	if got := model.composer.Line(); got != 0 {
		t.Fatalf("composer line before PgDown = %d, want 0", got)
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	model = updated.(Model)
	if got := model.composer.Line(); got == 0 {
		t.Fatal("PgDown did not move the composer's visible window")
	}
	afterDown := model.composer.Line()

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	model = updated.(Model)
	if got := model.composer.Line(); got >= afterDown {
		t.Fatalf("composer line after PgDown then PgUp = %d, want less than %d", got, afterDown)
	}
}

// TestComposeMouseWheelScrollsLongDraft pins the mouse wheel scrolling a
// long draft: before this task, every mouse event was ignored outright
// while composing (updateMouse's m.composing guard), so the wheel did
// nothing at all here.
func TestComposeMouseWheelScrollsLongDraft(t *testing.T) {
	client := &replyClient{}
	model := readyModel(client, "mail:a:1")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = updated.(Model)
	model.composer.SetValue(longDraft(30))
	for i := 0; i < 30; i++ {
		model.composer.CursorUp()
	}

	updated, _ = model.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	model = updated.(Model)
	if got := model.composer.Line(); got == 0 {
		t.Fatal("wheel down did not scroll the composer")
	}
	afterDown := model.composer.Line()

	updated, _ = model.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	model = updated.(Model)
	if got := model.composer.Line(); got >= afterDown {
		t.Fatalf("composer line after wheel down then wheel up = %d, want less than %d", got, afterDown)
	}
}

// TestComposeMouseClickStillIgnored guards that carving out the wheel
// exception in updateMouse did not also let a click reach the composer
// (or anything else) while composing.
func TestComposeMouseClickStillIgnored(t *testing.T) {
	client := &replyClient{}
	model := readyModel(client, "mail:a:1")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = updated.(Model)

	updated, cmd := model.Update(tea.MouseMsg{Y: 3, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	got := updated.(Model)
	if cmd != nil {
		t.Fatal("a click while composing unexpectedly returned a command")
	}
	if got.composer.Value() != model.composer.Value() {
		t.Fatalf("a click while composing changed the draft: %q -> %q", model.composer.Value(), got.composer.Value())
	}
}
