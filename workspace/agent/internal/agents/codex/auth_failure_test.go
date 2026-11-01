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
		// At is the failed turn's START when the tail holds it, else its end.
		wantAt := time.Date(2026, 10, 30, 2, 1, 0, 900_000_000, time.UTC)
		if c.name == "revoked login" {
			wantAt = time.Date(2026, 10, 30, 2, 1, 0, 0, time.UTC)
		}
		if ok && !f.At.Equal(wantAt) {
			t.Errorf("%s: At = %v, want %v", c.name, f.At, wantAt)
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

// Another session refreshing the shared auth.json DURING a turn makes that turn fail on the
// token it just lost ("already used"). The credentials were written after the turn started, so
// the login is alive and nothing may be blocked.
func TestAuthFailureJudgedFromTurnStart(t *testing.T) {
	f, ok := rolloutAuthFailure([][]byte{
		taskStarted("2026-10-30T02:01:00.000Z", "t2"),
		taskFailed("2026-10-30T02:01:05.000Z", "t2", `{"message":"Your refresh token was already used. Please log out and sign in again.","codex_error_info":"other"}`),
	})
	if !ok {
		t.Fatal("not recognised as an auth failure")
	}
	written := time.Date(2026, 10, 30, 2, 1, 4, 800_000_000, time.UTC) // between start and end
	tr := agents.AuthTraits{
		LastFailure:          func(session.Meta) (agents.AuthFailure, bool) { return f, true },
		CredentialsWrittenAt: func() time.Time { return written },
	}
	if tr.Blocked(session.Meta{}) {
		t.Fatal("credentials rewritten during the failed turn: want not blocked")
	}
	written = time.Date(2026, 10, 30, 1, 0, 0, 0, time.UTC)
	if !tr.Blocked(session.Meta{}) {
		t.Fatal("credentials older than the turn: want blocked")
	}
}

// A login rejected at turn/start has no turn behind it: blocked, but flagged so nothing resumes
// onto a thread that never received the instruction.
func TestManagedTurnStartRejectionIsNoTurn(t *testing.T) {
	m := session.Meta{Name: "cxauth2", Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged}
	h := newCodexTestHandle(t, nil, m.Name)
	registerCodexTestHandle(t, h)
	h.setLastError(codexError{label: "unauthorized", message: revokedLoginMsg})
	f, ok := LastAuthFailure(m)
	if !ok || !f.NoTurn {
		t.Fatalf("LastAuthFailure = %+v ok=%v, want NoTurn", f, ok)
	}
	h.setLastError(codexError{label: "unauthorized", message: revokedLoginMsg, turnID: "t9"})
	if f, ok := LastAuthFailure(m); !ok || f.NoTurn {
		t.Fatalf("a failed turn: %+v ok=%v, want a real turn", f, ok)
	}
}

const limitErrSnake = `{"message":"You've hit your usage limit.","codex_error_info":"usage_limit_exceeded"}`

// Only the NEWEST turn-ending record decides: a failed task_complete is the limit, a later
// started turn / clean completion / abort is not, and both spellings of the label count.
func TestRolloutTailUsageLimited(t *testing.T) {
	const at = "2026-10-30T02:01:00.900Z"
	for _, c := range []struct {
		name  string
		lines [][]byte
		want  bool
	}{
		{"snake_case label", [][]byte{taskStarted(at, "t2"), taskFailed(at, "t2", limitErrSnake)}, true},
		{"camelCase label", [][]byte{taskFailed(at, "t2", `{"message":"x","codex_error_info":"usageLimitExceeded"}`)}, true},
		{"other error", [][]byte{taskFailed(at, "t2", `{"message":"x","codex_error_info":"server_overloaded"}`)}, false},
		{"clean turn", [][]byte{taskComplete(at, "t2")}, false},
		{"a later turn started", [][]byte{taskFailed(at, "t2", limitErrSnake), taskStarted(at, "t3")}, false},
		{"a later clean turn", [][]byte{taskFailed(at, "t2", limitErrSnake), taskComplete(at, "t3")}, false},
		{"empty", nil, false},
	} {
		if got := rolloutTailUsageLimited(c.lines); got != c.want {
			t.Errorf("%s: = %v, want %v", c.name, got, c.want)
		}
	}
}

// After a stop or an Agent restart there is no handle, and IsRateLimitedMeta must still see the
// limit from the rollout (the handle-only IsRateLimited cannot). A newer turn clears it.
func TestIsRateLimitedMetaFallsBackToRollout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "cx-lim", Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged}
	writeSlotRollout(t, m, "01a0ee31-0000-7000-8000-0000000000aa",
		taskStarted("2026-10-30T02:01:00.000Z", "t2"),
		taskFailed("2026-10-30T02:01:00.900Z", "t2", limitErrSnake))
	if IsRateLimited(m.Name) {
		t.Fatal("IsRateLimited is handle-only and must stay false without one")
	}
	if !IsRateLimitedMeta(m) {
		t.Fatal("IsRateLimitedMeta = false, want true from the rollout's failed task_complete")
	}
	// A later turn appends to the same file (size and mtime change, so the memo is dropped).
	p := rolloutPath(sids.Read(session.UUID(m.Dir, m.Name)))
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(append(taskStarted("2026-10-30T03:00:00.000Z", "t3"), '\n'))
	f.Close()
	if IsRateLimitedMeta(m) {
		t.Fatal("IsRateLimitedMeta stayed true after a newer turn started")
	}
}
