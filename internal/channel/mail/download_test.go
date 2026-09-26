package mail

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

func TestAdapterDownloadAttachmentReturnsPartBytes(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", multipartMessage)

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))

	ctx, cancel := context.WithCancel(context.Background())
	sink := newFakeSink()
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()
	seed := waitForUpsert(t, sink, 5*time.Second)
	cancel()
	<-done

	item, err := adapter.Fetch(context.Background(), seed.ID)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(item.Attachments) != 1 {
		t.Fatalf("len(Attachments) = %d, want 1", len(item.Attachments))
	}

	rc, err := adapter.DownloadAttachment(context.Background(), item, 0)
	if err != nil {
		t.Fatalf("DownloadAttachment() error = %v", err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(data) != "fake video bytes" {
		t.Errorf("data = %q, want %q", data, "fake video bytes")
	}
}

func TestAdapterDownloadAttachmentRejectsOutOfRangeIndex(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", multipartMessage)

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))

	ctx, cancel := context.WithCancel(context.Background())
	sink := newFakeSink()
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()
	seed := waitForUpsert(t, sink, 5*time.Second)
	cancel()
	<-done

	item, err := adapter.Fetch(context.Background(), seed.ID)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}

	_, err = adapter.DownloadAttachment(context.Background(), item, 3)
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("DownloadAttachment() error = %v, want ErrNotFound", err)
	}
}
