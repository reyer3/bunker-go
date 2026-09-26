package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
)

// ErrAlreadyLinked is returned by Link when the account already has a
// stored device: pairing again would require an explicit unlink first.
var ErrAlreadyLinked = errors.New("whatsapp: account is already linked")

// linkWithClient drives the QR-pairing handshake against cli, rendering
// each code to out with a maintained terminal QR renderer. It never
// dials WhatsApp itself outside of cli.Connect/GetQRChannel, so it is
// fully testable with a fake client and a fake QR channel.
func linkWithClient(ctx context.Context, cli waClient, out io.Writer) error {
	if cli.IsLinked() {
		return ErrAlreadyLinked
	}

	qrChan, err := cli.GetQRChannel(ctx)
	if err != nil {
		return fmt.Errorf("whatsapp: link: get QR channel: %w", err)
	}
	if err := cli.Connect(); err != nil {
		return fmt.Errorf("whatsapp: link: connect: %w", err)
	}

	for evt := range qrChan {
		switch evt.Event {
		case "code":
			fmt.Fprintln(out, "Scan this QR code with WhatsApp > Linked Devices:")
			qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, out)
		case whatsmeow.QRChannelSuccess.Event:
			fmt.Fprintln(out, "WhatsApp linked successfully.")
			return nil
		case whatsmeow.QRChannelTimeout.Event:
			return fmt.Errorf("whatsapp: link: QR code timed out before it was scanned")
		case whatsmeow.QRChannelErrUnexpectedEvent.Event:
			return fmt.Errorf("whatsapp: link: unexpected pairing state")
		default:
			if evt.Error != nil {
				return fmt.Errorf("whatsapp: link: %w", evt.Error)
			}
			fmt.Fprintf(out, "whatsapp link: %s\n", evt.Event)
		}
	}
	return nil
}
