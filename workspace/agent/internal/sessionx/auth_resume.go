package sessionx

// Picking a session back up after the login was renewed (docs/log/47 §4-11).
//
// A turn killed by expired credentials is BLOCKED, not retryable: re-sending it reproduces the
// same 401 until a person signs in again. abort_resume.go therefore leaves it alone, and until
// now that was the end of it — the user re-authenticated in Settings > Agents and the session
// simply stayed where it died, with the work half-done and nothing saying it could now go on.
// The only route back was to type "continue" by hand, which is exactly the round trip
// docs/log/47 §4-6 removed for every other kind of cut-off.
//
// So this is the same watcher with a different clock. A retryable cut-off waits out a fault
// that clears in seconds; this one waits for a PERSON, for as long as that takes, and the event
// it waits for is the login being renewed.
//
//	renewed := claude.AuthOKAt() is AFTER the failed turn
//
// mtime, not "is the login valid right now". The failure this recovers from is frequently a
// server-side revocation, and after one of those the local credentials file goes on describing
// a perfectly healthy login — so "valid now" is true from the very first tick and would resume
// straight into the same 401. What cannot be faked is that the credential in force was WRITTEN
// after the turn that died: only signing in again (or a token refresh that succeeded, which is
// equally good news) rewrites that file.
//
// The Console's error block flips to "re-authenticated" on the very same comparison, done on
// the same two values (Session.AuthOkAt against the turn's timestamp). One rule, so the card
// cannot claim the login is fixed while the session sits there unresumed, or the reverse.

import (
	"log"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

const (
	// authResumeSettle is how long the renewed credential must have been on disk before the
	// resume is sent. claude writes .credentials.json during a sign-in it has not finished
	// wiring up yet, and the pane may still be in its own /login flow; a few seconds cost
	// nothing against a wait that was minutes long.
	authResumeSettle = 15 * time.Second
	// authResumeBackoff is the wait before a SECOND attempt in the same episode. A resume
	// that died again means the new credential is not the fix (a different account, a
	// still-blocked organisation), so slow down rather than burn the attempts at once.
	authResumeBackoff = 5 * time.Minute
	// authResumeMaxDeliverTries bounds attempts that never reached the session. More generous
	// than the retryable path's: signing in and then working in the pane by hand is exactly
	// what a user does here, and every such tick counts as undeliverable.
	authResumeMaxDeliverTries = 5
	// authResumeEpisodeTTL retires an episode nothing is happening on. Long, because the wait
	// is a person's: a login that expires on Friday evening is renewed on Monday, and giving
	// up over the weekend would leave precisely the case this feature exists for unresumed.
	authResumeEpisodeTTL = 14 * 24 * time.Hour
)

// Reasons for giving up (GaveUp). Empty = still waiting for, or acting on, the renewal.
const (
	authGaveUpCapped        = "capped"        // resumed, and the login error came straight back
	authGaveUpUndeliverable = "undeliverable" // the resume prompt cannot be delivered
	authGaveUpStale         = "stale"         // the episode passed its TTL
)

// authResumeState is one login-failure episode for one session. It opens when the transcript
// tail is an auth abort and closes when the tail is no longer one — the resume worked, the user
// continued by hand, or they moved on to something else.
type authResumeState struct {
	At           string `json:"at"`                     // episode t0 = the failed turn's time (else detection time)
	Msg          string `json:"msg,omitempty"`          // the error text, for the log and the give-up reason
	Attempts     int    `json:"attempts,omitempty"`     // resume prompts actually sent
	DeliverTries int    `json:"deliverTries,omitempty"` // attempts that never got through
	LastTry      string `json:"lastTry,omitempty"`      // the latest attempt, successful or not
	GaveUp       string `json:"gaveUp,omitempty"`       // non-empty = handed back to the user
}

var authResumeStates = fstore.JSON[authResumeState](paths.AgentConfigDir, "session-auth-resume", ".json")

// authResumeNow is the login moment, replaceable so tests need no credentials file.
var authResumeNow = claude.AuthOKAt

// authResumeStep advances the login-failure episode of one session. It is driven from
// abortResumeTick rather than from a loop of its own so that the transcript tail is read once
// per session per sweep; `a`/`ok` are that read's verdict.
func authResumeStep(m session.Meta, a claude.Abort, ok bool, now time.Time) {
	if !ok || !a.Auth {
		// The tail is no longer a login failure: it was resumed (by us or by the user), or
		// the session moved on. Nothing left to wait for.
		if _, has := authResumeStates.Read(m.Name); has {
			authResumeStates.Remove(m.Name)
		}
		return
	}
	if !SessionAlive(m) {
		return // a stopped session is resumed by starting it, not by typing into it
	}
	if !uiprefs.AbortAutoResume() {
		return // off: the failure was reported as before and the user drives the recovery
	}
	st, _ := authResumeStates.Read(m.Name)
	authResumeAttempt(m, st, a, now)
}

// authResumeAttempt advances one open episode: open → wait for the renewal → inject → give up.
// Split from the gates above so it can be driven without a tmux session (the same seam as
// abortResumeAttempt).
func authResumeAttempt(m session.Meta, st authResumeState, a claude.Abort, now time.Time) {
	if st.At == "" {
		st.At = abortEpisodeStart(a, now)
		st.Msg = a.Msg
		// Persist on opening, for the same reason as the retryable path: every branch below
		// returns early, so without this the episode would be reborn on every sweep and the
		// attempt count would never survive.
		_ = authResumeStates.Write(m.Name, st)
		log.Printf("auth-resume: the turn of %s died on the login (%s) — waiting for it to be renewed", m.Name, a.Msg)
	}
	if st.GaveUp != "" {
		return
	}
	if authEpisodeStale(st, now) {
		st.GaveUp = authGaveUpStale
		log.Printf("auth-resume: giving up on %s (%s)", m.Name, st.GaveUp)
		_ = authResumeStates.Write(m.Name, st)
		return
	}
	if st.Attempts >= chatx.MaxAutoResumeAttempts {
		// Resumed on a renewed login and it failed on the login again: whatever was signed in
		// with does not fix this session. Line the counter up so the report comes out with the
		// "cap reached" wording, exactly as the retryable path does.
		st.GaveUp = authGaveUpCapped
		chatx.SetAutoResumeAttempts(m.Name, st.Attempts)
		log.Printf("auth-resume: giving up on %s (the login error came back %d times)", m.Name, st.Attempts)
		_ = authResumeStates.Write(m.Name, st)
		return
	}
	if !authResumeRenewed(st, now) {
		return // still the login that failed — there is nothing to resume onto yet
	}
	if !authResumeDue(st, now) {
		return // inside the backoff after a previous attempt
	}
	if !abortResumeReady(m.Name) {
		// A modal, a running turn, or the send guard still refusing on expired credentials
		// (promptBlocker answers "auth" while they are dead). Count it and step back if it
		// keeps happening — a person may be working in the session.
		st.DeliverTries++
		st.LastTry = now.Format(time.RFC3339)
		if st.DeliverTries >= authResumeMaxDeliverTries {
			st.GaveUp = authGaveUpUndeliverable
			log.Printf("auth-resume: cannot deliver the resume prompt to %s, giving up", m.Name)
		}
		_ = authResumeStates.Write(m.Name, st)
		return
	}
	prompt := abortResumePrompt()
	// Record before sending: a crash in between must not roll the count back and let this
	// fire again (the same reason as abortResumeAttempt).
	st.Attempts++
	st.LastTry = now.Format(time.RFC3339)
	_ = authResumeStates.Write(m.Name, st)
	if err := abortResumeInject(m.Name, prompt); err != nil {
		st.Attempts--
		st.DeliverTries++
		if st.DeliverTries >= authResumeMaxDeliverTries {
			st.GaveUp = authGaveUpUndeliverable
		}
		_ = authResumeStates.Write(m.Name, st)
		log.Printf("auth-resume: failed to send the resume prompt to %s: %v", m.Name, err)
		return
	}
	recordInjection(m.Name, prompt, TurnSourceAutoResume)
	log.Printf("auth-resume: %s was re-authenticated, resumed automatically (attempt %d/%d)",
		m.Name, st.Attempts, chatx.MaxAutoResumeAttempts)
}

// authResumeRenewed reports whether the login in force was written after the turn that died on
// it — the one fact that says a person has been through Settings > Agents since. It must also
// have settled (authResumeSettle), so the resume does not land in the middle of a sign-in.
//
// An unreadable episode time (only possible if the file was hand-edited) counts as NOT renewed:
// resuming on a login that may well be the one that just failed is the costlier mistake — it
// spends an attempt and puts a second identical failure in the conversation.
func authResumeRenewed(st authResumeState, now time.Time) bool {
	at, err := time.Parse(time.RFC3339, st.At)
	if err != nil {
		return false
	}
	ok := authResumeNow()
	if ok.IsZero() {
		return false // nothing to judge on (no credentials file, an environment token)
	}
	return ok.After(at) && !now.Before(ok.Add(authResumeSettle))
}

// authResumeDue applies the backoff between attempts. The FIRST attempt has no delay of its
// own: authResumeRenewed already held it back until the renewal settled.
func authResumeDue(st authResumeState, now time.Time) bool {
	if st.LastTry == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, st.LastTry)
	return err != nil || !now.Before(t.Add(authResumeBackoff))
}

func authEpisodeStale(st authResumeState, now time.Time) bool {
	t, err := time.Parse(time.RFC3339, st.At)
	return err != nil || now.After(t.Add(authResumeEpisodeTTL))
}
