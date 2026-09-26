package mail

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// PasswordSource resolves the plain-text password for a LOGIN-auth
// account (the .cl Dovecot mailbox). It is a port so tests can fake it
// and so a real source (GOA, then an env file) can be swapped without
// touching adapter code.
type PasswordSource interface {
	Password(ctx context.Context, account string) (string, error)
}

// TokenSource resolves a bearer access token for an XOAUTH2-auth account
// (Gmail via Google Workspace). It is a port for the same reason as
// PasswordSource.
type TokenSource interface {
	Token(ctx context.Context, account string) (string, error)
}

// EnvFilePasswordSource reads a password from a simple "KEY=VALUE" file,
// the fallback used when GOA has no PasswordBased credential for an
// account (the mail MCP server already keeps one at this path). Values
// may be wrapped in single or double quotes.
type EnvFilePasswordSource struct {
	// Path to the env file, e.g. ~/.config/bunker-go/mail-cl.env.
	Path string
	// Key is the variable name to read. Defaults to "MAIL_PASSWORD".
	Key string
}

// Password implements PasswordSource.
func (s EnvFilePasswordSource) Password(_ context.Context, account string) (string, error) {
	key := s.Key
	if key == "" {
		key = "MAIL_PASSWORD"
	}

	data, err := os.ReadFile(s.Path)
	if err != nil {
		return "", fmt.Errorf("mail: read password file %s for account %q: %w", s.Path, account, err)
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != key {
			continue
		}
		return strings.Trim(strings.TrimSpace(v), `'"`), nil
	}
	return "", fmt.Errorf("mail: %s not found in %s for account %q", key, s.Path, account)
}
