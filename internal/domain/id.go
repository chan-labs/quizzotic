// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// ID is a UUIDv7 (RFC 9562): a 48-bit millisecond Unix timestamp followed by
// random bits, so IDs sort by creation time.
type ID [16]byte

// ErrInvalidID is returned by ParseID for malformed input.
var ErrInvalidID = errors.New("domain: invalid UUID")

// Nil is the zero ID.
var Nil ID

// IDGenerator produces UUIDv7s using a Clock. IDs from one generator are
// strictly increasing, even within the same millisecond or if the clock steps
// backwards. It is safe for concurrent use.
type IDGenerator struct {
	clock Clock

	mu      sync.Mutex
	lastMs  uint64
	lastSeq uint16 // 12-bit counter carried in rand_a
}

// NewIDGenerator returns a generator that reads time from clock.
func NewIDGenerator(clock Clock) *IDGenerator {
	return &IDGenerator{clock: clock}
}

// New returns a fresh ID.
func (g *IDGenerator) New() ID {
	g.mu.Lock()
	defer g.mu.Unlock()

	ms := uint64(g.clock.Now().UnixMilli())
	if ms > g.lastMs {
		g.lastMs = ms
		g.lastSeq = randSeq()
	} else {
		// Same millisecond or clock went backwards: keep the timestamp and
		// bump the counter; on overflow advance the timestamp by 1ms.
		if g.lastSeq == 0x0fff {
			g.lastMs++
			g.lastSeq = randSeq()
		} else {
			g.lastSeq++
		}
	}
	return build(g.lastMs, g.lastSeq)
}

// randSeq returns a random 11-bit start so the 12-bit counter has headroom.
func randSeq() uint16 {
	var b [2]byte
	mustRand(b[:])
	return (uint16(b[0])<<8 | uint16(b[1])) & 0x07ff
}

func build(ms uint64, seq uint16) ID {
	var id ID
	mustRand(id[8:])
	id[0] = byte(ms >> 40)
	id[1] = byte(ms >> 32)
	id[2] = byte(ms >> 24)
	id[3] = byte(ms >> 16)
	id[4] = byte(ms >> 8)
	id[5] = byte(ms)
	id[6] = 0x70 | byte(seq>>8)&0x0f // version 7
	id[7] = byte(seq)
	id[8] = id[8]&0x3f | 0x80 // variant 10
	return id
}

func mustRand(b []byte) {
	if _, err := rand.Read(b); err != nil {
		panic("domain: crypto/rand failed: " + err.Error())
	}
}

// Time returns the timestamp embedded in the ID.
func (id ID) Time() time.Time {
	ms := int64(id[0])<<40 | int64(id[1])<<32 | int64(id[2])<<24 |
		int64(id[3])<<16 | int64(id[4])<<8 | int64(id[5])
	return time.UnixMilli(ms).UTC()
}

// IsNil reports whether id is the zero ID.
func (id ID) IsNil() bool { return id == Nil }

// String returns the canonical 8-4-4-4-12 lowercase hex form.
func (id ID) String() string {
	var buf [36]byte
	hex.Encode(buf[0:8], id[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], id[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], id[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], id[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:], id[10:])
	return string(buf[:])
}

// ParseID parses the canonical textual form.
func ParseID(s string) (ID, error) {
	var id ID
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return id, ErrInvalidID
	}
	clean := s[0:8] + s[9:13] + s[14:18] + s[19:23] + s[24:]
	if _, err := hex.Decode(id[:], []byte(clean)); err != nil {
		return Nil, ErrInvalidID
	}
	return id, nil
}

// MarshalText implements encoding.TextMarshaler.
func (id ID) MarshalText() ([]byte, error) { return []byte(id.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (id *ID) UnmarshalText(b []byte) error {
	v, err := ParseID(string(b))
	if err != nil {
		return err
	}
	*id = v
	return nil
}
