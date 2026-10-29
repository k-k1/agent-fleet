package sessionx

// The state machine of the automatic resume from a cut-off (docs/log/47 §4-6). Detecting the
// cut-off itself is held down by internal/agents/claude/abort_test.go and the pane verdict by
// internal/tmuxx's golden corpus, so what is watched here is the wiring: when to send, after how
// many attempts to give up, what to hand the report side on giving up, and when to suppress the
// report. There is no tmux and no claude here, so the side effects are replaced.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

type abortFixture struct {
	sent      []string // resume prompts that were injected successfully
	origins   []agents.Origin
	injectErr error
	pane      tmuxx.PaneRead
}

func newAbortFixture(t *testing.T) *abortFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	f := &abortFixture{pane: tmuxx.PaneRead{OK: true, Idle: true}}
	origInject, origPane := abortResumeInject, abortResumeReadingPane
	abortResumeInject = func(name, prompt string, origin agents.Origin) error {
		if f.injectErr != nil {
			return f.injectErr
		}
		f.sent = append(f.sent, prompt)
		f.origins = append(f.origins, origin)
		return nil
	}
	abortResumeReadingPane = func(string) tmuxx.PaneRead { return f.pane }
	origAlive := abortResumeAlive
	abortResumeAlive = func(session.Meta) bool { return true }
	t.Cleanup(func() {
		abortResumeInject, abortResumeReadingPane, abortResumeAlive = origInject, origPane, origAlive
	})
	return f
}

func setAbortResumePref(t *testing.T, on bool) {
	t.Helper()
	p := uiprefs.Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"claudeAbortAutoResume": on})
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func abMeta() session.Meta {
	return session.Meta{Name: "ab1", Dir: "/tmp/ab1", Kind: session.KindClaude}
}

func abState(t *testing.T, name string) abortResumeState {
	t.Helper()
	st, _ := abortResumeStates.Read(name)
	return st
}

func retryableAbort(at time.Time) claude.Abort {
	return claude.Abort{Msg: "API Error: Stream idle timeout - no chunks received", Retryable: true, At: at}
}

// TestAbortResumeWaitsThenSends: nothing is fired straight after the cut-off (the backoff), and
// once the wait is over exactly one prompt is sent. Re-sending immediately throws away one of
// the few retries before the cause of a 529 / overloaded has cleared.
func TestAbortResumeWaitsThenSends(t *testing.T) {
	f := newAbortFixture(t)
	m := abMeta()
	cut := time.Now()
	a := retryableAbort(cut)

	abortResumeAttempt(m, abState(t, m.Name), a, cut.Add(5*time.Second))
	if len(f.sent) != 0 {
		t.Fatalf("sent during the backoff: %v", f.sent)
	}
	if st := abState(t, m.Name); st.At == "" {
		t.Fatal("the episode is not persisted the moment it opens (it would be reopened every tick)")
	}

	abortResumeAttempt(m, abState(t, m.Name), a, cut.Add(abortResumeFirstDelay+time.Second))
	if len(f.sent) != 1 {
		t.Fatalf("resume prompts = %d, want 1: %v", len(f.sent), f.sent)
	}
	// ADR 0105 decision 1: an auto-resume is not member input, so it never ends a stop episode.
	if f.origins[0] != (agents.Origin{Kind: agents.OriginAutoResume}) {
		t.Errorf("resume origin = %+v, want auto-resume", f.origins[0])
	}
	if st := abState(t, m.Name); st.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", st.Attempts)
	}
	// The very next tick does not fire again (a second attempt comes after abortResumeBackoff).
	abortResumeAttempt(m, abState(t, m.Name), a, cut.Add(abortResumeFirstDelay+abortResumeWatchInterval))
	if len(f.sent) != 1 {
		t.Errorf("ignored the backoff and sent %d times", len(f.sent))
	}
}

func TestAbortInfoForManagedUsesPersistedSignal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "oc-managed", Dir: "/tmp/oc-managed", Kind: session.KindOpencode, Driver: session.DriverManaged}
	at := time.Now().Truncate(time.Second)
	if err := managedAbortSignals.Write(m.Name, managedAbortSignal{At: at.Format(time.RFC3339), Msg: "HTTP 500"}); err != nil {
		t.Fatal(err)
	}
	a, ok := abortInfoFor(m)
	if !ok || !a.Retryable || a.Msg != "HTTP 500" || !a.At.Equal(at) {
		t.Fatalf("managed abort = %+v ok=%v", a, ok)
	}
}

// TestAbortResumeCapsThenHandsOver: when the cut-off survives re-sending up to the cap, step
// back and line the report side's counter up, so the report that goes out carries the "cap
// reached" wording. This is the seam of "the assistant hears about it only once we gave up".
func TestAbortResumeCapsThenHandsOver(t *testing.T) {
	f := newAbortFixture(t)
	m := abMeta()
	cut := time.Now()
	a := retryableAbort(cut)

	now := cut.Add(abortResumeFirstDelay + time.Second)
	for i := 0; i < chatx.MaxAutoResumeAttempts; i++ {
		abortResumeAttempt(m, abState(t, m.Name), a, now)
		now = now.Add(abortResumeBackoff + time.Second)
	}
	if len(f.sent) != chatx.MaxAutoResumeAttempts {
		t.Fatalf("resumes = %d, want %d", len(f.sent), chatx.MaxAutoResumeAttempts)
	}
	if abState(t, m.Name).GaveUp != "" {
		t.Fatal("gave up before the cap was reached")
	}

	abortResumeAttempt(m, abState(t, m.Name), a, now)
	st := abState(t, m.Name)
	if st.GaveUp != abortGaveUpCapped {
		t.Fatalf("gaveUp = %q, want %q", st.GaveUp, abortGaveUpCapped)
	}
	if len(f.sent) != chatx.MaxAutoResumeAttempts {
		t.Errorf("gave up and still sent: %v", f.sent)
	}
	if got := chatx.AutoResumeAttempts(m.Name); got != chatx.MaxAutoResumeAttempts {
		t.Errorf("the report side's counter = %d, want %d (the capped wording will not be used)", got, chatx.MaxAutoResumeAttempts)
	}
	// Giving up lifts the suppression: the cut-off report can be delivered again.
	if AbortResumeHolds(m.Name, a, now) {
		t.Error("gave up and is still suppressing the report")
	}
}

// TestAbortResumeSkipsBusyOrBlockedPane: nothing is fired at a session that is running or
// waiting on a question. The abort record stays at the tail, so the transcript still looks
// cut off after the user resumed by hand — sending "continue" there turns into an interrupting
// instruction to a running turn.
func TestAbortResumeSkipsBusyOrBlockedPane(t *testing.T) {
	for _, tc := range []struct {
		name string
		pane tmuxx.PaneRead
	}{
		{"running", tmuxx.PaneRead{OK: true, Busy: true}},
		{"not showing the idle prompt", tmuxx.PaneRead{OK: true}},
		{"pane cannot be read", tmuxx.PaneRead{}},
		{"usage-limit menu", tmuxx.PaneRead{OK: true, Idle: true, RateLimitMenu: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAbortFixture(t)
			f.pane = tc.pane
			m := abMeta()
			cut := time.Now()
			abortResumeAttempt(m, abState(t, m.Name), retryableAbort(cut), cut.Add(abortResumeFirstDelay+time.Second))
			if len(f.sent) != 0 {
				t.Fatalf("sent in a state where sending is forbidden: %v", f.sent)
			}
			if st := abState(t, m.Name); st.DeliverTries != 1 {
				t.Errorf("deliverTries = %d, want 1", st.DeliverTries)
			}
		})
	}
}

// TestAbortResumeUndeliverableGivesUp: give up once attempts keep failing to arrive. This is
// not the kind of failure that hammering fixes (a person is at the session, the TUI changed
// shape), so handing it to a person is faster.
func TestAbortResumeUndeliverableGivesUp(t *testing.T) {
	f := newAbortFixture(t)
	f.injectErr = errors.New("no pane")
	m := abMeta()
	cut := time.Now()
	a := retryableAbort(cut)

	now := cut.Add(abortResumeFirstDelay + time.Second)
	for i := 0; i < abortResumeMaxDeliverTries; i++ {
		abortResumeAttempt(m, abState(t, m.Name), a, now)
		now = now.Add(abortResumeBackoff + time.Second)
	}
	st := abState(t, m.Name)
	if st.GaveUp != abortGaveUpUndeliverable {
		t.Fatalf("gaveUp = %q, want %q", st.GaveUp, abortGaveUpUndeliverable)
	}
	if st.Attempts != 0 {
		t.Errorf("attempts = %d, want 0 (counted although nothing was delivered)", st.Attempts)
	}
}

// TestAbortResumeHoldsSuppressesReport: the conditions under which the report is suppressed.
// Suppression lasts only while the automatic resume has the cut-off in hand, and must be lifted
// when the feature is off, when it has been given up on, when the episode is stale, and for a
// cut-off re-sending cannot fix at all (blocked). If it is not lifted, the cut-off reaches
// nobody.
func TestAbortResumeHoldsSuppressesReport(t *testing.T) {
	now := time.Now()
	blocked := claude.Abort{Msg: "You've reached your Fable 5 limit.", At: now}

	t.Run("suppresses a fresh cut-off even with no episode yet", func(t *testing.T) {
		newAbortFixture(t)
		if !AbortResumeHolds("ab1", retryableAbort(now), now.Add(5*time.Second)) {
			t.Error("not suppressing a cut-off seen before the sweep (the report gets there first)")
		}
	})
	t.Run("does not suppress an old cut-off with no episode", func(t *testing.T) {
		newAbortFixture(t)
		if AbortResumeHolds("ab1", retryableAbort(now), now.Add(10*time.Minute)) {
			t.Error("still suppressing although no watcher is running (the report would never come out)")
		}
	})
	t.Run("does not suppress a cut-off with no timestamp", func(t *testing.T) {
		newAbortFixture(t)
		if AbortResumeHolds("ab1", claude.Abort{Msg: "x", Retryable: true}, now) {
			t.Error("suppressing a cut-off whose time cannot be read (the window can never close)")
		}
	})
	t.Run("does not suppress a blocked cut-off", func(t *testing.T) {
		newAbortFixture(t)
		if AbortResumeHolds("ab1", blocked, now) {
			t.Error("suppressing a cut-off that re-sending cannot fix")
		}
	})
	t.Run("does not suppress when the setting is off", func(t *testing.T) {
		newAbortFixture(t)
		setAbortResumePref(t, false)
		if AbortResumeHolds("ab1", retryableAbort(now), now) {
			t.Error("suppressing although the setting is off (the old report path is blocked)")
		}
	})
	t.Run("suppresses while an episode is open", func(t *testing.T) {
		newAbortFixture(t)
		_ = abortResumeStates.Write("ab1", abortResumeState{At: now.Format(time.RFC3339), Attempts: 1})
		if !AbortResumeHolds("ab1", retryableAbort(now), now.Add(2*time.Minute)) {
			t.Error("not suppressing in the middle of a resume")
		}
	})
	t.Run("does not suppress an episode past its TTL", func(t *testing.T) {
		newAbortFixture(t)
		_ = abortResumeStates.Write("ab1", abortResumeState{At: now.Format(time.RFC3339)})
		if AbortResumeHolds("ab1", retryableAbort(now), now.Add(abortResumeEpisodeTTL+time.Minute)) {
			t.Error("an episode making no progress keeps suppressing the report")
		}
	})
}

// TestAbortResumeTickClosesEpisode: once the tail is no longer a cut-off, the episode folds
// away. That is the whole mechanism by which a clean completion gives the retry budget back
// (there is no separate counter).
func TestAbortResumeTickClosesEpisode(t *testing.T) {
	newAbortFixture(t)
	m := abMeta()
	session.WriteMeta(m)
	_ = abortResumeStates.Write(m.Name, abortResumeState{At: time.Now().Format(time.RFC3339), Attempts: 2})
	orig := claudeAbortInfo
	claudeAbortInfo = func(string) (claude.Abort, bool) { return claude.Abort{}, false }
	t.Cleanup(func() { claudeAbortInfo = orig })

	abortResumeTick(time.Now())
	if _, ok := abortResumeStates.Read(m.Name); ok {
		t.Error("the cut-off is gone from the tail but the episode remains (no budget for the next cut-off)")
	}
}

// TestAbortResumeTickSkipsDeadAndArchived: the sweep reads no tail for an archived or stopped
// session. That read goes to the claude mount on EFS and, for a session with no transcript,
// sweeps every project directory; doing it for every archived meta every 30s was the dominant
// EFS metadata load (ADR 0087). An archived session's episodes are dropped at once; a stopped
// one's only once they pass their TTL.
func TestAbortResumeTickSkipsDeadAndArchived(t *testing.T) {
	newAbortFixture(t)
	now := time.Now()
	live := abMeta()
	stopped := session.Meta{Name: "ab-stopped", Dir: "/tmp/ab-stopped", Kind: session.KindClaude}
	stale := session.Meta{Name: "ab-stale", Dir: "/tmp/ab-stale", Kind: session.KindClaude}
	// Archived sessions are not alive either; claiming one alive proves the archived flag
	// alone keeps the sweep away from its tail.
	archived := session.Meta{Name: "ab-archived", Dir: "/tmp/ab-archived", Kind: session.KindClaude, Archived: true}
	fresh := now.Format(time.RFC3339)
	for _, m := range []session.Meta{live, stopped, archived} {
		session.WriteMeta(m)
		_ = abortResumeStates.Write(m.Name, abortResumeState{At: fresh, Attempts: 1})
		_ = authResumeStates.Write(m.Name, authResumeState{At: fresh})
	}
	session.WriteMeta(stale)
	_ = abortResumeStates.Write(stale.Name, abortResumeState{At: now.Add(-abortResumeEpisodeTTL - time.Minute).Format(time.RFC3339)})
	_ = authResumeStates.Write(stale.Name, authResumeState{At: now.Add(-authResumeEpisodeTTL - time.Minute).Format(time.RFC3339)})
	abortResumeAlive = func(m session.Meta) bool { return m.Name == live.Name || m.Name == archived.Name }

	var read []string
	orig := claudeAbortInfo
	claudeAbortInfo = func(sid string) (claude.Abort, bool) {
		read = append(read, sid)
		return retryableAbort(now), true
	}
	t.Cleanup(func() { claudeAbortInfo = orig })

	abortResumeTick(now)
	if want := session.UUID(live.Dir, live.Name); len(read) != 1 || read[0] != want {
		t.Errorf("tails read = %v, want only the live session's [%s]", read, want)
	}
	for _, m := range []session.Meta{archived, stale} {
		if _, ok := abortResumeStates.Read(m.Name); ok {
			t.Errorf("%s: the abort episode was kept", m.Name)
		}
		if _, ok := authResumeStates.Read(m.Name); ok {
			t.Errorf("%s: the auth episode was kept", m.Name)
		}
	}
	for _, m := range []session.Meta{live, stopped} {
		if _, ok := abortResumeStates.Read(m.Name); !ok {
			t.Errorf("%s: an abort episode inside its TTL was dropped", m.Name)
		}
	}
	if _, ok := authResumeStates.Read(stopped.Name); !ok {
		t.Error("the stopped session's auth episode inside its TTL was dropped")
	}
}

// TestAbortResumeKeepsBudgetAcrossRuntimeBlip: a Managed runtime reads not-alive while its writer
// socket is reconnected, then comes back to the same conversation with the same cut-off. The
// episode must survive that, or the cut-off gets a fresh retry budget past the cap.
func TestAbortResumeKeepsBudgetAcrossRuntimeBlip(t *testing.T) {
	f := newAbortFixture(t)
	setAbortResumePref(t, true)
	m := abMeta()
	session.WriteMeta(m)
	now := time.Now()
	capped := abortResumeState{At: now.Add(-time.Minute).Format(time.RFC3339), Attempts: chatx.MaxAutoResumeAttempts, GaveUp: abortGaveUpCapped}
	_ = abortResumeStates.Write(m.Name, capped)
	orig := claudeAbortInfo
	claudeAbortInfo = func(string) (claude.Abort, bool) { return retryableAbort(now.Add(-time.Minute)), true }
	t.Cleanup(func() { claudeAbortInfo = orig })

	abortResumeAlive = func(session.Meta) bool { return false }
	abortResumeTick(now)
	abortResumeAlive = func(session.Meta) bool { return true }
	abortResumeTick(now.Add(abortResumeBackoff))

	if len(f.sent) != 0 {
		t.Errorf("sent %v after the blip: the capped cut-off got a fresh budget", f.sent)
	}
	if st := abState(t, m.Name); st.GaveUp != abortGaveUpCapped || st.Attempts != chatx.MaxAutoResumeAttempts {
		t.Errorf("episode after the blip = %+v, want the capped one", st)
	}
}

// TestAbortResumePromptIsShort: the resume prompt is one word plus a note. It is not a long
// instruction because the cut-off happened seconds ago and the context is still there. The
// parenthesis stays so it can be told apart from a "continue" the user typed themselves (the
// injection source is matched on the exact body).
func TestAbortResumePromptIsShort(t *testing.T) {
	for _, locale := range []string{"ja", "en"} {
		p := abortResumePromptFor(locale)
		if len([]rune(p)) > 25 {
			t.Errorf("the %s resume prompt is too long (%d characters): %q", locale, len([]rune(p)), p)
		}
		if p == "続けて" || p == "continue" {
			t.Errorf("the %s resume prompt is a bare single word - indistinguishable from the user's own input: %q", locale, p)
		}
	}
}
