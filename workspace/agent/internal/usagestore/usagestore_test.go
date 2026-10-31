package usagestore

import (
	"os"
	"strings"
	"testing"
	"time"
)

type reading struct {
	Pct int `json:"pct"`
}

// restart drops the in-memory copy, as a new Agent process would start without it.
func restart() {
	mu.Lock()
	loadedAt = ""
	mu.Unlock()
}

func TestRecordSurvivesRestart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	Record("muse", "a@example.com", at, reading{Pct: 7})
	restart()
	var r reading
	got, ok := Load("muse", "a@example.com", &r)
	if !ok || r.Pct != 7 || !got.Equal(at) {
		t.Fatalf("after restart: ok=%v pct=%d at=%v, want 7 observed at %v", ok, r.Pct, got, at)
	}
	b, err := os.ReadFile(storePath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "a@example.com") {
		t.Error("the store holds the account identity in the clear")
	}
}

func TestOlderObservationNeverOverwrites(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now().Truncate(time.Second)
	Record("copilot", "tok", now.Add(-time.Minute), reading{Pct: 30})
	Record("copilot", "tok", now.Add(-time.Hour), reading{Pct: 10})
	var r reading
	if _, ok := Load("copilot", "tok", &r); !ok || r.Pct != 30 {
		t.Fatalf("pct = %d ok=%v, want the newer 30", r.Pct, ok)
	}
	Record("copilot", "tok", now, reading{Pct: 40})
	if _, ok := Load("copilot", "tok", &r); !ok || r.Pct != 40 {
		t.Fatalf("pct = %d ok=%v, want the newest 40", r.Pct, ok)
	}
}

func TestSameObservationIsNotRewritten(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	at := time.Now().Add(-time.Minute).Truncate(time.Second)
	Record("claude", "acct", at, reading{Pct: 5})
	if err := os.Remove(storePath()); err != nil {
		t.Fatal(err)
	}
	Record("claude", "acct", at, reading{Pct: 5})
	if _, err := os.Stat(storePath()); err == nil {
		t.Error("re-recording an unchanged observation wrote the file again")
	}
}

func TestAnotherAccountIsNotServed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	Record("agy", "a@example.com", time.Now().Add(-time.Hour), reading{Pct: 50})
	var r reading
	if _, ok := Load("agy", "b@example.com", &r); ok {
		t.Fatal("a reading stored under account a was served to account b")
	}
	// Signing back in to a does not bring a's old reading back either: it predates a's new
	// sighting, which is the same rule the limit board applies.
	if _, ok := Load("agy", "a@example.com", &r); ok {
		t.Fatal("a reading from before the re-login was served")
	}
}

func TestNoObservationTimeIsNotRecorded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	Record("muse", "a", time.Time{}, reading{Pct: 1})
	var r reading
	if _, ok := Load("muse", "a", &r); ok {
		t.Fatal("a reading without an observation time was stored")
	}
}

func TestKindsAreIndependent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	at := time.Now().Add(-time.Minute)
	Record("muse", "a", at, reading{Pct: 1})
	Record("copilot", "b", at, reading{Pct: 2})
	restart()
	var m, c reading
	if _, ok := Load("muse", "a", &m); !ok || m.Pct != 1 {
		t.Errorf("muse: %+v ok=%v", m, ok)
	}
	if _, ok := Load("copilot", "b", &c); !ok || c.Pct != 2 {
		t.Errorf("copilot: %+v ok=%v", c, ok)
	}
}

// A kind that cannot name its account must not share one entry between every such account.
func TestEmptyIdentityUsesNoStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	Record("agy", "", time.Now().Add(-time.Minute), reading{Pct: 3})
	var r reading
	if _, ok := Load("agy", "", &r); ok {
		t.Fatal("a reading was stored and served under an empty identity")
	}
	if _, err := os.Stat(storePath()); err == nil {
		t.Error("an empty identity wrote the store")
	}
}
