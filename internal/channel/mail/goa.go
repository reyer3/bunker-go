package mail

import (
	"context"
	"errors"
	"fmt"
)

// ErrGOAAccountNotFound is returned when no GNOME Online Accounts entry
// matches the identity a GOATokenSource/GOAPasswordSource was configured
// for, or when that entry has no mail host (it isn't a mailbox account).
var ErrGOAAccountNotFound = errors.New("mail: goa account not found")

// GOAMailAccount is the subset of a GOA account's "Mail" interface
// properties bunker-go needs.
type GOAMailAccount struct {
	// Identity is the account's PresentationIdentity, e.g. an email
	// address, from its "Account" interface.
	Identity string
	// Host is ImapHost. An empty Host means this GOA account has no Mail
	// interface at all (e.g. a calendar-only or Drive-only account).
	Host string
	User string
}

// goaBus is the low-level GNOME Online Accounts D-Bus surface bunker-go
// depends on. goaDBusConn implements it for real, over godbus; tests use
// an in-memory fake so nothing here ever touches a real session bus.
type goaBus interface {
	// AccountPaths lists every object path under
	// /org/gnome/OnlineAccounts/Accounts.
	AccountPaths(ctx context.Context) ([]string, error)
	// MailAccount reads an account's Account+Mail properties.
	MailAccount(ctx context.Context, path string) (GOAMailAccount, error)
	// AccessToken calls OAuth2Based.GetAccessToken on path.
	AccessToken(ctx context.Context, path string) (string, error)
	// Password calls PasswordBased.GetPassword on path.
	Password(ctx context.Context, path string) (string, error)
}

// findAccount locates the GOA object path whose PresentationIdentity is
// identity and which has a mail host, porting the matching logic from
// bunker/accounts.py's discover().
func findAccount(ctx context.Context, bus goaBus, identity string) (string, error) {
	paths, err := bus.AccountPaths(ctx)
	if err != nil {
		return "", fmt.Errorf("mail: list goa accounts: %w", err)
	}
	for _, path := range paths {
		acc, err := bus.MailAccount(ctx, path)
		if err != nil || acc.Identity != identity || acc.Host == "" {
			continue
		}
		return path, nil
	}
	return "", fmt.Errorf("mail: goa account %q: %w", identity, ErrGOAAccountNotFound)
}

// GOATokenSource resolves an XOAUTH2 access token from GNOME Online
// Accounts for the account whose PresentationIdentity is Identity.
type GOATokenSource struct {
	Bus      goaBus
	Identity string
}

// Token implements TokenSource.
func (s GOATokenSource) Token(ctx context.Context, _ string) (string, error) {
	path, err := findAccount(ctx, s.Bus, s.Identity)
	if err != nil {
		return "", err
	}
	token, err := s.Bus.AccessToken(ctx, path)
	if err != nil {
		return "", fmt.Errorf("mail: goa access token for %q: %w", s.Identity, err)
	}
	return token, nil
}

// GOAPasswordSource resolves a password from GNOME Online Accounts for
// the account whose PresentationIdentity is Identity. Accounts without a
// PasswordBased credential (or without a matching entry at all) report
// ErrGOAAccountNotFound so callers can fall back, e.g. to an env file.
type GOAPasswordSource struct {
	Bus      goaBus
	Identity string
}

// Password implements PasswordSource.
func (s GOAPasswordSource) Password(ctx context.Context, _ string) (string, error) {
	path, err := findAccount(ctx, s.Bus, s.Identity)
	if err != nil {
		return "", err
	}
	password, err := s.Bus.Password(ctx, path)
	if err != nil {
		return "", fmt.Errorf("mail: goa password for %q: %w", s.Identity, ErrGOAAccountNotFound)
	}
	return password, nil
}

// FallbackPasswordSource tries each PasswordSource in order, returning
// the first success. Used to try GOA first, then an env file.
type FallbackPasswordSource []PasswordSource

// Password implements PasswordSource.
func (s FallbackPasswordSource) Password(ctx context.Context, account string) (string, error) {
	var errs []error
	for _, src := range s {
		password, err := src.Password(ctx, account)
		if err == nil {
			return password, nil
		}
		errs = append(errs, err)
	}
	return "", fmt.Errorf("mail: no password source succeeded for %q: %w", account, errors.Join(errs...))
}
