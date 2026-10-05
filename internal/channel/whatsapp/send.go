package whatsapp

import (
	"context"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"

	"github.com/reyer3/bunker-go/internal/core"
)

// pacingWait returns how long to wait before the next send, given when
// the last one went out, the current time and the configured minimum
// interval. It never returns a negative duration.
func pacingWait(lastSend, now time.Time, minInterval time.Duration) time.Duration {
	if lastSend.IsZero() {
		return 0
	}
	wait := minInterval - now.Sub(lastSend)
	if wait < 0 {
		return 0
	}
	return wait
}

// waitForPacing blocks until this adapter's minimum send interval has
// elapsed since the last send, or ctx is done. It never lets Send become
// a bulk API: every send observes the same human pacing.
func (a *Adapter) waitForPacing(ctx context.Context) error {
	a.mu.Lock()
	wait := pacingWait(a.lastSend, time.Now(), a.minSendInterval)
	a.mu.Unlock()

	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}

	a.mu.Lock()
	a.lastSend = time.Now()
	a.mu.Unlock()
	return nil
}

// buildQuoteContext builds the ContextInfo that quotes the item replyTo
// identifies, filling in the quoted body from this adapter's cache when
// the item is still known. outgoingContext uses it for every reply (text,
// media and voice notes), so all of them quote the original the same way.
func (a *Adapter) buildQuoteContext(replyTo string) (*waE2E.ContextInfo, error) {
	_, quotedChat, quotedMsgID, err := parseItemID(replyTo)
	if err != nil {
		return nil, fmt.Errorf("reply to %q: %w", replyTo, err)
	}
	participant := quotedChat
	ctxInfo := &waE2E.ContextInfo{
		StanzaID: ptrString(quotedMsgID),
	}
	if cached, ok := a.cachedItem(replyTo); ok {
		if cached.From.ID != "" {
			participant = cached.From.ID
		}
		if cached.Body != "" {
			ctxInfo.QuotedMessage = &waE2E.Message{Conversation: ptrString(cached.Body)}
		}
	}
	ctxInfo.Participant = ptrString(participant)
	return ctxInfo, nil
}

// forwardingScore is the score a forward carries. core.Item does not
// record whether the original was itself a forward, so bunker never
// claims more than one hop (WhatsApp shows "forwarded many times" only
// from a higher score).
const forwardingScore = 1

// forwardedContext is the ContextInfo that makes WhatsApp clients show
// the native "Forwarded" label.
func forwardedContext() *waE2E.ContextInfo {
	return &waE2E.ContextInfo{IsForwarded: ptrBool(true), ForwardingScore: ptrUint32(forwardingScore)}
}

// outgoingContext builds the ContextInfo out's first message carries: a
// reply's quote of the original (buildQuoteContext), the forwarded label
// when out.Forward is set, both, or nil when neither applies. Send,
// SendMedia and SendVoice all go through it, so text, media and voice
// notes quote and forward the same way.
func (a *Adapter) outgoingContext(out core.Outgoing) (*waE2E.ContextInfo, error) {
	var ctxInfo *waE2E.ContextInfo
	if out.ReplyTo != "" {
		var err error
		ctxInfo, err = a.buildQuoteContext(out.ReplyTo)
		if err != nil {
			return nil, err
		}
	}
	if out.Forward {
		if ctxInfo == nil {
			ctxInfo = &waE2E.ContextInfo{}
		}
		fwd := forwardedContext()
		ctxInfo.IsForwarded, ctxInfo.ForwardingScore = fwd.IsForwarded, fwd.ForwardingScore
	}
	return ctxInfo, nil
}

// buildOutgoingMessage turns an Outgoing into the waE2E.Message to send.
// A reply carries a ContextInfo quoting the original message, filled in
// from this adapter's cache when the quoted item is still known; a
// forward carries the forwarded label. Either needs an
// ExtendedTextMessage, since a plain Conversation has no ContextInfo.
func (a *Adapter) buildOutgoingMessage(out core.Outgoing) (*waE2E.Message, error) {
	ctxInfo, err := a.outgoingContext(out)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: send: %w", err)
	}
	if ctxInfo == nil {
		return &waE2E.Message{Conversation: ptrString(out.Body)}, nil
	}
	return &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text:        ptrString(out.Body),
			ContextInfo: ctxInfo,
		},
	}, nil
}

// Send delivers out to a single JID or +E164 phone number, quoting the
// replied-to message when ReplyTo is set. Every send performs the human-
// emulation choreography (see withHumanEmulation) and paces itself
// against minSendInterval: never a bulk API.
func (a *Adapter) Send(ctx context.Context, out core.Outgoing) (core.Receipt, error) {
	jid, err := a.resolveTarget(ctx, out)
	if err != nil {
		return core.Receipt{}, err
	}
	msg, err := a.buildOutgoingMessage(out)
	if err != nil {
		return core.Receipt{}, err
	}

	var resp whatsmeow.SendResponse
	err = a.withHumanEmulation(ctx, jid, out.Body, false, func() error {
		if err := a.waitForPacing(ctx); err != nil {
			return fmt.Errorf("whatsapp: send: %w", err)
		}
		var sendErr error
		resp, sendErr = a.cli.SendMessage(ctx, jid, msg)
		if sendErr != nil {
			return fmt.Errorf("whatsapp: send: %w", sendErr)
		}
		return nil
	})
	if err != nil {
		return core.Receipt{}, err
	}
	return a.sentReceipt(ctx, jid, resp), nil
}

func ptrString(s string) *string { return &s }

func ptrUint32(v uint32) *uint32 { return &v }
