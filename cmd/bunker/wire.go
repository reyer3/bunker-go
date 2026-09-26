package main

import (
	"github.com/reyer3/bunker-go/internal/channel/mail"
	"github.com/reyer3/bunker-go/internal/channel/matrix"
	"github.com/reyer3/bunker-go/internal/channel/whatsapp"
)

// init wires every L2 channel package's constructor into
// adapterConstructors. cmd/bunker is the only place this can happen: it is
// package main, and none of mail, whatsapp or matrix can import it back to
// self-register from their own init() (see internal/channel/whatsapp's
// register.go).
func init() {
	RegisterAdapter("mail", mail.NewAdapter)
	RegisterAdapter("whatsapp", whatsapp.NewFromAccount)
	RegisterAdapter("matrix", matrix.New)
}
