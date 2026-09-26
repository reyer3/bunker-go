package whatsapp

import "testing"

func TestItemID(t *testing.T) {
	got := itemID("personal", "1234@s.whatsapp.net", "3EB0ABCDEF")
	want := "whatsapp:personal:1234@s.whatsapp.net/3EB0ABCDEF"
	if got != want {
		t.Fatalf("itemID() = %q, want %q", got, want)
	}
}

func TestParseItemID(t *testing.T) {
	cases := []struct {
		name    string
		id      string
		account string
		chat    string
		msg     string
		wantErr bool
	}{
		{
			name:    "well formed",
			id:      "whatsapp:personal:1234@s.whatsapp.net/3EB0ABCDEF",
			account: "personal",
			chat:    "1234@s.whatsapp.net",
			msg:     "3EB0ABCDEF",
		},
		{
			name:    "chat jid with device suffix keeps slash boundary from the last one",
			id:      "whatsapp:personal:120363000000000000@g.us/3EB0ABCDEF",
			account: "personal",
			chat:    "120363000000000000@g.us",
			msg:     "3EB0ABCDEF",
		},
		{name: "missing prefix", id: "mail:personal:1", wantErr: true},
		{name: "missing message id", id: "whatsapp:personal:1234@s.whatsapp.net", wantErr: true},
		{name: "empty", id: "", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			account, chat, msg, err := parseItemID(tc.id)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseItemID(%q) = nil error, want error", tc.id)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseItemID(%q) unexpected error: %v", tc.id, err)
			}
			if account != tc.account || chat != tc.chat || msg != tc.msg {
				t.Fatalf("parseItemID(%q) = (%q,%q,%q), want (%q,%q,%q)", tc.id, account, chat, msg, tc.account, tc.chat, tc.msg)
			}
		})
	}
}
