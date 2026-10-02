package core_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/oggfixture"
)

func TestOggOpusDuration(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "a.ogg")
	if err := oggfixture.Write(good, 12*time.Second+500*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	d, err := core.OggOpusDuration(good)
	if err != nil || d != 12*time.Second+500*time.Millisecond {
		t.Fatalf("duration = %v, %v", d, err)
	}

	// A recorder killed mid-write leaves a truncated last page: the pages
	// before it carry no audio position, so it is a clear error, never a
	// panic or a bogus length.
	data := oggfixture.Bytes(3 * time.Second)
	trunc := filepath.Join(dir, "t.ogg")
	if err := os.WriteFile(trunc, data[:len(data)-100], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := core.OggOpusDuration(trunc); !errors.Is(err, core.ErrNotOggOpus) {
		t.Fatalf("truncated: want ErrNotOggOpus (no audio), got %v", err)
	}
}

func TestOggOpusDurationRejects(t *testing.T) {
	dir := t.TempDir()
	vorbis := oggfixture.Bytes(time.Second)
	copy(vorbis[28:], "VorbisHd") // first packet no longer OpusHead
	cases := map[string][]byte{
		"empty.ogg":  nil,
		"text.ogg":   []byte("this is not an ogg file at all, just some text"),
		"vorbis.ogg": vorbis,
	}
	for name, data := range cases {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := core.OggOpusDuration(p)
		if !errors.Is(err, core.ErrNotOggOpus) {
			t.Errorf("%s: want ErrNotOggOpus, got %v", name, err)
		}
	}
	if _, err := core.OggOpusDuration(filepath.Join(dir, "missing.ogg")); err == nil {
		t.Error("missing file must fail")
	}
}
