// Package oggfixture crafts minimal Ogg Opus files for tests. The files
// carry a valid OpusHead, an OpusTags page and one audio page whose granule
// position encodes the requested length; the audio payload is not decodable,
// which is all the code under test (it reads page headers) needs.
package oggfixture

import (
	"bytes"
	"encoding/binary"
	"os"
	"time"
)

// PreSkip is the pre-skip the fixtures declare, in 48 kHz samples.
const PreSkip = 312

// crc computes Ogg's CRC-32 (polynomial 0x04C11DB7, MSB first, no
// reflection, zero init), which differs from Go's reflected IEEE table.
func crc(data []byte) uint32 {
	var c uint32
	for _, b := range data {
		c ^= uint32(b) << 24
		for i := 0; i < 8; i++ {
			if c&0x80000000 != 0 {
				c = c<<1 ^ 0x04C11DB7
			} else {
				c <<= 1
			}
		}
	}
	return c
}

func page(headerType byte, granule int64, serial, seq uint32, payload []byte) []byte {
	var b bytes.Buffer
	b.WriteString("OggS")
	b.WriteByte(0)
	b.WriteByte(headerType)
	_ = binary.Write(&b, binary.LittleEndian, granule)
	_ = binary.Write(&b, binary.LittleEndian, serial)
	_ = binary.Write(&b, binary.LittleEndian, seq)
	b.Write([]byte{0, 0, 0, 0})
	var segs []byte
	for n := len(payload); ; n -= 255 {
		if n >= 255 {
			segs = append(segs, 255)
			continue
		}
		segs = append(segs, byte(n))
		break
	}
	b.WriteByte(byte(len(segs)))
	b.Write(segs)
	b.Write(payload)
	out := b.Bytes()
	binary.LittleEndian.PutUint32(out[22:26], crc(out))
	return out
}

// Bytes returns an Ogg Opus stream lasting d.
func Bytes(d time.Duration) []byte {
	head := []byte("OpusHead")
	head = append(head, 1, 1)
	head = binary.LittleEndian.AppendUint16(head, PreSkip)
	head = binary.LittleEndian.AppendUint32(head, 48000)
	head = append(head, 0, 0, 0) // gain, mapping family
	tags := append([]byte("OpusTags"), 0, 0, 0, 0, 0, 0, 0, 0)
	samples := int64(d)*48000/int64(time.Second) + PreSkip
	var out []byte
	out = append(out, page(0x02, 0, 7, 0, head)...)
	out = append(out, page(0x00, 0, 7, 1, tags)...)
	out = append(out, page(0x04, samples, 7, 2, bytes.Repeat([]byte{0xAB}, 300))...)
	return out
}

// Write saves Bytes(d) at path.
func Write(path string, d time.Duration) error {
	return os.WriteFile(path, Bytes(d), 0o600)
}
