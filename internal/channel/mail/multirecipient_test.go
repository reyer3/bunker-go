package mail

import (
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

// TestAdapterImplementsMultiRecipientSender covers T13(a): mail addresses
// every To/Cc recipient in one SMTP submission (see Send), so
// core.Service must never fan it out into N single-recipient calls the
// way it does for WhatsApp/Matrix. Its presence alone is the marker; the
// method itself does nothing.
func TestAdapterImplementsMultiRecipientSender(t *testing.T) {
	var a Adapter
	var _ core.MultiRecipientSender = &a
}
