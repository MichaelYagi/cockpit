// Package ulid generates ULIDs: 128-bit, time-sortable identifiers encoded
// as 26 Crockford base32 characters. Used for request and event IDs.
package ulid

import (
	"crypto/rand"
	"time"
)

const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// New returns a ULID for time t. Randomness comes from crypto/rand.
func New(t time.Time) string {
	var b [16]byte
	ms := uint64(t.UnixMilli())
	for i := 5; i >= 0; i-- {
		b[i] = byte(ms)
		ms >>= 8
	}
	_, _ = rand.Read(b[6:]) // crypto/rand.Read never returns an error

	// Encode 128 bits as 26 base32 characters (the first carries 3 bits).
	var out [26]byte
	var acc uint64
	bits := 2 // pad to 130 bits so the leading character holds the top 3
	idx := 0
	for _, v := range b {
		acc = acc<<8 | uint64(v)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out[idx] = alphabet[(acc>>uint(bits))&0x1f]
			idx++
		}
	}
	return string(out[:])
}
