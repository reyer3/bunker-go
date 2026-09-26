// Package rpc exposes core.Service over a line-delimited JSON protocol on
// a unix socket, so the CLI (and Claude Code) can drive a running daemon
// without linking against core or store directly.
package rpc

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/reyer3/bunker-go/internal/core"
)

// Request is one line-delimited JSON call.
type Request struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is one line-delimited JSON reply. ErrCode is set for the
// well-known sentinel errors (core.ErrNotFound, core.ErrUnsupported) so
// clients can recover them with errors.Is; Error always carries a
// human-readable message.
type Response struct {
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   string          `json:"error,omitempty"`
	ErrCode string          `json:"errCode,omitempty"`
}

const (
	errCodeNotFound    = "not_found"
	errCodeUnsupported = "unsupported"
)

// Method names understood by the server.
const (
	MethodList       = "list"
	MethodGet        = "get"
	MethodFetch      = "fetch"
	MethodRead       = "read"
	MethodCounts     = "counts"
	MethodReply      = "reply"
	MethodSend       = "send"
	MethodOrganize   = "organize"
	MethodPostStatus = "status"
	MethodDownload   = "download"
	MethodAvatar     = "avatar"
)

type listParams struct {
	Filter core.Filter `json:"filter"`
}
type listResult struct {
	Items []core.Item `json:"items"`
}

type idParams struct {
	ID string `json:"id"`
}
type itemResult struct {
	Item core.Item `json:"item"`
}

// readParams is MethodRead's params: fetch item ID's full body and,
// unless MarkReceipt is false (the CLI's --no-receipt), mark it read on
// the channel itself when the adapter supports it (T13c).
type readParams struct {
	ID          string `json:"id"`
	MarkReceipt bool   `json:"markReceipt"`
}

type countsResult struct {
	Counts map[core.Channel]map[string]int `json:"counts"`
}

type replyParams struct {
	ID          string   `json:"id"`
	Body        string   `json:"body"`
	Cc          []string `json:"cc"`
	Attachments []string `json:"attachments"`
	DryRun      bool     `json:"dryRun"`
}

type sendParams struct {
	Outgoing core.Outgoing `json:"outgoing"`
	DryRun   bool          `json:"dryRun"`
}

type planReceiptResult struct {
	Plan    core.Plan    `json:"plan"`
	Receipt core.Receipt `json:"receipt"`
}

type organizeParams struct {
	ID     string          `json:"id"`
	Op     core.OrganizeOp `json:"op"`
	DryRun bool            `json:"dryRun"`
}

type planResult struct {
	Plan core.Plan `json:"plan"`
}

type statusParams struct {
	Channel core.Channel `json:"channel"`
	Account string       `json:"account"`
	Status  core.Status  `json:"status"`
	DryRun  bool         `json:"dryRun"`
}

// downloadParams is MethodDownload's params: save item ID's attachment
// at Index to Path on the machine the daemon runs on. The daemon writes
// the file itself instead of returning its bytes (see Client.Download
// and core.Service.Download): the CLI and the daemon always run as the
// same user on the same machine, so this is both simpler and keeps every
// download under one enforced size cap, instead of also needing to fit
// a 100 MB attachment through this line-delimited JSON protocol's much
// smaller read buffer (see Server.handleConn's 8 MB scanner buffer).
type downloadParams struct {
	ID    string `json:"id"`
	Index int    `json:"index"`
	Path  string `json:"path"`
	Force bool   `json:"force"`
}
type downloadResult struct {
	Result core.DownloadResult `json:"result"`
}

// avatarParams is MethodAvatar's params: the same (channel, account,
// thread) triple that groups Items into one conversation.
type avatarParams struct {
	Channel core.Channel `json:"channel"`
	Account string       `json:"account"`
	Thread  string       `json:"thread"`
}
type avatarResult struct {
	Result core.AvatarResult `json:"result"`
}

// DefaultSocketPath returns BUNKER_SOCKET if set, else
// $XDG_RUNTIME_DIR/bunker-go.sock, else a bunker-go.sock under os.TempDir().
func DefaultSocketPath() string {
	if p := os.Getenv("BUNKER_SOCKET"); p != "" {
		return p
	}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "bunker-go.sock")
	}
	return filepath.Join(os.TempDir(), "bunker-go.sock")
}
