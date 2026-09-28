package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

func contactsBackend() *fakeBackend {
	return &fakeBackend{contacts: []core.Contact{
		{Channel: core.ChannelWhatsApp, Account: "personal", Name: "José Pérez", Address: "51911@s.whatsapp.net"},
		{Channel: core.ChannelWhatsApp, Account: "personal", Name: "Ana Díaz", Address: "51922@s.whatsapp.net"},
		{Channel: core.ChannelWhatsApp, Account: "personal", Name: "Ana Soto", Address: "51933@s.whatsapp.net"},
		{Channel: core.ChannelMail, Account: "cl", Name: "José Pérez", Address: "jose@example.cl"},
	}}
}

func TestLooksLikeName(t *testing.T) {
	names := []string{"Ana", "josé pérez", "Equipo 2"}
	addresses := []string{"+51 999", "5511999", "a@b.cl", "51911@s.whatsapp.net", "@bob:example.org", "!room:example.org", "#sala:example.org", " "}
	for _, n := range names {
		if !looksLikeName(n) {
			t.Errorf("%q should be a name", n)
		}
	}
	for _, a := range addresses {
		if looksLikeName(a) {
			t.Errorf("%q should be an address", a)
		}
	}
}

func TestCmdContactsJSON(t *testing.T) {
	backend := contactsBackend()
	code, out, _ := runCallCmd(t, backend, "contacts", "jose", "--channel", "whatsapp", "--json")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	var got struct{ Contacts []core.Contact }
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout %q: %v", out, err)
	}
	if len(backend.contactsCalls) != 1 || backend.contactsCalls[0].Query != "jose" || backend.contactsCalls[0].Channel != core.ChannelWhatsApp {
		t.Fatalf("filter = %+v", backend.contactsCalls)
	}
	if len(got.Contacts) == 0 {
		t.Fatal("no contacts in JSON")
	}
}

func TestCmdSendAndCallResolveNames(t *testing.T) {
	backend := contactsBackend()
	code, _, stderr := runCallCmd(t, backend, "send", "whatsapp", "personal", "jose", "hola", "--dry-run")
	if code != 0 || len(backend.sendCalls) != 1 || backend.sendCalls[0].To[0] != "51911@s.whatsapp.net" {
		t.Fatalf("send by name: code %d, calls %+v, stderr %q", code, backend.sendCalls, stderr)
	}
	if !strings.Contains(stderr, "José Pérez <51911@s.whatsapp.net>") {
		t.Errorf("the resolution should be shown: %q", stderr)
	}

	code, _, _ = runCallCmd(t, backend, "call", "whatsapp", "personal", "Ana Díaz", "--dry-run")
	if code != 0 || len(backend.placeCallCalls) != 1 || backend.placeCallCalls[0].To != "51922@s.whatsapp.net" {
		t.Fatalf("call by name: code %d, calls %+v", code, backend.placeCallCalls)
	}

	// Mail resolves within its own account: the WhatsApp José is not a
	// candidate there.
	code, _, _ = runCallCmd(t, backend, "send", "mail", "cl", "jose", "hola", "--dry-run")
	if code != 0 || backend.sendCalls[1].To[0] != "jose@example.cl" {
		t.Fatalf("mail send by name: code %d, calls %+v", code, backend.sendCalls)
	}
}

func TestCmdSendAmbiguousNameFails(t *testing.T) {
	backend := contactsBackend()
	code, _, stderr := runCallCmd(t, backend, "send", "whatsapp", "personal", "ana", "hola", "--dry-run")
	if code == 0 || len(backend.sendCalls) != 0 {
		t.Fatalf("an ambiguous name must not send: code %d, calls %+v", code, backend.sendCalls)
	}
	if !strings.Contains(stderr, "Ana Díaz") || !strings.Contains(stderr, "Ana Soto") {
		t.Fatalf("stderr %q should list the candidates", stderr)
	}
	code, _, _ = runCallCmd(t, backend, "call", "whatsapp", "personal", "pedro", "--dry-run")
	if code == 0 || len(backend.placeCallCalls) != 0 {
		t.Fatal("an unknown name must not call")
	}
}

func TestCmdSendAddressSkipsContacts(t *testing.T) {
	backend := contactsBackend()
	if code, _, _ := runCallCmd(t, backend, "send", "whatsapp", "personal", "+51911", "hola", "--dry-run"); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if len(backend.contactsCalls) != 0 {
		t.Fatal("an address should go straight through, without a contacts lookup")
	}
}

func TestCmdUnread(t *testing.T) {
	backend := &fakeBackend{unreadLocal: true}
	code, out, _ := runCallCmd(t, backend, "unread", "whatsapp:wa:1", "--json")
	if code != 0 || len(backend.unreadCalls) != 1 || !strings.Contains(out, `"local_only":true`) {
		t.Fatalf("code %d, calls %v, out %q", code, backend.unreadCalls, out)
	}
	if code, _, _ := runCallCmd(t, backend, "unread"); code != 2 {
		t.Fatalf("missing id: code %d, want usage error", code)
	}
}
