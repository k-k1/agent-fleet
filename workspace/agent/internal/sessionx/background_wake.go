package sessionx

// The background-done nudge (#4581). agy backgrounds a long run_command about 2 s after it
// starts and ends the turn there; when the command finishes, agy is supposed to start a new turn
// by itself, and sometimes does not. The session then reads idle with the model still "waiting
// for the completion notification", idle-stop folds it an hour later, and the wait is lost for
// good (on resume agy does not re-attach to the old tasks). This watch closes that gap at the
// source: once the background work of an idle session has ended and nothing followed it for
// backgroundWakeGrace, it types ONE prompt telling the model to continue.
//
// Invariants:
//   - Only kinds that opt in through agents.BackgroundWaker (agy). Its FinishedTurnMark is what
//     proves "no turn followed": a turn the CLI starts on its own ends in a new mark, and
//     LastTurnEndAt alone cannot be trusted for agy (#4267: a self-started turn may never reach
//     the persisted status).
//   - Never into a session waiting on a person, one with a peer message queued or in flight (that
//     message wakes it anyway), one armed to stop after its turn (the injection would cancel the
//     arm), one still being handed its first prompt, or one stopped or archived.
//   - At most once per finished turn, recorded on the meta (Meta.BackgroundNudge) so an Agent
//     restart cannot repeat it, and at most backgroundWakeMaxPerHour per session, so a model that
//     answers every nudge by backgrounding something it never collects cannot loop.
//   - A period is "busy seen, then quiet for the whole grace". Any busy reading inside the grace
//     restarts it, so a flapping BackgroundBusy (a gap between two commands) does not fire.
//
// Its own loop for the same reason stalled_tree.go has one: it must work at 3 a.m. with no
// Console open. A period the Agent did not see begin (it restarted mid-period) is not nudged:
// missing one is the status quo, nudging on a guess is not.

import (
	"log"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const (
	// backgroundWakeGrace is how long the background work must have been over, with the session
	// idle and no turn started, before the nudge. agy starts its own follow-up turn within a few
	// seconds of a completion when it does start one (docs/log/32, background commands).
	backgroundWakeGrace = 60 * time.Second
	// backgroundWakeMaxPerHour bounds nudges per session in a rolling hour.
	backgroundWakeMaxPerHour = 3
	// backgroundWakeRetry spaces delivery retries, and backgroundWakeAttempts bounds them.
	backgroundWakeRetry    = 2 * time.Minute
	backgroundWakeAttempts = 3

	backgroundWakeNotice = "[agent-fleet:notice kind=background-done reply=none] Your background task(s) have finished; " +
		"read their output and continue. This is an automated notice, sent once: if you have already handled " +
		"them, ignore it."
)

// backgroundWakeInterval is the loop's tick; a variable so a test could shorten it.
var backgroundWakeInterval = 15 * time.Second

// Side effects, replaceable by tests.
var (
	backgroundWakeAlive = SessionAlive
	backgroundWakeWire  = wireSession
	backgroundWakeReady = func(m session.Meta) bool { ready, _ := peerDeliveryReady(m); return ready }
	backgroundWakeMark  = func(m session.Meta) (string, bool) {
		w, ok := AgentOf(m.Kind).(agents.BackgroundWaker)
		if !ok {
			return "", false
		}
		return w.FinishedTurnMark(m)
	}
	backgroundWakeQueued = func(name string) bool { return peersInFlight(map[string]bool{name: true}) }
	backgroundWakeInject = injectSessionPrompt
	// backgroundWakeOptedIn reports whether the kind takes the nudge at all.
	backgroundWakeOptedIn = func(kind string) bool {
		_, ok := AgentOf(kind).(agents.BackgroundWaker)
		return ok
	}
)

// bgWakePeriod is one background period of one session, as this process saw it.
type bgWakePeriod struct {
	mark    string // the finished-turn mark when the work was first seen; "" = cannot nudge
	turnEnd string // LastTurnEndAt then
	// quietSince is the first reading of no background work after the busy ones; zero while
	// busy.
	quietSince time.Time
	done       bool // nudged, cancelled or given up: wait for the next period
	attempts   int
	retryAt    time.Time
}

var (
	bgWakeMu      sync.Mutex
	bgWakePeriods = map[string]*bgWakePeriod{}
)

// StartBackgroundWakeWatch runs the watch for the life of the agent.
func StartBackgroundWakeWatch() {
	go func() {
		time.Sleep(50 * time.Second) // let tmux and the drivers come up after a start
		for {
			backgroundWakeTick(time.Now())
			time.Sleep(backgroundWakeInterval)
		}
	}()
}

// backgroundWakeTick is one sweep over the live sessions of the opted-in kinds.
func backgroundWakeTick(now time.Time) {
	seen := map[string]bool{}
	for _, m := range session.ListMetas() {
		if m.Archived || !backgroundWakeOptedIn(m.Kind) || !backgroundWakeAlive(m) {
			continue
		}
		seen[m.Name] = true
		backgroundWakeObserve(m, backgroundWakeWire(m, true), now)
	}
	bgWakeMu.Lock()
	for name := range bgWakePeriods {
		if !seen[name] {
			delete(bgWakePeriods, name) // stopped, archived or gone: a resume starts afresh
		}
	}
	bgWakeMu.Unlock()
}

// backgroundWakeObserve advances one session's period and nudges when it is due.
func backgroundWakeObserve(m session.Meta, s session.Session, now time.Time) {
	bgWakeMu.Lock()
	p := bgWakePeriods[m.Name]
	if s.BackgroundBusy {
		if p == nil || p.done {
			mark, ok := backgroundWakeMark(m)
			if !ok {
				mark = ""
			}
			bgWakePeriods[m.Name] = &bgWakePeriod{mark: mark, turnEnd: s.LastTurnEndAt}
		} else {
			p.quietSince = time.Time{}
		}
		bgWakeMu.Unlock()
		return
	}
	if p == nil || p.done {
		delete(bgWakePeriods, m.Name)
		bgWakeMu.Unlock()
		return
	}
	if p.quietSince.IsZero() {
		p.quietSince = now
	}
	due := now.Sub(p.quietSince) >= backgroundWakeGrace && !now.Before(p.retryAt)
	period := *p
	bgWakeMu.Unlock()
	if !due {
		return
	}
	verdict := backgroundWakeVerdict(m, s, period, now)
	switch verdict {
	case bgWakeWait:
		return
	case bgWakeSend:
		backgroundWakeSend(m, period, now)
	default:
		if verdict != bgWakeTurnStarted {
			log.Printf("background-wake: %s: not nudging (%s)", m.Name, verdict)
		}
		backgroundWakeDone(m.Name)
	}
}

const (
	bgWakeSend        = "send"
	bgWakeWait        = "wait"
	bgWakeTurnStarted = "a turn followed the background work"
)

// backgroundWakeVerdict is the last look before a send, on a fresh meta. It answers
// bgWakeWait only for states that can still end in a nudge (a turn still running, or a reading
// that is not idle yet); everything else closes the period.
func backgroundWakeVerdict(m session.Meta, s session.Session, p bgWakePeriod, now time.Time) string {
	fresh, ok := session.ReadMeta(m.Name)
	if !ok || fresh.Archived || !backgroundWakeAlive(fresh) {
		return "stopped or archived"
	}
	if p.mark == "" {
		return "no finished-turn mark when the work began"
	}
	if s.LastTurnEndAt != p.turnEnd {
		return bgWakeTurnStarted
	}
	mark, ok := backgroundWakeMark(fresh)
	if !ok {
		return "finished-turn mark unreadable"
	}
	if mark != p.mark {
		return bgWakeTurnStarted
	}
	if s.State != "idle" {
		// Working: agy started its own turn, which moves the mark when it ends. A person wait
		// (question, plan, permission, limit) is not ours to interrupt; it ends in a turn too.
		return bgWakeWait
	}
	if fresh.BackgroundNudge != nil && fresh.BackgroundNudge.Mark == p.mark {
		return "already nudged for this turn"
	}
	if backgroundWakeCapped(fresh.BackgroundNudge, now) {
		return "hourly cap reached"
	}
	if fresh.StopAfterTurnAt != "" {
		return "armed to stop after its turn"
	}
	if fresh.InitialPromptState == session.InitialPromptPending {
		return "first prompt still being delivered"
	}
	if backgroundWakeQueued(fresh.Name) {
		return "a peer message is queued or in flight"
	}
	if !backgroundWakeReady(fresh) {
		return bgWakeWait
	}
	return bgWakeSend
}

// backgroundWakeCapped reports whether the rolling hour already holds the maximum.
func backgroundWakeCapped(last *session.BackgroundNudge, now time.Time) bool {
	return backgroundWakeCount(last, now) >= backgroundWakeMaxPerHour
}

// backgroundWakeCount is how many nudges the hour before now holds, from the last record.
func backgroundWakeCount(last *session.BackgroundNudge, now time.Time) int {
	if last == nil {
		return 0
	}
	at, err := time.Parse(time.RFC3339, last.At)
	if err != nil || now.Sub(at) >= time.Hour {
		return 0
	}
	return last.Count
}

func backgroundWakeSend(m session.Meta, p bgWakePeriod, now time.Time) {
	// Recorded before the send so the mirror badges the turn from its first poll.
	recordInjection(m.Name, backgroundWakeNotice, TurnSourceNotice)
	if err := backgroundWakeInject(m.Name, backgroundWakeNotice, agents.Origin{Kind: agents.OriginNotice}); err != nil {
		bgWakeMu.Lock()
		if cur := bgWakePeriods[m.Name]; cur != nil {
			cur.attempts++
			cur.retryAt = now.Add(backgroundWakeRetry)
			if cur.attempts >= backgroundWakeAttempts {
				cur.done = true
			}
		}
		bgWakeMu.Unlock()
		log.Printf("background-wake: could not deliver the notice to %s: %v", m.Name, err)
		return
	}
	backgroundWakeDone(m.Name)
	UpdateSessionMeta(m.Name, func(cur *session.Meta) bool {
		cur.BackgroundNudge = &session.BackgroundNudge{
			Mark: p.mark, At: now.UTC().Format(time.RFC3339), Count: backgroundWakeCount(cur.BackgroundNudge, now) + 1,
		}
		return true
	})
	recordFleetGraphInstruct(m.Name, TurnSourceNotice, "", "", backgroundWakeNotice)
	log.Printf("background-wake: nudged %s (background work over for %s, no turn followed)", m.Name, backgroundWakeGrace)
}

func backgroundWakeDone(name string) {
	bgWakeMu.Lock()
	defer bgWakeMu.Unlock()
	if p := bgWakePeriods[name]; p != nil {
		p.done = true
	}
}
