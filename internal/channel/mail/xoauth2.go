package mail

import "github.com/emersion/go-sasl"

// xoauth2Client implements Google's XOAUTH2 SASL mechanism
// (https://developers.google.com/gmail/imap/xoauth2-protocol), which
// go-sasl does not ship (it only has the newer, differently-framed
// OAUTHBEARER).
type xoauth2Client struct {
	username, token string
}

// newXOAuth2Client returns a sasl.Client for AUTHENTICATE XOAUTH2.
func newXOAuth2Client(username, token string) sasl.Client {
	return &xoauth2Client{username: username, token: token}
}

// Start implements sasl.Client.
func (c *xoauth2Client) Start() (mech string, ir []byte, err error) {
	return "XOAUTH2", []byte("user=" + c.username + "\x01auth=Bearer " + c.token + "\x01\x01"), nil
}

// Next implements sasl.Client. A server that rejects the token sends a
// JSON error challenge and expects an empty response to end the
// exchange cleanly (the tagged NO that follows carries the real error).
func (c *xoauth2Client) Next(challenge []byte) ([]byte, error) {
	return []byte{}, nil
}
