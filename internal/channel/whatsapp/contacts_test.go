package whatsapp

import (
	"context"
	"errors"
	"testing"

	"go.mau.fi/whatsmeow/types"

	"github.com/reyer3/bunker-go/internal/core"
)

type fakeDirectory struct {
	contacts map[types.JID]types.ContactInfo
	err      error
}

func (f fakeDirectory) AllContacts(context.Context) (map[types.JID]types.ContactInfo, error) {
	return f.contacts, f.err
}

func TestContactsListsNamedPeople(t *testing.T) {
	a := newTestAdapter("personal", newFakeWAClient())
	if _, err := a.Contacts(context.Background()); !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("no directory = %v, want ErrUnsupported", err)
	}
	a.SetContactDirectory(fakeDirectory{contacts: map[types.JID]types.ContactInfo{
		types.NewJID("51911", types.DefaultUserServer): {FullName: "José Pérez", PushName: "Pepe"},
		types.NewJID("51922", types.DefaultUserServer): {PushName: "Ana"},
		types.NewJID("51933", types.DefaultUserServer): {},
		types.NewJID("777", types.HiddenUserServer):    {FullName: "Solo LID"},
	}})
	got, err := a.Contacts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, c := range got {
		names[c.Name] = c.Address
	}
	if len(got) != 2 || names["José Pérez"] != "51911@s.whatsapp.net" || names["Ana"] != "51922@s.whatsapp.net" {
		t.Fatalf("contacts = %+v; want the saved name first, push names as fallback, no nameless or LID entries", got)
	}

	a.SetContactDirectory(fakeDirectory{err: errors.New("db locked")})
	if _, err := a.Contacts(context.Background()); err == nil || errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("a store error = %v, want it surfaced", err)
	}
}

func TestCallNumber(t *testing.T) {
	cases := map[string]string{
		"+51911":               "51911",
		" 51911 ":              "51911",
		"51911@s.whatsapp.net": "51911",
	}
	for in, want := range cases {
		if got, err := callNumber(in); err != nil || got != want {
			t.Errorf("callNumber(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := callNumber("1203@g.us"); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("calling a group = %v, want ErrUnsupported", err)
	}
}
