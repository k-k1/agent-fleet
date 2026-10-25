package sessionx

// Holding a peer message until a just-started claude Terminal session has settled (#2741).
//
// send_to_peer_session resumes a stopped peer and waits for sessionInputReady, which a claude
// pane passes as soon as its composer footer is drawn. That is not the moment it takes input:
// of three sends that resumed a peer, two typed prompts vanished without a turn and the third
// was reported delivered on a turn the resume itself started (the notification of a background
// task that ended while the session was stopped). A busy session needs no such hold, because
// claude queues what is typed mid-turn; the boot is the window where typed text is dropped.
//
// So a peer send to a claude session whose CLI was launched less than peerBootHoldMax ago goes
// to the pending spool (peer_pending.go) with blocked_on "starting", and leaves it once the
// pane has stood on its idle prompt for the idle-settle window (tmuxx/idlesettle.go): nothing
// still booting, or running the turn the resume started, sits still that long. Past
// peerBootHoldMax the hold ends anyway and the spool's own readiness rule (idle, input ready,
// two looks in a row) decides, so a pane that never settles holds a message minutes, not a day.

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
	bootMu     sync.Mutex
	bootStarts = map[string]time.Time{}
	// peerBootNow and peerBootSettled are the clock and the pane read of the hold. Variables so
	// tests stub them.
	peerBootNow     = time.Now
	peerBootSettled = func(name string) bool { return tmuxx.ReadPane(name).IdleSettled }
)

// noteSessionLaunched records that name's CLI was just (re)launched in its pane.
func noteSessionLaunched(name string) {
	bootMu.Lock()
	defer bootMu.Unlock()
	now := peerBootNow()
	for n, at := range bootStarts {
		if now.Sub(at) >= peerBootHoldMax {
			delete(bootStarts, n)
		}
	}
	bootStarts[name] = now
}

// peerBootBlocker answers peerBlockedStarting while m is a claude Terminal session still inside
// its boot hold, "" otherwise. The first answer of "" ends the hold for that launch.
func peerBootBlocker(m session.Meta) string {
	if m.DriverKind() == session.DriverManaged || NormalizeKind(m.Kind) != session.KindClaude {
		return ""
	}
	bootMu.Lock()
	at, ok := bootStarts[m.Name]
	bootMu.Unlock()
	if !ok {
		return ""
	}
	if peerBootNow().Sub(at) < peerBootHoldMax && !peerBootSettled(m.Name) {
		return peerBlockedStarting
	}
	bootMu.Lock()
	if cur, ok := bootStarts[m.Name]; ok && cur.Equal(at) {
		delete(bootStarts, m.Name)
	}
	bootMu.Unlock()
	return ""
}
