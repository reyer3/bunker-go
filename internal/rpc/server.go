package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"

	"github.com/reyer3/bunker-go/internal/core"
)

// Server dispatches line-delimited JSON requests to a core.Service over a
// unix socket.
type Server struct {
	svc *core.Service
}

// NewServer wraps svc for RPC.
func NewServer(svc *core.Service) *Server {
	return &Server{svc: svc}
}

// Serve listens on socketPath and handles connections until ctx is
// canceled, then closes the listener and returns.
func (s *Server) Serve(ctx context.Context, socketPath string) error {
	_ = os.Remove(socketPath) // stale socket from a previous crashed run

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("rpc: listen %s: %w", socketPath, err)
	}

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("rpc: accept: %w", err)
		}
		go s.handleConn(ctx, conn)
	}
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	enc := json.NewEncoder(conn)

	for scanner.Scan() {
		var req Request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			enc.Encode(Response{Error: fmt.Sprintf("rpc: bad request: %v", err)})
			continue
		}
		resp := s.dispatch(ctx, req)
		if err := enc.Encode(resp); err != nil {
			return
		}
	}
}

func (s *Server) dispatch(ctx context.Context, req Request) Response {
	result, err := s.call(ctx, req)
	if err != nil {
		return errResponse(req.ID, err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return errResponse(req.ID, fmt.Errorf("rpc: marshal result: %w", err))
	}
	return Response{ID: req.ID, Result: raw}
}

func (s *Server) call(ctx context.Context, req Request) (any, error) {
	switch req.Method {
	case MethodList:
		var p listParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("rpc: bad params: %w", err)
		}
		items, err := s.svc.List(ctx, p.Filter)
		if err != nil {
			return nil, err
		}
		return listResult{Items: items}, nil

	case MethodListPage:
		var p listPageParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("rpc: bad params: %w", err)
		}
		return s.svc.ListPage(ctx, p.Filter, p.Query)

	case MethodGet:
		var p idParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("rpc: bad params: %w", err)
		}
		item, err := s.svc.Get(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		return itemResult{Item: item}, nil

	case MethodFetch:
		var p idParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("rpc: bad params: %w", err)
		}
		item, err := s.svc.Fetch(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		return itemResult{Item: item}, nil

	case MethodRead:
		var p readParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("rpc: bad params: %w", err)
		}
		item, err := s.svc.Read(ctx, p.ID, p.MarkReceipt)
		if err != nil {
			return nil, err
		}
		return itemResult{Item: item}, nil

	case MethodCounts:
		counts, err := s.svc.Counts(ctx)
		if err != nil {
			return nil, err
		}
		return countsResult{Counts: counts}, nil

	case MethodReply:
		var p replyParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("rpc: bad params: %w", err)
		}
		replyCtx := core.WithIdempotencyKey(ctx, p.IdempotencyKey)
		if p.Voice {
			replyCtx = core.WithVoice(replyCtx)
		}
		plan, receipt, err := s.svc.Reply(replyCtx, p.ID, p.Body, p.Cc, p.Attachments, p.DryRun)
		if err != nil {
			return nil, err
		}
		return planReceiptResult{Plan: plan, Receipt: receipt}, nil

	case MethodSend:
		var p sendParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("rpc: bad params: %w", err)
		}
		plan, receipt, err := s.svc.Send(core.WithIdempotencyKey(ctx, p.IdempotencyKey), p.Outgoing, p.DryRun)
		if err != nil {
			return nil, err
		}
		return planReceiptResult{Plan: plan, Receipt: receipt}, nil

	case MethodEdit:
		var p editParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("rpc: bad params: %w", err)
		}
		plan, receipt, err := s.svc.EditMessage(core.WithIdempotencyKey(ctx, p.IdempotencyKey), p.ID, p.Text, p.DryRun)
		if err != nil {
			return nil, err
		}
		return planReceiptResult{Plan: plan, Receipt: receipt}, nil

	case MethodDelete:
		var p deleteParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("rpc: bad params: %w", err)
		}
		plan, receipt, err := s.svc.DeleteMessage(core.WithIdempotencyKey(ctx, p.IdempotencyKey), p.ID, p.DryRun)
		if err != nil {
			return nil, err
		}
		return planReceiptResult{Plan: plan, Receipt: receipt}, nil

	case MethodReact:
		var p reactParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("rpc: bad params: %w", err)
		}
		plan, receipt, err := s.svc.React(core.WithIdempotencyKey(ctx, p.IdempotencyKey), p.ID, p.Emoji, p.DryRun)
		if err != nil {
			return nil, err
		}
		return planReceiptResult{Plan: plan, Receipt: receipt}, nil

	case MethodOrganize:
		var p organizeParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("rpc: bad params: %w", err)
		}
		plan, err := s.svc.Organize(ctx, p.ID, p.Op, p.DryRun)
		if err != nil {
			return nil, err
		}
		return planResult{Plan: plan}, nil

	case MethodPostStatus:
		var p statusParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("rpc: bad params: %w", err)
		}
		plan, receipt, err := s.svc.PostStatus(ctx, p.Channel, p.Account, p.Status, p.DryRun)
		if err != nil {
			return nil, err
		}
		return planReceiptResult{Plan: plan, Receipt: receipt}, nil

	case MethodDownload:
		var p downloadParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("rpc: bad params: %w", err)
		}
		res, err := s.svc.Download(ctx, p.ID, p.Index, p.Path, core.DownloadOptions{Force: p.Force})
		if err != nil {
			return nil, err
		}
		return downloadResult{Result: res}, nil

	case MethodAvatar:
		var p avatarParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("rpc: bad params: %w", err)
		}
		res, err := s.svc.Avatar(ctx, p.Channel, p.Account, p.Thread)
		if err != nil {
			return nil, err
		}
		return avatarResult{Result: res}, nil

	case MethodThread:
		var p threadParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("rpc: bad params: %w", err)
		}
		items, err := s.svc.Thread(ctx, string(p.Channel), p.Account, p.Thread, p.Before, p.Limit)
		if err != nil {
			return nil, err
		}
		return threadResult{Items: items}, nil

	case MethodReadThread:
		var p readThreadParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("rpc: bad params: %w", err)
		}
		count, err := s.svc.ReadThread(ctx, string(p.Channel), p.Account, p.Thread, p.Receipt)
		if err != nil {
			return nil, err
		}
		return readThreadResult{Count: count}, nil

	case MethodPresence:
		var p presenceParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("rpc: bad params: %w", err)
		}
		res, err := s.svc.Presence(ctx, string(p.Channel), p.Account, p.Thread)
		if err != nil {
			return nil, err
		}
		return presenceResult{Presence: res}, nil

	case MethodPresenceKeepalive:
		var p presenceKeepaliveParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("rpc: bad params: %w", err)
		}
		if err := s.svc.PresenceKeepalive(ctx, string(p.Channel), p.Account, p.Thread, p.Focused); err != nil {
			return nil, err
		}
		return nil, nil

	case MethodTyping:
		var p typingParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("rpc: bad params: %w", err)
		}
		if err := s.svc.Typing(ctx, string(p.Channel), p.Account, p.Thread, p.Composing); err != nil {
			return nil, err
		}
		return nil, nil

	case MethodCall:
		var p callParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("rpc: bad params: %w", err)
		}
		plan, call, err := s.svc.PlaceCall(ctx, p.Channel, p.Account, p.To, p.DryRun)
		if err != nil {
			return nil, err
		}
		return planCallResult{Plan: plan, Call: call}, nil

	case MethodCallControl:
		var p callControlParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("rpc: bad params: %w", err)
		}
		plan, call, err := s.svc.ControlCall(ctx, p.ID, p.Action, p.DryRun)
		if err != nil {
			return nil, err
		}
		return planCallResult{Plan: plan, Call: call}, nil

	case MethodCalls:
		calls, err := s.svc.Calls(ctx)
		if err != nil {
			return nil, err
		}
		return callsResult{Calls: calls}, nil

	case MethodMarkUnread:
		var p markUnreadParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("rpc: bad params: %w", err)
		}
		local, err := s.svc.MarkUnread(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		return markUnreadResult{LocalOnly: local}, nil

	case MethodContacts:
		var p contactsParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &p); err != nil {
				return nil, fmt.Errorf("rpc: bad params: %w", err)
			}
		}
		contacts, err := s.svc.Contacts(ctx, p.Filter)
		if err != nil {
			return nil, err
		}
		return contactsResult{Contacts: contacts}, nil

	case MethodConversations:
		var p conversationsParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &p); err != nil {
				return nil, fmt.Errorf("rpc: bad params: %w", err)
			}
		}
		conversations, err := s.svc.Conversations(ctx, p.Filter)
		if err != nil {
			return nil, err
		}
		return conversationsResult{Conversations: conversations}, nil

	case MethodMeetings:
		var p meetingsParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &p); err != nil {
				return nil, fmt.Errorf("rpc: bad params: %w", err)
			}
		}
		meetings, err := s.svc.Meetings(ctx, p.Filter)
		if err != nil {
			return nil, err
		}
		return meetingsResult{Meetings: meetings}, nil

	case MethodTodoAdd:
		var p todoAddParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("rpc: bad params: %w", err)
		}
		todo, err := s.svc.AddTodo(ctx, p.Todo)
		if err != nil {
			return nil, err
		}
		return todoResult{Todo: todo}, nil

	case MethodTodos:
		var p todosParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &p); err != nil {
				return nil, fmt.Errorf("rpc: bad params: %w", err)
			}
		}
		todos, err := s.svc.Todos(ctx, p.Filter)
		if err != nil {
			return nil, err
		}
		return todosResult{Todos: todos}, nil

	case MethodTodoSet:
		var p todoSetParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("rpc: bad params: %w", err)
		}
		var todo core.Todo
		var err error
		switch p.Status {
		case core.TodoDone:
			todo, err = s.svc.CompleteTodo(ctx, p.ID)
		case core.TodoOpen:
			todo, err = s.svc.ReopenTodo(ctx, p.ID)
		default:
			err = fmt.Errorf("rpc: to-do status %q: %w", p.Status, core.ErrInvalidTodo)
		}
		if err != nil {
			return nil, err
		}
		return todoResult{Todo: todo}, nil

	case MethodHealth:
		report, err := s.svc.HealthReport(ctx)
		if err != nil {
			return nil, err
		}
		return healthResult{Adapters: report.Adapters, UpdateAvailable: report.Update.Available, LatestVersion: report.Update.Latest}, nil
	case MethodBackfill:
		var p backfillParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("rpc: bad params: %w", err)
		}
		result, err := s.svc.Backfill(ctx, p.Channel, p.Account, p.Folder, p.Since, p.DryRun)
		if err != nil {
			return nil, err
		}
		return backfillResult{Result: result}, nil

	case MethodSearch:
		var p searchParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("rpc: bad params: %w", err)
		}
		items, err := s.svc.Search(ctx, p.Channel, p.Account, p.Criteria)
		if err != nil {
			return nil, err
		}
		return listResult{Items: items}, nil

	default:
		return nil, fmt.Errorf("rpc: unknown method %q", req.Method)
	}
}

func errResponse(id string, err error) Response {
	resp := Response{ID: id, Error: err.Error()}
	switch {
	case errors.Is(err, core.ErrNotFound):
		resp.ErrCode = errCodeNotFound
	case errors.Is(err, core.ErrUnsupported):
		resp.ErrCode = errCodeUnsupported
	case errors.Is(err, core.ErrTodoNotFound):
		resp.ErrCode = errCodeTodoNotFound
	case errors.Is(err, core.ErrInvalidTodo):
		resp.ErrCode = errCodeInvalidTodo
	}
	return resp
}
