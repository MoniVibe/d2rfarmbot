package coverage

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/bits"
)

// Snapshot is a map's compact persisted form: frame + two deflated bitsets,
// base64'd. A 400×400 area with 13k seen tiles measures ~1.3 KB — cheap
// enough to land in the WAL every 10 s (only when something new was seen).
type Snapshot struct {
	OffX int    `json:"x"`
	OffY int    `json:"y"`
	W    int    `json:"w"`
	H    int    `json:"h"`
	Seen string `json:"seen"`
	Wall string `json:"wall"`
}

const maxDim = 4096 // the live level frame is rejected above 4000

// Snapshot serializes the map.
func (m *Map) Snapshot() Snapshot {
	return Snapshot{OffX: m.OffX, OffY: m.OffY, W: m.W, H: m.H, Seen: packBits(m.seen), Wall: packBits(m.wall)}
}

// FromSnapshot restores a map; a malformed snapshot is an error, never a panic.
func FromSnapshot(s Snapshot) (*Map, error) {
	if s.W <= 0 || s.H <= 0 || s.W > maxDim || s.H > maxDim {
		return nil, fmt.Errorf("coverage: implausible frame %dx%d", s.W, s.H)
	}
	m := New(s.OffX, s.OffY, s.W, s.H)
	if err := unpackBits(s.Seen, m.seen); err != nil {
		return nil, fmt.Errorf("coverage: seen: %w", err)
	}
	if err := unpackBits(s.Wall, m.wall); err != nil {
		return nil, fmt.Errorf("coverage: wall: %w", err)
	}
	// Bits past the frame in the last word are garbage from a foreign writer.
	if tail := uint(s.W*s.H) & 63; tail != 0 && len(m.seen) > 0 {
		m.seen[len(m.seen)-1] &= 1<<tail - 1
		m.wall[len(m.wall)-1] &= 1<<tail - 1
	}
	for _, w := range m.seen {
		m.nSeen += bits.OnesCount64(w)
	}
	m.ver = 1
	return m, nil
}

func packBits(b bitset) string {
	raw := make([]byte, len(b)*8)
	for i, w := range b {
		binary.LittleEndian.PutUint64(raw[i*8:], w)
	}
	var buf bytes.Buffer
	zw, _ := flate.NewWriter(&buf, flate.BestCompression)
	zw.Write(raw)
	zw.Close()
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func unpackBits(s string, into bitset) error {
	z, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(flate.NewReader(bytes.NewReader(z)), int64(len(into)*8+1)))
	if err != nil {
		return err
	}
	if len(raw) != len(into)*8 {
		return errors.New("bitset length does not match the frame")
	}
	for i := range into {
		into[i] = binary.LittleEndian.Uint64(raw[i*8:])
	}
	return nil
}
