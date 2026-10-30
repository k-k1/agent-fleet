package main

// The spend the per-session budget compares with (#1054, session.Spend).
//
// It is priced from the session's own transcript, and from its subagents' transcripts where the
// CLI keeps them apart (claude), through the same turn fold the ledger uses (foldTurnRows) and
// the same price table the usage view uses (usageEstCostUSD). The ledger does not take in the
// subagents yet (#3054), so for a session that delegated, its usage-view row is lower than its
// budget chip. The ledger itself is not read: it has no per-session query, it folds lazily on
// read, and adding its rows to a transcript sum would count every turn twice.

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

func sessionSpend(m session.Meta) session.Spend {
	return spendOfRows(m, foldTurnRows(sessionx.UsageTurns(m), true), subagentSpendRows(m))
}

// subagentRowsEntry is one agent's transcripts folded into rows, valid while every copy keeps
// the size and mtime recorded in stamp.
type subagentRowsEntry struct {
	stamp string
	rows  []usageTurnRow
}

// subagentRowsCache holds each session's subagent transcripts already folded, keyed by session
// name and then by agent (its copies' paths). A Workflow run leaves a hundred or more of them, and the budget sweep
// prices a capped session every spendCacheTTL: re-reading them all each time costs tens of
// megabytes per tick on network storage, while only the agents still running ever change.
var subagentRowsCache = struct {
	sync.Mutex
	m map[string]subagentRowsCacheSession
}{m: map[string]subagentRowsCacheSession{}}

type subagentRowsCacheSession struct {
	createdAt string // a recreated name is a new session; never serve its predecessor's rows
	agents    map[string]subagentRowsEntry
	used      time.Time
}

// subagentRowsIdle drops a session's folded rows once nobody has priced it for this long. A
// stopped, archived or deleted session leaves the budget sweep and is never read again, and
// without this its rows would stay until the Agent restarts. A session priced after that is
// re-read in full once.
const subagentRowsIdle = 10 * time.Minute

// subagentSpendRows folds each of the session's subagent transcripts on its own (an agent
// reports only its own context, so two agents must never share a logical turn), re-reading
// only the files that changed since the last call.
func subagentSpendRows(m session.Meta) []usageTurnRow {
	return subagentSpendRowsAt(m, time.Now())
}

func subagentSpendRowsAt(m session.Meta, now time.Time) []usageTurnRow {
	groups := sessionx.SubagentLogs(m)
	subagentRowsCache.Lock()
	prev := subagentRowsCache.m[m.Name]
	subagentRowsCache.Unlock()
	if prev.createdAt != m.CreatedAt {
		prev.agents = nil
	}
	next := subagentRowsCacheSession{createdAt: m.CreatedAt, agents: make(map[string]subagentRowsEntry, len(groups)), used: now}
	var rows []usageTurnRow
	for _, paths := range groups {
		// Stat before reading: a file that grows in between is read with more than the cached
		// size says, so the next call sees a different stamp and reads it again.
		var live []string
		var stamp strings.Builder
		for _, p := range paths {
			if fi, err := os.Stat(p); err == nil {
				live = append(live, p)
				fmt.Fprintf(&stamp, "%s\x00%d\x00%d\x00", p, fi.Size(), fi.ModTime().UnixNano())
			}
		}
		if len(live) == 0 {
			continue
		}
		key := strings.Join(paths, "\x00")
		e, ok := prev.agents[key]
		if !ok || e.stamp != stamp.String() {
			turns, err := sessionx.SubagentTurns(m, live)
			switch {
			case err != nil && len(turns) == 0:
				// Not cached, so the next call reads it again; until then the last good fold
				// stands in, an undercount of only what was appended since.
				if ok {
					next.agents[key] = e
					rows = append(rows, e.rows...)
				}
				continue
			case err != nil:
				// Some copies read: price what they hold now, but keep the last complete fold
				// under its old stamp, so the next call reads every copy again and a later
				// total failure still has it to fall back on.
				if ok {
					next.agents[key] = e
				}
				rows = append(rows, foldTurnRows(turns, true)...)
				continue
			}
			e = subagentRowsEntry{stamp: stamp.String(), rows: foldTurnRows(turns, true)}
		}
		next.agents[key] = e
		rows = append(rows, e.rows...)
	}
	subagentRowsCache.Lock()
	if len(next.agents) == 0 {
		delete(subagentRowsCache.m, m.Name)
	} else {
		subagentRowsCache.m[m.Name] = next
	}
	for name, c := range subagentRowsCache.m {
		if now.Sub(c.used) > subagentRowsIdle {
			delete(subagentRowsCache.m, name)
		}
	}
	subagentRowsCache.Unlock()
	return rows
}

// spendOfTurns prices one session's own turns, without subagents.
func spendOfTurns(m session.Meta, turns []transcript.Turn) session.Spend {
	return spendOfRows(m, foldTurnRows(turns, true), nil)
}

// spendOfRows prices one session's folded turns. Per logical turn: the CLI's reported cost when it
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
//
// Subagent rows are priced like the session's own and merged in by time: the Marks have to run
// oldest first for SpendCrossingBound to find the last turn that stayed under the cap.
func spendOfRows(m session.Meta, rows, subagentRows []usageTurnRow) session.Spend {
	if len(subagentRows) > 0 {
		rows = append(rows[:len(rows):len(rows)], subagentRows...)
		sort.SliceStable(rows, func(i, j int) bool { return rowEnd(rows[i].TS).Before(rowEnd(rows[j].TS)) })
	}
	var sp session.Spend
	born, bornOK := session.SpendStart(m)
	for _, r := range rows {
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
