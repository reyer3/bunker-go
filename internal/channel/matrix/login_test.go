package matrix

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"
)

// capturingWriter feeds every Write to a channel line by line so a test
// goroutine can observe what SSOLogin printed while it is still blocked
// waiting for the callback.
type capturingWriter struct {
	lines chan string
}

func newCapturingWriter() *capturingWriter {
	return &capturingWriter{lines: make(chan string, 8)}
}

func (w *capturingWriter) Write(p []byte) (int, error) {
	w.lines <- string(p)
	return len(p), nil
}

var urlPattern = regexp.MustCompile(`https?://\S+`)

func TestSSOLoginPrintsRedirectURLAndExchangesToken(t *testing.T) {
	const expectedToken = "sso-login-token-123"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/_matrix/client/v3/login/sso/redirect":
			// A real Synapse redirects the browser to redirectUrl once SSO
			// finishes; here we skip the identity provider and redirect
			// straight to it, exactly like a completed SSO round trip.
			redirect := r.URL.Query().Get("redirectUrl")
			if redirect == "" {
				t.Errorf("sso redirect request missing redirectUrl query param")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			http.Redirect(w, r, redirect, http.StatusFound)
		case r.Method == http.MethodPost && r.URL.Path == "/_matrix/client/v3/login":
			var body struct {
				Type  string `json:"type"`
				Token string `json:"token"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode login body: %v", err)
			}
			if body.Type != "m.login.token" {
				t.Errorf("login type = %q, want m.login.token", body.Type)
			}
			if body.Token != expectedToken {
				t.Errorf("login token = %q, want %q", body.Token, expectedToken)
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{
				"access_token": "syt_abc",
				"device_id":    "DEVICEXYZ",
				"user_id":      "@alice:example.com",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	out := newCapturingWriter()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	type result struct {
		session Session
		err     error
	}
	done := make(chan result, 1)
	go func() {
		session, err := SSOLogin(ctx, srv.URL, out)
		done <- result{session, err}
	}()

	var printed string
	select {
	case printed = <-out.lines:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SSOLogin to print the redirect URL")
	}

	match := urlPattern.FindString(printed)
	if match == "" {
		t.Fatalf("no URL found in printed output: %q", printed)
	}

	noRedirectClient := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	redirectResp, err := noRedirectClient.Get(match)
	if err != nil {
		t.Fatalf("GET redirect URL: %v", err)
	}
	redirectResp.Body.Close()
	if redirectResp.StatusCode/100 != 3 {
		t.Fatalf("redirect status = %d, want a 3xx redirect to the homeserver SSO endpoint", redirectResp.StatusCode)
	}
	// The homeserver redirects straight to our local callback URL (it was
	// passed verbatim as redirectUrl); hit it with the SSO login token,
	// exactly as a browser finishing SSO would.
	callbackURL := redirectResp.Header.Get("Location")
	if callbackURL == "" {
		t.Fatal("redirect response missing Location header")
	}

	resp, err := http.Get(callbackURL + "?loginToken=" + expectedToken)
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	resp.Body.Close()

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("SSOLogin: %v", res.err)
		}
		if res.session.AccessToken != "syt_abc" {
			t.Errorf("AccessToken = %q, want syt_abc", res.session.AccessToken)
		}
		if res.session.DeviceID != "DEVICEXYZ" {
			t.Errorf("DeviceID = %q, want DEVICEXYZ", res.session.DeviceID)
		}
		if res.session.UserID != "@alice:example.com" {
			t.Errorf("UserID = %q, want @alice:example.com", res.session.UserID)
		}
		if res.session.HomeserverURL != srv.URL {
			t.Errorf("HomeserverURL = %q, want %q", res.session.HomeserverURL, srv.URL)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for SSOLogin to finish")
	}
}
