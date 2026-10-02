package core

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Call directions.
const (
	CallIncoming = "incoming"
	CallOutgoing = "outgoing"
)

// Call states. A call moves ringing/calling -> connecting -> active ->
// ended; an unanswered or rejected call goes straight to ended.
const (
	CallStateRinging    = "ringing"
	CallStateCalling    = "calling"
	CallStateConnecting = "connecting"
	CallStateActive     = "active"
	CallStateEnded      = "ended"
)

// Call is one live (or just-ended) voice call on a channel that supports
// them (today only WhatsApp, see Caller). ID is the channel's own call id,
// unique across every account the daemon runs, so answer/reject/hangup
// only need it (see Service.ControlCall).
type Call struct {
	ID        string
	Channel   Channel
	Account   string
	Peer      string
	PeerName  string
	Direction string
	State     string
	StartedAt time.Time
	// ConnectedAt is when media started flowing (zero until then, and
	// forever for a call nobody answered); EndedAt is when it ended.
	// Duration counts from ConnectedAt, so ringing time is excluded.
	ConnectedAt time.Time
	EndedAt     time.Time
	EndReason   string
	// AudioError says why this call has no (or one-way) sound: the audio
	// helper is missing or died, or no media ever arrived from the peer.
	// Empty while audio is fine. It is how a connected but silent call is
	// told apart from a working one in "bunker calls" and the TUI.
	AudioError string `json:"audio_error,omitempty"`
}

// Duration is how long the call has been (or was) connected as of now:
// zero while it has not connected yet.
func (c Call) Duration(now time.Time) time.Duration {
	if c.ConnectedAt.IsZero() {
		return 0
	}
	end := now
	if !c.EndedAt.IsZero() {
		end = c.EndedAt
	}
	if end.Before(c.ConnectedAt) {
		return 0
	}
	return end.Sub(c.ConnectedAt)
}

// FormatCallDuration renders d as m:ss (h:mm:ss from one hour up).
func FormatCallDuration(d time.Duration) string {
	s := int(d.Round(time.Second) / time.Second)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// CallAction is what Service.ControlCall does to a live call.
type CallAction string

const (
	CallAnswer CallAction = "answer"
	CallReject CallAction = "reject"
	CallHangup CallAction = "hangup"
)

func (a CallAction) valid() bool {
	return a == CallAnswer || a == CallReject || a == CallHangup
}

// Caller is an optional capability: an adapter that can place and control
// voice calls implements it. CanCall reports whether calling is actually
// available on this account (nil) or why not (wrapping ErrUnsupported),
// since calling is opt-in per account: Service checks it before planning,
// so a dry-run never promises a call the adapter would refuse.
type Caller interface {
	CanCall() error
	PlaceCall(ctx context.Context, to string) (Call, error)
	ControlCall(ctx context.Context, callID string, action CallAction) (Call, error)
	// ActiveCalls lists this account's calls that have not ended yet.
	ActiveCalls() []Call
}

// PlaceCall starts a voice call to to on channel/account. dryRun returns
// the Plan alone and never touches the network.
func (s *Service) PlaceCall(ctx context.Context, channel Channel, account, to string, dryRun bool) (Plan, Call, error) {
	to = strings.TrimSpace(to)
	if to == "" {
		return Plan{}, Call{}, fmt.Errorf("core: call: no recipient given")
	}
	caller, err := s.callerFor(channel, account)
	if err != nil {
		return Plan{}, Call{}, err
	}
	plan := Plan{
		Action:     "call",
		Channel:    channel,
		Account:    account,
		Target:     to,
		Recipients: []string{to},
	}
	if dryRun {
		return plan, Call{}, nil
	}
	call, err := caller.PlaceCall(ctx, to)
	if err != nil {
		return Plan{}, Call{}, fmt.Errorf("core: call failed: %w", err)
	}
	return plan, call, nil
}

// ControlCall answers, rejects or hangs up the live call callID, whichever
// account owns it. dryRun returns the Plan and the call's current state
// without changing it.
func (s *Service) ControlCall(ctx context.Context, callID string, action CallAction, dryRun bool) (Plan, Call, error) {
	if !action.valid() {
		return Plan{}, Call{}, fmt.Errorf("core: call: unknown action %q (want answer, reject or hangup)", action)
	}
	for _, adapter := range s.registry.List() {
		caller, ok := adapter.(Caller)
		if !ok {
			continue
		}
		for _, c := range caller.ActiveCalls() {
			if c.ID != callID {
				continue
			}
			plan := Plan{
				Action:  "call " + string(action),
				Channel: c.Channel,
				Account: c.Account,
				Target:  c.Peer,
			}
			if dryRun {
				return plan, c, nil
			}
			updated, err := caller.ControlCall(ctx, callID, action)
			if err != nil {
				return Plan{}, Call{}, fmt.Errorf("core: call %s failed: %w", action, err)
			}
			return plan, updated, nil
		}
	}
	return Plan{}, Call{}, fmt.Errorf("core: call %q: %w", callID, ErrNotFound)
}

// Calls lists every account's live calls, oldest first.
func (s *Service) Calls(ctx context.Context) ([]Call, error) {
	var out []Call
	for _, adapter := range s.registry.List() {
		if caller, ok := adapter.(Caller); ok {
			out = append(out, caller.ActiveCalls()...)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out, nil
}

func (s *Service) callerFor(channel Channel, account string) (Caller, error) {
	adapter, err := s.adapterFor(channel, account)
	if err != nil {
		return nil, err
	}
	caller, ok := adapter.(Caller)
	if !ok {
		return nil, fmt.Errorf("core: adapter %s/%s cannot place calls: %w", channel, account, ErrUnsupported)
	}
	if err := caller.CanCall(); err != nil {
		return nil, fmt.Errorf("core: adapter %s/%s: %w", channel, account, err)
	}
	return caller, nil
}
