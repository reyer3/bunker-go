package mail

import (
	"context"
	"fmt"

	"github.com/emersion/go-imap/v2/imapclient"
)

// dialReal is the production dialFunc: TLS to cfg.IMAPHost:IMAPPort, then
// LOGIN or AUTHENTICATE XOAUTH2 depending on cfg.Auth.
func dialReal(ctx context.Context, cfg AccountConfig, passwordSource PasswordSource, tokenSource TokenSource, handler *imapclient.UnilateralDataHandler) (*imapclient.Client, error) {
	addr := fmt.Sprintf("%s:%d", cfg.IMAPHost, cfg.IMAPPort)
	client, err := imapclient.DialTLS(addr, &imapclient.Options{UnilateralDataHandler: handler})
	if err != nil {
		return nil, fmt.Errorf("mail: dial %s: %w", addr, err)
	}

	switch cfg.Auth {
	case AuthXOAuth2:
		token, err := tokenSource.Token(ctx, cfg.Name)
		if err != nil {
			client.Close()
			return nil, fmt.Errorf("mail: get xoauth2 token for %q: %w", cfg.Name, err)
		}
		if err := client.Authenticate(newXOAuth2Client(cfg.Username, token)); err != nil {
			client.Close()
			return nil, fmt.Errorf("mail: xoauth2 authenticate %q: %w", cfg.Name, err)
		}
	default:
		password, err := passwordSource.Password(ctx, cfg.Name)
		if err != nil {
			client.Close()
			return nil, fmt.Errorf("mail: get password for %q: %w", cfg.Name, err)
		}
		if err := client.Login(cfg.Username, password).Wait(); err != nil {
			client.Close()
			return nil, fmt.Errorf("mail: login %q: %w", cfg.Name, err)
		}
	}

	return client, nil
}
