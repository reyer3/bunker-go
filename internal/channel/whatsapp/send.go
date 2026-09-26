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
// the item is still known. It is shared by buildOutgoingMessage (a plain
// text reply) and SendMedia (a reply carrying attachments), so both quote
// the original message the same way.
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

// buildOutgoingMessage turns an Outgoing into the waE2E.Message to send.
// A reply carries a ContextInfo quoting the original message, filled in
// from this adapter's cache when the quoted item is still known.
func (a *Adapter) buildOutgoingMessage(out core.Outgoing) (*waE2E.Message, error) {
	if out.ReplyTo == "" {
		return &waE2E.Message{Conversation: ptrString(out.Body)}, nil
	}

	ctxInfo, err := a.buildQuoteContext(out.ReplyTo)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: send: %w", err)
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
	return core.Receipt{
		ID:      itemID(a.account, jid.String(), string(resp.ID)),
		Channel: core.ChannelWhatsApp,
		At:      resp.Timestamp,
	}, nil
}

func ptrString(s string) *string { return &s }
