package main

// The spend the per-session budget compares with (#1054, session.Spend).
//
// It is priced from the session's own transcript through the same turn fold the ledger uses
// (foldTurnRows) and the same price table the usage view uses (usageEstCostUSD), so a session's
// budget chip and its row in the usage view agree. The ledger itself is not read: it has no
// per-session query, it folds lazily on read, and adding its rows to a transcript sum would
// count every turn twice.

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

func sessionSpend(m session.Meta) session.Spend {
	return spendOfTurns(m, sessionx.UsageTurns(m))
}

// spendOfTurns prices one session's turns. Per logical turn: the CLI's reported cost when it
// gave one, otherwise the list-price estimate of the turn's tokens. The two are alternatives,
// never added — the usage view shows them side by side for the same reason.
//
// The open turn is included (includeTrailing): the hard limit exists for a turn that is still
// running, and a transcript written per message already carries its spend so far.
//
// Turns whose timestamp is before the session's own start (session.SpendStart) are skipped. Claude and codex forks
// start from a copy of the source's history with the source's own timestamps; opencode zeroes
// the copied cost but keeps the tokens, which the estimate would price. A turn with no usable
// timestamp is counted — undercounting is the failure the budget exists to prevent.
func spendOfTurns(m session.Meta, turns []transcript.Turn) session.Spend {
	var sp session.Spend
	born, bornOK := session.SpendStart(m)
	for _, r := range foldTurnRows(turns, true) {
		if bornOK && r.TS != "" {
			if ts, err := time.Parse(time.RFC3339Nano, r.TS); err == nil && ts.Before(born) {
				continue
			}
		}
		if r.CostUSD > 0 {
			sp.USD += r.CostUSD
			sp.Priced, sp.Reported = true, true
			sp.Marks = append(sp.Marks, session.SpendMark{End: rowEnd(r.TS), USD: sp.USD})
			continue
		}
		agg := usageAgg{In: r.Tokens.In, Out: r.Tokens.Out,
			CacheRead: r.Tokens.CacheRead, CacheCreate: r.Tokens.CacheCreate}
		if agg.In+agg.Out+agg.CacheRead+agg.CacheCreate == 0 {
			continue
		}
		model := r.Model
		if model == "" {
			model, _ = usagex.ModelFallback(m.Model)
		}
		usd, _, ok := usageEstCostUSD(m.Kind, model, agg)
		if !ok {
			sp.Unpriced = true
			continue
		}
		sp.USD += usd
		sp.Priced = true
		sp.Marks = append(sp.Marks, session.SpendMark{End: rowEnd(r.TS), USD: sp.USD})
	}
	return sp
}

// rowEnd is a folded turn's timestamp (its last event), zero when there is none to parse.
func rowEnd(ts string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}
	}
	return t
}
