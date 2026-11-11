package ulid

import (
	"strings"
	"testing"
	"time"
)

func TestFormat(t *testing.T) {
	id := New(time.Now())
	if len(id) != 26 {
		t.Fatalf("len %d", len(id))
	}
	for _, c := range id {
		if !strings.ContainsRune(alphabet, c) {
			t.Fatalf("invalid character %q in %s", c, id)
		}
	}
	if id[0] > '7' {
		t.Fatalf("leading character must encode only 3 bits: %s", id)
	}
}

func TestKnownTimestamp(t *testing.T) {
	// Timestamp part of the ULID spec example 01ARZ3NDEK... is 1469922850259 ms.
	id := New(time.UnixMilli(1469922850259))
	if got := id[:10]; got != "01ARZ3NDEK" {
		t.Fatalf("timestamp encoding %s, want 01ARZ3NDEK", got)
	}
}

func TestSortableAndUnique(t *testing.T) {
	a := New(time.UnixMilli(1000))
	b := New(time.UnixMilli(2000))
	if a >= b {
		t.Fatalf("not time-sortable: %s >= %s", a, b)
	}
	seen := map[string]bool{}
	now := time.Now()
	for i := 0; i < 1000; i++ {
		id := New(now)
		if seen[id] {
			t.Fatal("duplicate id")
		}
		seen[id] = true
	}
}
