package sessionx

// ADR 0096 decision 12 / docs/log/101 §101.8's third P0-review defect: HandleListSessions
// (write site ②, the only place a lazily-noticed exit is normally recorded) never looks at
// an archived session again (`if m.Archived { continue }`), so folding a LIVE session away
// through /archive is the ONLY chance to record that its run ended. Getting the ORDER wrong
// (archived before death, or no death at all) leaves the ledger reading "still running"
// forever — the line runs solid to the right edge with no × — even though the session and
// its meta both correctly show `presence: archived`.

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// lineageEventIndices returns the LINE indices (file order, not re-sorted) of every
// lineage.jsonl row naming ev/name, so a test can assert write ORDER directly rather than
// through BuildPage's per-lane sort (which does not promise anything about two rows that
// land in the same millisecond).
func lineageEventIndices(t *testing.T, ev, name string) []int {
	t.Helper()
	p := filepath.Join(paths.AgentStateDir(), "fleet-graph", "lineage.jsonl")
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var idx []int
	for i, ln := range strings.Split(string(b), "\n") {
		if ln == "" {
			continue
		}
		if strings.Contains(ln, `"ev":"`+ev+`"`) && strings.Contains(ln, `"name":"`+name+`"`) {
			idx = append(idx, i)
		}
	}
	return idx
}

func TestHandleArchiveSession_RecordsDeathBeforeArchived_WhenLive(t *testing.T) {
	const name = "slot_archive_live"
	tn := session.TmuxName(name)
	fakeTmuxOnlyFor(t, tn)
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude}
	session.WriteMeta(m)

	req := httptest.NewRequest("POST", "/sessions/"+name+"/archive", nil)
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	HandleArchiveSession(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	deaths := lineageEventIndices(t, "death", name)
	archives := lineageEventIndices(t, "archived", name)
	if len(deaths) != 1 {
		t.Fatalf("archiving a LIVE session must record exactly one death, got %d", len(deaths))
	}
	if len(archives) != 1 {
		t.Fatalf("want exactly one archived event, got %d", len(archives))
	}
	if deaths[0] >= archives[0] {
		t.Fatalf("death must be written BEFORE archived (death at line %d, archived at line %d) — "+
			"a reader stops looking at this lane once archived:true is seen, so a death after it "+
			"is as good as never written", deaths[0], archives[0])
	}
}

func TestHandleArchiveSession_NoDeathWhenAlreadyStopped(t *testing.T) {
	const name = "slot_archive_stopped"
	// No fake tmux "alive" target at all — tmuxx.HasSession reports false for everything,
	// simulating a session that was already stopped before the archive click.
	fakeTmuxOnlyFor(t, session.TmuxName("nobody-home"))
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude, StoppedAt: "2026-09-20T00:00:00Z"}
	session.WriteMeta(m)

	req := httptest.NewRequest("POST", "/sessions/"+name+"/archive", nil)
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	HandleArchiveSession(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	if deaths := lineageEventIndices(t, "death", name); len(deaths) != 0 {
		t.Fatalf("archiving an ALREADY-STOPPED session must not record a second death, got %d", len(deaths))
	}
	if archives := lineageEventIndices(t, "archived", name); len(archives) != 1 {
		t.Fatalf("want exactly one archived event, got %d", len(archives))
	}
}
