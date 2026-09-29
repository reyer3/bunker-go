package mail

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/reyer3/bunker-go/internal/config"
	"github.com/reyer3/bunker-go/internal/core"
)

// TestItemIDOtherFolderRoundTrip covers #52's ids for folders other than
// INBOX and Sent: the mailbox name, however odd, comes back exactly, and
// the id never collides with the INBOX or Sent one for the same UID.
func TestItemIDOtherFolderRoundTrip(t *testing.T) {
	for _, mailbox := range []string{
		"INBOX.Archive",
		"INBOX.Clients.Acme",
		"[Gmail]/All Mail",
		"Proyectos/2026: cierre",
		"Año 100%",
		"sent.like",
	} {
		id := itemID("cl", mailbox, 42, 1234)
		account, folder, uidValidity, uid, err := parseItemID(id)
		if err != nil {
			t.Fatalf("parseItemID(%q) error = %v", id, err)
		}
		if account != "cl" || folder != mailbox || uidValidity != 42 || uid != 1234 {
			t.Errorf("parseItemID(%q) = (%q, %q, %d, %d), want (cl, %q, 42, 1234)", id, account, folder, uidValidity, uid, mailbox)
		}
		if strings.Count(id, ":") != 2 {
			t.Errorf("itemID(%q) = %q, want no ':' after the account", mailbox, id)
		}
		for _, other := range []string{"INBOX", "Sent"} {
			if itemID("cl", other, 42, 1234) == id {
				t.Errorf("itemID(%q) = %q collides with the %s id", mailbox, id, other)
			}
		}
	}
	if got := itemID("cl", "INBOX.Archive", 42, 1234); got != "mail:cl:INBOX.Archive/42.1234" {
		t.Errorf("itemID(INBOX.Archive) = %q, want it readable", got)
	}
}

// TestParseItemIDLegacyStable: ids stored before #52 keep parsing to the
// same folder, so an existing database needs no migration.
func TestParseItemIDLegacyStable(t *testing.T) {
	for id, want := range map[string]string{
		"mail:cl:1700000000.100":      "INBOX",
		"mail:cl:sent.1700000000.100": "Sent",
	} {
		_, folder, _, _, err := parseItemID(id)
		if err != nil || folder != want {
			t.Errorf("parseItemID(%q) folder = %q, %v; want %q", id, folder, err, want)
		}
	}
}

func TestParseItemIDRejectsTrailingText(t *testing.T) {
	for _, id := range []string{
		"mail:cl:42.1234x",
		"mail:cl:42.1234.5",
		"mail:cl:INBOX.Archive/42",
		"mail:cl:/42.1234",
		"mail:cl:%ZZ/42.1234",
	} {
		if _, _, _, _, err := parseItemID(id); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("parseItemID(%q) error = %v, want core.ErrNotFound", id, err)
		}
	}
}

func TestParseAccountConfigSyncAndExcludeFolders(t *testing.T) {
	acc := config.Account{
		Channel: "mail",
		Name:    "cl",
		Options: map[string]interface{}{
			"imap_host":       "mail.example.cl",
			"username":        "x",
			"sync_folders":    []interface{}{"Archive", "Clients.Acme"},
			"exclude_folders": []interface{}{"Newsletters"},
		},
	}
	cfg, err := ParseAccountConfig(acc)
	if err != nil {
		t.Fatalf("ParseAccountConfig() error = %v", err)
	}
	if want := []string{"Archive", "Clients.Acme"}; !reflect.DeepEqual(cfg.SyncFolders, want) {
		t.Errorf("SyncFolders = %v, want %v", cfg.SyncFolders, want)
	}
	if want := []string{"Newsletters"}; !reflect.DeepEqual(cfg.ExcludeFolders, want) {
		t.Errorf("ExcludeFolders = %v, want %v", cfg.ExcludeFolders, want)
	}
}

func TestParseAccountConfigFolderListsDefaultEmpty(t *testing.T) {
	cfg, err := ParseAccountConfig(config.Account{Name: "cl", Options: map[string]interface{}{
		"imap_host": "mail.example.cl", "username": "x",
	}})
	if err != nil {
		t.Fatalf("ParseAccountConfig() error = %v", err)
	}
	if len(cfg.SyncFolders) != 0 || len(cfg.ExcludeFolders) != 0 {
		t.Errorf("SyncFolders = %v, ExcludeFolders = %v; want both empty", cfg.SyncFolders, cfg.ExcludeFolders)
	}
}

// TestParseAccountConfigFolderListsMustBeLists: a string where a list
// belongs (sync_folders = "Archive") is a config error, not ignored.
func TestParseAccountConfigFolderListsMustBeLists(t *testing.T) {
	for _, tc := range []struct {
		key string
		val interface{}
	}{
		{"sync_folders", "Archive"},
		{"exclude_folders", []interface{}{"Spam", int64(3)}},
		{"sync_folders", []interface{}{""}},
	} {
		acc := config.Account{Name: "cl", Options: map[string]interface{}{
			"imap_host": "mail.example.cl", "username": "x", tc.key: tc.val,
		}}
		if _, err := ParseAccountConfig(acc); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%s = %#v: error = %v, want ErrInvalidConfig", tc.key, tc.val, err)
		}
	}
}
