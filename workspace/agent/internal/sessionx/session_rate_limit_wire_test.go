package sessionx

// A session stopped by a usage limit must read as waiting for the limit to reset
// (agents.StateLimited), not as waiting for input (docs/log/47 §4-9). The blocked state of the
// limit modal (nothing moves until a human chooses in the pane) is covered by another test, so
// what is pinned here is the picture after that: the menu has been dismissed automatically, or
// never appeared, and the pane is back at the idle prompt.
//
// False positives (claiming the reset wait when the limit is not, or no longer, in force) are
// pinned just as hard. A row that says "waiting" is one the user leaves alone, so a lie there
// turns into "broken and impossible to notice".

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// atLimitProbe replaces the transcript probe and counts the calls: this is the list-polling
// path, so the count is what lets a test confirm "no transcript read for a session without an
// episode". An empty kind means "not at the limit".
func atLimitProbe(t *testing.T, kind claude.LimitKind) *int {
	t.Helper()
	n := 0
	orig := claudeUsageLimitAbort
	claudeUsageLimitAbort = func(string) (claude.Abort, claude.LimitKind, bool) {
		n++
		if kind == "" {
			return claude.Abort{}, "", false
		}
		return claude.Abort{Msg: "You've hit your session limit."}, kind, true
	}
	t.Cleanup(func() { claudeUsageLimitAbort = orig })
	return &n
}

// limitedMeta writes a claude meta and opens a usage-limit episode for it.
func limitedMeta(t *testing.T, name string, st rateLimitState) session.Meta {
	t.Helper()
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude}
	session.WriteMeta(m)
	if err := RateLimitStates.Write(name, st); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestWireSessionShowsRateLimitWait: with a scheduled episode, and a transcript whose tail is
// still the turn cut off by the limit, both the list and the chat show the reset wait plus the
// scheduled resume time.
func TestWireSessionShowsRateLimitWait(t *testing.T) {
	isolateAgentState(t)
	calls := atLimitProbe(t, claude.LimitWindow)
	now := time.Now()
	resume := now.Add(2 * time.Hour).Format(time.RFC3339)
	m := limitedMeta(t, "rlwire1", rateLimitState{
		At: now.Format(time.RFC3339), Menu: true, Dismissed: true, ResumeAt: resume, ScheduleID: "sch_x",
	})

	s := wireSession(m, true)
	if s.State != agents.StateLimited {
		t.Fatalf("wireSession state = %q, want %q (a limit wait looks the same as waiting for input)", s.State, agents.StateLimited)
	}
	if s.RateLimitResumeAt != resume {
		t.Errorf("rateLimitResumeAt = %q, want %q (cannot say when it will move again)", s.RateLimitResumeAt, resume)
	}
	// The list badge and the chat/mirror chip must claim the same state.
	if got := DriveState(m, true, false); got != agents.StateLimited {
		t.Errorf("DriveState = %q, want %q (the chip disagrees between the list and the body)", got, agents.StateLimited)
	}
	if *calls == 0 {
		t.Error("the transcript was never read — the wait is claimed from the episode file alone")
	}
}

// TestWireSessionShowsSpendLimit: a spend or balance limit (docs/log/47 §4-10) gets its own
// state, not the reset wait. It is the same 429, but waiting never clears it, so a display that
// reads as "wait" leaves the user waiting for a reset that never comes. There is no scheduled
// resume time (nothing is scheduled), so it stays empty.
func TestWireSessionShowsSpendLimit(t *testing.T) {
	isolateAgentState(t)
	atLimitProbe(t, claude.LimitSpend)
	now := time.Now()
	m := limitedMeta(t, "rlwire6", rateLimitState{At: now.Format(time.RFC3339), Spend: true})

	s := wireSession(m, true)
	if s.State != agents.StateSpendLimit {
		t.Fatalf("wireSession state = %q, want %q", s.State, agents.StateSpendLimit)
	}
	if s.RateLimitResumeAt != "" {
		t.Errorf("rateLimitResumeAt = %q, want empty (showing a resume time that will never come)", s.RateLimitResumeAt)
	}
	if got := DriveState(m, true, false); got != agents.StateSpendLimit {
		t.Errorf("DriveState = %q, want %q (the chip disagrees between the list and the body)", got, agents.StateSpendLimit)
	}
}

// TestWireSessionSpendKindComesFromTranscript: the kind is re-derived from the transcript right
// now, not read off the episode file. That way the display follows transitions such as "the
// budget was raised and now the window limit is the one being hit".
func TestWireSessionSpendKindComesFromTranscript(t *testing.T) {
	isolateAgentState(t)
	atLimitProbe(t, claude.LimitWindow)
	now := time.Now()
	m := limitedMeta(t, "rlwire7", rateLimitState{
		At: now.Format(time.RFC3339), Spend: true, // the old record still says spend
		ResumeAt: now.Add(time.Hour).Format(time.RFC3339),
	})

	if s := wireSession(m, true); s.State != agents.StateLimited {
		t.Errorf("state = %q, want %q (the transcript points at the window limit)", s.State, agents.StateLimited)
	}
}

// TestWireSessionRateLimitClearedByTranscript: even with the episode still open, the wait is not
// claimed once the transcript tail is no longer a limit record (the user switched model, or
// another turn ran). Going by the file's lifetime alone (scheduled time plus grace, or the
// 12-hour TTL) would produce a lie here.
func TestWireSessionRateLimitClearedByTranscript(t *testing.T) {
	isolateAgentState(t)
	atLimitProbe(t, "")
	now := time.Now()
	m := limitedMeta(t, "rlwire2", rateLimitState{
		At: now.Format(time.RFC3339), ResumeAt: now.Add(time.Hour).Format(time.RFC3339),
	})

	if s := wireSession(m, true); s.State == agents.StateLimited {
		t.Errorf("state = %q — stuck on the reset wait after the limit cleared", s.State)
	}
	if got := DriveState(m, true, false); got == agents.StateLimited {
		t.Errorf("DriveState = %q — same as above", got)
	}
}

// TestWireSessionRateLimitEpisodeExpired: an episode past its scheduled time plus grace (about
// to be folded away) does not claim the wait. A reset wait pointing at a time already gone
// cannot be read as "wait and it will fix itself".
func TestWireSessionRateLimitEpisodeExpired(t *testing.T) {
	isolateAgentState(t)
	calls := atLimitProbe(t, claude.LimitWindow)
	now := time.Now()
	m := limitedMeta(t, "rlwire3", rateLimitState{
		At:       now.Add(-6 * time.Hour).Format(time.RFC3339),
		ResumeAt: now.Add(-rateLimitCleanupGrace - time.Hour).Format(time.RFC3339),
	})

	if s := wireSession(m, true); s.State == agents.StateLimited {
		t.Errorf("state = %q — claiming the wait on a finished episode", s.State)
	}
	if *calls != 0 {
		t.Error("read the transcript for a finished episode (prune on the state file first)")
	}
}

// TestWireSessionWithoutEpisodeSkipsTranscript: an ordinary session that has not hit a limit
// gets neither a changed state nor a transcript read. List polling sweeps every claude session
// each round, so reading the transcript here would turn "detecting something that rarely
// happens" into a permanent cost of polling.
func TestWireSessionWithoutEpisodeSkipsTranscript(t *testing.T) {
	isolateAgentState(t)
	calls := atLimitProbe(t, claude.LimitWindow)
	m := session.Meta{Name: "rlwire4", Dir: t.TempDir(), Kind: session.KindClaude}
	session.WriteMeta(m)

	if s := wireSession(m, true); s.State == agents.StateLimited {
		t.Errorf("state = %q — claiming the wait with no episode at all", s.State)
	}
	if *calls != 0 {
		t.Errorf("read the transcript %d times — a session without an episode must not be read", *calls)
	}
}

// TestWireSessionRateLimitOnlyWhileAlive: a stopped session stays stopped. A limit episode is a
// state of a live session and must not overwrite what the row means.
func TestWireSessionRateLimitOnlyWhileAlive(t *testing.T) {
	isolateAgentState(t)
	atLimitProbe(t, claude.LimitWindow)
	now := time.Now()
	m := limitedMeta(t, "rlwire5", rateLimitState{
		At: now.Format(time.RFC3339), ResumeAt: now.Add(time.Hour).Format(time.RFC3339),
	})

	if s := wireSession(m, false); s.State == agents.StateLimited || s.RateLimitResumeAt != "" {
		t.Errorf("stopped session: state = %q / resumeAt = %q, want empty", s.State, s.RateLimitResumeAt)
	}
}
