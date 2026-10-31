package codex

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func revokedRecord(ts string) []byte {
	return taskFailed(ts, "t2", `{"message":"`+revokedLoginMsg+`","codex_error_info":"unauthorized"}`)
}

// Only the NEWEST lifecycle record decides, and only an auth-classified failure counts. The
// negative rows are the ones a live "auth" state must never be entered on: a limit, a
// permission failure, an overloaded server, a clean turn, and a turn running again.
func TestRolloutAuthFailure(t *testing.T) {
	const at = "2026-10-30T02:01:00.900Z"
	for _, c := range []struct {
		name  string
		lines [][]byte
		want  bool
	}{
		{"revoked login", [][]byte{taskStarted("2026-10-30T02:01:00.000Z", "t2"), revokedRecord(at)}, true},
		{"usage limit", [][]byte{taskFailed(at, "t2", `{"message":"You've hit your usage limit.","codex_error_info":"usage_limit_exceeded"}`)}, false},
		{"HTTP 403", [][]byte{taskFailed(at, "t2", `{"message":"forbidden","codex_error_info":{"http_connection_failed":{"http_status_code":403}}}`)}, false},
		{"server overloaded", [][]byte{taskFailed(at, "t2", `{"message":"Service is overloaded","codex_error_info":"server_overloaded"}`)}, false},
		{"clean turn", [][]byte{taskComplete(at, "t2")}, false},
		{"a new turn started after the failure", [][]byte{revokedRecord(at), taskStarted("2026-10-30T02:05:00.000Z", "t3")}, false},
		{"a clean turn after the failure", [][]byte{revokedRecord(at), taskComplete("2026-10-30T02:05:00.000Z", "t3")}, false},
	} {
		f, ok := rolloutAuthFailure(c.lines)
		if ok != c.want {
			t.Errorf("%s: ok = %v, want %v", c.name, ok, c.want)
		}
		if ok && !f.At.Equal(time.Date(2026, 10, 30, 2, 1, 0, 900_000_000, time.UTC)) {
			t.Errorf("%s: At = %v", c.name, f.At)
		}
	}
}

// Blocked is judged on WHEN auth.json was written, never on anything the CLI says about the
// login: a file older than the failed turn is the login that failed (a server-side revocation
// leaves it looking healthy), a file rewritten after it is the renewal.
func TestAuthTraitsBlockedByCredentialsMtime(t *testing.T) {
	m := session.Meta{Name: "cxauth1", Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged}
	h := newCodexTestHandle(t, nil, m.Name)
	registerCodexTestHandle(t, h)
	home := t.TempDir() // after the handle: newCodexTestHandle points HOME at a directory of its own
	t.Setenv("HOME", home)
	h.setLastError(codexError{label: "unauthorized", message: revokedLoginMsg})
	failedAt := h.turnError().at

	authFile := filepath.Join(home, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(authFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authFile, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	impl := agentImpl{}
	set := func(d time.Duration) {
		if err := os.Chtimes(authFile, failedAt.Add(d), failedAt.Add(d)); err != nil {
			t.Fatal(err)
		}
	}

	set(-time.Hour)
	if !impl.AuthTraits().Blocked(m) {
		t.Fatal("auth.json older than the failed turn: want blocked")
	}
	if got := impl.WireLive(m, true).State; got != agents.StateAuth {
		t.Fatalf("WireLive = %q, want %q", got, agents.StateAuth)
	}
	set(time.Minute)
	if impl.AuthTraits().Blocked(m) {
		t.Fatal("auth.json rewritten after the failed turn: want not blocked")
	}
	if got := impl.WireLive(m, true).State; got != "idle" {
		t.Fatalf("WireLive after re-auth = %q, want idle", got)
	}

	// A failure that is not the login never enters the state, whatever auth.json says.
	set(-time.Hour)
	h.setLastError(codexError{label: "usageLimitExceeded", message: "You've hit your usage limit."})
	if impl.AuthTraits().Blocked(m) {
		t.Fatal("usage limit entered the auth state")
	}
}
