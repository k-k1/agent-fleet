package tmuxx

import (
	"hash/fnv"
	"sync"
	"time"
)

// The layer that decides, by time, whether a frame that looks like an idle prompt may be
// read as "really waiting". A single frame cannot decide it — measured, hence this split.
//
// # Why one frame is not enough (measured 2026-08-28, claude 2.1.25x)
//
// claude draws no spinner line while it renders an assistant text block. During that time
// the pane is structurally identical to an idle prompt: header, transcript, input box and
// mode footer all present. In a 150-second series of frames taken from our own session
// (claude_scpsygq) at 0.25s intervals, the whole 21.4 seconds spent drawing body text was
// spinnerActive=false / atIdlePrompt=true, with the spinner showing only before and after.
// So for those 20 seconds, while the body streams into the TUI, a pane-derived verdict
// asserts "waiting for input".
//
// Diffing does not save it either: claude draws markdown block by block, so the pane is
// never redrawn while a long paragraph is generated. In the same measurement those 21.4
// seconds held only 5 distinct frames across 82 captures, and the longest run of identical
// bytes was 11.4 seconds. Polling every few seconds draws the same frame twice in a row, so
// "unchanged since last time means idle" falls into the same hole.
//
// The asymmetry is what saves it: a pane that really waits for input never changes until
// something happens (another session, claude_swovou6, sampled every second for 47 seconds,
// yielded one distinct frame), while a pane drawing body text is rewritten at every block
// boundary. So require "an idle-looking frame that stayed unchanged for at least
// idleSettleWindow":
//
//   - While body text is drawn, each block's redraw rewinds the clock, so it never settles.
//     However long the answer is, nothing is misjudged unless one block exceeds the window.
//   - A real wait is never redrawn, so it always settles, just delayed by the window.
//
// # The window value
//
// Four times the margin over the longest measured still period (11.4s, one long paragraph).
// A longer window loses fewer body-drawing frames, but it lengthens by just as much the time
// a stall that fires no hook (killed and resumed, a turn cut short by an API error, an
// abandoned modal) is shown as in progress. Wrongly claiming "waiting for input" does more
// harm (the stop button disappears, completion notifications and reports fire early, the
// idle verdict is affected) than a badge lagging by tens of seconds, so this errs on the
// generous side.
const idleSettleWindow = 45 * time.Second

// idleSettleNow is the clock. Only tests replace it.
var idleSettleNow = time.Now

// paneSighting is one session's "when the pane's frame last changed".
type paneSighting struct {
	sig     uint64
	changed time.Time // when this frame appeared (i.e. when it first differed from the previous one)
	seen    time.Time // when it was last observed (for eviction)
}

var (
	sightMu sync.Mutex
	sights  = map[string]paneSighting{}
)

// sightingTTL drops the leftovers of sessions that are gone. Polling runs every few
// seconds, so anything unseen for this long no longer exists (and settling again from
// scratch would be harmless anyway).
const sightingTTL = 30 * time.Minute

// frameSig fingerprints one frame. Only used to compare contents, so collision resistance
// is not needed.
func frameSig(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

// observeFrame records this capture and reports whether the pane has looked the same for
// at least idleSettleWindow. Callers combine it with the idle verdict itself (a pane can
// sit unchanged while it is busy — the thinking spinner does change, but a modal does
// not, and those are already excluded by atIdlePrompt).
//
// The first observation counts as "just changed", erring on the conservative side: right
// after an agent restart even a genuinely waiting session stays "in progress" for the
// length of the window, which is better than the reverse.
func observeFrame(name, frame string) bool {
	now := idleSettleNow()
	sig := frameSig(frame)
	sightMu.Lock()
	defer sightMu.Unlock()
	prev, ok := sights[name]
	if !ok || prev.sig != sig {
		prev = paneSighting{sig: sig, changed: now}
	}
	prev.seen = now
	sights[name] = prev
	for n, s := range sights {
		if now.Sub(s.seen) > sightingTTL {
			delete(sights, n)
		}
	}
	return now.Sub(prev.changed) >= idleSettleWindow
}

// ForgetPane drops a session's recorded sighting. Called when a pane is known to be gone
// so a later session reusing the name does not inherit its clock.
func ForgetPane(name string) {
	sightMu.Lock()
	delete(sights, name)
	sightMu.Unlock()
}
