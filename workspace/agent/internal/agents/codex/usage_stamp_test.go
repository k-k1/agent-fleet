package codex

import (
	"os"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const stampID = "01a0ee31-0000-7000-8000-0000000000bb"

func appendLines(t *testing.T, p string, lines ...[]byte) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.Write(append(l, '\n')); err != nil {
			t.Fatal(err)
		}
	}
}

// The stamp must move with every append and with a different session, say nothing without a
// rollout, and the loader must say "not up to date" when it fell back on a cached parse.
func TestUsageSourceStampAndFreshness(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "stamp3112", Dir: t.TempDir(), Kind: session.KindCodex}
	slot := session.UUID(m.Dir, m.Name)
	a := agentImpl{}

	sids.Write(slot, stampID)
	if _, ok := a.UsageSource(m); ok {
		t.Fatal("no rollout yet, but a source was reported")
	}

	p := putRollout(t, "01", stampID)
	appendLines(t, p, taskStarted("2026-10-01T00:00:01Z", "t1"), userSays("2026-10-01T00:00:02Z", "hi"))
	src, ok := a.UsageSource(m)
	if !ok || src.Stamp == "" {
		t.Fatalf("source = %+v ok=%v", src, ok)
	}
	if turns, fresh := src.Turns(); !fresh || len(turns) == 0 {
		t.Fatalf("turns=%d fresh=%v, want a fresh read", len(turns), fresh)
	}
	again, _ := a.UsageSource(m)
	if again.Stamp != src.Stamp {
		t.Fatal("an untouched rollout changed its stamp")
	}

	appendLines(t, p, userSays("2026-10-01T00:00:03Z", "more"))
	grown, _ := a.UsageSource(m)
	if grown.Stamp == src.Stamp {
		t.Fatal("an append did not change the stamp")
	}

	other := "01a0ee31-0000-7000-8000-0000000000cc"
	putRollout(t, "02", other)
	sids.Write(slot, other)
	if swapped, ok := a.UsageSource(m); !ok || swapped.Stamp == grown.Stamp {
		t.Fatalf("another sid kept the stamp (ok=%v)", ok)
	}
	sids.Write(slot, stampID)

	// Stat passes, open fails: the shared parse serves what it held. That is not "seen".
	appendLines(t, p, userSays("2026-10-01T00:00:04Z", "late"))
	src, _ = a.UsageSource(m)
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(p, 0o600)
	if _, fresh := src.Turns(); fresh {
		t.Fatal("a failed read over a cached parse was reported fresh")
	}
}
