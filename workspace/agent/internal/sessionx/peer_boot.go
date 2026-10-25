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
// The launch is read from tmux, which outlives an Agent restart (tmuxx.PaneLaunch), not from a
// note the Agent keeps, so a restart while the CLI still boots neither loses the hold nor moves
// its cap. Only "this launch's hold has ended" is kept in memory, keyed by the launch's identity
// because its start time moves between reads; a restart forgets it and the hold is decided
// again, which costs at most one more settle window.

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

// bootHoldEnd is a launch whose hold has ended: its identity, and when it started (for pruning).
type bootHoldEnd struct {
	id string
	at time.Time
}

var (
	bootMu    sync.Mutex
	bootEnded = map[string]bootHoldEnd{}
	// peerBootNow, peerBootLaunch and peerBootSettled are the clock, the launch running in the
	// pane and the pane read of the hold. Variables so tests stub them. The launch's start errs
	// towards holding where only tmux's whole second is known (tmuxx.PaneLaunch).
	peerBootNow    = time.Now
	peerBootLaunch = func(name string) (id string, at time.Time, ok bool) {
		return tmuxx.PaneLaunch(session.TmuxName(name))
	}
	peerBootSettled = func(name string) bool { return tmuxx.ReadPane(name).IdleSettled }
)

// peerBootBlocker answers peerBlockedStarting while m is a claude Terminal session still inside
// its boot hold, "" otherwise. The first answer of "" ends the hold for that launch.
func peerBootBlocker(m session.Meta) string {
	if m.DriverKind() == session.DriverManaged || NormalizeKind(m.Kind) != session.KindClaude {
		return ""
	}
	id, at, ok := peerBootLaunch(m.Name)
	if !ok || peerBootNow().Sub(at) >= peerBootHoldMax {
		return ""
	}
	// Matched by the launch's identity: its start time moves between two reads (PaneLaunch).
	bootMu.Lock()
	ended := bootEnded[m.Name].id == id
	bootMu.Unlock()
	if ended {
		return ""
	}
	if !peerBootSettled(m.Name) {
		return peerBlockedStarting
	}
	bootMu.Lock()
	now := peerBootNow()
	for n, e := range bootEnded {
		if now.Sub(e.at) >= peerBootHoldMax {
			delete(bootEnded, n)
		}
	}
	bootEnded[m.Name] = bootHoldEnd{id: id, at: at}
	bootMu.Unlock()
	return ""
}
