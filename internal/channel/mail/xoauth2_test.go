package mail

import "testing"

func TestXOAuth2ClientStart(t *testing.T) {
	c := newXOAuth2Client("alice@example.com", "ya29.fake-token")
	mech, ir, err := c.Start()
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if mech != "XOAUTH2" {
		t.Errorf("mech = %q, want XOAUTH2", mech)
	}
	want := "user=alice@example.com\x01auth=Bearer ya29.fake-token\x01\x01"
	if string(ir) != want {
		t.Errorf("initial response = %q, want %q", ir, want)
	}
}

func TestXOAuth2ClientNextOnErrorChallengeRespondsEmpty(t *testing.T) {
	c := newXOAuth2Client("alice@example.com", "ya29.fake-token")
	if _, _, err := c.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	// Per Google's XOAUTH2 spec, a server that rejects the token sends a
	// JSON error challenge; the client must reply with an empty response
	// to complete the exchange instead of erroring out immediately.
	resp, err := c.Next([]byte(`{"status":"400","schemes":"bearer"}`))
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if len(resp) != 0 {
		t.Errorf("Next() response = %q, want empty", resp)
	}
}
