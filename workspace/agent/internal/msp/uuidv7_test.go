package msp

import (
	"regexp"
	"testing"
	"time"
)

var uuidShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// The host validates this strictly: measured, a v4 is refused
// `invalid session/start commandId: expected UUIDv7`, so the version and variant nibbles are
// a wire contract and not a formality.
func TestNewCommandIDIsAUUIDv7(t *testing.T) {
	for i := 0; i < 100; i++ {
		id := NewCommandID()
		if !uuidShape.MatchString(id) {
			t.Fatalf("NewCommandID() = %q, which is not a UUIDv7", id)
		}
	}
}

func TestNewCommandIDIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 10000; i++ {
		id := NewCommandID()
		if seen[id] {
			t.Fatalf("duplicate id after %d draws: %s", i, id)
		}
		seen[id] = true
	}
}

// Time ordering is why the schema chose v7: ids sort into the order the commands were issued.
func TestUUIDv7IsTimeOrdered(t *testing.T) {
	base := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	early := uuidV7(base)
	late := uuidV7(base.Add(time.Second))
	if !(early < late) {
		t.Errorf("ids are not time-ordered: %s !< %s", early, late)
	}
	if uuidV7(base)[:8] != early[:8] {
		t.Error("the same millisecond should share the timestamp prefix")
	}
}

// The timestamp really is the Unix millisecond, not an arbitrary counter — a v7 whose clock
// is wrong still parses, so only this catches it.
func TestUUIDv7CarriesTheUnixMilliseconds(t *testing.T) {
	when := time.UnixMilli(0x0123456789AB)
	got := uuidV7(when)
	if want := "0123456789ab"; got[:8]+got[9:13] != want {
		t.Errorf("timestamp prefix = %q, want %q", got[:8]+got[9:13], want)
	}
}
