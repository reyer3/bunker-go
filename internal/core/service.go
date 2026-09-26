package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// defaultFanoutMaxRecipients/PauseMin/PauseMax are Service's built-in
// fan-out limits (T13a), used whenever an adapter does not implement
// FanoutConfigurer.
const (
	defaultFanoutMaxRecipients = 10
	defaultFanoutPauseMin      = 3 * time.Second
	defaultFanoutPauseMax      = 8 * time.Second
)

// Service is the channel-agnostic application layer: it reads through the
// Store and dispatches write ops to the Registry's adapters, gated by
// dryRun. A dryRun call returns a Plan describing the action and MUST NOT
// reach the adapter; a real call executes it and also returns the Receipt.
type Service struct {
	store    Store
	registry *Registry

	// sleep and choosePause drive the pause Send/Reply waits between
	// fan-out recipients (T13a). They default to the real time.Sleep and
	// a math/rand-based chooser; tests inject fakes so a broadcast test
	// never sleeps for real (T13f).
	sleep       func(time.Duration)
	choosePause func(min, max time.Duration) time.Duration

	// avatarCacheDir, avatarClock, avatarLimiter and avatarCacheCapBytes
	// back Service.Avatar (see avatar.go). avatarCacheDir is empty until
	// SetAvatarCacheDir is called (production wiring in
	// cmd/bunker/daemon.go; a t.TempDir() in tests) — Avatar refuses to
	// run without it rather than guessing a default under a real HOME.
	avatarCacheDir      string
	avatarClock         func() time.Time
	avatarLimiter       *avatarLimiter
	avatarCacheCapBytes int64
}

// NewService wires a Service to its Store and Registry.
func NewService(store Store, registry *Registry) *Service {
	return &Service{
		store:               store,
		registry:            registry,
		sleep:               time.Sleep,
		choosePause:         defaultChoosePause,
		avatarClock:         time.Now,
		avatarLimiter:       newAvatarLimiter(defaultAvatarFetchInterval),
		avatarCacheCapBytes: avatarCacheCapBytes,
	}
}

// SetSleeper overrides how Service waits between fan-out recipients.
// Tests inject a fake that records durations without blocking.
func (s *Service) SetSleeper(sleep func(time.Duration)) { s.sleep = sleep }

// SetPauseChooser overrides how Service picks each fan-out pause duration
// within [min, max]. Tests inject a deterministic chooser.
func (s *Service) SetPauseChooser(choose func(min, max time.Duration) time.Duration) {
	s.choosePause = choose
}

// defaultChoosePause picks a uniformly random duration in [min, max].
func defaultChoosePause(min, max time.Duration) time.Duration {
	if max <= min {
		return min
	}
	return min + time.Duration(rand.Int63n(int64(max-min)))
}

// List returns the items matching filter.
func (s *Service) List(ctx context.Context, filter Filter) ([]Item, error) {
	return s.store.List(ctx, filter)
}

// Get returns the stored item for id, or ErrNotFound.
func (s *Service) Get(ctx context.Context, id string) (Item, error) {
	return s.store.Get(ctx, id)
}

// Counts returns unread counts per channel and account.
func (s *Service) Counts(ctx context.Context) (map[Channel]map[string]int, error) {
	return s.store.Counts(ctx)
}

// Fetch returns the full item body. It uses the registered adapter's
// Fetcher capability when available, falling back to the stored copy.
func (s *Service) Fetch(ctx context.Context, id string) (Item, error) {
	item, err := s.store.Get(ctx, id)
	if err != nil {
		return Item{}, err
	}
	adapter, ok := s.registry.Get(item.Channel, item.Account)
	if !ok {
		return item, nil
	}
	fetcher, ok := adapter.(Fetcher)
	if !ok {
		return item, nil
	}
	fetched, err := fetcher.Fetch(ctx, id)
	if errors.Is(err, ErrNotFound) {
		// The adapter only knows what this process saw (e.g. WhatsApp's
		// in-memory cache after a restart); the stored copy is still valid.
		return item, nil
	}
	return fetched, err
}

// Read fetches item id's full body (see Fetch) and, unless markReceipt is
// false, additionally marks it read on the channel itself when the
// registered adapter implements ReadMarker (WhatsApp, Matrix). Mail never
// implements ReadMarker, so a mail item is never marked read here
// regardless of markReceipt — Fetch's IMAP BODY.PEEK guarantee is
// untouched (T13c).
func (s *Service) Read(ctx context.Context, id string, markReceipt bool) (Item, error) {
	item, err := s.Fetch(ctx, id)
	if err != nil {
		return Item{}, err
	}
	if !markReceipt {
		return item, nil
	}
	adapter, ok := s.registry.Get(item.Channel, item.Account)
	if !ok {
		return item, nil
	}
	marker, ok := adapter.(ReadMarker)
	if !ok {
		return item, nil
	}
	if err := marker.MarkRead(ctx, id); err != nil {
		return item, fmt.Errorf("core: read: mark read: %w", err)
	}
	if err := s.store.MarkRead(ctx, id, true); err != nil {
		return item, fmt.Errorf("core: read: store mark read: %w", err)
	}
	item.Unread = false
	return item, nil
}

func (s *Service) adapterFor(channel Channel, account string) (Adapter, error) {
	adapter, ok := s.registry.Get(channel, account)
	if !ok {
		return nil, fmt.Errorf("core: no adapter registered for %s/%s: %w", channel, account, ErrUnsupported)
	}
	return adapter, nil
}

// inspectAttachment stats and content-sniffs path into an AttachmentInfo.
// It reads at most 512 bytes — enough for http.DetectContentType — never
// the whole file, so this runs identically on dry-run and real calls
// without ever approaching an upload. When sniffing is inconclusive
// (DetectContentType's generic "application/octet-stream" fallback), the
// file extension is tried via mime.TypeByExtension.
func inspectAttachment(path string) (AttachmentInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return AttachmentInfo{}, fmt.Errorf("core: attachment %q: %w", path, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return AttachmentInfo{}, fmt.Errorf("core: attachment %q: %w", path, err)
	}
	if info.IsDir() {
		return AttachmentInfo{}, fmt.Errorf("core: attachment %q is a directory, not a file", path)
	}

	buf := make([]byte, 512)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return AttachmentInfo{}, fmt.Errorf("core: attachment %q: read: %w", path, err)
	}
	mimeType := http.DetectContentType(buf[:n])
	if mimeType == "application/octet-stream" {
		if guessed := mime.TypeByExtension(filepath.Ext(path)); guessed != "" {
			mimeType = guessed
		}
	}
	return AttachmentInfo{Name: filepath.Base(path), MIME: mimeType, Size: info.Size()}, nil
}

// prepareAttachments inspects every path and validates the result against
// policy, so a MediaSender's SendMedia is never reached with a file it
// would reject anyway. It never reads more than inspectAttachment's
// 512-byte sniff per file: this is safe to run on dry-run calls too.
func prepareAttachments(policy AttachmentPolicy, paths []string) ([]AttachmentInfo, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	infos := make([]AttachmentInfo, 0, len(paths))
	var total int64
	for _, p := range paths {
		info, err := inspectAttachment(p)
		if err != nil {
			return nil, err
		}
		max, ok := policy.MaxBytes[info.MIME]
		if !ok {
			max, ok = policy.MaxBytes[AnyMIME]
		}
		if !ok {
			return nil, fmt.Errorf("core: attachment %q has unsupported type %q", info.Name, info.MIME)
		}
		if info.Size > max {
			return nil, fmt.Errorf("core: attachment %q is %d bytes, over the %d byte limit for %s", info.Name, info.Size, max, info.MIME)
		}
		total += info.Size
		infos = append(infos, info)
	}
	if policy.MaxTotalBytes > 0 && total > policy.MaxTotalBytes {
		return nil, fmt.Errorf("core: attachments total %d bytes, over the %d byte total limit", total, policy.MaxTotalBytes)
	}
	return infos, nil
}

// replySubject computes the "Re: " prefixed subject Plan.Subject shows
// for a reply's approval preview, mirroring the mail adapter's own
// ReplySubject rule (a subject already carrying a reply prefix, checked
// case-insensitively, is left as-is) without core importing the mail
// package. It never changes what an adapter actually transmits. An empty
// orig (WhatsApp/Matrix items have no Subject) yields "" — "else empty".
func replySubject(orig string) string {
	if orig == "" {
		return ""
	}
	lower := toLowerASCII(orig)
	if len(lower) >= 3 && lower[:3] == "re:" {
		return orig
	}
	return "Re: " + orig
}

// toLowerASCII lowercases only ASCII letters — replySubject's prefix
// check never needs full Unicode case folding.
func toLowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// Reply answers item id with body, optionally carrying attachments. dryRun
// returns the Plan alone; a real call also sends through the adapter's
// Sender capability and returns its Receipt. When attachments is
// non-empty, the adapter must implement the stronger MediaSender
// capability, exactly like Send: a plain Sender is never used to send
// text-only and silently drop the attachments.
func (s *Service) Reply(ctx context.Context, id string, body string, cc []string, attachments []string, dryRun bool) (Plan, Receipt, error) {
	item, err := s.store.Get(ctx, id)
	if err != nil {
		return Plan{}, Receipt{}, err
	}

	plan := Plan{
		Action:     "reply",
		Channel:    item.Channel,
		Account:    item.Account,
		Target:     id,
		Cc:         cc,
		Subject:    replySubject(item.Subject),
		Preview:    body,
		Media:      attachments,
		Recipients: []string{item.From.ID},
	}

	adapter, err := s.adapterFor(item.Channel, item.Account)
	if err != nil {
		return Plan{}, Receipt{}, err
	}

	out := Outgoing{
		Channel:     item.Channel,
		Account:     item.Account,
		To:          []string{item.From.ID},
		Cc:          cc,
		Thread:      item.Thread,
		ReplyTo:     id,
		Subject:     item.Subject,
		Body:        body,
		Attachments: attachments,
	}

	if len(attachments) > 0 {
		mediaSender, ok := adapter.(MediaSender)
		if !ok {
			return Plan{}, Receipt{}, fmt.Errorf("core: adapter %s/%s cannot send media: %w", item.Channel, item.Account, ErrUnsupported)
		}
		infos, err := prepareAttachments(mediaSender.AttachmentPolicy(), attachments)
		if err != nil {
			return Plan{}, Receipt{}, fmt.Errorf("core: reply: %w", err)
		}
		plan.Attachments = infos
		if dryRun {
			return plan, Receipt{}, nil
		}
		receipt, err := mediaSender.SendMedia(ctx, out)
		if err != nil {
			return Plan{}, Receipt{}, fmt.Errorf("core: reply send failed: %w", err)
		}
		return plan, receipt, nil
	}

	sender, ok := adapter.(Sender)
	if !ok {
		return Plan{}, Receipt{}, fmt.Errorf("core: adapter %s/%s cannot send: %w", item.Channel, item.Account, ErrUnsupported)
	}
	if dryRun {
		return plan, Receipt{}, nil
	}
	receipt, err := sender.Send(ctx, out)
	if err != nil {
		return Plan{}, Receipt{}, fmt.Errorf("core: reply send failed: %w", err)
	}
	return plan, receipt, nil
}

// Send delivers a fresh outgoing message. dryRun returns the Plan alone.
// When out.Attachments is non-empty, the adapter must implement the
// stronger MediaSender capability: a plain Sender is never used to send
// text-only and silently drop the attachments. Every attachment is
// inspected and validated against the adapter's AttachmentPolicy before
// SendMedia is called, on dry-run too, so a bad file is caught without
// ever uploading anything.
func (s *Service) Send(ctx context.Context, out Outgoing, dryRun bool) (Plan, Receipt, error) {
	plan := Plan{
		Action:     "send",
		Channel:    out.Channel,
		Account:    out.Account,
		Target:     fmt.Sprint(out.To),
		Cc:         out.Cc,
		Subject:    out.Subject,
		Preview:    out.Body,
		Media:      out.Attachments,
		Recipients: append([]string{}, out.To...),
	}

	adapter, err := s.adapterFor(out.Channel, out.Account)
	if err != nil {
		return Plan{}, Receipt{}, err
	}

	if _, native := adapter.(MultiRecipientSender); !native && len(out.To) > 1 {
		return s.sendFanout(ctx, adapter, plan, out, dryRun)
	}

	if len(out.Attachments) > 0 {
		mediaSender, ok := adapter.(MediaSender)
		if !ok {
			return Plan{}, Receipt{}, fmt.Errorf("core: adapter %s/%s cannot send media: %w", out.Channel, out.Account, ErrUnsupported)
		}
		infos, err := prepareAttachments(mediaSender.AttachmentPolicy(), out.Attachments)
		if err != nil {
			return Plan{}, Receipt{}, fmt.Errorf("core: send: %w", err)
		}
		plan.Attachments = infos
		if dryRun {
			return plan, Receipt{}, nil
		}
		receipt, err := mediaSender.SendMedia(ctx, out)
		if err != nil {
			return Plan{}, Receipt{}, fmt.Errorf("core: send media failed: %w", err)
		}
		return plan, receipt, nil
	}

	sender, ok := adapter.(Sender)
	if !ok {
		return Plan{}, Receipt{}, fmt.Errorf("core: adapter %s/%s cannot send: %w", out.Channel, out.Account, ErrUnsupported)
	}
	if dryRun {
		return plan, Receipt{}, nil
	}

	receipt, err := sender.Send(ctx, out)
	if err != nil {
		return Plan{}, Receipt{}, fmt.Errorf("core: send failed: %w", err)
	}
	return plan, receipt, nil
}

// fanoutPolicy resolves the fan-out limits/pacing Service must honor for
// adapter: the adapter's own FanoutConfigurer when it implements one
// (filling in any zero field with Service's default), else Service's
// built-in default outright.
func fanoutPolicy(adapter Adapter) FanoutPolicy {
	policy := FanoutPolicy{
		MaxRecipients: defaultFanoutMaxRecipients,
		PauseMin:      defaultFanoutPauseMin,
		PauseMax:      defaultFanoutPauseMax,
	}
	fc, ok := adapter.(FanoutConfigurer)
	if !ok {
		return policy
	}
	configured := fc.FanoutPolicy()
	if configured.MaxRecipients > 0 {
		policy.MaxRecipients = configured.MaxRecipients
	}
	if configured.PauseMin > 0 {
		policy.PauseMin = configured.PauseMin
	}
	if configured.PauseMax > 0 {
		policy.PauseMax = configured.PauseMax
	}
	if policy.PauseMax < policy.PauseMin {
		policy.PauseMax = policy.PauseMin
	}
	return policy
}

// sendFanout delivers out to every out.To recipient with N sequential
// single-recipient adapter calls, for an adapter that does not implement
// MultiRecipientSender (T13a). It enforces the resolved FanoutPolicy's
// MaxRecipients before ever sending anything, and paces every recipient
// after the first with a randomized pause chosen from
// [PauseMin,PauseMax]. One recipient's failure never stops the rest and
// is never hidden: it is reported in Receipt.Recipients, and Service
// never turns an individual recipient failure into a top-level error —
// only the CLI decides the process exit code from those per-recipient
// results. A broadcast with Cc is rejected up front (chat channels have no
// Cc, and silently stripping it would drop recipients).
func (s *Service) sendFanout(ctx context.Context, adapter Adapter, plan Plan, out Outgoing, dryRun bool) (Plan, Receipt, error) {
	policy := fanoutPolicy(adapter)
	if len(out.Cc) > 0 {
		return Plan{}, Receipt{}, fmt.Errorf("core: send: %s has no Cc; a broadcast cannot carry %d Cc recipient(s): %w", out.Channel, len(out.Cc), ErrUnsupported)
	}
	if len(out.To) > policy.MaxRecipients {
		return Plan{}, Receipt{}, fmt.Errorf("core: send: %d recipients exceeds the %d broadcast limit: %w", len(out.To), policy.MaxRecipients, ErrTooManyRecipients)
	}

	var mediaSender MediaSender
	var sender Sender
	if len(out.Attachments) > 0 {
		ms, ok := adapter.(MediaSender)
		if !ok {
			return Plan{}, Receipt{}, fmt.Errorf("core: adapter %s/%s cannot send media: %w", out.Channel, out.Account, ErrUnsupported)
		}
		infos, err := prepareAttachments(ms.AttachmentPolicy(), out.Attachments)
		if err != nil {
			return Plan{}, Receipt{}, fmt.Errorf("core: send: %w", err)
		}
		plan.Attachments = infos
		mediaSender = ms
	} else {
		sd, ok := adapter.(Sender)
		if !ok {
			return Plan{}, Receipt{}, fmt.Errorf("core: adapter %s/%s cannot send: %w", out.Channel, out.Account, ErrUnsupported)
		}
		sender = sd
	}

	if n := len(out.To); n > 1 {
		plan.FanoutPauseMin = time.Duration(n-1) * policy.PauseMin
		plan.FanoutPauseMax = time.Duration(n-1) * policy.PauseMax
	}

	if dryRun {
		return plan, Receipt{}, nil
	}

	results := make([]RecipientResult, 0, len(out.To))
	var firstReceipt Receipt
	for i, to := range out.To {
		if i > 0 {
			s.sleep(s.choosePause(policy.PauseMin, policy.PauseMax))
		}

		single := out
		single.To = []string{to}

		var receipt Receipt
		var sendErr error
		if mediaSender != nil {
			receipt, sendErr = mediaSender.SendMedia(ctx, single)
		} else {
			receipt, sendErr = sender.Send(ctx, single)
		}

		result := RecipientResult{To: to, Receipt: receipt}
		if sendErr != nil {
			result.Error = sendErr.Error()
		} else if firstReceipt.ID == "" {
			firstReceipt = receipt
		}
		results = append(results, result)
	}

	firstReceipt.Recipients = results
	return plan, firstReceipt, nil
}

// Organize mutates an item's labels/folder/read state. dryRun returns the
// Plan alone; the Organizer port has no Receipt.
func (s *Service) Organize(ctx context.Context, id string, op OrganizeOp, dryRun bool) (Plan, error) {
	item, err := s.store.Get(ctx, id)
	if err != nil {
		return Plan{}, err
	}

	plan := Plan{
		Action:  "organize",
		Channel: item.Channel,
		Account: item.Account,
		Target:  id,
		Preview: fmt.Sprintf("%+v", op),
	}

	adapter, err := s.adapterFor(item.Channel, item.Account)
	if err != nil {
		return Plan{}, err
	}
	mover, isMover := adapter.(FolderMover)
	organizer, _ := adapter.(Organizer)
	if !isMover && organizer == nil {
		return Plan{}, fmt.Errorf("core: adapter %s/%s cannot organize: %w", item.Channel, item.Account, ErrUnsupported)
	}
	if dryRun {
		return plan, nil
	}

	// move.ID defaults to id (the address is unchanged) for a plain
	// Organizer, which has no notion of relocating the item; only a
	// FolderMover (mail) can report a different one.
	move := OrganizeMove{ID: id}
	if isMover {
		move, err = mover.OrganizeMove(ctx, id, op)
		if err != nil {
			return Plan{}, fmt.Errorf("core: organize failed: %w", err)
		}
	} else if err := organizer.Organize(ctx, id, op); err != nil {
		return Plan{}, fmt.Errorf("core: organize failed: %w", err)
	}

	if err := s.reconcileOrganize(ctx, item, op, move); err != nil {
		return Plan{}, fmt.Errorf("core: organize store reconcile: %w", err)
	}
	return plan, nil
}

// reconcileOrganize applies a successful Organize's effects to the
// store: Seen toggles Unread, AddLabels/RemoveLabels update Labels, and
// — when move relocated the item to a new address — the row is rekeyed
// to move.ID (with Meta["folder"] updated) instead of leaving a stale
// copy behind under the old id, so `bunker counts`/`list` reflect the
// move instead of the item lingering in the folder view it left.
func (s *Service) reconcileOrganize(ctx context.Context, item Item, op OrganizeOp, move OrganizeMove) error {
	updated := item
	if move.ID != "" {
		updated.ID = move.ID
	}
	if op.Seen != nil {
		updated.Unread = !*op.Seen
	}
	if len(op.AddLabels) > 0 || len(op.RemoveLabels) > 0 {
		updated.Labels = mergeLabels(item.Labels, op.AddLabels, op.RemoveLabels)
	}
	if move.Folder != "" {
		meta := make(map[string]string, len(item.Meta)+1)
		for k, v := range item.Meta {
			meta[k] = v
		}
		meta["folder"] = move.Folder
		updated.Meta = meta
	}

	if err := s.store.Upsert(ctx, updated); err != nil {
		return err
	}
	if updated.ID != item.ID {
		if err := s.store.Delete(ctx, item.ID); err != nil {
			return err
		}
	}
	return nil
}

// mergeLabels applies remove then add to current, deduplicated and
// sorted so the result is deterministic regardless of set ordering.
func mergeLabels(current, add, remove []string) []string {
	set := make(map[string]struct{}, len(current))
	for _, l := range current {
		set[l] = struct{}{}
	}
	for _, l := range remove {
		delete(set, l)
	}
	for _, l := range add {
		set[l] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for l := range set {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// PostStatus publishes a status/story on channel/account. dryRun returns
// the Plan alone.
func (s *Service) PostStatus(ctx context.Context, channel Channel, account string, status Status, dryRun bool) (Plan, Receipt, error) {
	plan := Plan{
		Action:  "status",
		Channel: channel,
		Account: account,
		Target:  account,
		Preview: status.Text,
	}

	adapter, err := s.adapterFor(channel, account)
	if err != nil {
		return Plan{}, Receipt{}, err
	}
	publisher, ok := adapter.(StatusPublisher)
	if !ok {
		return Plan{}, Receipt{}, fmt.Errorf("core: adapter %s/%s cannot post status: %w", channel, account, ErrUnsupported)
	}
	if dryRun {
		return plan, Receipt{}, nil
	}

	receipt, err := publisher.PostStatus(ctx, status)
	if err != nil {
		return Plan{}, Receipt{}, fmt.Errorf("core: post status failed: %w", err)
	}
	return plan, receipt, nil
}
