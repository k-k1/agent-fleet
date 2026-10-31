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
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
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
	// The three below apply only to claude giving up on its token-refresh lock
	// (claude.RefreshStuck), where nobody has to sign in: the holder of the lock finishes the
	// refresh by itself, measured 19s after the failed turn. Waiting for a person's pace (the
	// 15s settle and a 30s sweep) would turn that into a minute of dead session.
	//
	// authResumeRefreshSettle is the settle for that case: the pane is not in a /login flow.
	authResumeRefreshSettle = 3 * time.Second
	// authResumeFastPoll is how often the credentials file is stat'ed while such an episode is
	// open. One stat of one file, not a sweep over every session's transcript.
	authResumeFastPoll = 3 * time.Second
	// authResumeRefreshForceAfter is when to resume WITHOUT a rewritten credential. A holder
	// that really died leaves a lock that goes stale after 60s, and claude does not retry a
	// refresh on its own, so the session would wait forever; a resume makes it try again.
	authResumeRefreshForceAfter = 90 * time.Second
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
	// Kind is the session's kind when it is a recoverer kind (agents.AuthRecoverer), which says
	// whose credentials mtime the renewal is read from. Empty = claude, so claude's stored
	// episodes and its tests are unchanged.
	Kind string `json:"kind,omitempty"`
}

var authResumeStates = fstore.JSON[authResumeState](paths.AgentStateDir, "session-auth-resume", ".json")

// authResumeNow is the login moment, replaceable so tests need no credentials file.
var authResumeNow = claude.AuthOKAt

// authResumeMu serializes authResumeStep: the sweep and the fast watch below both call it, and
// the attempt count they read-modify-write must not be lost between them.
var authResumeMu sync.Mutex

// authResumeFastWatch starts the quick poll for a refresh-lock episode; a var so tests do not
// spawn it. Assigned in init because startRefreshWatch leads back here (an initialization cycle).
var authResumeFastWatch func(session.Meta, authResumeState)

func init() { authResumeFastWatch = startRefreshWatch }

// refreshWatchTail reads the transcript tail for the watcher, and refreshWatchHandOver is what it
// hands the session to; vars so a test can stand in for the transcript and the live session.
var (
	refreshWatchTail     = abortInfoFor
	refreshWatchHandOver = authResumeStep
)

// refreshWatching holds the sessions that already have a fast watch running.
var refreshWatching sync.Map

// authResumeStep advances the login-failure episode of one session. It is driven from
// abortResumeTick rather than from a loop of its own so that the transcript tail is read once
// per session per sweep; `a`/`ok` are that read's verdict.
func authResumeStep(m session.Meta, a claude.Abort, ok bool, now time.Time) {
	authResumeMu.Lock()
	defer authResumeMu.Unlock()
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
	if t, rec := authRecovererOf(m.Kind); rec && t.LastFailure != nil {
		if f, has := t.LastFailure(m); has && f.NoTurn {
			// The login rejected turn/start itself: the thread holds no record of the user's
			// instruction, so "continue" would run whatever came before it. The send block
			// still lifts on re-authentication; the instruction is the user's to repeat.
			if _, open := authResumeStates.Read(m.Name); open {
				authResumeStates.Remove(m.Name)
			}
			return
		}
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
		if _, ok := authRecovererOf(m.Kind); ok {
			st.Kind = m.Kind
		}
		// Persist on opening, for the same reason as the retryable path: every branch below
		// returns early, so without this the episode would be reborn on every sweep and the
		// attempt count would never survive.
		_ = authResumeStates.Write(m.Name, st)
		log.Printf("auth-resume: the turn of %s died on the login (%s) — waiting for it to be renewed", m.Name, a.Msg)
		if stuckFor(st.Kind, a.Msg) {
			// claude does not say why the lock was contended, so record what can be seen from
			// outside while it is still fresh.
			log.Printf("auth-resume: %s refresh lock state: %s", m.Name, claude.RefreshLockSnapshot(now))
			authResumeFastWatch(m, st)
		}
	}
	if st.GaveUp != "" {
		return
	}
	if refreshEpisodeRestarted(st, a) || authFailureRepeated(st, a) {
		// A new failure inside one episode that the refresh-lock shortcuts must not inherit: the
		// kind changed (a forced resume after a refresh-lock error that then died on a revoked
		// login, or the reverse), or another refresh-lock failure landed after the episode's
		// clock (a manual retry that the sweep never saw between). Restart the clock at the new
		// failure and wait for a credential newer than it. Attempts and LastTry are kept, so the
		// cap and the backoff still count the resumes already spent.
		st.Msg = a.Msg
		st.At = abortEpisodeStart(a, now)
		_ = authResumeStates.Write(m.Name, st)
		log.Printf("auth-resume: a new failure of %s restarted the episode (%s)", m.Name, a.Msg)
		if stuckFor(st.Kind, a.Msg) {
			authResumeFastWatch(m, st)
		}
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
	if st.Kind != "" && authBlockedFor(m) {
		// The kind's own verdict at full precision: the episode clock above is stored to the
		// second, so a credential written in the failure's second would read as renewed.
		return
	}
	if !authResumeRenewed(st, now) && !authResumeForced(st, now) {
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
	if err := abortResumeInject(m.Name, prompt, agents.Origin{Kind: agents.OriginAutoResume}); err != nil {
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
	recordFleetGraphInstruct(m.Name, TurnSourceAutoResume, "", "", prompt)
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
	ok := authOKAtFor(st)
	if ok.IsZero() {
		return false // nothing to judge on (no credentials file, an environment token)
	}
	settle := authResumeSettle
	if stuckFor(st.Kind, st.Msg) {
		settle = authResumeRefreshSettle
	}
	return ok.After(at) && !now.Before(ok.Add(settle))
}

// stuckFor is claude.RefreshStuck for an episode of claude (empty kind) only: the refresh-lock
// shortcuts read claude's lock and credentials, so a recoverer kind's message never takes them.
func stuckFor(kind, msg string) bool { return kind == "" && claude.RefreshStuck(msg) }

// authOKAtFor is the login moment the episode is judged on: claude's, or the recoverer kind's
// own credentials file.
func authOKAtFor(st authResumeState) time.Time {
	if st.Kind != "" {
		if t, ok := authRecovererOf(st.Kind); ok && t.CredentialsWrittenAt != nil {
			return t.CredentialsWrittenAt()
		}
		return time.Time{}
	}
	return authResumeNow()
}

// authFailureRepeated reports that a recoverer kind's turn failed on the login AGAIN after the
// episode's clock: the resume (or the user's own retry) died the same way, so the credentials
// that were judged renewed are not the fix. The clock moves to the new failure, and the next
// resume waits for a credential written after IT; attempts and LastTry are kept, so the cap and
// the backoff still count what was spent. Without this a revoked login would be resumed once
// per backoff until the cap, on a credentials file that is newer than the first failure only.
func authFailureRepeated(st authResumeState, a claude.Abort) bool {
	if st.Kind == "" || a.At.IsZero() {
		return false
	}
	at, err := time.Parse(time.RFC3339, st.At)
	return err == nil && a.At.Truncate(time.Second).After(at)
}

// refreshEpisodeRestarted reports that a, the failure now at the tail, is not the one this open
// episode was started for, in a way that matters to the refresh-lock shortcuts.
func refreshEpisodeRestarted(st authResumeState, a claude.Abort) bool {
	if stuckFor(st.Kind, st.Msg) != stuckFor(st.Kind, a.Msg) {
		return true
	}
	if !stuckFor(st.Kind, a.Msg) || a.At.IsZero() {
		return false
	}
	at, err := time.Parse(time.RFC3339, st.At)
	// st.At was stored with its fraction dropped, so compare at the same precision.
	return err == nil && a.At.Truncate(time.Second).After(at)
}

// authResumeForced reports that a refresh-lock episode has outlived any lock claude could still
// be waiting on, so the resume goes out without a credential rewritten after the failure (see
// authResumeRefreshForceAfter). The attempt cap and backoff still bound it.
//
// It stands in for ONE missing fact only, "nothing was written since the failure". It still needs
// a login that can be judged (a readable OAuth record whose refresh half is alive: no file, an
// unparsable file, an environment token or a dead login give no moment, and nothing is forced
// onto them), and a credential written moments ago must settle first, as authResumeRenewed
// demands, since that write may be a sign-in still being wired up.
func authResumeForced(st authResumeState, now time.Time) bool {
	if !stuckFor(st.Kind, st.Msg) {
		return false
	}
	at, err := time.Parse(time.RFC3339, st.At)
	if err != nil || now.Before(at.Add(authResumeRefreshForceAfter)) {
		return false
	}
	ok := authResumeNow()
	if ok.IsZero() {
		return false
	}
	return !ok.After(at) || !now.Before(ok.Add(authResumeRefreshSettle))
}

// startRefreshWatch polls the credentials file for a refresh-lock episode and hands the session
// back to authResumeStep the moment the refresh lands (or the force point passes). The loop
// re-reads the episode on every poll, so a restarted episode (refreshEpisodeRestarted) is followed
// rather than abandoned, and it ends when the episode is gone, is no longer a refresh-lock one,
// or is past the force point by a minute; the 30s sweep stays the fallback.
func startRefreshWatch(m session.Meta, _ authResumeState) {
	if _, busy := refreshWatching.LoadOrStore(m.Name, struct{}{}); busy {
		return // the running watcher reads the current episode itself
	}
	go func() {
		defer refreshWatching.Delete(m.Name)
		for {
			time.Sleep(authResumeFastPoll)
			if done := refreshWatchStep(m, time.Now()); done {
				return
			}
		}
	}()
}

// refreshWatchStep is one poll of startRefreshWatch; done ends the watch.
func refreshWatchStep(m session.Meta, now time.Time) (done bool) {
	st, has := authResumeStates.Read(m.Name)
	if !has || st.GaveUp != "" || !stuckFor(st.Kind, st.Msg) {
		return true
	}
	at, err := time.Parse(time.RFC3339, st.At)
	if err != nil || now.After(at.Add(authResumeRefreshForceAfter+time.Minute)) {
		return true
	}
	renewed := authResumeRenewed(st, now)
	if !renewed && !authResumeForced(st, now) {
		return false
	}
	if renewed {
		log.Printf("auth-resume: %s refresh finished %s after the failed turn",
			m.Name, authResumeNow().Sub(at).Round(time.Second))
	}
	a, ok := refreshWatchTail(m)
	refreshWatchHandOver(m, a, ok, now)
	// The tail read just now can be a newer failure than the episode this poll judged; the hand
	// over then restarted the episode, and its own watcher request was refused because this one
	// holds the key. Carry on for that episode instead of leaving it to the 30s sweep.
	next, has := authResumeStates.Read(m.Name)
	return !(has && next.GaveUp == "" && stuckFor(next.Kind, next.Msg) && next.At != st.At)
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
