package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/reyer3/bunker-go/internal/core"
)

const mcpTodoListDefault = 50

type (
	mcpTodoAddIn struct {
		Text      string `json:"text" jsonschema:"the to-do as one short line in the conversation's language, e.g. 'Enviar el informe a Ana' (at most 500 characters)"`
		Direction string `json:"direction" jsonschema:"mine: a promise the user made (the user wrote 'te lo mando mañana'); theirs: something someone owes the user (they wrote 'te confirmo el lunes', or the user asked them for something)"`
		Due       string `json:"due,omitempty" jsonschema:"when it is due, if the message says: YYYY-MM-DD, or 2d / 1w from today; resolve words like 'el viernes' to a date; omit when there is no date"`
		ItemID    string `json:"item_id,omitempty" jsonschema:"id of the message the to-do came from, as list, read or thread return it; always pass it when there is one: it links the to-do to the conversation, fills channel, account and person, and makes a rescan of the same message a no-op"`
		Person    string `json:"person,omitempty" jsonschema:"who the to-do is with; omit to take it from item_id's message"`
	}
	mcpTodoListIn struct {
		Status    string `json:"status,omitempty" jsonschema:"open (default), done or all"`
		Direction string `json:"direction,omitempty" jsonschema:"mine or theirs; empty for both"`
		Limit     int    `json:"limit,omitempty" jsonschema:"at most this many to-dos (default 50, max 100)"`
	}
	mcpTodoDoneIn struct {
		ID     string `json:"id" jsonschema:"the to-do id, as todo_add or todo_list return it"`
		Reopen bool   `json:"reopen,omitempty" jsonschema:"mark it open again instead of done"`
	}
	// mcpTodo is core.Todo with its times as text: a date for due, RFC
	// 3339 for created and done.
	mcpTodo struct {
		ID        string `json:"id"`
		Text      string `json:"text"`
		Direction string `json:"direction" jsonschema:"mine (the user promised it) or theirs (someone owes it to the user)"`
		Status    string `json:"status" jsonschema:"open or done"`
		Due       string `json:"due,omitempty" jsonschema:"YYYY-MM-DD, empty when it has no due date"`
		ItemID    string `json:"item_id,omitempty" jsonschema:"the message it came from"`
		Channel   string `json:"channel,omitempty"`
		Account   string `json:"account,omitempty"`
		Thread    string `json:"thread,omitempty"`
		Person    string `json:"person,omitempty"`
		Created   string `json:"created"`
		Done      string `json:"done,omitempty" jsonschema:"when it was completed"`
	}
	mcpAwaitingIn struct {
		Days   int  `json:"days,omitempty" jsonschema:"only conversations unanswered for at least this many days (default 3)"`
		Groups bool `json:"groups,omitempty" jsonschema:"include groups, left out by default"`
		Mail   bool `json:"mail,omitempty" jsonschema:"include every mail thread; by default only mail threads whose last mail of the user asks a question"`
		Limit  int  `json:"limit,omitempty" jsonschema:"at most this many conversations (default 50, max 100)"`
	}
	// mcpAwaiting is core.Awaiting with its time as RFC 3339 text.
	mcpAwaiting struct {
		ItemID  string `json:"item_id" jsonschema:"the user's last message in the conversation"`
		Channel string `json:"channel"`
		Account string `json:"account"`
		Thread  string `json:"thread"`
		Person  string `json:"person" jsonschema:"who the user is waiting on: the chat's name or a mail's first recipient"`
		Preview string `json:"preview" jsonschema:"the start of the user's last message (a mail's subject)"`
		Sent    string `json:"sent" jsonschema:"when the user's last message was sent, RFC 3339"`
		Days    int    `json:"days" jsonschema:"whole days without an answer"`
	}
	mcpAwaitingOut struct {
		Awaiting []mcpAwaiting `json:"awaiting" jsonschema:"newest first"`
	}
	mcpTodoOut struct {
		Todo mcpTodo `json:"todo"`
	}
	mcpTodosOut struct {
		Todos []mcpTodo `json:"todos" jsonschema:"open first, then by due date (soonest first, none last), then oldest first"`
	}
)

func toMCPTodo(t core.Todo) mcpTodo {
	out := mcpTodo{
		ID: t.ID, Text: t.Text, Direction: string(t.Direction), Status: string(t.Status), ItemID: t.ItemID,
		Channel: string(t.Channel), Account: t.Account, Thread: t.Thread, Person: t.Person,
		Created: t.Created.Format(time.RFC3339),
	}
	if !t.Due.IsZero() {
		out.Due = t.Due.Local().Format("2006-01-02")
	}
	if !t.Done.IsZero() {
		out.Done = t.Done.Format(time.RFC3339)
	}
	return out
}

// addMCPAwaitingTool registers awaiting_reply, the MCP twin of bunker
// awaiting. It only reads the store.
func addMCPAwaitingTool(server *mcp.Server, dial mcpDialer) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "awaiting_reply",
		Description: "List the conversations awaiting a reply, like bunker awaiting: where the user wrote last and nobody " +
			"has answered for at least days days (default 3), newest first. Groups are left out unless groups is set; " +
			"mail threads count only when the user's last mail asks a question (quoted history aside) unless mail is set. " +
			"bunker derives the list on every call, so an answer makes a conversation drop off by itself. " +
			"Use it to answer who has not replied to the user; read or thread opens the conversation.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpAwaitingIn) (*mcp.CallToolResult, mcpAwaitingOut, error) {
		if in.Days < 0 {
			return nil, mcpAwaitingOut{}, fmt.Errorf("awaiting_reply: days %d: want 1 or more", in.Days)
		}
		filter := core.AwaitingFilter{Days: in.Days, Groups: in.Groups, Mail: in.Mail, Limit: mcpTodoListDefault}
		if in.Limit > 0 {
			filter.Limit = min(in.Limit, mcpListMax)
		}
		rows, err := withBackend(ctx, dial, func(ctx context.Context, b Backend) ([]core.Awaiting, error) {
			return b.AwaitingReply(ctx, filter)
		})
		if err != nil {
			return nil, mcpAwaitingOut{}, err
		}
		out := mcpAwaitingOut{Awaiting: make([]mcpAwaiting, 0, len(rows))}
		for _, a := range rows {
			out.Awaiting = append(out.Awaiting, mcpAwaiting{
				ItemID: a.ItemID, Channel: string(a.Channel), Account: a.Account, Thread: a.Thread,
				Person: a.Person, Preview: a.Preview, Sent: a.Sent.Format(time.RFC3339), Days: a.Days,
			})
		}
		return nil, out, nil
	})
}

// addMCPTodoTools registers todo_add, todo_list and todo_done, the MCP
// twins of bunker todo. Spotting a to-do in a message is the agent's job;
// these only keep the list. The writes touch only bunker's local store,
// like download: nothing is sent, so they need neither confirm nor
// --allow-send. They are idempotent (adding the same to-do again returns
// the stored one; completing a done one changes nothing), not
// destructive and closed-world.
func addMCPTodoTools(server *mcp.Server, dial mcpDialer) {
	notDestructive, closedWorld := false, false
	localWrite := &mcp.ToolAnnotations{DestructiveHint: &notDestructive, OpenWorldHint: &closedWorld, IdempotentHint: true}

	mcp.AddTool(server, &mcp.Tool{
		Name: "todo_add",
		Description: "Record a to-do found in a conversation, like bunker todo add. " +
			"direction mine is a promise the user made; theirs is something someone owes the user. " +
			"Always pass item_id, the id of the message it came from: it links the to-do to its conversation and person, " +
			"and makes adding it again (a retry or a later scan of the same message) return the stored to-do instead of a duplicate. " +
			"Only record clear commitments with something concrete to do, not greetings or vague plans; check todo_list first when unsure. " +
			"Writes only bunker's local store (nothing is sent, so --allow-send does not apply). Returns the to-do with its id.",
		Annotations: localWrite,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpTodoAddIn) (*mcp.CallToolResult, mcpTodoOut, error) {
		due, err := core.ParseTodoDue(in.Due, time.Now())
		if err != nil {
			return nil, mcpTodoOut{}, err
		}
		todo := core.Todo{
			Text: in.Text, Direction: core.TodoDirection(strings.TrimSpace(in.Direction)), Due: due,
			ItemID: in.ItemID, Person: in.Person,
		}
		added, err := withBackend(ctx, dial, func(ctx context.Context, b Backend) (core.Todo, error) {
			return b.AddTodo(ctx, todo)
		})
		if err != nil {
			return nil, mcpTodoOut{}, err
		}
		return nil, mcpTodoOut{Todo: toMCPTodo(added)}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "todo_list",
		Description: "List the user's to-dos, like bunker todo list: open ones by default, soonest due first. " +
			"Use it to answer what the user has pending or is owed, and before todo_add to avoid recording one twice.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpTodoListIn) (*mcp.CallToolResult, mcpTodosOut, error) {
		filter := core.TodoFilter{Direction: core.TodoDirection(in.Direction), Limit: mcpTodoListDefault}
		switch in.Status {
		case "", string(core.TodoOpen):
			filter.Status = core.TodoOpen
		case string(core.TodoDone):
			filter.Status = core.TodoDone
		case "all":
		default:
			return nil, mcpTodosOut{}, fmt.Errorf("todo_list: status %q: want open, done or all", in.Status)
		}
		if in.Limit > 0 {
			filter.Limit = min(in.Limit, mcpListMax)
		}
		todos, err := withBackend(ctx, dial, func(ctx context.Context, b Backend) ([]core.Todo, error) {
			return b.Todos(ctx, filter)
		})
		if err != nil {
			return nil, mcpTodosOut{}, err
		}
		out := mcpTodosOut{Todos: make([]mcpTodo, 0, len(todos))}
		for _, t := range todos {
			out.Todos = append(out.Todos, toMCPTodo(t))
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "todo_done",
		Description: "Mark a to-do done (or, with reopen, open again), like bunker todo done. " +
			"Use it when the user says it is done or a later message shows it was fulfilled. " +
			"Writes only bunker's local store; completing a done to-do changes nothing.",
		Annotations: localWrite,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpTodoDoneIn) (*mcp.CallToolResult, mcpTodoOut, error) {
		if strings.TrimSpace(in.ID) == "" {
			return nil, mcpTodoOut{}, errors.New("todo_done: id is required")
		}
		todo, err := withBackend(ctx, dial, func(ctx context.Context, b Backend) (core.Todo, error) {
			if in.Reopen {
				return b.ReopenTodo(ctx, in.ID)
			}
			return b.CompleteTodo(ctx, in.ID)
		})
		if err != nil {
			return nil, mcpTodoOut{}, err
		}
		return nil, mcpTodoOut{Todo: toMCPTodo(todo)}, nil
	})
}
