package sessionx

// A carried interaction still there on a live row is reported under its own field, never under
// `carried` (which stays stopped-only so the session list does not change).

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

func TestWireSessionCarriedLiveOnlyOnLiveRow(t *testing.T) {
	withTempHome(t)
	m := session.Meta{Name: "cl-1", Dir: t.TempDir(), Kind: session.KindClaude}
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)
	if !status.PutCarried(sid, status.Carried{Kind: "permission", Permission: "Bash · ls"}, "halt") {
		t.Fatal("the carried interaction cannot be written")
	}

	live := wireSession(m, true)
	if live.CarriedLive != "permission" || live.Carried != "" {
		t.Fatalf("live row: carriedLive=%q carried=%q, want permission / empty", live.CarriedLive, live.Carried)
	}
	stopped := wireSession(m, false)
	if stopped.Carried != "permission" || stopped.CarriedLive != "" {
		t.Fatalf("stopped row: carried=%q carriedLive=%q, want permission / empty", stopped.Carried, stopped.CarriedLive)
	}
}

func TestWireSessionCarriedLiveNoneWhenExpiredOrAbsent(t *testing.T) {
	withTempHome(t)
	m := session.Meta{Name: "cl-2", Dir: t.TempDir(), Kind: session.KindClaude}
	session.WriteMeta(m)
	if got := wireSession(m, true); got.CarriedLive != "" || got.Carried != "" {
		t.Fatalf("no entry: carriedLive=%q carried=%q", got.CarriedLive, got.Carried)
	}
	// Older than CarriedTTL: ReadCarried drops it, so neither field shows.
	sid := session.UUID(m.Dir, m.Name)
	old := time.Now().Add(-status.CarriedTTL - time.Hour).Format(time.RFC3339)
	body := `{"kind":"plan","plan":"p","capturedAt":"` + old + `"}`
	dir := filepath.Join(paths.AgentStateDir(), "carried-interaction")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sid+".json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, alive := range []bool{true, false} {
		if got := wireSession(m, alive); got.CarriedLive != "" || got.Carried != "" {
			t.Fatalf("alive=%v expired: carriedLive=%q carried=%q", alive, got.CarriedLive, got.Carried)
		}
	}
}
