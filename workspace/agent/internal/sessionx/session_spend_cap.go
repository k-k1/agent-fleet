package sessionx

// The per-session spend budget (#1054): a session whose estimated spend reaches its cap stops
// after the turn it is running, and the user decides whether it continues.
//
// It is built on the stop-after-turn arm rather than beside it. Crossing the cap ARMS the stop;
// the reconciler's existing end-of-turn evidence, background-work check and debounce decide
// when the halt happens, and haltSessionMeta does it, so Managed and Terminal sessions stop
// exactly the way a user-set arm stops them. Only two things are the budget's own:
//
//   - a new prompt to an over-budget session re-arms the stop instead of releasing it
//     (cancelStopArmOnNewPrompt), so each further turn ends in a stop until the cap is raised;
//   - past SpendCapHardFactor × the cap the session is halted at once, mid-turn — the one
//     runaway turn that waiting for its end would let run on.
//
// The spend is an estimate (session.Spend), and the UI says so.

import (
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const spendCapRangeMsg = "spend_cap_usd must be a number from 0 (no budget) to 100000"

// spendCacheTTL bounds how often one session's transcript is re-priced. The sweep runs on the
// reconciler's tick and the Console polls the spend while a session is open; both read through
// this cache, so a capped session costs one whole-transcript read per TTL however many look.
// The price of the TTL is a stop that comes up to this much later than the crossing.
const spendCacheTTL = 10 * time.Second

type spendEntry struct {
	at        time.Time
	createdAt string // a recreated name is a new session; never serve its predecessor's spend
	spend     session.Spend
}

var spendCache = struct {
	sync.Mutex
	m map[string]spendEntry
}{m: map[string]spendEntry{}}

// sessionSpendOf is the session's spend, at most spendCacheTTL old.
func sessionSpendOf(m session.Meta, now time.Time) session.Spend {
	spendCache.Lock()
	e, ok := spendCache.m[m.Name]
	spendCache.Unlock()
	if ok && e.createdAt == m.CreatedAt && now.Sub(e.at) < spendCacheTTL && !now.Before(e.at) {
		return e.spend
	}
	return sessionSpendFresh(m, now)
}

// sessionSpendFresh re-prices the transcript and refreshes the cache. Used where a decision
// is about to be written that a stale figure would get wrong (raising the cap).
func sessionSpendFresh(m session.Meta, now time.Time) session.Spend {
	sp := sessionSpendUncached(m)
	spendCache.Lock()
	spendCache.m[m.Name] = spendEntry{at: now, createdAt: m.CreatedAt, spend: sp}
	spendCache.Unlock()
	return sp
}

func forgetSpend(name string) {
	spendCache.Lock()
	delete(spendCache.m, name)
	spendCache.Unlock()
}

// spendCapCandidate: a live session with a budget whose kind has a transcript to price.
func spendCapCandidate(m session.Meta) bool {
	return session.ValidName(m.Name) && !m.Archived && m.StoppedAt == "" && m.SpendCapUSD > 0 &&
		AgentOf(m.Kind).Caps().CanTranscript
}

// SweepSpendCaps is one pass over every capped live session (the reconciler's tick).
func SweepSpendCaps(now time.Time) {
	for _, m := range session.ListMetas() {
		if !spendCapCandidate(m) {
			continue
		}
		evaluateSpendCap(m, sessionSpendOf(m, now), now)
	}
}

// evaluateSpendCap decides for one session. Split from the sweep so tests can hand it a spend.
func evaluateSpendCap(m session.Meta, sp session.Spend, now time.Time) {
	if !session.OverSpendCap(m, sp.USD) {
		return
	}
	stamp := now.Format(time.RFC3339)
	// First crossing: record it and arm the stop. Both under the meta mutex and against the
	// meta as it is now, so a cap the user raised a moment ago is what is compared, and a
	// prompt that just released an arm cannot interleave between the two writes.
	// An unchanged meta (already crossed) comes back with ok=false; only a missing one is empty.
	cur, _ := UpdateSessionMeta(m.Name, func(c *session.Meta) bool {
		if c.StoppedAt != "" || c.SpendCapHitAt != "" || !session.OverSpendCap(*c, sp.USD) {
			return false
		}
		c.SpendCapHitAt = stamp
		start, _ := session.SpendStart(*c)
		armForCrossing(c, session.SpendCrossingBound(sp, c.SpendCapUSD, start, now), now)
		return true
	})
	if cur.Name == "" || cur.StoppedAt != "" || !session.OverSpendCap(cur, sp.USD) {
		return
	}
	if !session.OverHardSpendCap(cur, sp.USD) {
		return
	}
	// Past the hard limit the turn is not waited for. The halt promotes a pending interaction
	// and stops Managed and Terminal sessions the same way the user's halt button does.
	halted, err := haltSessionMeta(cur)
	if err != nil {
		log.Printf("spend-cap: %s: hard halt failed, retrying next tick: %v", m.Name, err)
		return
	}
	log.Printf("spend-cap: halted %s mid-turn (≈$%.2f of $%.2f)", m.Name, sp.USD, cur.SpendCapUSD)
	notifySpendCapStop(halted, sp, true)
}

// armForCrossing arms the budget's stop at bound — the end of the last turn that stayed under
// the cap — so the turn that crossed is the one whose end stops the session, even when it ended
// before this tick saw the crossing. An arm already live at or before bound covers that turn
// too and is left as it is (still the user's). A later one is displaced and kept in
// SpendCapArmPrev, to be put back if the crossing is lifted.
func armForCrossing(c *session.Meta, bound, now time.Time) {
	if at, live := session.StopArmedAt(*c, now); live && !at.After(bound) {
		return
	}
	c.SpendCapArmPrev = ""
	if _, live := session.StopArmedAt(*c, now); live {
		c.SpendCapArmPrev = c.StopAfterTurnAt
	}
	c.StopAfterTurnAt = bound.Format(time.RFC3339)
	c.SpendCapArmAt = c.StopAfterTurnAt
}

// overSpendCapArmed: the session has crossed its budget and the cap has not been raised since.
// While true, a new prompt re-arms the stop rather than releasing it.
func overSpendCapArmed(m session.Meta) bool {
	return m.SpendCapHitAt != "" && m.SpendCapUSD > 0
}

// notifySpendCapStop tells the notification centre that the budget stopped a session. It
// replaces the stop-after-turn notice for that stop: one stop, one notification, and this one
// says why and carries what the "raise and resume" action needs.
func notifySpendCapStop(m session.Meta, sp session.Spend, midTurn bool) {
	ev := notice.New("spend-budget", m.Name, m.Kind, session.Display(m))
	ev.Payload["capUsd"] = m.SpendCapUSD
	ev.Payload["spendUsd"] = sp.USD
	ev.Payload["midTurn"] = midTurn
	_ = notice.Put(ev)
}

// HandleSessionSpendCap (POST /sessions/{name}/spend-cap {"usd":N}) sets the budget; 0 removes
// it. A cap above the current spend lifts a crossing: the hit is cleared together with the arm
// it set, which is the "raise the cap" half of "raise and resume" (the Console then starts the
// session through the ordinary resume).
func HandleSessionSpendCap(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	var req struct {
		USD *float64 `json:"usd"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if req.USD == nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_spend_cap", spendCapRangeMsg)
		return
	}
	usd, ok := session.NormalizeSpendCap(*req.USD)
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_spend_cap", spendCapRangeMsg)
		return
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	sp := sessionSpendFresh(m, time.Now())
	m, ok = setSpendCap(name, usd, sp)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"name": name, "spendCapUsd": m.SpendCapUSD, "spendCapHitAt": m.SpendCapHitAt,
		"stopAfterTurnAt": stopArmVisible(m), "spendUsd": sp.USD,
	})
}

// setSpendCap writes the cap and, when the new cap lifts the crossing, clears the hit and the
// stop the budget armed (restoring any arm that stop displaced). A cap still at or under the spend keeps the hit (the next sweep re-arms if
// the arm was consumed), so lowering a cap cannot quietly cancel a pending stop.
func setSpendCap(name string, usd float64, sp session.Spend) (session.Meta, bool) {
	return UpdateSessionMeta(name, func(m *session.Meta) bool {
		m.SpendCapUSD = usd
		if m.SpendCapHitAt != "" && !session.OverSpendCap(*m, sp.USD) {
			m.SpendCapHitAt = ""
			// Only the budget's own arm is released; one it displaced comes back, and an arm the
			// user or a schedule set (before or after the crossing) is not the budget's to drop.
			if session.SpendCapOwnsArm(*m) {
				m.StopAfterTurnAt = m.SpendCapArmPrev
			}
			m.SpendCapArmAt, m.SpendCapArmPrev = "", ""
		}
		return true
	})
}

// spendChild is one descendant's line in the parent's spend view.
type spendChild struct {
	Name     string  `json:"name"`
	Display  string  `json:"display"`
	Kind     string  `json:"kind"`
	Alive    bool    `json:"alive"`
	CapUSD   float64 `json:"spendCapUsd,omitempty"`
	SpendUSD float64 `json:"spendUsd"`
	Priced   bool    `json:"priced"`
}

// HandleSessionSpend (GET /sessions/{name}/spend) is the budget chip's read: the session's own
// spend against its cap, and its create_session descendants' spend beside it. Children are
// shown, not charged: each has its own cap, and a parent stopped by spend it never made would
// be the wrong session to stop.
func HandleSessionSpend(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	now := time.Now()
	var sp session.Spend
	if AgentOf(m.Kind).Caps().CanTranscript {
		sp = sessionSpendOf(m, now)
	}
	children := []spendChild{}
	var childrenUSD float64
	for _, c := range spendDescendants(name, session.ListMetas()) {
		if !AgentOf(c.Kind).Caps().CanTranscript {
			continue
		}
		csp := sessionSpendOf(c, now)
		childrenUSD += csp.USD
		children = append(children, spendChild{Name: c.Name, Display: session.Display(c), Kind: c.Kind,
			Alive: c.StoppedAt == "", CapUSD: c.SpendCapUSD, SpendUSD: csp.USD, Priced: csp.Priced})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"name": name, "spendCapUsd": m.SpendCapUSD, "spendCapHitAt": m.SpendCapHitAt,
		"hardFactor": session.SpendCapHardFactor,
		"spendUsd":   sp.USD, "priced": sp.Priced, "unpriced": sp.Unpriced, "reported": sp.Reported,
		"children": children, "childrenUsd": childrenUSD,
	})
}

// spendDescendants is every session create_session started from name, transitively, in a stable
// order. "Started by" is origin=session plus a matching OriginSession — never OriginSession
// alone, which a fork of a child also carries (session.InUnattendedChain's note).
func spendDescendants(name string, metas []session.Meta) []session.Meta {
	byParent := map[string][]session.Meta{}
	for _, m := range metas {
		if m.Archived || session.OriginOf(m) != session.OriginSession || m.OriginSession == "" {
			continue
		}
		byParent[m.OriginSession] = append(byParent[m.OriginSession], m)
	}
	var out []session.Meta
	seen := map[string]bool{name: true}
	queue := []string{name}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		kids := byParent[p]
		sort.Slice(kids, func(i, j int) bool { return kids[i].Name < kids[j].Name })
		for _, k := range kids {
			if seen[k.Name] {
				continue
			}
			seen[k.Name] = true
			out = append(out, k)
			queue = append(queue, k.Name)
		}
	}
	return out
}
