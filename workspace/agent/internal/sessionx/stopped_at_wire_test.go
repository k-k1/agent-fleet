package sessionx

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// A stopped row carries the instant the Agent first saw it stopped; a live row and a session
// stopped before the Agent stamped one carry none. The Console's Active view reads an absent
// value as "unknown", so it must never be filled in here.
func TestWireSessionCarriesStoppedAtOnAStoppedRowOnly(t *testing.T) {
	withTempHome(t)
	m := session.Meta{Name: "stp", Dir: t.TempDir(), Kind: session.KindClaude, StoppedAt: "2026-11-26T01:02:03Z"}
	session.WriteMeta(m)

	if got := wireSession(m, false); got.StoppedAt != m.StoppedAt {
		t.Fatalf("stopped row stoppedAt = %q, want %q", got.StoppedAt, m.StoppedAt)
	}
	if got := wireSession(m, true); got.StoppedAt != "" {
		t.Fatalf("live row stoppedAt = %q, want none", got.StoppedAt)
	}
	m.StoppedAt = ""
	b, err := json.Marshal(wireSession(m, false))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "stoppedAt") {
		t.Fatalf("a row with no stamp must omit the key, got %s", b)
	}
}
