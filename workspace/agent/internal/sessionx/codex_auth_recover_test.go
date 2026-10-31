package sessionx

// Codex's login recovery (#3021 stage 3): a Terminal codex turn that died on a revoked login
// reads as "auth", refuses sends, and is resumed once ~/.codex/auth.json is rewritten AFTER the
// failed turn. The login is judged on that mtime only, never on what `codex login status` says.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

const cxRevoked = "Your access token could not be refreshed because your refresh token was revoked. Please log out and sign in again."

type cxAuthFixture struct {
	ab       *abortFixture // the resume's side effects (no tmux): what was sent
	m        session.Meta
	rollout  string
	authFile string
	failedAt time.Time
}

// newCxAuthFixture seeds a Terminal codex session whose newest rollout record is a turn that
// failed with errJSON at failedAt, and an auth.json written loginAt.
func newCxAuthFixture(t *testing.T, name, errJSON string, loginAgo time.Duration) *cxAuthFixture {
	t.Helper()
	isolateAgentState(t)
	ab := newAbortFixture(t) // points HOME and the sessions dir at fresh temp dirs
	f := &cxAuthFixture{ab: ab, m: session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex}}
	session.WriteMeta(f.m)
	sid := session.UUID(f.m.Dir, f.m.Name)
	thread := "01a0ee31-0000-7000-8000-0000000000" + name[len(name)-2:]
	codex.RememberSid(sid, thread)
	home := os.Getenv("HOME")
	dir := filepath.Join(home, ".codex", "sessions", "2026", "10", "30")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f.rollout = filepath.Join(dir, "rollout-2026-10-30T02-00-00-"+thread+".jsonl")
	f.failedAt = time.Now().Add(-10 * time.Minute).UTC().Truncate(time.Millisecond)
	f.writeRollout(t, errJSON, f.failedAt)
	f.authFile = filepath.Join(home, ".codex", "auth.json")
	if err := os.WriteFile(f.authFile, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f.setLogin(t, f.failedAt.Add(-loginAgo))
	status.Persist(sid, "idle")
	return f
}

func (f *cxAuthFixture) writeRollout(t *testing.T, errJSON string, at time.Time) {
	t.Helper()
	ts := func(d time.Duration) string { return at.Add(d).Format(time.RFC3339Nano) }
	body := `{"timestamp":"` + ts(-time.Second) + `","type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}` + "\n" +
		`{"timestamp":"` + ts(0) + `","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","error":` + errJSON + `}}` + "\n"
	if err := os.WriteFile(f.rollout, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *cxAuthFixture) setLogin(t *testing.T, at time.Time) {
	t.Helper()
	if err := os.Chtimes(f.authFile, at, at); err != nil {
		t.Fatal(err)
	}
}

func cxAuthErr(msg, info string) string {
	return `{"message":"` + msg + `","codex_error_info":` + info + `}`
}

// The state, the send guard and the Console's "re-authenticated" clock all come from the same
// comparison, so they flip together.
func TestCodexAuthStateAndSendBlock(t *testing.T) {
	f := newCxAuthFixture(t, "cxar01", cxAuthErr(cxRevoked, `"unauthorized"`), time.Hour)

	if got := DriveState(f.m, true, true); got != agents.StateAuth {
		t.Fatalf("DriveState = %q, want %q", got, agents.StateAuth)
	}
	st := promptBlocker(f.m.Name)
	if st != agents.StateAuth || blockedErrCode(st) != "auth_expired" {
		t.Fatalf("promptBlocker = %q (code %q), want the auth_expired refusal", st, blockedErrCode(st))
	}

	f.setLogin(t, f.failedAt.Add(time.Minute)) // codex login again
	if got := DriveState(f.m, true, true); got != "idle" {
		t.Fatalf("DriveState after re-auth = %q, want idle", got)
	}
	if st := promptBlocker(f.m.Name); st != "" {
		t.Fatalf("promptBlocker after re-auth = %q, want free", st)
	}
}

// A failure that a new sign-in cannot fix must never enter the auth state or block a send.
func TestCodexNonAuthErrorsDoNotEnterAuthState(t *testing.T) {
	for i, e := range []string{
		cxAuthErr("You've hit your usage limit.", `"usage_limit_exceeded"`),
		cxAuthErr("forbidden", `{"http_connection_failed":{"http_status_code":403}}`),
		cxAuthErr("Service is overloaded", `"server_overloaded"`),
	} {
		f := newCxAuthFixture(t, "cxar1"+string(rune('0'+i)), e, time.Hour)
		if got := DriveState(f.m, true, true); got == agents.StateAuth {
			t.Errorf("case %d: DriveState = auth", i)
		}
		if st := promptBlocker(f.m.Name); st == agents.StateAuth {
			t.Errorf("case %d: send blocked as auth", i)
		}
		if _, ok := abortInfoFor(f.m); ok {
			if a, _ := abortInfoFor(f.m); a.Auth {
				t.Errorf("case %d: abortInfoFor marks Auth", i)
			}
		}
	}
}

// The resume rule: nothing is sent until auth.json was written after the turn that died, however
// long the wait and whatever the CLI would say about the login.
func TestCodexAuthResumeWaitsForCredentialsWrittenAfterFailure(t *testing.T) {
	f := newCxAuthFixture(t, "cxar20", cxAuthErr(cxRevoked, `"unauthorized"`), time.Hour)
	a, ok := abortInfoFor(f.m)
	if !ok || !a.Auth || a.Retryable || !a.At.Equal(f.failedAt.Add(-time.Second)) {
		t.Fatalf("abortInfoFor = %+v ok=%v, want a blocked auth abort starting at the failed turn's start", a, ok)
	}
	for _, d := range []time.Duration{time.Second, time.Hour, 48 * time.Hour} {
		authResumeAttempt(f.m, arState(t, f.m.Name), a, f.failedAt.Add(d))
	}
	if len(f.ab.sent) != 0 {
		t.Fatalf("resumed on the login that failed: %v", f.ab.sent)
	}
	if st := arState(t, f.m.Name); st.Kind != session.KindCodex {
		t.Fatalf("episode kind = %q, want codex", st.Kind)
	}

	renewed := f.failedAt.Add(5 * time.Minute)
	f.setLogin(t, renewed)
	authResumeAttempt(f.m, arState(t, f.m.Name), a, renewed.Add(time.Second))
	if len(f.ab.sent) != 0 {
		t.Fatalf("sent before the new credential settled: %v", f.ab.sent)
	}
	authResumeAttempt(f.m, arState(t, f.m.Name), a, renewed.Add(authResumeSettle+time.Second))
	if len(f.ab.sent) != 1 || f.ab.sent[0] != abortResumePrompt() {
		t.Fatalf("sent = %v, want one auto-resume prompt", f.ab.sent)
	}
	if f.ab.origins[0].Kind != agents.OriginAutoResume {
		t.Fatalf("origin = %v", f.ab.origins[0])
	}
}

// A revoked login that was "renewed" with something that still fails must not loop: the resume
// dies on the login again, that failure is newer than auth.json, and the next resume waits for
// a credential written after IT. The cap is only the backstop.
func TestCodexAuthResumeDoesNotLoopOnRepeatedFailure(t *testing.T) {
	f := newCxAuthFixture(t, "cxar30", cxAuthErr(cxRevoked, `"unauthorized"`), time.Hour)
	a, _ := abortInfoFor(f.m)
	renewed := f.failedAt.Add(time.Minute)
	f.setLogin(t, renewed)
	now := renewed.Add(authResumeSettle + time.Second)
	authResumeAttempt(f.m, arState(t, f.m.Name), a, now)
	if len(f.ab.sent) != 1 {
		t.Fatalf("first resume: sent %d", len(f.ab.sent))
	}

	// The resumed turn dies on the login again, after the credentials were written.
	again := now.Add(30 * time.Second).Truncate(time.Millisecond)
	f.writeRollout(t, cxAuthErr(cxRevoked, `"unauthorized"`), again)
	a2, ok := abortInfoFor(f.m)
	if !ok || !a2.Auth {
		t.Fatalf("second failure not seen: %+v %v", a2, ok)
	}
	for i := 0; i < chatx.MaxAutoResumeAttempts+2; i++ {
		now = now.Add(authResumeBackoff + time.Minute)
		authResumeAttempt(f.m, arState(t, f.m.Name), a2, now)
	}
	if len(f.ab.sent) != 1 {
		t.Fatalf("looped: %d resumes on a login that was not rewritten after the second failure", len(f.ab.sent))
	}

	// A real re-authentication after the second failure releases it, once.
	f.setLogin(t, again.Add(time.Minute))
	now = again.Add(time.Minute).Add(authResumeSettle + authResumeBackoff + time.Second)
	authResumeAttempt(f.m, arState(t, f.m.Name), a2, now)
	if len(f.ab.sent) != 2 {
		t.Fatalf("after re-auth: sent %d, want 2", len(f.ab.sent))
	}
}

// claude's refresh-lock shortcuts (fast watch, forced resume on claude's credentials) must not
// fire for a codex episode whose message happens to carry claude's stem.
func TestCodexEpisodeIgnoresRefreshLockShortcuts(t *testing.T) {
	f := newCxAuthFixture(t, "cxar40", cxAuthErr(cxRevoked, `"unauthorized"`), time.Hour)
	a, _ := abortInfoFor(f.m)
	a.Msg = "[error] unauthorized: " + "refresh your login" + " " + cxRevoked
	watched := 0
	orig := authResumeFastWatch
	authResumeFastWatch = func(session.Meta, authResumeState) { watched++ }
	t.Cleanup(func() { authResumeFastWatch = orig })

	authResumeAttempt(f.m, arState(t, f.m.Name), a, f.failedAt.Add(authResumeRefreshForceAfter+time.Minute))
	if watched != 0 || len(f.ab.sent) != 0 {
		t.Fatalf("fast watch %d, resumes %v: the claude shortcuts ran for a codex episode", watched, f.ab.sent)
	}
}
