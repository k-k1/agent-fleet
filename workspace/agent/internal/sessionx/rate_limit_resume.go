package sessionx

// Dismissing the usage-limit modal, and resuming automatically at the reset instant
// (docs/log/47 §4-4).
//
// A claude that hit its limit puts up a menu (/rate-limit-options) and stops waiting for a
// keypress. docs/log/47 §4-3 only made that *readable* as blocked; the session stays stuck.
// The two steps that actually move it forward:
//
//	1. Confirm the menu's default (1. Stop and wait for limit to reset) and return the pane
//	   to its waiting prompt. Done regardless of the setting: while the menu is up the
//	   session accepts nothing, and the choice is the one that spends nothing, so this is
//	   not deciding on the user's behalf (tmuxx.DismissRateLimitModal).
//	2. Register a one-shot schedule with the CP that sends "carry on" to this same session
//	   at the instant the limit lifts (claude.ResetAt). Toggled by the "auto-resume after
//	   usage limit reset" setting.
//
// Why the waiting is handed to the CP scheduler: once step 1 clears the menu the session is
// an ordinary idle one, so during the hours until the reset the idle-reaper may stop the
// whole workspace (which is fine - the turn is over). An in-process timer cannot survive
// that, but a CP schedule with wake_policy=wake wakes the workspace before injecting
// (docs/log/38 P6, session_mode=reuse = inject into an existing session).
//
// The order is 2 then 1: a successful dismissal removes the menu and this detection path
// never opens again, so the resume is booked first. Only when booking fails does a later
// tick retry it from the state file (rateLimitFollowUp).

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

const (
	rateLimitNoticeReached = "rate-limit-reached"
	rateLimitNoticeResumed = "rate-limit-resumed"
	// rateLimitWatchInterval is the sweep cadence. A minute is enough - the wait ahead is
	// hours long, and this interval is exactly the worst-case delay between the menu
	// appearing and it being dismissed.
	rateLimitWatchInterval = time.Minute
	// maxRateLimitDismissTries bounds the Enter presses per episode. A dismissal fails only
	// when the selection has moved to 2 (a person is at the keyboard) or the TUI changed
	// shape, and neither is fixed by keeping on hammering the key. At the bound the session
	// is simply left blocked, waiting for a human.
	maxRateLimitDismissTries = 3
	// maxRateLimitScheduleTries bounds the CP registration attempts per episode (a CP that
	// is only briefly unreachable goes through on the next tick).
	maxRateLimitScheduleTries = 5
	// rateLimitResumeLead is the floor on how soon a resume may be scheduled. An immediate
	// resume - the reset instant has already passed because the menu sat there for hours -
	// also goes through this floor: the scheduler ticks once a minute, so anything shorter
	// means nothing.
	rateLimitResumeLead = 2 * time.Minute
	// rateLimitCleanupGrace is how long after the scheduled instant the episode is kept
	// before the spent once-schedule is deleted and the state file dropped.
	rateLimitCleanupGrace = 30 * time.Minute
	// rateLimitEpisodeTTL drops an episode that never got a resume time (auto-resume off, or
	// no instant could be determined) so a stale file can't suppress the next episode.
	rateLimitEpisodeTTL = 12 * time.Hour
)

// rateLimitState is one usage-limit episode for one session: it exists from the moment
// the menu is seen until the scheduled resume has come and gone. It gets its own file for
// the same reason resumeState does: it does not ride along in Meta, which has many writers.
type rateLimitState struct {
	At            string `json:"at"`                      // when the episode was detected (RFC3339)
	Menu          bool   `json:"menu,omitempty"`          // limit came with a menu (= an account window)
	DismissTries  int    `json:"dismissTries,omitempty"`  // Enter presses sent
	Dismissed     bool   `json:"dismissed,omitempty"`     // the menu was confirmed gone
	LastTry       string `json:"lastTry,omitempty"`       // most recent dismissal attempt
	ResumeAt      string `json:"resumeAt,omitempty"`      // scheduled auto-resume instant (RFC3339)
	Source        string `json:"source,omitempty"`        // evidence for the instant (banner / capture ...)
	ScheduleID    string `json:"scheduleId,omitempty"`    // schedule id on the CP side
	ScheduleTries int    `json:"scheduleTries,omitempty"` // registration attempts
	// Spend marks an episode opened by a spend/balance limit (claude.LimitSpend). Unlike a
	// window limit it does not end at an instant, so nothing is booked, ResumeAt stays empty,
	// and the episode is retired only once the transcript tail is no longer at the limit
	// (episodeStale / rateLimitFollowUp).
	Spend bool `json:"spend,omitempty"`
}

var RateLimitStates = fstore.JSON[rateLimitState](paths.AgentConfigDir, "session-rate-limit", ".json")

// The side effects stay replaceable: tests have neither tmux nor a CP.
var (
	dismissRateLimitModal = tmuxx.DismissRateLimitModal
	putRateLimitSchedule  = createRateLimitSchedule
	dropRateLimitSchedule = deleteRateLimitSchedule
	rateLimitResetAt      = claude.ResetAt
	claudeUsageLimitAbort = claude.UsageLimitAbort
)

// StartRateLimitWatch runs the sweep for the life of the agent.
//
// Why a dedicated loop rather than riding along on the list-polling path: dismissal and
// resume are worth nothing unless they work while nobody is looking. wireSession is a read
// path that runs only when the Console or CP calls it, and a side effect placed there turns
// this into a feature that works only if someone has a screen open.
func StartRateLimitWatch() {
	go func() {
		time.Sleep(45 * time.Second) // let tmux and the CP come up after a start
		for {
			rateLimitTick(time.Now())
			time.Sleep(rateLimitWatchInterval)
		}
	}()
}

// rateLimitTick is one sweep: every claude session is classified as "at its usage limit
// now" (recover) or "has an open episode" (follow up / clean up). ListMetas is deliberately
// the only population gate: origin=operator / owner conversation / instruction-ledger
// presence are irrelevant, so a standalone session launched directly from Console is
// recovered exactly like one launched or steered by an assistant.
//
// Detection has two paths because a limit has more than one shape (see the comment on
// claude.UsageLimitAbort): an account window pins the pane on a menu waiting for a human,
// while a per-model limit prints a one-line error and returns to the ordinary input box.
// The latter is indistinguishable from the pane, so it is picked up from the transcript
// tail. The pane is checked first because a visible menu means the session is stuck right
// now, which is the more urgent case.
func rateLimitTick(now time.Time) {
	for _, m := range session.ListMetas() {
		if NormalizeKind(m.Kind) != session.KindClaude {
			continue
		}
		st, has := RateLimitStates.Read(m.Name)
		alive := SessionAlive(m)
		if alive {
			if tmuxx.ReadPane(m.Name).RateLimitMenu {
				// Only an account window puts up a menu, and that is the kind waiting clears.
				rateLimitRecover(m, st, now, true, claude.LimitWindow)
				continue
			}
			if _, kind, atLimit := claudeUsageLimitAbort(session.UUID(m.Dir, m.Name)); atLimit {
				rateLimitRecover(m, st, now, false, kind)
				continue
			}
		}
		if has {
			rateLimitFollowUp(m, st, now, alive)
		}
	}
}

// rateLimitRecover handles a session stopped by its usage limit. onMenu says which form it
// is: true = the pane is pinned on the /rate-limit-options menu, false = the transcript tail
// is a turn cut off by the limit (no menu, and the session can accept input).
func rateLimitRecover(m session.Meta, st rateLimitState, now time.Time, onMenu bool, kind claude.LimitKind) {
	if episodeStale(st, now) {
		st = rateLimitState{} // the previous episode is over - treat this as a new limit
	}
	if st.At == "" {
		st.At = now.Format(time.RFC3339)
		switch {
		case onMenu:
			log.Printf("rate-limit: %s is stopped on the usage-limit menu", m.Name)
		case kind == claude.LimitSpend:
			log.Printf("rate-limit: turn on %s was cut off by the spend/balance limit (needs a raised limit)", m.Name)
		default:
			log.Printf("rate-limit: turn on %s was cut off by the usage limit", m.Name)
		}
	}
	// Monotonic, never cleared: if a menu shows up partway through an episode, it counts as a
	// menu episode from then on. A menu that disappears is the sign the dismissal worked, so
	// the flag is not taken back.
	if onMenu {
		st.Menu = true
	}
	if kind == claude.LimitSpend {
		// Spend/balance limit: book nothing and raise no "usage limit reached" notice. Both
		// rest on the assumption that waiting clears it, which is a lie in this shape - waking
		// hits the same 429 and waiting never lifts it. The user already gets the turn-failed
		// notification and report carrying the wording verbatim ("...monthly spend limit ·
		// run /usage-credits..."). All af can add is a state visible in the list: the
		// agents.StateSpendLimit chip.
		st.Spend = true
		_ = RateLimitStates.Write(m.Name, st)
		return
	}
	st = scheduleRateLimitResume(m, st, now)
	notifyRateLimitReached(m, st)

	if onMenu && !st.Dismissed && st.DismissTries < maxRateLimitDismissTries && !triedRecently(st, now) {
		st.DismissTries++
		st.LastTry = now.Format(time.RFC3339)
		// Record before sending: a crash midway must not roll the count back and let the key
		// be pressed forever.
		_ = RateLimitStates.Write(m.Name, st)
		if dismissRateLimitModal(m.Name) {
			st.Dismissed = true
			log.Printf("rate-limit: dismissed the usage-limit menu on %s (1. wait for the reset)", m.Name)
		} else {
			log.Printf("rate-limit: could not dismiss the menu on %s (attempt %d/%d)",
				m.Name, st.DismissTries, maxRateLimitDismissTries)
		}
	}
	_ = RateLimitStates.Write(m.Name, st)
}

// notifyRateLimitReached records the attention event once per episode. PutOnce's marker
// survives the CP draining the outbox, so a menu that remains visible across many watcher
// ticks cannot keep reappearing as unread. This event intentionally says only that the
// limit was reached: scheduling may be disabled or may still succeed on a later retry.
func notifyRateLimitReached(m session.Meta, st rateLimitState) {
	if st.At == "" {
		return
	}
	ev := notice.New(rateLimitNoticeReached, m.Name, m.Kind, session.Display(m))
	if at, err := time.Parse(time.RFC3339, st.At); err == nil {
		ev.CreatedAt = at.UTC().Format(time.RFC3339)
	}
	if err := notice.PutOnce("rate-limit-reached:"+m.Name+":"+st.At, ev); err != nil {
		log.Printf("rate-limit: could not store the usage-limit notice for %s: %v", m.Name, err)
	}
}

// notifyRateLimitResumeDelivered records a resume only after /input's delivery
// confirmation succeeded. The schedule instant alone is insufficient: overlap/target
// guards may skip it, and delivery may fail. The open episode + exact internal prompt +
// scheduler source keep an ordinary scheduled or Console prompt from impersonating it.
func notifyRateLimitResumeDelivered(name, prompt, source string, now time.Time) {
	if injectionSource(source) != TurnSourceSchedule || !isRateLimitResumePrompt(prompt) {
		return
	}
	st, ok := RateLimitStates.Read(name)
	if !ok || st.ScheduleID == "" {
		return
	}
	m, ok := session.ReadMeta(name)
	if !ok || NormalizeKind(m.Kind) != session.KindClaude {
		return
	}
	ev := notice.New(rateLimitNoticeResumed, m.Name, m.Kind, session.Display(m))
	ev.CreatedAt = now.UTC().Format(time.RFC3339)
	ev.Payload["resumeAt"] = st.ResumeAt
	if err := notice.PutOnce("rate-limit-resumed:"+st.ScheduleID, ev); err != nil {
		log.Printf("rate-limit: could not store the auto-resume notice for %s: %v", m.Name, err)
	}
}

// rateLimitFollowUp runs for a session with an open episode whose menu is already gone:
// retry a registration that failed while the menu was still up, then retire the episode.
func rateLimitFollowUp(m session.Meta, st rateLimitState, now time.Time, alive bool) {
	if st.Spend {
		// A spend limit does not end at an instant, so arriving here is the only termination
		// condition: on a live session the transcript tail is no longer at the limit, meaning
		// the limit was raised or another turn ran. A stopped session is left alone - it may
		// come back still at the same limit, and retiring it would leave the list saying
		// nothing until the next tick.
		if alive {
			RateLimitStates.Remove(m.Name)
		}
		return
	}
	if next := scheduleRateLimitResume(m, st, now); next != st {
		st = next
		_ = RateLimitStates.Write(m.Name, st)
	}
	if !episodeStale(st, now) {
		return
	}
	// Do not leave the spent once-schedule behind: dead rows pile up in the Console's list.
	if st.ScheduleID != "" {
		dropRateLimitSchedule(st.ScheduleID)
	}
	RateLimitStates.Remove(m.Name)
}

// scheduleRateLimitResume registers the one-shot resume when it is wanted and not yet
// in place. It returns the updated state; the caller writes it.
func scheduleRateLimitResume(m session.Meta, st rateLimitState, now time.Time) rateLimitState {
	if st.ScheduleID != "" || !uiprefs.RateLimitAutoResume() || st.ScheduleTries >= maxRateLimitScheduleTries {
		return st
	}
	at, source, ok := rateLimitResetAt(session.UUID(m.Dir, m.Name), now)
	// For a limit without a menu the instant is trusted only when it comes from the banner,
	// i.e. the wording this session actually received. resolveResetAt's fallback is the
	// statusline capture, which describes the account's 5-hour / weekly windows, but a
	// per-model limit lives in a different window that never appears there (claude packs only
	// five_hour and seven_day into statusLine). Measured 2026-08-05 s6no6jv: at the moment
	// the limit hit, the 5-hour window was at 23% and the weekly at 75%, and the fallback
	// returned 19:30 that day (the 5-hour reset) - resuming there just hits the same limit.
	if ok && !st.Menu && !strings.HasPrefix(source, "banner") {
		ok = false
	}
	if !ok {
		// No instant can be determined (the banner is unreadable and the capture unusable).
		// Waking on a guess only hits the limit again, so nothing is booked - but the attempt
		// is still counted.
		st.ScheduleTries++
		log.Printf("rate-limit: could not read the reset instant for %s, so no auto-resume is booked", m.Name)
		return st
	}
	if floor := now.Add(rateLimitResumeLead); at.Before(floor) {
		at = floor // already past, or imminent - run as soon as the scheduler's tick allows
	}
	st.ScheduleTries++
	st.ResumeAt, st.Source = at.Format(time.RFC3339), source
	id, err := putRateLimitSchedule(m, at)
	if err != nil {
		log.Printf("rate-limit: could not register the auto-resume for %s: %v", m.Name, err)
		return st
	}
	st.ScheduleID = id
	log.Printf("rate-limit: booked the auto-resume for %s at %s (%s, schedule=%s)",
		m.Name, at.Local().Format("01/02 15:04"), source, id)
	return st
}

// rateLimitWaiting reports whether this claude session is sitting at its usage limit
// *right now*, which state that is, and when the reserved resume is due ("" = nothing
// booked). It is the predicate the display side (wireSession / DriveState) uses to reinterpret
// idle.
//
// There are two states because the user's next move is the opposite in each: a window that
// waiting clears (agents.StateLimited) versus a spend/balance limit that it does not
// (agents.StateSpendLimit, docs/log/47 §4-10). The kind is taken from the transcript afresh
// rather than from the episode file's Spend, so the display follows transitions such as a
// raised limit that then hits a different (window) limit.
//
// Both sources are needed because either one alone lies:
//   - Episode file alone: true for the whole reserved interval, or for the TTL when
//     auto-resume is off. It would keep claiming "waiting for the limit to lift" even after
//     the user switched models and started working normally.
//   - Transcript alone (claudeUsageLimitAbort): correct, but it would read the transcript
//     tail of every claude session on each pass of this list-polling path. Episodes are rare,
//     so the small state file prunes first and only then is the transcript read.
//
// The transcript side returns to false on its own once the tail becomes a new user/assistant
// record, so no separate path is needed to learn that the limit lifted - the same design as
// StateBlocked.
func rateLimitWaiting(m session.Meta, now time.Time) (state, resumeAt string, ok bool) {
	st, has := RateLimitStates.Read(m.Name)
	if !has || episodeStale(st, now) {
		return "", "", false
	}
	_, kind, atLimit := claudeUsageLimitAbort(session.UUID(m.Dir, m.Name))
	if !atLimit {
		return "", "", false
	}
	if kind == claude.LimitSpend {
		return agents.StateSpendLimit, "", true // no booking exists: no instant clears this
	}
	return agents.StateLimited, st.ResumeAt, true
}

// triedRecently rate-limits the Enter presses inside one episode.
func triedRecently(st rateLimitState, now time.Time) bool {
	t, err := time.Parse(time.RFC3339, st.LastTry)
	return err == nil && now.Sub(t) < rateLimitWatchInterval/2
}

// episodeStale reports whether an episode is finished: its resume time (plus the grace
// for the scheduler to fire) has passed, or it never got one and has aged out.
func episodeStale(st rateLimitState, now time.Time) bool {
	if st.At == "" {
		return true
	}
	if st.Spend {
		return false // no instant ends it - retired only when the transcript changes (rateLimitFollowUp)
	}
	if st.ResumeAt != "" {
		t, err := time.Parse(time.RFC3339, st.ResumeAt)
		return err != nil || now.After(t.Add(rateLimitCleanupGrace))
	}
	t, err := time.Parse(time.RFC3339, st.At)
	return err != nil || now.After(t.Add(rateLimitEpisodeTTL))
}

// createRateLimitSchedule books the resume with the CP scheduler (docs/log/38).
//
//	spec_kind=once           - fires once, then disables itself.
//	session_mode=reuse       - injects into the session that stopped, not into a new one.
//	reuse_target=<name>      - pinned reuse; rotation does not apply to a user's session.
//	missing_target_policy=fail - do not recreate a session that is gone. Spawning some other
//	                           session that cannot carry on the interrupted work does more harm.
//	overlap_policy=skip      - if a person is already driving it at that instant, skip silently.
//	wake_policy=wake         - deliver even if the workspace stopped during the wait, which is
//	                           the whole reason this is a CP schedule and not an in-process timer.
//	report=false             - do not report this injection to an operator conversation: there
//	                           is none to report to (owner_conv is empty). The resumed turn
//	                           still raises the usual "answer ready" notice when it ends, so it
//	                           does not become invisible to the user.
func createRateLimitSchedule(m session.Meta, at time.Time) (string, error) {
	body, err := json.Marshal(map[string]any{
		"spec_kind":             "once",
		"spec":                  at.UTC().Format(time.RFC3339),
		"spec_label":            rateLimitScheduleLabel(m.Name),
		"tz":                    scheduleTZName(),
		"session_mode":          "reuse",
		"reuse_target":          m.Name,
		"missing_target_policy": "fail",
		"overlap_policy":        "skip",
		"wake_policy":           "wake",
		"agent_kind":            session.KindClaude,
		"prompt":                rateLimitResumePrompt(),
		"report":                false,
	})
	if err != nil {
		return "", err
	}
	out, err := mcpx.CPScheduleDo(http.MethodPost, "/internal/schedules", body)
	if err != nil {
		return "", err
	}
	var wire struct {
		ID      string `json:"id"`
		Warning string `json:"warning"`
	}
	if json.Unmarshal([]byte(out), &wire) != nil || wire.ID == "" {
		return "", fmt.Errorf("could not read the schedule id: %s", out)
	}
	if wire.Warning != "" {
		log.Printf("rate-limit: %s", wire.Warning)
	}
	return wire.ID, nil
}

// scheduleTZName is the zone the CP renders this schedule's instant in. Display only - a
// once-schedule fires at an absolute instant (RFC3339 in UTC) - but a name like "Local" would
// resolve to something else on the CP side, so a zone is sent only when it can be named in
// IANA form.
func scheduleTZName() string {
	if tz := os.Getenv("TZ"); strings.Contains(tz, "/") {
		return tz
	}
	if n := time.Local.String(); strings.Contains(n, "/") {
		return n
	}
	return ""
}

// deleteRateLimitSchedule removes a spent once-schedule, best-effort.
func deleteRateLimitSchedule(id string) {
	if _, err := mcpx.CPScheduleDo(http.MethodDelete, "/internal/schedules/"+url.PathEscape(id), nil); err != nil {
		log.Printf("rate-limit: could not delete the spent schedule %s: %v", id, err)
	}
}

// rateLimitResumePrompt is the nudge sent when the limit lifts. It mixes in no new
// instruction, the same policy as the resume text in docs/log/47 §3-4. Its language follows
// the display language: with no per-conversation language, the language the user reads and
// writes in is the best estimate.
func rateLimitResumePrompt() string {
	return rateLimitResumePromptFor(uiprefs.Locale())
}

func rateLimitResumePromptFor(locale string) string {
	if locale == "en" {
		return "The usage limit has reset. Continue the work that was cut off, from where it stopped. " +
			"This is an automatic resume — there is no new instruction. " +
			"If you cannot tell where it stopped, say so instead of starting something new."
	}
	return "利用上限がリセットされました。上限で中断した作業を、止まったところから続けてください。" +
		"これは自動再開なので新しい指示はありません。" +
		"どこで止まったか分からない場合は、新しい作業を始めずにその旨を伝えてください。"
}

func isRateLimitResumePrompt(prompt string) bool {
	prompt = strings.TrimSpace(prompt)
	return prompt == strings.TrimSpace(rateLimitResumePromptFor("ja")) ||
		prompt == strings.TrimSpace(rateLimitResumePromptFor("en"))
}

func rateLimitScheduleLabel(name string) string {
	if uiprefs.Locale() == "en" {
		return "auto-resume after usage limit (" + name + ")"
	}
	return "利用上限リセット後の自動再開（" + name + "）"
}
