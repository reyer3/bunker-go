package whatsapp

import (
	"context"

	"github.com/purpshell/meowcaller"
)

// meowEngine adapts *meowcaller.Client to callEngine. meowcaller is a
// pure-Go (no CGO) implementation of WhatsApp's VoIP stack, including its
// MLow audio codec.
//
// go.mod pins meowcaller to 27a3c6b, its last commit built on upstream
// go.mau.fi/whatsmeow: later ones moved to the polymorfa/hypermeow fork,
// whose *whatsmeow.Client is a different type from the one this package
// already links against.
type meowEngine struct{ c *meowcaller.Client }

func (m meowEngine) Call(ctx context.Context, target string) (liveCall, error) {
	call, err := m.c.Call(ctx, target)
	if err != nil {
		return nil, err
	}
	return meowCall{call}, nil
}

func (m meowEngine) OnIncomingCall(fn func(liveCall)) {
	m.c.OnIncomingCall(func(call *meowcaller.Call) { fn(meowCall{call}) })
}

// meowCall adapts *meowcaller.Call to liveCall.
type meowCall struct{ *meowcaller.Call }

func (c meowCall) AttachAudio(src meowcaller.AudioSource, sink meowcaller.AudioSink) {
	if sink != nil {
		c.Receive(sink)
	}
	if src != nil {
		c.Play(src)
	}
}
