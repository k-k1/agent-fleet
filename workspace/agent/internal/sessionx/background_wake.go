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
//     arm), one still being handed its first prompt, one being retired (ADR 0137: no automatic
//     input once RetiredAt is set), or one stopped or archived; never while the pane shows a
//     running turn (BackgroundWaker.InputIdle).
//   - At most once per finished turn, recorded on the meta (Meta.BackgroundNudge) so an Agent
//     restart cannot repeat it, and at most backgroundWakeMaxPerHour per session in any rolling
//     hour, so a model that answers every nudge by backgrounding something it never collects
//     cannot loop.
//   - A period is "busy seen, then idle with no background work for the whole grace". A busy
//     reading, a non-idle state or a not-ready pane inside the grace restarts it, so a flapping
//     BackgroundBusy (a gap between two commands) or a brief idle between a person's answer and
//     the turn it starts does not fire. The mark and LastTurnEndAt are re-read on every busy tick,
//     so a later turn that backgrounds work again starts a period of its own.
//   - Free of the turn-end side effects: it reads WireLive and the status store, never
//     DriveState, which would settle and notify the interim turn end (turn_end_poll.go).
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
	// backgroundWakeGrace is how long the session must have sat idle with its background work
	// over, and no turn started, before the nudge. agy starts its own follow-up turn within a
	// few seconds of a completion when it does start one (docs/log/32, background commands).
	backgroundWakeGrace = 60 * time.Second
	// backgroundWakeMaxPerHour bounds nudges per session in any rolling hour.
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

// bgWakeLive is one tick's reading of a session.
type bgWakeLive struct {
	state   string
	busy    bool
	turnEnd string
}

// Side effects, replaceable by tests.
var (
	backgroundWakeAlive = SessionAlive
	// backgroundWakeProbe is the cheap first look (a /proc walk, no DB) that keeps sessions with
	// nothing running out of the full read.
	backgroundWakeProbe = func(m session.Meta) bool {
		br, ok := AgentOf(m.Kind).(agents.BackgroundReporter)
		if !ok {
			return false
		}
		busy, _ := br.BackgroundWork(m)
		return busy
	}
	// backgroundWakeRead is the kind's own WireLive plus the recorded turn end: the three fields
	// this watch uses, without the rest of wireSession (overview facts, git, images, the list's
	// own turn-end recording).
	backgroundWakeRead = backgroundWakeReadDefault
	backgroundWakeMark = func(m session.Meta) (string, bool) {
		w, ok := AgentOf(m.Kind).(agents.BackgroundWaker)
		if !ok {
			return "", false
		}
		return w.FinishedTurnMark(m)
	}
	// backgroundWakeReady: nothing a person must answer, and the pane at its idle prompt.
	backgroundWakeReady  = backgroundWakeReadyDefault
	backgroundWakeQueued = func(name string) bool { return peersInFlight(map[string]bool{name: true}) }
	backgroundWakeInject = injectSessionPrompt
	// backgroundWakeOptedIn reports whether the kind takes the nudge at all.
	backgroundWakeOptedIn = backgroundWakeOptedInDefault
)

func backgroundWakeReadDefault(m session.Meta) bgWakeLive {
	li := AgentOf(m.Kind).WireLive(m, true)
	return bgWakeLive{state: li.State, busy: li.BackgroundBusy, turnEnd: lastTurnEndAt(m)}
}

func backgroundWakeReadyDefault(m session.Meta) bool {
	w, ok := AgentOf(m.Kind).(agents.BackgroundWaker)
	return ok && peerQueueBlocker(m) == "" && w.InputIdle(m)
}

func backgroundWakeOptedInDefault(kind string) bool {
	_, ok := AgentOf(kind).(agents.BackgroundWaker)
	return ok
}

// bgWakePeriod is one background period of one session, as this process saw it.
type bgWakePeriod struct {
	mark    string // the finished-turn mark while the work ran; "" = cannot nudge
	turnEnd string // LastTurnEndAt then
	// quietSince is the first of the unbroken idle, no-background readings; zero otherwise.
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

func bgWakeTracked(name string) bool {
	bgWakeMu.Lock()
	defer bgWakeMu.Unlock()
	_, ok := bgWakePeriods[name]
	return ok
}

// backgroundWakeTick is one sweep over the live sessions of the opted-in kinds.
func backgroundWakeTick(now time.Time) {
	seen := map[string]bool{}
	for _, m := range session.ListMetas() {
		if m.Archived || isRetired(m) || !backgroundWakeOptedIn(m.Kind) || !backgroundWakeAlive(m) {
			continue
		}
		seen[m.Name] = true
		if !bgWakeTracked(m.Name) && !backgroundWakeProbe(m) {
			continue
		}
		backgroundWakeObserve(m, backgroundWakeRead(m), now)
	}
	bgWakeMu.Lock()
	for name := range bgWakePeriods {
		if !seen[name] {
			delete(bgWakePeriods, name) // stopped, archived, retired or gone: a resume starts afresh
		}
	}
	bgWakeMu.Unlock()
}

// backgroundWakeObserve advances one session's period and nudges when it is due.
func backgroundWakeObserve(m session.Meta, s bgWakeLive, now time.Time) {
	if s.busy {
		// Read outside the lock: it opens the conversation DB.
		mark, ok := backgroundWakeMark(m)
		if !ok {
			mark = ""
		}
		bgWakeMu.Lock()
		p := bgWakePeriods[m.Name]
		if p == nil || p.done || p.mark == "" || mark != p.mark || s.turnEnd != p.turnEnd {
			// A new period, or the one we track turned out to belong to a later turn (agy ran a
			// turn of its own and backgrounded work again), or its mark was not written yet when
			// the work was first seen.
			bgWakePeriods[m.Name] = &bgWakePeriod{mark: mark, turnEnd: s.turnEnd}
		} else {
			p.quietSince = time.Time{}
		}
		bgWakeMu.Unlock()
		return
	}
	bgWakeMu.Lock()
	p := bgWakePeriods[m.Name]
	if p == nil || p.done {
		delete(bgWakePeriods, m.Name)
		bgWakeMu.Unlock()
		return
	}
	if s.state != "idle" {
		// A turn of agy's own, or a person wait: the grace counts only unbroken idle.
		p.quietSince = time.Time{}
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
	switch verdict := backgroundWakeVerdict(m, s, period, now); verdict {
	case bgWakeSend:
		backgroundWakeSend(m, period, now)
	case bgWakeWait:
		bgWakeMu.Lock()
		if cur := bgWakePeriods[m.Name]; cur != nil {
			cur.quietSince = time.Time{}
		}
		bgWakeMu.Unlock()
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

// backgroundWakeVerdict is the last look before a send, on a fresh meta. bgWakeWait (restart
// the grace) only for a pane that cannot take input yet; everything else that is not a send
// closes the period.
func backgroundWakeVerdict(m session.Meta, s bgWakeLive, p bgWakePeriod, now time.Time) string {
	fresh, ok := session.ReadMeta(m.Name)
	if !ok || fresh.Archived || !backgroundWakeAlive(fresh) {
		return "stopped or archived"
	}
	if isRetired(fresh) {
		return "retired"
	}
	if p.mark == "" {
		return "no finished-turn mark while the work ran"
	}
	if s.turnEnd != p.turnEnd {
		return bgWakeTurnStarted
	}
	mark, ok := backgroundWakeMark(fresh)
	if !ok {
		return "finished-turn mark unreadable"
	}
	if mark != p.mark {
		return bgWakeTurnStarted
	}
	if fresh.BackgroundNudge != nil && fresh.BackgroundNudge.Mark == p.mark {
		return "already nudged for this turn"
	}
	if backgroundWakeCount(fresh.BackgroundNudge, now) >= backgroundWakeMaxPerHour {
		return "hourly cap reached"
	}
	if _, armed := session.StopArmedAt(fresh, now); armed {
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

// backgroundWakeRecent is the record's send times that fall in the hour before now.
func backgroundWakeRecent(last *session.BackgroundNudge, now time.Time) []string {
	if last == nil {
		return nil
	}
	var out []string
	for _, v := range last.Recent {
		if at, err := time.Parse(time.RFC3339, v); err == nil && now.Sub(at) < time.Hour {
			out = append(out, v)
		}
	}
	return out
}

func backgroundWakeCount(last *session.BackgroundNudge, now time.Time) int {
	return len(backgroundWakeRecent(last, now))
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
		at := now.UTC().Format(time.RFC3339)
		cur.BackgroundNudge = &session.BackgroundNudge{
			Mark: p.mark, At: at, Recent: append(backgroundWakeRecent(cur.BackgroundNudge, now), at),
		}
		return true
	})
	recordFleetGraphInstruct(m.Name, TurnSourceNotice, "", "", backgroundWakeNotice)
	log.Printf("background-wake: nudged %s (idle %s after its background work, no turn followed)", m.Name, backgroundWakeGrace)
}

func backgroundWakeDone(name string) {
	bgWakeMu.Lock()
	defer bgWakeMu.Unlock()
	if p := bgWakePeriods[name]; p != nil {
		p.done = true
	}
}
