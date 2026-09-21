package muse

// Subscription quota (ADR 0095 decision 10). MSP carries it two ways and they are the same
// object: `usage/read` asks for the host's last observation, and `usage/changed` pushes one
// when it moves. AF records both into one process-wide value, because what they describe is
// the ACCOUNT — under decision 3's one-host-per-session shape, five sessions all report the
// same subscription and the newest observation is simply the truest one.
//
// ⚠️ It is an observation, not a query: measured, `usage/read` answers `{}` until that host has
// seen a completion. A workspace whose muse sessions have all just started therefore has no
// reading at all, and `ok: false` is the honest answer — not zero.

import (
	"net/http"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
)

var quotaMu sync.Mutex
var quotaSeen *msp.SubscriptionUsage

// recordQuota keeps the newest observation. Newest by the host's own `observedAtMs` rather
// than by arrival: two hosts can deliver out of order, and an older reading overwriting a
// newer one is a chip that walks backwards.
func recordQuota(u msp.SubscriptionUsage) {
	quotaMu.Lock()
	defer quotaMu.Unlock()
	if quotaSeen != nil && u.ObservedAtMs < quotaSeen.ObservedAtMs {
		return
	}
	cp := u
	quotaSeen = &cp
}

func lastQuota() *msp.SubscriptionUsage {
	quotaMu.Lock()
	defer quotaMu.Unlock()
	if quotaSeen == nil {
		return nil
	}
	cp := *quotaSeen
	return &cp
}

// readQuota asks a live host for its last observation, so a Console that opens the chip
// before any turn has completed in THIS process still gets whatever the host remembers.
// Nothing is spawned for it: a quota reading is not worth a 299 MB process start, and a
// workspace with no muse session running has nothing to ask.
func readQuota() {
	for _, h := range liveHandles() {
		h.mu.Lock()
		cl := h.cl
		h.mu.Unlock()
		if cl == nil {
			continue
		}
		var res msp.UsageReadResult
		// No params: the schema declares none — it reads the host's own last observation and
		// has nothing to narrow.
		if cl.CallInto(msp.MethodUsageRead, nil, callTimeout, &res) != nil {
			continue
		}
		if res.Usage != nil {
			recordQuota(*res.Usage)
			return
		}
	}
}

// HandleUsage (GET /muse/usage) answers the WsBar's quota chip.
//
// The window names are the other chips' (`fiveHour` / `sevenDay`) because one Console
// component renders every agent's chip from that shape. Muse's own vocabulary is "the current
// window" and "the rolling weekly block", and the window's length is a wire field rather than
// a constant — measured 300 minutes, i.e. five hours, but carried through as `windowMins` so
// the surface never has to assume it.
func HandleUsage(w http.ResponseWriter, r *http.Request) {
	authed := Installed() && readCredential().Present
	out := map[string]any{"ok": false, "authed": authed}
	if !authed {
		httpx.WriteJSON(w, http.StatusOK, out)
		return
	}
	if lastQuota() == nil {
		readQuota()
	}
	u := lastQuota()
	if u == nil {
		// Signed in, nothing observed yet. The chip stays visible (authed) and says it has no
		// reading, which is a different fact from 0% used.
		httpx.WriteJSON(w, http.StatusOK, out)
		return
	}
	out["ok"] = true
	out["plan"] = u.Tier
	out["windowMins"] = u.Window.WindowDurationMins
	out["fiveHour"] = map[string]any{
		"pct":      u.Window.UsedPercent,
		"resetsAt": epochMsToRFC3339(u.Window.ResetsAtMs),
	}
	out["sevenDay"] = map[string]any{
		"pct":      u.Weekly.UsedPercent,
		"resetsAt": epochMsToRFC3339(u.Weekly.ResetsAtMs),
	}
	out["ageSec"] = int(time.Since(time.UnixMilli(u.ObservedAtMs)).Seconds())
	httpx.WriteJSON(w, http.StatusOK, out)
}

// epochMsToRFC3339 renders a wire timestamp the way the other usage endpoints do. Zero means
// "the provider named no reset", and it must not become 1970 on the screen.
func epochMsToRFC3339(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}
