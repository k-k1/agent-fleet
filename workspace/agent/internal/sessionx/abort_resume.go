package sessionx

// Automatic resume from a cut-off that a re-send fixes: a dropped connection, a transient
// rate limit, the stream watchdog (docs/log/47 §4-6).
//
// The resume of docs/log/47 §3-4 was assistant-driven: cut-off → completion report →
// the operator sends "continue" with send_to_session. That has two gaps.
//
//	1. A session not tied to a conversation (launched straight from the Console) is never
//	   resumed. There is nowhere to report to, so the cut-off is notified but nobody
//	   resumes it (the item left over in §5).
//	2. Even with a conversation, every round trip runs one assistant turn. A cut-off only
//	   re-runs work the user already asked for and carries no judgement, yet it paid the
//	   tokens of a judgement-making LLM every time.
//
// So the first move of a resume is sent by the Agent itself, as the usage-limit path already
// does as an exception (rate_limit_resume.go). The assistant only catches what was given up
// on: if the cut-off persists after re-sending up to the cap (maxAutoResumeAttempts), it is
// not a transient fault, and only then is it reported and escalated to the user.
//
// ADR0030 §3's first reason for avoiding a direct send from the Agent, "who sent what becomes
// invisible", is resolved by the injection-source record of docs/log/37/38 (recordInjection →
// the mirror's badge). The resume prompt stays in the transcript with source auto-resume and
// is distinguishable in the mirror.
//
// Suppressing the report is not the same as swallowing the cut-off. The cut-off notification
// (notification centre) appears as before; what is held back is only the report into the
// conversation, i.e. the assistant turn. Once the resumed turn completes, that completion
// report closes the one instruction correctly (two reports become one). The suppression is
// decided by collectAbortSignal / evalReportEvidence in chat_report_reconcile.go, which read
// AbortResumeHolds.

import (
	"log"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

const (
	// abortResumeWatchInterval is the sweep cadence. Shorter than the usage-limit episode
	// (1 minute) because what it faces here is a transient fault that clears in seconds, and
	// the wait is the user's wait. One sweep costs a transcript-tail read per claude session.
	abortResumeWatchInterval = 30 * time.Second
	// abortResumeFirstDelay is how long to wait after the cut-off before the first
	// resume. Firing immediately is wrong for 529 / overloaded: re-sending before the cause
	// clears just draws the same cut-off again, and throws away one of the few retries.
	abortResumeFirstDelay = 30 * time.Second
	// abortResumeBackoff is the wait before a SECOND resume in the same episode. A first
	// attempt that also ended in a cut-off means the other side is still unhealthy.
	abortResumeBackoff = 5 * time.Minute
	// abortResumeMaxDeliverTries bounds the injection attempts that never reached the
	// session (the pane cannot be read, the injection fails). Retrying does not fix that
	// kind of failure, so give up and hand it to the assistant / the user.
	abortResumeMaxDeliverTries = 3
	// abortResumeEpisodeTTL retires an episode that stopped making progress. A safety net,
	// not the normal path (normally an episode closes when the transcript tail is no longer
	// a cut-off) — without it, a file stuck in an unwritable state could suppress reports
	// forever.
	abortResumeEpisodeTTL = 30 * time.Minute
)

// Reasons for giving up (GaveUp). Empty = automatic resume is still taking care of it.
const (
	abortGaveUpCapped        = "capped"        // re-sent, cut-off persists (maxAutoResumeAttempts in a row)
	abortGaveUpUndeliverable = "undeliverable" // the resume prompt cannot be delivered to the session
	abortGaveUpStale         = "stale"         // the episode passed its TTL (no progress)
)

// abortResumeState is one cut-off episode for one session: it opens when the transcript
// tail is a retryable abort and closes when the tail is no longer one (the resume worked, the
// user moved on themselves, or the turn ended normally). It lives in its own file for the same
// reason rateLimitState does.
type abortResumeState struct {
	At           string `json:"at"`                     // episode start (the abort record's time, else the detection time)
	Msg          string `json:"msg,omitempty"`          // the abort text (for logs and the give-up reason)
	Attempts     int    `json:"attempts,omitempty"`     // resume prompts actually sent
	DeliverTries int    `json:"deliverTries,omitempty"` // attempts that never got through
	LastTry      string `json:"lastTry,omitempty"`      // the latest attempt, successful or not
	GaveUp       string `json:"gaveUp,omitempty"`       // non-empty = auto-resume stepped back (suppression lifts too)
}

var abortResumeStates = fstore.JSON[abortResumeState](paths.AgentConfigDir, "session-abort-resume", ".json")

type managedAbortSignal struct {
	At  string `json:"at"`
	Msg string `json:"msg"`
}

var managedAbortSignals = fstore.JSON[managedAbortSignal](paths.AgentConfigDir, "session-managed-abort", ".json")

// The side effects stay replaceable (tests have no tmux).
var (
	abortResumeInject      = injectSessionPrompt
	abortResumeReadingPane = func(name string) tmuxx.PaneRead { return tmuxx.ReadPane(name) }
)

// StartAbortResumeWatch runs the sweep in its own loop. It does not ride on the list polling,
// for the same reason as rate_limit_resume.go: it is pointless unless it works while nobody is
// looking at a screen.
func StartAbortResumeWatch() {
	go func() {
		time.Sleep(40 * time.Second) // wait for tmux to come up after a start
		for {
			abortResumeTick(time.Now())
			time.Sleep(abortResumeWatchInterval)
		}
	}()
}

// abortResumeTick is one sweep over every session kind whose driver can distinguish a
// retryable cut-off from a permanent failure.
//
// ListMetas is the only gate on the population (as in rateLimitTick): whether a session is tied
// to a conversation or has a row in the instruction ledger is irrelevant, and a standalone
// session launched from the Console is treated exactly the same — that is the point of this
// feature.
func abortResumeTick(now time.Time) {
	for _, m := range session.ListMetas() {
		st, has := abortResumeStates.Read(m.Name)
		a, ok := abortInfoFor(m)
		if !ok || !a.Retryable {
			// The tail is not a cut-off: the resume worked, the user moved on themselves,
			// or the turn ended normally. Blocked aborts (usage limit, balance, prompt too
			// long) close here as well; those go straight to the report as before, with no
			// suppression.
			if has {
				abortResumeStates.Remove(m.Name)
			}
			continue
		}
		if !SessionAlive(m) {
			continue // a dead session belongs to record_exit.go (an abnormal exit, not a cut-off)
		}
		if !uiprefs.AbortAutoResume() {
			continue // off: no episode is opened, so nothing is suppressed (the old report path)
		}
		abortResumeAttempt(m, st, a, now)
	}
}

func abortInfoFor(m session.Meta) (claude.Abort, bool) {
	if NormalizeKind(m.Kind) == session.KindClaude {
		return claudeAbortInfo(session.UUID(m.Dir, m.Name))
	}
	if m.DriverKind() != session.DriverManaged || (m.Kind != session.KindCodex && m.Kind != session.KindOpencode) {
		return claude.Abort{}, false
	}
	s, ok := managedAbortSignals.Read(m.Name)
	if !ok {
		return claude.Abort{}, false
	}
	at, _ := time.Parse(time.RFC3339, s.At)
	return claude.Abort{Msg: s.Msg, Retryable: true, At: at}, true
}

// abortResumeAttempt advances one open episode: open → wait out the backoff → inject → give up.
func abortResumeAttempt(m session.Meta, st abortResumeState, a claude.Abort, now time.Time) {
	if st.At == "" {
		st.At = abortEpisodeStart(a, now)
		st.Msg = a.Msg
		// Always write on opening. The branches below return early, so without persisting
		// here the episode would be born again every tick, and the report suppression
		// (AbortResumeHolds) would rest on the short "the cut-off is still fresh" window
		// instead of on the file.
		_ = abortResumeStates.Write(m.Name, st)
		log.Printf("abort-resume: the turn of %s ended in a cut-off (%s)", m.Name, a.Msg)
	}
	if st.GaveUp != "" {
		return // an episode already stepped back from; the report path has taken it over
	}
	if abortEpisodeStale(st, now) {
		st.GaveUp = abortGaveUpStale
		log.Printf("abort-resume: giving up on the automatic resume of %s (%s)", m.Name, st.GaveUp)
		_ = abortResumeStates.Write(m.Name, st)
		escalateManagedAbort(m, st)
		return
	}
	if st.Attempts >= chatx.MaxAutoResumeAttempts {
		// The cut-off survives re-sending, so it is not a transient fault. From here it is
		// the assistant's / the user's business, so line the counter up first, to make the
		// report come out with the "cap reached" wording (reportKeyTurnAbortedCapped), and
		// only then lift the suppression.
		st.GaveUp = abortGaveUpCapped
		chatx.SetAutoResumeAttempts(m.Name, st.Attempts)
		log.Printf("abort-resume: giving up on the automatic resume of %s (%d cut-offs in a row)", m.Name, st.Attempts)
		_ = abortResumeStates.Write(m.Name, st)
		escalateManagedAbort(m, st)
		return
	}
	if !abortResumeDue(st, now) {
		return // inside the backoff
	}
	if !abortResumeReady(m.Name) {
		// A pending question / plan / permission, a modal, a running turn — it cannot be
		// sent now. Count it as an undelivered attempt and give up if it keeps happening
		// (a person may be operating the session).
		st.DeliverTries++
		st.LastTry = now.Format(time.RFC3339)
		if st.DeliverTries >= abortResumeMaxDeliverTries {
			st.GaveUp = abortGaveUpUndeliverable
			log.Printf("abort-resume: cannot deliver the resume prompt to %s, giving up", m.Name)
		}
		_ = abortResumeStates.Write(m.Name, st)
		if st.GaveUp != "" {
			escalateManagedAbort(m, st)
		}
		return
	}
	prompt := abortResumePrompt()
	// Record before sending, so a crash midway cannot roll the count back and keep firing
	// (the same reason as rateLimitRecover).
	st.Attempts++
	st.LastTry = now.Format(time.RFC3339)
	_ = abortResumeStates.Write(m.Name, st)
	if err := abortResumeInject(m.Name, prompt); err != nil {
		st.Attempts--
		st.DeliverTries++
		if st.DeliverTries >= abortResumeMaxDeliverTries {
			st.GaveUp = abortGaveUpUndeliverable
		}
		_ = abortResumeStates.Write(m.Name, st)
		if st.GaveUp != "" {
			escalateManagedAbort(m, st)
		}
		log.Printf("abort-resume: failed to send the resume prompt to %s: %v", m.Name, err)
		return
	}
	recordInjection(m.Name, prompt, TurnSourceAutoResume)
	log.Printf("abort-resume: automatically resumed %s (attempt %d/%d)", m.Name, st.Attempts, chatx.MaxAutoResumeAttempts)
}

// abortResumeReady reports whether a free-text prompt may be typed into the session right
// now. injectSessionPrompt rejects a waiting state itself; this check comes first so that the
// reason it cannot be sent is counted on the episode (if it keeps being rejected, give up and
// hand it to a person).
//
// Rejecting a busy pane is the crux: the abort record stays at the tail of the transcript, so
// the tail still looks like a cut-off even after the user resumed by hand. Firing "continue"
// at a running turn would turn it into an interrupting instruction.
func abortResumeReady(name string) bool {
	if promptBlocker(name) != "" {
		return false
	}
	if m, ok := session.ReadMeta(name); ok && m.DriverKind() == session.DriverManaged {
		return SessionAlive(m) && DriveState(m, true, false) == "idle"
	}
	pr := abortResumeReadingPane(name)
	return pr.OK && pr.Idle && !pr.Busy && !pr.RateLimitMenu
}

func escalateManagedAbort(m session.Meta, st abortResumeState) {
	if m.DriverKind() != session.DriverManaged {
		return
	}
	RecordSessionNotification(session.UUID(m.Dir, m.Name), "working", agents.StateAborted, st.Msg)
}

// abortResumeDue applies the backoff: abortResumeFirstDelay after the cut-off for the first
// attempt, abortResumeBackoff after the latest attempt for the ones after it.
func abortResumeDue(st abortResumeState, now time.Time) bool {
	if st.LastTry != "" {
		t, err := time.Parse(time.RFC3339, st.LastTry)
		return err != nil || !now.Before(t.Add(abortResumeBackoff))
	}
	t, err := time.Parse(time.RFC3339, st.At)
	return err != nil || !now.Before(t.Add(abortResumeFirstDelay))
}

// abortEpisodeStart is the episode's t0: the abort record's time, or the detection time when
// there is none. The record's time is used so that "when it stopped" does not move across an
// Agent restart.
func abortEpisodeStart(a claude.Abort, now time.Time) string {
	if !a.At.IsZero() {
		return a.At.Format(time.RFC3339)
	}
	return now.Format(time.RFC3339)
}

func abortEpisodeStale(st abortResumeState, now time.Time) bool {
	t, err := time.Parse(time.RFC3339, st.At)
	return err != nil || now.After(t.Add(abortResumeEpisodeTTL))
}

// AbortResumeHolds reports whether the automatic resume has taken responsibility for this
// cut-off — i.e. the reconciler must NOT deliver an aborted-turn report yet (docs/log/47 §4-6).
//
// It also suppresses while the episode's file does not exist yet (the sweep runs every 30s, so
// this is always the state right after a cut-off), but only while the cut-off is fresh: if the
// time cannot be read, or the cut-off is old and there is still no episode, the watcher is not
// running (feature off, an old Agent, a dead loop), so report as before instead of suppressing.
// That keeps the suppression from becoming a one-way ticket.
func AbortResumeHolds(name string, a claude.Abort, now time.Time) bool {
	if !a.Retryable || !uiprefs.AbortAutoResume() {
		return false
	}
	st, ok := abortResumeStates.Read(name)
	if !ok {
		return !a.At.IsZero() && now.Sub(a.At) < abortResumeFirstDelay+abortResumeWatchInterval
	}
	if st.GaveUp != "" {
		return false
	}
	return !abortEpisodeStale(st, now)
}

// abortResumePrompt is the nudge itself, and one word is enough. The cut-off happened seconds
// ago with the conversation and the work state still intact, so anything beyond "continue" only
// repeats context. (The usage-limit resume text is long because it arrives hours later, after a
// workspace restart — a different situation.) The parenthesised word is added for two reasons:
//
//  1. It is distinguishable from a "continue" the user typed themselves. The injection
//     source is matched on the exact body (recordInjection), so a bare word would make the
//     user's own input look like an automatic resume.
//  2. Both the transcript and the mirror keep the fact that this is self-healing, not a new
//     instruction.
//
// The language follows the display language (the same reason as rateLimitResumePrompt: with no
// per-session language, the language that user reads and writes is the best guess).
func abortResumePrompt() string { return abortResumePromptFor(uiprefs.Locale()) }

func abortResumePromptFor(locale string) string {
	if locale == "en" {
		return "continue (auto-resume)"
	}
	return "続けて（自動再開）"
}
