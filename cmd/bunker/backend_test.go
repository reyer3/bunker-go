package main

import (
	"context"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// fakeBackend is a scriptable Backend double for CLI command tests: no
// socket, no store, just recorded calls and canned returns.
type fakeBackend struct {
	items map[string]core.Item

	listErr, getErr, fetchErr, readErr, countsErr error
	replyErr, sendErr, organizeErr, statusErr     error
	downloadErr                                   error
	avatarErr                                     error

	counts map[core.Channel]map[string]int

	sendCalls     []core.Outgoing
	replyCalls    []replyCall
	organizeCalls []organizeCall
	statusCalls   []statusCall
	readCalls     []readCall
	downloadCalls []downloadCall
	avatarCalls   []avatarCall
	threadCalls   []threadCall

	receipt        core.Receipt
	sendPlan       core.Plan // overrides Send's default Plan when Action != ""
	downloadResult core.DownloadResult
	avatarResult   core.AvatarResult
	threadErr      error
	threadItems    []core.Item

	readThreadCalls []readThreadCall
	readThreadCount int
	readThreadErr   error

	health         []core.AdapterHealth
	healthErr      error
	backfillCalls  []backfillCall
	backfillResult core.BackfillResult
	backfillErr    error

	searchCalls []searchCall
	searchItems []core.Item
	searchErr   error

	placeCallCalls   []placeCallCall
	controlCallCalls []controlCallCall
	call             core.Call
	calls            []core.Call
	callErr          error
}

type placeCallCall struct {
	Channel core.Channel
	Account string
	To      string
	DryRun  bool
}

type controlCallCall struct {
	ID     string
	Action core.CallAction
	DryRun bool
}

func (f *fakeBackend) PlaceCall(ctx context.Context, channel core.Channel, account, to string, dryRun bool) (core.Plan, core.Call, error) {
	f.placeCallCalls = append(f.placeCallCalls, placeCallCall{channel, account, to, dryRun})
	if f.callErr != nil {
		return core.Plan{}, core.Call{}, f.callErr
	}
	plan := core.Plan{Action: "call", Channel: channel, Account: account, Target: to}
	if dryRun {
		return plan, core.Call{}, nil
	}
	return plan, f.call, nil
}

func (f *fakeBackend) ControlCall(ctx context.Context, id string, action core.CallAction, dryRun bool) (core.Plan, core.Call, error) {
	f.controlCallCalls = append(f.controlCallCalls, controlCallCall{id, action, dryRun})
	if f.callErr != nil {
		return core.Plan{}, core.Call{}, f.callErr
	}
	return core.Plan{Action: "call " + string(action), Channel: f.call.Channel, Account: f.call.Account, Target: f.call.Peer}, f.call, nil
}

func (f *fakeBackend) Calls(ctx context.Context) ([]core.Call, error) {
	return f.calls, f.callErr
}

type backfillCall struct {
	Channel core.Channel
	Account string
	Folder  string
	Since   time.Time
	DryRun  bool
}

type searchCall struct {
	Channel  core.Channel
	Account  string
	Criteria core.SearchCriteria
}

type threadCall struct {
	Channel, Account, Thread string
	Before                   time.Time
	Limit                    int
}

type readThreadCall struct {
	Channel, Account, Thread string
	Receipt                  bool
}

type downloadCall struct {
	ID       string
	Index    int
	DestPath string
	Opts     core.DownloadOptions
}

type avatarCall struct {
	Channel core.Channel
	Account string
	Thread  string
}

type readCall struct {
	ID          string
	MarkReceipt bool
}

type replyCall struct {
	ID          string
	Body        string
	Cc          []string
	Attachments []string
	DryRun      bool
}

type organizeCall struct {
	ID     string
	Op     core.OrganizeOp
	DryRun bool
}

type statusCall struct {
	Channel core.Channel
	Account string
	Status  core.Status
	DryRun  bool
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{items: map[string]core.Item{}, receipt: core.Receipt{ID: "fake-receipt"}}
}

func (f *fakeBackend) List(ctx context.Context, filter core.Filter) ([]core.Item, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]core.Item, 0, len(f.items))
	for _, it := range f.items {
		out = append(out, it)
	}
	return out, nil
}

func (f *fakeBackend) Get(ctx context.Context, id string) (core.Item, error) {
	if f.getErr != nil {
		return core.Item{}, f.getErr
	}
	return f.items[id], nil
}

func (f *fakeBackend) Fetch(ctx context.Context, id string) (core.Item, error) {
	if f.fetchErr != nil {
		return core.Item{}, f.fetchErr
	}
	return f.items[id], nil
}

func (f *fakeBackend) Read(ctx context.Context, id string, markReceipt bool) (core.Item, error) {
	f.readCalls = append(f.readCalls, readCall{ID: id, MarkReceipt: markReceipt})
	if f.readErr != nil {
		return core.Item{}, f.readErr
	}
	return f.items[id], nil
}

func (f *fakeBackend) Counts(ctx context.Context) (map[core.Channel]map[string]int, error) {
	if f.countsErr != nil {
		return nil, f.countsErr
	}
	return f.counts, nil
}

func (f *fakeBackend) Reply(ctx context.Context, id, body string, cc, attachments []string, dryRun bool) (core.Plan, core.Receipt, error) {
	f.replyCalls = append(f.replyCalls, replyCall{ID: id, Body: body, Cc: cc, Attachments: attachments, DryRun: dryRun})
	if f.replyErr != nil {
		return core.Plan{}, core.Receipt{}, f.replyErr
	}
	plan := core.Plan{Action: "reply", Target: id, Preview: body}
	if dryRun {
		return plan, core.Receipt{}, nil
	}
	return plan, f.receipt, nil
}

func (f *fakeBackend) Send(ctx context.Context, out core.Outgoing, dryRun bool) (core.Plan, core.Receipt, error) {
	f.sendCalls = append(f.sendCalls, out)
	if f.sendErr != nil {
		return core.Plan{}, core.Receipt{}, f.sendErr
	}
	plan := core.Plan{Action: "send", Channel: out.Channel, Account: out.Account, Preview: out.Body}
	if f.sendPlan.Action != "" {
		plan = f.sendPlan
	}
	if dryRun {
		return plan, core.Receipt{}, nil
	}
	return plan, f.receipt, nil
}

func (f *fakeBackend) Organize(ctx context.Context, id string, op core.OrganizeOp, dryRun bool) (core.Plan, error) {
	f.organizeCalls = append(f.organizeCalls, organizeCall{ID: id, Op: op, DryRun: dryRun})
	if f.organizeErr != nil {
		return core.Plan{}, f.organizeErr
	}
	return core.Plan{Action: "organize", Target: id}, nil
}

func (f *fakeBackend) PostStatus(ctx context.Context, channel core.Channel, account string, status core.Status, dryRun bool) (core.Plan, core.Receipt, error) {
	f.statusCalls = append(f.statusCalls, statusCall{Channel: channel, Account: account, Status: status, DryRun: dryRun})
	if f.statusErr != nil {
		return core.Plan{}, core.Receipt{}, f.statusErr
	}
	plan := core.Plan{Action: "status", Channel: channel, Account: account, Preview: status.Text}
	if dryRun {
		return plan, core.Receipt{}, nil
	}
	return plan, f.receipt, nil
}

func (f *fakeBackend) Download(ctx context.Context, id string, index int, destPath string, opts core.DownloadOptions) (core.DownloadResult, error) {
	f.downloadCalls = append(f.downloadCalls, downloadCall{ID: id, Index: index, DestPath: destPath, Opts: opts})
	if f.downloadErr != nil {
		return core.DownloadResult{}, f.downloadErr
	}
	if f.downloadResult.Path != "" {
		return f.downloadResult, nil
	}
	return core.DownloadResult{Path: destPath}, nil
}

func (f *fakeBackend) Avatar(ctx context.Context, channel core.Channel, account, thread string) (core.AvatarResult, error) {
	f.avatarCalls = append(f.avatarCalls, avatarCall{Channel: channel, Account: account, Thread: thread})
	if f.avatarErr != nil {
		return core.AvatarResult{}, f.avatarErr
	}
	return f.avatarResult, nil
}

func (f *fakeBackend) Thread(ctx context.Context, channel, account, thread string, before time.Time, limit int) ([]core.Item, error) {
	f.threadCalls = append(f.threadCalls, threadCall{Channel: channel, Account: account, Thread: thread, Before: before, Limit: limit})
	if f.threadErr != nil {
		return nil, f.threadErr
	}
	return f.threadItems, nil
}

func (f *fakeBackend) ReadThread(ctx context.Context, channel, account, thread string, receipt bool) (int, error) {
	f.readThreadCalls = append(f.readThreadCalls, readThreadCall{Channel: channel, Account: account, Thread: thread, Receipt: receipt})
	if f.readThreadErr != nil {
		return 0, f.readThreadErr
	}
	return f.readThreadCount, nil
}

func (f *fakeBackend) Health(ctx context.Context) ([]core.AdapterHealth, error) {
	if f.healthErr != nil {
		return nil, f.healthErr
	}
	return f.health, nil
}

func (f *fakeBackend) Backfill(ctx context.Context, channel core.Channel, account, folder string, since time.Time, dryRun bool) (core.BackfillResult, error) {
	f.backfillCalls = append(f.backfillCalls, backfillCall{Channel: channel, Account: account, Folder: folder, Since: since, DryRun: dryRun})
	if f.backfillErr != nil {
		return core.BackfillResult{}, f.backfillErr
	}
	return f.backfillResult, nil
}

func (f *fakeBackend) Search(ctx context.Context, channel core.Channel, account string, criteria core.SearchCriteria) ([]core.Item, error) {
	f.searchCalls = append(f.searchCalls, searchCall{Channel: channel, Account: account, Criteria: criteria})
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	return f.searchItems, nil
}

var _ Backend = (*fakeBackend)(nil)
