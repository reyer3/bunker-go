package core

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// ErrNotOggOpus reports that a file is not an Ogg container carrying one
// Opus stream, the only format a voice note can be sent in.
var ErrNotOggOpus = errors.New("not an Ogg Opus file")

// opusSampleRate is the fixed rate Ogg Opus granule positions count in,
// whatever rate the encoder was fed (RFC 7845 section 4).
const opusSampleRate = 48000

// OggOpusDuration returns the playing time of the Ogg Opus file at path
// without decoding it: the last page's granule position, minus the
// pre-skip the OpusHead header declares, over 48 kHz. Both WhatsApp and
// Matrix need the length of a voice note up front (neither derives it from
// the media), and shelling out to a tool just to read two numbers would
// make sending depend on one more binary.
//
// A file that is not Ogg Opus fails with an error wrapping ErrNotOggOpus
// whose text names how to convert it, so the caller can show it as is.
func OggOpusDuration(path string) (time.Duration, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("core: open voice note %q: %w", path, err)
	}
	defer f.Close()
	d, err := oggOpusDuration(bufio.NewReader(f))
	if err != nil {
		return 0, fmt.Errorf("core: voice note %q: %w", path, err)
	}
	return d, nil
}

var errNotOggOpusHint = fmt.Errorf("%w: a voice note must be Ogg Opus; convert it with: ffmpeg -i in.wav -c:a libopus -b:a 24k out.ogg", ErrNotOggOpus)

func oggOpusDuration(r *bufio.Reader) (time.Duration, error) {
	var (
		preSkip    uint16
		haveHead   bool
		lastGran   int64
		haveGran   bool
		serial     uint32
		haveSerial bool
	)
	for {
		var hdr [27]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break // end of file, or a truncated last page: keep what we have
			}
			return 0, fmt.Errorf("read ogg page: %w", err)
		}
		if string(hdr[:4]) != "OggS" {
			if !haveHead {
				return 0, errNotOggOpusHint
			}
			break // trailing garbage after valid pages
		}
		gran := int64(binary.LittleEndian.Uint64(hdr[6:14]))
		pageSerial := binary.LittleEndian.Uint32(hdr[14:18])
		nseg := int(hdr[26])
		segs := make([]byte, nseg)
		if _, err := io.ReadFull(r, segs); err != nil {
			break
		}
		size := 0
		for _, s := range segs {
			size += int(s)
		}
		if !haveHead {
			if hdr[5]&0x02 == 0 {
				return 0, errNotOggOpusHint
			}
			head := make([]byte, size)
			if _, err := io.ReadFull(r, head); err != nil {
				return 0, errNotOggOpusHint
			}
			if len(head) < 19 || string(head[:8]) != "OpusHead" {
				return 0, errNotOggOpusHint
			}
			preSkip = binary.LittleEndian.Uint16(head[10:12])
			serial, haveSerial, haveHead = pageSerial, true, true
			continue
		}
		if _, err := r.Discard(size); err != nil {
			break
		}
		// -1 means no packet ends on this page; another logical stream
		// (chained files) is not a voice note we can size.
		if haveSerial && pageSerial == serial && gran >= 0 {
			lastGran, haveGran = gran, true
		}
	}
	if !haveHead {
		return 0, errNotOggOpusHint
	}
	if !haveGran || lastGran <= int64(preSkip) {
		return 0, fmt.Errorf("%w: the file has no audio", ErrNotOggOpus)
	}
	samples := lastGran - int64(preSkip)
	return time.Duration(samples) * time.Second / opusSampleRate, nil
}
