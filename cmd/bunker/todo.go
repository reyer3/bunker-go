package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

const todoUsage = `usage: bunker todo add <text> [--theirs] [--due YYYY-MM-DD|2d] [--item id] [--person name] [--json]
       bunker todo [list] [--all] [--mine|--theirs] [--json]
       bunker todo done|reopen <id> [--json]`

// cmdTodo keeps the local to-do list: what I promised (mine) and what
// others owe me (theirs). Every subcommand only touches bunker's store,
// so none needs --dry-run.
func cmdTodo(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return cmdTodoList(ctx, backend, args, stdout, stderr)
	}
	switch args[0] {
	case "add":
		return cmdTodoAdd(ctx, backend, args[1:], stdout, stderr)
	case "list":
		return cmdTodoList(ctx, backend, args[1:], stdout, stderr)
	case "done":
		return cmdTodoSet(ctx, backend, "done", args[1:], stdout, stderr)
	case "reopen":
		return cmdTodoSet(ctx, backend, "reopen", args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, todoUsage)
		return 2
	}
}

func cmdTodoAdd(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("todo add", stderr)
	theirs := fs.Bool("theirs", false, "someone owes it to me (default: I promised it)")
	due := fs.String("due", "", "due date: YYYY-MM-DD, or 2d / 1w from today")
	itemID := fs.String("item", "", "id of the message the to-do came from")
	person := fs.String("person", "", "who it is with (default: from --item)")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	text := strings.Join(positionals, " ")
	if strings.TrimSpace(text) == "" {
		fmt.Fprintln(stderr, todoUsage)
		return 2
	}
	dueAt, err := core.ParseTodoDue(*due, time.Now())
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	direction := core.TodoMine
	if *theirs {
		direction = core.TodoTheirs
	}
	todo, err := backend.AddTodo(ctx, core.Todo{
		Text: text, Direction: direction, Due: dueAt, ItemID: *itemID, Person: *person,
	})
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	if *jsonOut {
		writeJSON(stdout, map[string]any{"todo": todo})
		return 0
	}
	fmt.Fprintln(stdout, formatTodo(todo))
	return 0
}

func cmdTodoList(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("todo list", stderr)
	all := fs.Bool("all", false, "include the done ones")
	mine := fs.Bool("mine", false, "only what I promised")
	theirs := fs.Bool("theirs", false, "only what others owe me")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) > 0 || (*mine && *theirs) {
		fmt.Fprintln(stderr, todoUsage)
		return 2
	}
	filter := core.TodoFilter{Status: core.TodoOpen}
	if *all {
		filter.Status = ""
	}
	switch {
	case *mine:
		filter.Direction = core.TodoMine
	case *theirs:
		filter.Direction = core.TodoTheirs
	}
	todos, err := backend.Todos(ctx, filter)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	if *jsonOut {
		if todos == nil {
			todos = []core.Todo{}
		}
		writeJSON(stdout, map[string]any{"todos": todos})
		return 0
	}
	if len(todos) == 0 {
		if *all {
			fmt.Fprintln(stdout, "no to-dos")
		} else {
			fmt.Fprintln(stdout, "no open to-dos")
		}
		return 0
	}
	for _, t := range todos {
		fmt.Fprintln(stdout, formatTodo(t))
	}
	return 0
}

// cmdTodoSet is "todo done" and "todo reopen" (sub names which).
func cmdTodoSet(ctx context.Context, backend Backend, sub string, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("todo "+sub, stderr)
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, todoUsage)
		return 2
	}
	var todo core.Todo
	if sub == "done" {
		todo, err = backend.CompleteTodo(ctx, positionals[0])
	} else {
		todo, err = backend.ReopenTodo(ctx, positionals[0])
	}
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	if *jsonOut {
		writeJSON(stdout, map[string]any{"todo": todo})
		return 0
	}
	fmt.Fprintln(stdout, formatTodo(todo))
	return 0
}

// formatTodo is one tab-separated record per line: id (what "todo done"
// takes), due date or "-", who owes it, text, person; a done to-do ends
// with "hecho".
func formatTodo(t core.Todo) string {
	due := "-"
	if !t.Due.IsZero() {
		due = t.Due.Local().Format("2006-01-02")
	}
	who := "debo"
	if t.Direction == core.TodoTheirs {
		who = "me deben"
	}
	line := fmt.Sprintf("%s\t%s\t%s\t%s\t%s", t.ID, due, who, strings.Join(strings.Fields(t.Text), " "), t.Person)
	if t.Status == core.TodoDone {
		line += "\thecho"
	}
	return line
}
