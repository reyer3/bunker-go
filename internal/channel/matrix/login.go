package matrix

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"

	"maunium.net/go/mautrix"
)

// ssoCallback is a short-lived local HTTP server that captures the
// loginToken query parameter Synapse's SSO flow redirects the browser to
// after a successful SAML/SSO round trip.
type ssoCallback struct {
	listener net.Listener
	server   *http.Server
	tokens   chan string
}

func newSSOCallback() (*ssoCallback, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("matrix: listen for SSO callback: %w", err)
	}

	cb := &ssoCallback{listener: listener, tokens: make(chan string, 1)}
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("loginToken")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if token == "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintln(w, "missing loginToken")
			return
		}
		fmt.Fprintln(w, "bunker-go: signed in, you can close this tab.")
		select {
		case cb.tokens <- token:
		default:
		}
	})
	cb.server = &http.Server{Handler: mux}
	go cb.server.Serve(listener)
	return cb, nil
}

// URL is the local callback address to pass as the SSO redirectUrl.
func (cb *ssoCallback) URL() string {
	return fmt.Sprintf("http://%s/callback", cb.listener.Addr().String())
}

// Wait blocks until the callback receives a loginToken or ctx is done.
func (cb *ssoCallback) Wait(ctx context.Context) (string, error) {
	select {
	case token := <-cb.tokens:
		return token, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (cb *ssoCallback) Close() error {
	return cb.server.Close()
}

// ssoRedirectURL builds the URL a person opens in their browser to start
// Synapse's SSO login flow, with redirectUrl pointing back at our local
// callback server.
func ssoRedirectURL(homeserverURL, callbackURL string) string {
	return fmt.Sprintf("%s/_matrix/client/v3/login/sso/redirect?redirectUrl=%s",
		homeserverURL, url.QueryEscape(callbackURL))
}

// SSOLogin drives an interactive SSO login against homeserverURL: it
// prints the SSO redirect URL to out, listens on a localhost callback for
// the resulting loginToken, then exchanges it for an access token via
// m.login.token. It never dials a real server itself beyond the given
// homeserverURL and the final token exchange, and it is never run against
// a real account in tests.
func SSOLogin(ctx context.Context, homeserverURL string, out io.Writer) (Session, error) {
	callback, err := newSSOCallback()
	if err != nil {
		return Session{}, err
	}
	defer callback.Close()

	fmt.Fprintf(out, "Open this URL to sign in via SSO:\n%s\n", ssoRedirectURL(homeserverURL, callback.URL()))

	token, err := callback.Wait(ctx)
	if err != nil {
		return Session{}, fmt.Errorf("matrix: waiting for SSO callback: %w", err)
	}

	client, err := mautrix.NewClient(homeserverURL, "", "")
	if err != nil {
		return Session{}, fmt.Errorf("matrix: create client: %w", err)
	}

	resp, err := client.Login(ctx, &mautrix.ReqLogin{
		Type:                     mautrix.AuthTypeToken,
		Token:                    token,
		InitialDeviceDisplayName: "bunker-go",
	})
	if err != nil {
		return Session{}, fmt.Errorf("matrix: exchange SSO token: %w", err)
	}

	return Session{
		HomeserverURL: homeserverURL,
		UserID:        resp.UserID.String(),
		AccessToken:   resp.AccessToken,
		DeviceID:      resp.DeviceID.String(),
	}, nil
}
