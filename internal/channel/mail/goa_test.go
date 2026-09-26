package mail

import (
	"context"
	"errors"
	"testing"
)

// fakeGOABus is an in-memory goaBus used by every GOA test: it never
// touches a real session bus.
type fakeGOABus struct {
	paths      []string
	identities map[string]string // path -> PresentationIdentity
	hosts      map[string]string // path -> Mail ImapHost
	users      map[string]string // path -> Mail ImapUserName
	tokens     map[string]string // path -> OAuth2 access token
	passwords  map[string]string // path -> PasswordBased password
}

func (b *fakeGOABus) AccountPaths(context.Context) ([]string, error) {
	return b.paths, nil
}

func (b *fakeGOABus) MailAccount(_ context.Context, path string) (GOAMailAccount, error) {
	return GOAMailAccount{
		Identity: b.identities[path],
		Host:     b.hosts[path],
		User:     b.users[path],
	}, nil
}

func (b *fakeGOABus) AccessToken(_ context.Context, path string) (string, error) {
	if tok, ok := b.tokens[path]; ok {
		return tok, nil
	}
	return "", errors.New("fakeGOABus: no access token")
}

func (b *fakeGOABus) Password(_ context.Context, path string) (string, error) {
	if pw, ok := b.passwords[path]; ok {
		return pw, nil
	}
	return "", errors.New("fakeGOABus: no password")
}

func TestGOATokenSourceToken(t *testing.T) {
	bus := &fakeGOABus{
		paths:      []string{"/org/gnome/OnlineAccounts/Accounts/account_1", "/org/gnome/OnlineAccounts/Accounts/account_2"},
		identities: map[string]string{"/org/gnome/OnlineAccounts/Accounts/account_2": "alice@example.com"},
		hosts:      map[string]string{"/org/gnome/OnlineAccounts/Accounts/account_2": "imap.gmail.com"},
		tokens:     map[string]string{"/org/gnome/OnlineAccounts/Accounts/account_2": "ya29.fake-token"},
	}
	src := GOATokenSource{Bus: bus, Identity: "alice@example.com"}

	got, err := src.Token(context.Background(), "com")
	if err != nil {
		t.Fatalf("Token() error = %v", err)
	}
	if got != "ya29.fake-token" {
		t.Errorf("Token() = %q, want %q", got, "ya29.fake-token")
	}
}

func TestGOATokenSourceNoMatchingIdentity(t *testing.T) {
	bus := &fakeGOABus{
		paths:      []string{"/org/gnome/OnlineAccounts/Accounts/account_1"},
		identities: map[string]string{"/org/gnome/OnlineAccounts/Accounts/account_1": "someone-else@example.com"},
		hosts:      map[string]string{"/org/gnome/OnlineAccounts/Accounts/account_1": "imap.gmail.com"},
	}
	src := GOATokenSource{Bus: bus, Identity: "alice@example.com"}

	if _, err := src.Token(context.Background(), "com"); !errors.Is(err, ErrGOAAccountNotFound) {
		t.Errorf("Token() error = %v, want ErrGOAAccountNotFound", err)
	}
}

func TestGOATokenSourceSkipsAccountWithNoImapHost(t *testing.T) {
	bus := &fakeGOABus{
		paths:      []string{"/org/gnome/OnlineAccounts/Accounts/account_1"},
		identities: map[string]string{"/org/gnome/OnlineAccounts/Accounts/account_1": "alice@example.com"},
		// no Mail interface / ImapHost: this account isn't a mailbox (e.g. a
		// calendar-only or Google Drive GOA entry).
		tokens: map[string]string{"/org/gnome/OnlineAccounts/Accounts/account_1": "ya29.fake-token"},
	}
	src := GOATokenSource{Bus: bus, Identity: "alice@example.com"}

	if _, err := src.Token(context.Background(), "com"); !errors.Is(err, ErrGOAAccountNotFound) {
		t.Errorf("Token() error = %v, want ErrGOAAccountNotFound", err)
	}
}

func TestGOAPasswordSourcePassword(t *testing.T) {
	bus := &fakeGOABus{
		paths:      []string{"/org/gnome/OnlineAccounts/Accounts/account_1"},
		identities: map[string]string{"/org/gnome/OnlineAccounts/Accounts/account_1": "alice@example.cl"},
		hosts:      map[string]string{"/org/gnome/OnlineAccounts/Accounts/account_1": "mail.example.cl"},
		passwords:  map[string]string{"/org/gnome/OnlineAccounts/Accounts/account_1": "dovecot-pass"},
	}
	src := GOAPasswordSource{Bus: bus, Identity: "alice@example.cl"}

	got, err := src.Password(context.Background(), "cl")
	if err != nil {
		t.Fatalf("Password() error = %v", err)
	}
	if got != "dovecot-pass" {
		t.Errorf("Password() = %q, want %q", got, "dovecot-pass")
	}
}

func TestGOAPasswordSourceFallsBackWhenGOAHasNoPassword(t *testing.T) {
	bus := &fakeGOABus{
		paths:      []string{"/org/gnome/OnlineAccounts/Accounts/account_1"},
		identities: map[string]string{"/org/gnome/OnlineAccounts/Accounts/account_1": "alice@example.cl"},
		hosts:      map[string]string{"/org/gnome/OnlineAccounts/Accounts/account_1": "mail.example.cl"},
		// no password registered for this path
	}
	src := GOAPasswordSource{Bus: bus, Identity: "alice@example.cl"}

	if _, err := src.Password(context.Background(), "cl"); !errors.Is(err, ErrGOAAccountNotFound) {
		t.Errorf("Password() error = %v, want ErrGOAAccountNotFound", err)
	}
}

// FallbackPasswordSource chains sources, using the first that succeeds.
func TestFallbackPasswordSource(t *testing.T) {
	primary := stubPasswordSource{err: ErrGOAAccountNotFound}
	fallback := stubPasswordSource{password: "env-password"}
	src := FallbackPasswordSource{primary, fallback}

	got, err := src.Password(context.Background(), "cl")
	if err != nil {
		t.Fatalf("Password() error = %v", err)
	}
	if got != "env-password" {
		t.Errorf("Password() = %q, want %q", got, "env-password")
	}
}

func TestFallbackPasswordSourceAllFail(t *testing.T) {
	src := FallbackPasswordSource{
		stubPasswordSource{err: errors.New("boom")},
		stubPasswordSource{err: errors.New("bang")},
	}
	if _, err := src.Password(context.Background(), "cl"); err == nil {
		t.Error("Password() error = nil, want an error when every source fails")
	}
}

type stubPasswordSource struct {
	password string
	err      error
}

func (s stubPasswordSource) Password(context.Context, string) (string, error) {
	return s.password, s.err
}
