package core_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

// bookAdapter is a core.ContactLister spy.
type bookAdapter struct {
	callerAdapter
	book []core.Contact
	err  error
}

func (b *bookAdapter) Contacts(context.Context) ([]core.Contact, error) { return b.book, b.err }

func TestServiceContactsMergesBookAndConversations(t *testing.T) {
	wa := &bookAdapter{callerAdapter: callerAdapter{channel: core.ChannelWhatsApp, account: "personal"}, book: []core.Contact{
		{Name: "José Pérez", Address: "51911@s.whatsapp.net"},
		{Name: "Ana Díaz", Address: "51922@s.whatsapp.net"},
	}}
	store := newMemStore(
		core.Item{ID: "whatsapp:personal:1", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "51911@s.whatsapp.net", ThreadName: "José Pérez", From: core.Address{Name: "José Pérez"}},
		core.Item{ID: "whatsapp:personal:2", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "1203@g.us", ThreadName: "Equipo"},
		core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", From: core.Address{ID: "ana@example.cl", Name: "Ana Soto"}},
		core.Item{ID: "mail:cl:2", Channel: core.ChannelMail, Account: "cl", FromMe: true, From: core.Address{ID: "me@example.cl", Name: "Yo"}},
	)
	reg := core.NewRegistry()
	reg.Register(wa)
	svc := core.NewService(store, reg)

	all, err := svc.Contacts(context.Background(), core.ContactFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("contacts = %+v, want José (deduplicated), Ana Díaz, Equipo and Ana Soto; never the account's own address", all)
	}
	for _, c := range all {
		if c.Name == "José Pérez" && c.Thread != "51911@s.whatsapp.net" {
			t.Errorf("José lost his existing thread when merged: %+v", c)
		}
	}

	ana, err := svc.Contacts(context.Background(), core.ContactFilter{Query: "ana"})
	if err != nil || len(ana) != 2 {
		t.Fatalf("query ana = %+v, %v; want both Anas", ana, err)
	}
	jose, _ := svc.Contacts(context.Background(), core.ContactFilter{Query: "jose"})
	if len(jose) != 1 || jose[0].Name != "José Pérez" {
		t.Fatalf("query jose = %+v, want an accent-insensitive match", jose)
	}
	mail, _ := svc.Contacts(context.Background(), core.ContactFilter{Channel: core.ChannelMail})
	if len(mail) != 1 || mail[0].Address != "ana@example.cl" {
		t.Fatalf("mail contacts = %+v", mail)
	}
}

func TestServiceContactsFailsLoudly(t *testing.T) {
	broken := &bookAdapter{callerAdapter: callerAdapter{channel: core.ChannelWhatsApp, account: "personal"}, err: errors.New("store closed")}
	reg := core.NewRegistry()
	reg.Register(broken)
	if _, err := core.NewService(newMemStore(), reg).Contacts(context.Background(), core.ContactFilter{}); err == nil {
		t.Fatal("a failing address book should be an error, not an empty list")
	}
	broken.err = core.ErrUnsupported
	if _, err := core.NewService(newMemStore(), reg).Contacts(context.Background(), core.ContactFilter{}); err != nil {
		t.Fatalf("an adapter without a book should be skipped: %v", err)
	}
}

func TestResolveContact(t *testing.T) {
	book := []core.Contact{
		{Name: "Ana Díaz", Address: "a1"},
		{Name: "Ana Soto", Address: "a2"},
		{Name: "Anabel", Address: "a3"},
		{Name: "José Pérez", Address: "j1"},
	}
	if c, err := core.ResolveContact(book, "jose"); err != nil || c.Address != "j1" {
		t.Errorf("jose = %+v, %v", c, err)
	}
	if c, err := core.ResolveContact(book, "ana díaz"); err != nil || c.Address != "a1" {
		t.Errorf("exact name = %+v, %v", c, err)
	}
	_, err := core.ResolveContact(book, "ana")
	if err == nil || !strings.Contains(err.Error(), "Ana Díaz <a1>") || !strings.Contains(err.Error(), "Anabel <a3>") {
		t.Errorf("ambiguous = %v, want the candidates listed", err)
	}
	if _, err := core.ResolveContact(book, "pedro"); err == nil {
		t.Error("no match should be an error")
	}
}
