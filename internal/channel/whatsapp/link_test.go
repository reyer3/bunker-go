package whatsapp

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"go.mau.fi/whatsmeow"
)

func TestLinkWithClientRendersQRAndSucceeds(t *testing.T) {
	cli := newFakeWAClient()
	cli.qrChan = make(chan whatsmeow.QRChannelItem, 2)
	cli.qrChan <- whatsmeow.QRChannelItem{Event: "code", Code: "1@abc,def,ghi=="}
	cli.qrChan <- whatsmeow.QRChannelSuccess
	close(cli.qrChan)

	var out bytes.Buffer
	if err := linkWithClient(context.Background(), cli, &out); err != nil {
		t.Fatalf("linkWithClient() error = %v", err)
	}
	if out.Len() == 0 {
		t.Error("no QR code was rendered to out")
	}
	if !cli.IsConnected() {
		t.Error("Connect() was never called")
	}
}

func TestLinkWithClientAlreadyLinked(t *testing.T) {
	cli := newFakeWAClient()
	cli.linked = true

	var out bytes.Buffer
	err := linkWithClient(context.Background(), cli, &out)
	if !errors.Is(err, ErrAlreadyLinked) {
		t.Fatalf("linkWithClient() error = %v, want ErrAlreadyLinked", err)
	}
	if cli.IsConnected() {
		t.Error("Connect() was called although the device was already linked")
	}
}

func TestLinkWithClientReportsTimeout(t *testing.T) {
	cli := newFakeWAClient()
	cli.qrChan = make(chan whatsmeow.QRChannelItem, 1)
	cli.qrChan <- whatsmeow.QRChannelTimeout
	close(cli.qrChan)

	var out bytes.Buffer
	err := linkWithClient(context.Background(), cli, &out)
	if err == nil {
		t.Fatal("linkWithClient() error = nil, want an error on QR timeout")
	}
}

func TestLinkWithClientConnectError(t *testing.T) {
	cli := newFakeWAClient()
	cli.qrChan = make(chan whatsmeow.QRChannelItem)
	cli.connectErr = errors.New("boom")

	var out bytes.Buffer
	err := linkWithClient(context.Background(), cli, &out)
	if err == nil {
		t.Fatal("linkWithClient() error = nil, want the Connect error")
	}
}
