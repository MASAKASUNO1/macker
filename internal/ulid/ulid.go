// Package ulid implements a small, dependency-free ULID generator.
//
// A ULID is a 128-bit, lexicographically sortable identifier: a 48-bit
// millisecond timestamp followed by 80 bits of randomness, encoded with
// Crockford's base32. We use ULIDs as event IDs so that the append-only
// event log is naturally time-ordered and so that collectors can dedupe
// replayed events idempotently.
package ulid

import (
	"crypto/rand"
	"errors"
	"sync"
	"time"
)

// crockford is Crockford's base32 alphabet (no I, L, O, U).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// Len is the length of the canonical string encoding.
const Len = 26

var (
	mu       sync.Mutex
	lastMS   uint64
	lastRand [10]byte
)

// New returns a new ULID string using the current time. It is safe for
// concurrent use and is monotonic within the same millisecond: if called
// multiple times in one millisecond, the random component is incremented
// so that generated IDs keep increasing.
func New() string {
	return newAt(time.Now())
}

func newAt(t time.Time) string {
	ms := uint64(t.UnixMilli())

	mu.Lock()
	if ms <= lastMS {
		// Same millisecond, or the wall clock went backwards (e.g. an NTP
		// step): clamp to the last timestamp and increment the random value so
		// IDs stay strictly increasing and the log stays time-ordered.
		ms = lastMS
		incr(&lastRand)
	} else {
		lastMS = ms
		_, _ = rand.Read(lastRand[:])
	}
	r := lastRand
	mu.Unlock()

	var id [16]byte
	id[0] = byte(ms >> 40)
	id[1] = byte(ms >> 32)
	id[2] = byte(ms >> 24)
	id[3] = byte(ms >> 16)
	id[4] = byte(ms >> 8)
	id[5] = byte(ms)
	copy(id[6:], r[:])

	return encode(id)
}

// incr increments a 10-byte big-endian counter in place. On overflow it
// wraps to zero, which is acceptable for our dedupe/ordering needs.
func incr(b *[10]byte) {
	for i := len(b) - 1; i >= 0; i-- {
		b[i]++
		if b[i] != 0 {
			return
		}
	}
}

// encode renders the 16-byte value as a 26-char Crockford base32 string.
func encode(id [16]byte) string {
	out := make([]byte, Len)
	// 128 bits -> 26 base32 chars (130 bits, top 2 bits are zero).
	out[0] = crockford[(id[0]&224)>>5]
	out[1] = crockford[id[0]&31]
	out[2] = crockford[(id[1]&248)>>3]
	out[3] = crockford[((id[1]&7)<<2)|((id[2]&192)>>6)]
	out[4] = crockford[(id[2]&62)>>1]
	out[5] = crockford[((id[2]&1)<<4)|((id[3]&240)>>4)]
	out[6] = crockford[((id[3]&15)<<1)|((id[4]&128)>>7)]
	out[7] = crockford[(id[4]&124)>>2]
	out[8] = crockford[((id[4]&3)<<3)|((id[5]&224)>>5)]
	out[9] = crockford[id[5]&31]
	out[10] = crockford[(id[6]&248)>>3]
	out[11] = crockford[((id[6]&7)<<2)|((id[7]&192)>>6)]
	out[12] = crockford[(id[7]&62)>>1]
	out[13] = crockford[((id[7]&1)<<4)|((id[8]&240)>>4)]
	out[14] = crockford[((id[8]&15)<<1)|((id[9]&128)>>7)]
	out[15] = crockford[(id[9]&124)>>2]
	out[16] = crockford[((id[9]&3)<<3)|((id[10]&224)>>5)]
	out[17] = crockford[id[10]&31]
	out[18] = crockford[(id[11]&248)>>3]
	out[19] = crockford[((id[11]&7)<<2)|((id[12]&192)>>6)]
	out[20] = crockford[(id[12]&62)>>1]
	out[21] = crockford[((id[12]&1)<<4)|((id[13]&240)>>4)]
	out[22] = crockford[((id[13]&15)<<1)|((id[14]&128)>>7)]
	out[23] = crockford[(id[14]&124)>>2]
	out[24] = crockford[((id[14]&3)<<3)|((id[15]&224)>>5)]
	out[25] = crockford[id[15]&31]
	return string(out)
}

// ErrInvalid is returned when a string is not a valid ULID encoding.
var ErrInvalid = errors.New("ulid: invalid encoding")

// Valid reports whether s is a syntactically valid ULID string.
func Valid(s string) bool {
	if len(s) != Len {
		return false
	}
	for i := 0; i < len(s); i++ {
		if decodeChar(s[i]) == 0xff {
			return false
		}
	}
	return true
}

func decodeChar(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'A' && c <= 'H':
		return c - 'A' + 10
	case c == 'J' || c == 'K':
		return c - 'J' + 18
	case c >= 'M' && c <= 'N':
		return c - 'M' + 20
	case c >= 'P' && c <= 'T':
		return c - 'P' + 22
	case c >= 'V' && c <= 'Z':
		return c - 'V' + 27
	// lowercase tolerance
	case c >= 'a' && c <= 'h':
		return c - 'a' + 10
	case c == 'j' || c == 'k':
		return c - 'j' + 18
	case c >= 'm' && c <= 'n':
		return c - 'm' + 20
	case c >= 'p' && c <= 't':
		return c - 'p' + 22
	case c >= 'v' && c <= 'z':
		return c - 'v' + 27
	default:
		return 0xff
	}
}
