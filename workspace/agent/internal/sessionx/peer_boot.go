package sessionx

// Holding a peer message until a just-started claude Terminal session has settled (#2741).
//
// send_to_peer_session resumes a stopped peer and waits for sessionInputReady, which a claude
// pane passes as soon as its composer footer is drawn. That is not the moment it takes input:
// in the sends that resumed a peer, typed prompts vanished without a turn, and one was reported
// delivered on a turn the resume itself started (the notification of a background task that
// ended while the session was stopped). A busy session needs no such hold, because
// claude queues what is typed mid-turn; the boot is the window where typed text is dropped.
//
// So a peer send to a claude session whose CLI was launched less than peerBootHoldMax ago goes
// to the pending spool (peer_pending.go) with blocked_on "starting", and leaves it once the
// pane has stood on its idle prompt for the idle-settle window (tmuxx/idlesettle.go): nothing
// still booting, or running the turn the resume started, sits still that long. Past
// peerBootHoldMax the hold ends anyway and the spool's own readiness rule (idle, input ready,
// two looks in a row) decides, so a pane that never settles holds a message minutes, not a day.
//
// The launch time is read from tmux, which outlives an Agent restart: the pane's own creation
// stamp, not a note the Agent keeps, so a restart while the CLI still boots neither loses the
// hold nor moves its cap. Only "this launch's hold has ended" is kept in memory; a restart
// forgets it and the hold is decided again, which costs at most one more settle window.

import (
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// peerBlockedStarting is the blocked_on of a message held for a booting session.
const peerBlockedStarting = "starting"

// peerBootHoldMax bounds the hold: the idle-settle window (45 s) after a boot of a few seconds,
// with room for a turn the resume started.
const peerBootHoldMax = 3 * time.Minute

var (
	bootMu    sync.Mutex
	bootEnded = map[string]time.Time{} // session -> the launch whose hold has ended
	// peerBootNow, peerBootLaunchedAt and peerBootSettled are the clock, the launch time and the
	// pane read of the hold. Variables so tests stub them. CLIRecordsSince rounds a launch known
	// only to the second up, which errs towards holding.
	peerBootNow        = time.Now
	peerBootLaunchedAt = func(name string) (time.Time, bool) {
		return tmuxx.CLIRecordsSince(session.TmuxName(name))
	}
	peerBootSettled = func(name string) bool { return tmuxx.ReadPane(name).IdleSettled }
)

// peerBootBlocker answers peerBlockedStarting while m is a claude Terminal session still inside
// its boot hold, "" otherwise. The first answer of "" ends the hold for that launch.
func peerBootBlocker(m session.Meta) string {
	if m.DriverKind() == session.DriverManaged || NormalizeKind(m.Kind) != session.KindClaude {
		return ""
	}
	at, ok := peerBootLaunchedAt(m.Name)
	if !ok || peerBootNow().Sub(at) >= peerBootHoldMax {
		return ""
	}
	bootMu.Lock()
	ended := bootEnded[m.Name].Equal(at)
	bootMu.Unlock()
	if ended {
		return ""
	}
	if !peerBootSettled(m.Name) {
		return peerBlockedStarting
	}
	bootMu.Lock()
	now := peerBootNow()
	for n, t := range bootEnded {
		if now.Sub(t) >= peerBootHoldMax {
			delete(bootEnded, n)
		}
	}
	bootEnded[m.Name] = at
	bootMu.Unlock()
	return ""
}
