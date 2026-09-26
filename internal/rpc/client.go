package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// Client is a synchronous connection to a Server over a unix socket.
type Client struct {
	mu      sync.Mutex
	conn    net.Conn
	scanner *bufio.Scanner
	enc     *json.Encoder
	seq     int
}

// Dial connects to the server listening on socketPath.
func Dial(socketPath string) (*Client, error) {
	return DialContext(context.Background(), socketPath)
}

// DialContext bounds connection setup by the caller's query deadline.
func DialContext(ctx context.Context, socketPath string) (*Client, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("rpc: dial %s: %w", socketPath, err)
	}
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	return &Client{conn: conn, scanner: scanner, enc: json.NewEncoder(conn)}, nil
}

// Close closes the underlying connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// unsupportedErr and notFoundErr wrap core's sentinel errors so
// errors.Is(err, core.ErrNotFound/core.ErrUnsupported) keeps working
// across the RPC boundary.
type remoteError struct {
	msg     string
	wrapped error
}

func (e *remoteError) Error() string { return e.msg }
func (e *remoteError) Unwrap() error { return e.wrapped }

func (c *Client) call(ctx context.Context, method string, params any, result any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if deadline, ok := ctx.Deadline(); ok {
		if err := c.conn.SetDeadline(deadline); err != nil {
			return fmt.Errorf("rpc: set deadline: %w", err)
		}
		defer c.conn.SetDeadline(time.Time{})
	}

	c.seq++
	req := Request{ID: strconv.Itoa(c.seq), Method: method}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("rpc: marshal params: %w", err)
		}
		req.Params = raw
	}

	if err := c.enc.Encode(req); err != nil {
		return fmt.Errorf("rpc: send request: %w", err)
	}

	if !c.scanner.Scan() {
		if err := c.scanner.Err(); err != nil {
			return fmt.Errorf("rpc: read response: %w", err)
		}
		return fmt.Errorf("rpc: connection closed")
	}

	var resp Response
	if err := json.Unmarshal(c.scanner.Bytes(), &resp); err != nil {
		return fmt.Errorf("rpc: decode response: %w", err)
	}
	if resp.Error != "" {
		switch resp.ErrCode {
		case errCodeNotFound:
			return &remoteError{msg: resp.Error, wrapped: core.ErrNotFound}
		case errCodeUnsupported:
			return &remoteError{msg: resp.Error, wrapped: core.ErrUnsupported}
		default:
			return fmt.Errorf("%s", resp.Error)
		}
	}
	if result != nil && len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return fmt.Errorf("rpc: decode result: %w", err)
		}
	}
	return nil
}

// List returns the items matching filter.
func (c *Client) List(ctx context.Context, filter core.Filter) ([]core.Item, error) {
	var res listResult
	if err := c.call(ctx, MethodList, listParams{Filter: filter}, &res); err != nil {
		return nil, err
	}
	return res.Items, nil
}

// Get returns the stored item for id.
func (c *Client) Get(ctx context.Context, id string) (core.Item, error) {
	var res itemResult
	if err := c.call(ctx, MethodGet, idParams{ID: id}, &res); err != nil {
		return core.Item{}, err
	}
	return res.Item, nil
}

// Fetch returns the full item body for id.
func (c *Client) Fetch(ctx context.Context, id string) (core.Item, error) {
	var res itemResult
	if err := c.call(ctx, MethodFetch, idParams{ID: id}, &res); err != nil {
		return core.Item{}, err
	}
	return res.Item, nil
}

// Read fetches item id's full body and, unless markReceipt is false,
// marks it read on the channel itself when the adapter supports it
// (T13c; mail never marks anything regardless of markReceipt).
func (c *Client) Read(ctx context.Context, id string, markReceipt bool) (core.Item, error) {
	var res itemResult
	if err := c.call(ctx, MethodRead, readParams{ID: id, MarkReceipt: markReceipt}, &res); err != nil {
		return core.Item{}, err
	}
	return res.Item, nil
}

// Counts returns unread counts per channel and account.
func (c *Client) Counts(ctx context.Context) (map[core.Channel]map[string]int, error) {
	var res countsResult
	if err := c.call(ctx, MethodCounts, nil, &res); err != nil {
		return nil, err
	}
	return res.Counts, nil
}

// Reply answers item id with body, optionally carrying Cc recipients and
// attaching local files.
func (c *Client) Reply(ctx context.Context, id, body string, cc, attachments []string, dryRun bool) (core.Plan, core.Receipt, error) {
	var res planReceiptResult
	err := c.call(ctx, MethodReply, replyParams{ID: id, Body: body, Cc: cc, Attachments: attachments, DryRun: dryRun}, &res)
	if err != nil {
		return core.Plan{}, core.Receipt{}, err
	}
	return res.Plan, res.Receipt, nil
}

// Send delivers a fresh outgoing message.
func (c *Client) Send(ctx context.Context, out core.Outgoing, dryRun bool) (core.Plan, core.Receipt, error) {
	var res planReceiptResult
	err := c.call(ctx, MethodSend, sendParams{Outgoing: out, DryRun: dryRun}, &res)
	if err != nil {
		return core.Plan{}, core.Receipt{}, err
	}
	return res.Plan, res.Receipt, nil
}

// Organize mutates an item's labels/folder/read state.
func (c *Client) Organize(ctx context.Context, id string, op core.OrganizeOp, dryRun bool) (core.Plan, error) {
	var res planResult
	err := c.call(ctx, MethodOrganize, organizeParams{ID: id, Op: op, DryRun: dryRun}, &res)
	if err != nil {
		return core.Plan{}, err
	}
	return res.Plan, nil
}

// PostStatus publishes a status/story on channel/account.
func (c *Client) PostStatus(ctx context.Context, channel core.Channel, account string, status core.Status, dryRun bool) (core.Plan, core.Receipt, error) {
	var res planReceiptResult
	err := c.call(ctx, MethodPostStatus, statusParams{Channel: channel, Account: account, Status: status, DryRun: dryRun}, &res)
	if err != nil {
		return core.Plan{}, core.Receipt{}, err
	}
	return res.Plan, res.Receipt, nil
}

// Download saves item id's attachment at index to destPath on the
// machine the daemon runs on: the daemon writes the file itself (see
// downloadParams), so this call never streams the attachment's bytes
// back over the socket. opts.MaxBytes is not carried over the wire (the
// CLI exposes no flag for it); the daemon's own DefaultMaxDownloadBytes
// cap always applies remotely.
func (c *Client) Download(ctx context.Context, id string, index int, destPath string, opts core.DownloadOptions) (core.DownloadResult, error) {
	var res downloadResult
	err := c.call(ctx, MethodDownload, downloadParams{ID: id, Index: index, Path: destPath, Force: opts.Force}, &res)
	if err != nil {
		return core.DownloadResult{}, err
	}
	return res.Result, nil
}

// Avatar returns a local PNG path for (channel, account, thread)'s
// conversation avatar, written by the daemon itself (see avatarResult and
// core.Service.Avatar) — like Download, the bytes never travel over the
// socket.
func (c *Client) Avatar(ctx context.Context, channel core.Channel, account, thread string) (core.AvatarResult, error) {
	var res avatarResult
	err := c.call(ctx, MethodAvatar, avatarParams{Channel: channel, Account: account, Thread: thread}, &res)
	if err != nil {
		return core.AvatarResult{}, err
	}
	return res.Result, nil
}
