package ulid

import (
	"testing"
	"time"
)

func TestNewFormat(t *testing.T) {
	id := New()
	if len(id) != Len {
		t.Fatalf("len = %d, want %d", len(id), Len)
	}
	if !Valid(id) {
		t.Fatalf("New() produced invalid ULID: %q", id)
	}
}

func TestMonotonicWithinMillisecond(t *testing.T) {
	// Force many IDs at the same instant to exercise the increment path.
	now := time.UnixMilli(1_700_000_000_000)
	prev := newAt(now)
	for i := 0; i < 10000; i++ {
		cur := newAt(now)
		if cur <= prev {
			t.Fatalf("not monotonic at i=%d: %q <= %q", i, cur, prev)
		}
		prev = cur
	}
}

func TestOrderingAcrossTime(t *testing.T) {
	a := newAt(time.UnixMilli(1000))
	b := newAt(time.UnixMilli(2000))
	if a >= b {
		t.Fatalf("expected earlier time to sort first: %q >= %q", a, b)
	}
}

func TestValidRejects(t *testing.T) {
	cases := []string{"", "short", "01KS8F382PZ5PH9RZ4N7E08FEI" /* has I */}
	for _, c := range cases {
		if Valid(c) {
			t.Errorf("Valid(%q) = true, want false", c)
		}
	}
}
