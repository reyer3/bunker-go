package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

func todosBackend() *fakeBackend {
	b := newFakeBackend()
	due := time.Date(2026, 10, 9, 0, 0, 0, 0, time.Local)
	b.todos = []core.Todo{
		{ID: "a1", Text: "Mandar el informe", Direction: core.TodoMine, Status: core.TodoOpen, Due: due, Person: "Demo Ana"},
		{ID: "b2", Text: "Confirmar la sala", Direction: core.TodoTheirs, Status: core.TodoOpen},
		{ID: "c3", Text: "Pagar la luz", Direction: core.TodoMine, Status: core.TodoDone},
	}
	return b
}

func TestCmdTodoAdd(t *testing.T) {
	b := todosBackend()
	code, out, stderr := runCallCmd(t, b, "todo", "add", "Revisar", "el", "contrato", "--theirs", "--due", "2026-10-12",
		"--item", "mail:cl:1", "--person", "Demo Luis", "--json")
	if code != 0 {
		t.Fatalf("code = %d stderr = %q", code, stderr)
	}
	if len(b.todoAddCalls) != 1 {
		t.Fatalf("add calls = %d", len(b.todoAddCalls))
	}
	got := b.todoAddCalls[0]
	if got.Text != "Revisar el contrato" || got.Direction != core.TodoTheirs || got.ItemID != "mail:cl:1" ||
		got.Person != "Demo Luis" || got.Due.Format("2006-01-02") != "2026-10-12" {
		t.Errorf("added = %+v", got)
	}
	var res struct {
		Todo core.Todo `json:"todo"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.Todo.ID == "" {
		t.Fatalf("json = %q, %v", out, err)
	}

	code, out, _ = runCallCmd(t, b, "todo", "add", "Llamar al banco")
	if code != 0 || b.todoAddCalls[1].Direction != core.TodoMine || !strings.Contains(out, "Llamar al banco") {
		t.Errorf("default mine: code=%d out=%q calls=%+v", code, out, b.todoAddCalls)
	}
	if !b.todoAddCalls[1].Due.IsZero() {
		t.Errorf("due without --due = %v", b.todoAddCalls[1].Due)
	}
}

func TestCmdTodoAddErrors(t *testing.T) {
	b := todosBackend()
	if code, _, _ := runCallCmd(t, b, "todo", "add"); code != 2 {
		t.Errorf("no text code = %d, want 2", code)
	}
	if code, _, stderr := runCallCmd(t, b, "todo", "add", "x", "--due", "mañana"); code == 0 || !strings.Contains(stderr, "due") {
		t.Errorf("bad due: code=%d stderr=%q", code, stderr)
	}
	if len(b.todoAddCalls) != 0 {
		t.Errorf("a bad call reached the backend: %+v", b.todoAddCalls)
	}
	b.todoErr = errors.New("boom")
	if code, _, stderr := runCallCmd(t, b, "todo", "add", "x"); code == 0 || !strings.Contains(stderr, "boom") {
		t.Errorf("backend error: code=%d stderr=%q", code, stderr)
	}
}

func TestCmdTodoList(t *testing.T) {
	b := todosBackend()
	code, out, _ := runCallCmd(t, b, "todo", "list")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if got := b.todosCalls[0]; got.Status != core.TodoOpen || got.Direction != "" {
		t.Errorf("default filter = %+v", got)
	}
	if !strings.Contains(out, "a1\t2026-10-09\tdebo\tMandar el informe\tDemo Ana") ||
		!strings.Contains(out, "b2\t-\tme deben\tConfirmar la sala\t") || strings.Contains(out, "Pagar la luz") {
		t.Errorf("out = %q", out)
	}

	_, _, _ = runCallCmd(t, b, "todo", "list", "--all", "--theirs")
	if got := b.todosCalls[1]; got.Status != "" || got.Direction != core.TodoTheirs {
		t.Errorf("--all --theirs filter = %+v", got)
	}
	_, out, _ = runCallCmd(t, b, "todo", "list", "--all", "--mine", "--json")
	var res struct {
		Todos []core.Todo `json:"todos"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || len(res.Todos) != 2 {
		t.Fatalf("json = %q, %v", out, err)
	}
	// bare "todo" lists too.
	if code, _, _ := runCallCmd(t, b, "todo"); code != 0 || len(b.todosCalls) != 4 {
		t.Errorf("bare todo: code=%d calls=%d", code, len(b.todosCalls))
	}
	if code, _, _ := runCallCmd(t, b, "todo", "list", "--mine", "--theirs"); code != 2 {
		t.Errorf("--mine --theirs code = %d, want 2", code)
	}
	_, empty, _ := runCallCmd(t, newFakeBackend(), "todo", "list", "--json")
	if strings.TrimSpace(empty) != `{"todos":[]}` {
		t.Errorf("empty JSON = %q", empty)
	}
	_, empty, _ = runCallCmd(t, newFakeBackend(), "todo", "list")
	if !strings.Contains(empty, "no open to-dos") {
		t.Errorf("empty text = %q", empty)
	}
}

func TestCmdTodoDoneAndReopen(t *testing.T) {
	b := todosBackend()
	code, out, _ := runCallCmd(t, b, "todo", "done", "a1")
	if code != 0 || b.todos[0].Status != core.TodoDone || !strings.Contains(out, "Mandar el informe") {
		t.Fatalf("done: code=%d out=%q todo=%+v", code, out, b.todos[0])
	}
	code, out, _ = runCallCmd(t, b, "todo", "reopen", "a1", "--json")
	var res struct {
		Todo core.Todo `json:"todo"`
	}
	if err := json.Unmarshal([]byte(out), &res); code != 0 || err != nil || res.Todo.Status != core.TodoOpen {
		t.Fatalf("reopen: code=%d out=%q err=%v", code, out, err)
	}
	if code, _, stderr := runCallCmd(t, b, "todo", "done", "zz"); code == 0 || !strings.Contains(stderr, "not found") {
		t.Errorf("unknown: code=%d stderr=%q", code, stderr)
	}
	if code, _, _ := runCallCmd(t, b, "todo", "done"); code != 2 {
		t.Errorf("missing id code = %d", code)
	}
	if code, _, _ := runCallCmd(t, b, "todo", "frobnicate"); code != 2 {
		t.Errorf("unknown subcommand code = %d", code)
	}
}
