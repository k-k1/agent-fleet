package sessionx

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// ledgerBody is a valid queue create (it needs an item and a durable key), reserved.
func ledgerBody(p *createProbe) map[string]any {
	body := queueBody(p, []map[string]any{{"id": "m-1", "kind": "text", "body": "work", "provenance": "member"}})
	reserveDurable(p, "q_d-123")
	return body
}

func ledgerFiles(t *testing.T) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(paths.AgentStateDir(), "session-first-turn", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// failingResumeDriver stands in for a managed driver whose runtime cannot start.
type failingResumeDriver struct{ agents.Driver }

func (failingResumeDriver) Resume(session.Meta) (agents.ThreadHandle, error) {
	return nil, errors.New("runtime down")
}

func TestFailedQueueCreateLeavesNoFirstTurnLedger(t *testing.T) {
	t.Run("tmux launch fails", func(t *testing.T) {
		p := newCreateProbe(t)
		launchTmuxFn = func(session.Meta, bool) error { return errors.New("tmux died") }
		if rec := p.post(ledgerBody(p), ""); rec.Code < 300 {
			t.Fatalf("create = %d, want a failure", rec.Code)
		}
		if got := ledgerFiles(t); len(got) != 0 {
			t.Errorf("ledgers after a failed create = %v", got)
		}
	})
	t.Run("managed start fails", func(t *testing.T) {
		p := newCreateProbe(t)
		prev, had := managedDrivers[session.KindCodex]
		managedDrivers[session.KindCodex] = failingResumeDriver{}
		t.Cleanup(func() {
			if had {
				managedDrivers[session.KindCodex] = prev
			} else {
				delete(managedDrivers, session.KindCodex)
			}
		})
		body := ledgerBody(p)
		body["kind"], body["driver"] = session.KindCodex, session.DriverManaged
		if rec := p.post(body, ""); rec.Code < 300 {
			t.Fatalf("create = %d, want a failure", rec.Code)
		}
		if got := ledgerFiles(t); len(got) != 0 {
			t.Errorf("ledgers after a failed create = %v", got)
		}
	})
	t.Run("a successful create keeps it", func(t *testing.T) {
		p := newCreateProbe(t)
		launchTmuxFn = func(session.Meta, bool) error { return nil }
		if rec := p.post(ledgerBody(p), ""); rec.Code >= 300 {
			t.Fatalf("create = %d %s", rec.Code, rec.Body)
		}
		if got := ledgerFiles(t); len(got) != 1 {
			t.Errorf("ledgers after a successful create = %v, want one", got)
		}
	})
}

// armAged arms a ledger and moves its ArmedAt into the past.
func armAged(t *testing.T, sid string, age time.Duration) {
	t.Helper()
	status.ArmFirstTurn(sid, false)
	path := filepath.Join(paths.AgentStateDir(), "session-first-turn", sid+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var ft status.FirstTurn
	if err := json.Unmarshal(b, &ft); err != nil {
		t.Fatal(err)
	}
	ft.ArmedAt = time.Now().Add(-age).Format(time.RFC3339Nano)
	b, _ = json.Marshal(ft)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func ledgerExists(sid string) bool {
	_, err := os.Stat(filepath.Join(paths.AgentStateDir(), "session-first-turn", sid+".json"))
	return err == nil
}

func TestSweepFirstTurnOrphans(t *testing.T) {
	old := 2 * firstTurnOrphanAge
	setup := func(t *testing.T) (orphan, fresh, owned string) {
		isolateAgentState(t)
		dir := t.TempDir()
		session.WriteMeta(session.Meta{Name: "alive1", Dir: dir, Kind: session.KindClaude})
		owned = session.UUID(dir, "alive1")
		orphan, fresh = session.UUID(dir, "gone1"), session.UUID(dir, "inflight1")
		armAged(t, orphan, old)
		armAged(t, owned, old)
		armAged(t, fresh, time.Minute)
		return
	}
	t.Run("removes an old orphan, keeps the rest", func(t *testing.T) {
		orphan, fresh, owned := setup(t)
		if n := SweepFirstTurnOrphans(); n != 1 {
			t.Errorf("removed %d, want 1", n)
		}
		if ledgerExists(orphan) {
			t.Error("the old orphan stayed")
		}
		if !ledgerExists(fresh) {
			t.Error("a create in flight lost its ledger")
		}
		if !ledgerExists(owned) {
			t.Error("a session's ledger was removed")
		}
	})
	t.Run("keeps everything when a meta is unreadable", func(t *testing.T) {
		orphan, fresh, owned := setup(t)
		if err := os.WriteFile(session.MetaPath("cut1"), []byte(`{"name":"cut1","di`), 0o600); err != nil {
			t.Fatal(err)
		}
		if n := SweepFirstTurnOrphans(); n != 0 {
			t.Errorf("removed %d, want 0", n)
		}
		for _, sid := range []string{orphan, fresh, owned} {
			if !ledgerExists(sid) {
				t.Errorf("ledger %s removed while a meta was unreadable", sid)
			}
		}
	})
}
