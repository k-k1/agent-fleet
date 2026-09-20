package fleetgraph

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// defaultWindow is the page's default span when the caller omits since (ADR 0096
// decision 8: default 24h, right edge is "now").
const defaultWindow = 24 * time.Hour

// parseMillis parses one ledger timestamp (RFC3339, millisecond precision) into unix
// millis for the DTO (decision 2). An unparseable stamp reads as zero rather than failing
// the whole page — one corrupt line must not blank the graph.
func parseMillis(ts string) int64 {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// rawLineage is one decoded lineage.jsonl row plus its millis, kept together so the
// window/overlap arithmetic below never re-parses a timestamp. seq is the row's position
// in the file (append order) — the identity BuildPage's dedup uses, because two DISTINCT
// rows can otherwise share every visible field: append-only guarantees order, not that
// (name, ev, ts) is collision-free at millisecond resolution (two genuine transitions for
// two different sessions, or two ObserveConv calls back to back after a ForgetSession,
// can land in the same millisecond under real load or a fast test).
type rawLineage struct {
	line lineageLine
	ms   int64
	seq  int
}

// readLineageFile loads the whole permanent ledger (ADR 0096 decision 7: lineage is never
// clipped, so there is nothing to save by streaming it). Its size is a few lines per
// session for the fleet's lifetime (docs/log/101 §101.3 estimate: well under a MB for
// years of use), so reading it whole on every request is the cheap side of correct.
func readLineageFile() ([]rawLineage, error) {
	b, err := os.ReadFile(lineagePath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []rawLineage
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 {
			continue
		}
		var l lineageLine
		if err := json.Unmarshal(raw, &l); err != nil {
			continue // one bad line must not sink the page
		}
		out = append(out, rawLineage{line: l, ms: parseMillis(l.Ts), seq: len(out)})
	}
	return out, nil
}

// readActivityWindow loads activity lines from every UTC day the [since,until] window
// touches, filtering to lines strictly inside it — unlike lineage, activity IS clipped
// (decision 7 is a lineage-only exception; activity is what rotates at 30 days).
func readActivityWindow(sinceMs, untilMs int64) []activityLine {
	from := utcDay(time.UnixMilli(sinceMs))
	to := utcDay(time.UnixMilli(untilMs))
	var out []activityLine
	for day := from; ; day = nextUTCDay(day) {
		b, err := os.ReadFile(activityPath(day))
		if err == nil {
			sc := bufio.NewScanner(bytes.NewReader(b))
			sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for sc.Scan() {
				raw := bytes.TrimSpace(sc.Bytes())
				if len(raw) == 0 {
					continue
				}
				var a activityLine
				if json.Unmarshal(raw, &a) != nil {
					continue
				}
				ms := parseMillis(a.Ts)
				if ms < sinceMs || ms > untilMs {
					continue
				}
				out = append(out, a)
			}
		}
		if day == to {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return parseMillis(out[i].Ts) < parseMillis(out[j].Ts) })
	return out
}

// ActivityRetentionDays is the default rotation window (ADR 0096 decision 2): activity is
// what rotates, lineage never does.
const ActivityRetentionDays = 30

// PruneActivity deletes activity-<day>.jsonl files older than ActivityRetentionDays,
// judged by the UTC date in the filename itself (never the file's mtime, which a restore
// or a backup tool can change independent of what the name promises). Best-effort: a
// leftover old file costs disk, never correctness, so an error here is logged and dropped.
func PruneActivity() {
	entries, err := os.ReadDir(dir())
	if err != nil {
		return
	}
	cutoff := clockNow().UTC().AddDate(0, 0, -ActivityRetentionDays).Format("2006-01-02")
	for _, e := range entries {
		n := e.Name()
		if len(n) != len("activity-YYYY-MM-DD.jsonl") || n[:9] != "activity-" {
			continue
		}
		day := n[9:19]
		if day < cutoff {
			_ = os.Remove(filepath.Join(dir(), n))
		}
	}
}

// StartActivityPruner runs PruneActivity once immediately and then once every 24h for as
// long as the process lives. A one-shot call at boot alone (the P1 implementation) only
// rotates activity on a restart — an Agent that stays up for weeks would never trim it, the
// exact case the 30-day retention exists for. Call once from main; it never returns.
func StartActivityPruner() {
	PruneActivity()
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for range t.C {
		PruneActivity()
	}
}

func nextUTCDay(day string) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	return t.AddDate(0, 0, 1).Format("2006-01-02")
}

// laneRun is a lane's observed [start, end) run, built from birth/revive...death events,
// used only to decide window overlap (the render model's own LaneRun is S-LOGIC's job).
type laneRun struct {
	start int64
	end   int64 // 0 = still open
}

// laneRuns groups one lane's lineage rows (already sorted by ts) into runs: a birth or
// revive opens one, the next death closes it.
func laneRuns(rows []rawLineage) []laneRun {
	var runs []laneRun
	var open *laneRun
	for _, r := range rows {
		switch r.line.Ev {
		case "birth", "revive":
			if open != nil {
				runs = append(runs, *open) // an open run with no death before the next open: cut it here
			}
			open = &laneRun{start: r.ms}
		case "death":
			if open != nil {
				open.end = r.ms
				runs = append(runs, *open)
				open = nil
			}
		}
	}
	if open != nil {
		runs = append(runs, *open)
	}
	return runs
}

func runsOverlap(runs []laneRun, sinceMs, untilMs int64) bool {
	for _, r := range runs {
		if r.start > untilMs {
			continue
		}
		if r.end != 0 && r.end < sinceMs {
			continue
		}
		return true
	}
	return false
}

// BuildPage assembles GET /api/fleet-graph's body for [sinceMs, untilMs]. It implements
// ADR 0096 decision 7's non-clipping rule in full:
//  1. every lineage event inside the window;
//  2. EVERY lineage event of every lane whose runs overlap the window, however old —
//     birth, convid, death, revive and archived alike (docs/log/101 §101.8: clipping this
//     to birth-only makes a stopped stretch vanish on a later window);
//  3. the birth of those lanes' ancestors (family ordering — decision 9), context only.
//
// Activity, unlike lineage, IS clipped to the window (it is what rotates).
func BuildPage(sinceMs, untilMs int64) (FleetGraphPage, error) {
	now := clockNow()
	if untilMs == 0 {
		untilMs = now.UnixMilli()
	}
	if sinceMs == 0 {
		sinceMs = untilMs - defaultWindow.Milliseconds()
	}
	if sinceMs > untilMs {
		sinceMs, untilMs = untilMs, sinceMs
	}

	all, err := readLineageFile()
	if err != nil {
		return FleetGraphPage{}, err
	}

	byLane := map[string][]rawLineage{}
	births := map[string]rawLineage{} // name -> its birth row, for the ancestor walk
	var oldestLineageMs int64
	haveOldest := false // NOT "oldestLineageMs == 0": a single unparsable ts (parseMillis
	// returns 0 on error) would otherwise freeze the running minimum at 0 forever, and
	// coverage.lineageSince would read "none kept" even though real lineage exists.
	for _, r := range all {
		byLane[r.line.Name] = append(byLane[r.line.Name], r)
		if r.line.Ev == "birth" {
			births[r.line.Name] = r
		}
		if r.ms <= 0 {
			continue // an unparsable timestamp must not corrupt the running minimum
		}
		if !haveOldest || r.ms < oldestLineageMs {
			oldestLineageMs = r.ms
			haveOldest = true
		}
	}
	for name := range byLane {
		rows := byLane[name]
		// Tie-break on seq (file/append order): two rows at the same millisecond must
		// still sort deterministically, or which one "comes first" changes between calls.
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].ms != rows[j].ms {
				return rows[i].ms < rows[j].ms
			}
			return rows[i].seq < rows[j].seq
		})
		byLane[name] = rows
	}

	seen := map[int]bool{} // key: r.seq (file order) — content CAN collide at 1ms resolution, position cannot
	var result []any
	addRow := func(r rawLineage) {
		if seen[r.seq] {
			return
		}
		seen[r.seq] = true
		if dto := toDTOLineage(r.line, r.ms); dto != nil {
			result = append(result, dto)
		}
	}

	overlapping := map[string]bool{}
	for name, rows := range byLane {
		runs := laneRuns(rows)
		inWindow := false
		for _, r := range rows {
			if r.ms >= sinceMs && r.ms <= untilMs {
				inWindow = true
				addRow(r) // ① every event inside the window
			}
		}
		if inWindow || runsOverlap(runs, sinceMs, untilMs) {
			overlapping[name] = true
		}
	}
	for name := range overlapping {
		for _, r := range byLane[name] { // ② the WHOLE lineage of an overlapping lane
			addRow(r)
		}
	}
	// ③ ancestor births, context only — walk originSession chains starting from every
	// overlapping lane's own birth, however far back, stopping when a link cannot be
	// resolved (the parent's own lineage was erased — decision 6/9).
	for name := range overlapping {
		b, ok := births[name]
		// Bounded rather than a bare "until unresolved": lineage is Agent-written and a
		// cycle should never occur, but a bug that produced one must not hang the handler.
		for hops := 0; ok && b.line.OriginSession != "" && hops < 64; hops++ {
			parent := b.line.OriginSession
			pb, pok := births[parent]
			if !pok {
				break
			}
			addRow(pb)
			b, ok = pb, pok
		}
	}

	activity := readActivityWindow(sinceMs, untilMs)
	activityDTO := make([]any, 0, len(activity))
	for _, a := range activity {
		if dto := toDTOActivity(a); dto != nil {
			activityDTO = append(activityDTO, dto)
		}
	}

	cov := GraphCoverage{}
	if haveOldest {
		v := oldestLineageMs
		cov.LineageSince = &v
	}
	if v, ok := oldestActivityMs(); ok {
		cov.ActivitySince = &v
	}
	if v, ok := readBackfillMarker(); ok {
		cov.BackfilledBefore = v
	}

	return FleetGraphPage{
		Since: sinceMs, Until: untilMs, Now: now.UnixMilli(),
		Lineage: nonNil(result), Activity: nonNil(activityDTO), Coverage: cov,
	}, nil
}

// nonNil turns a nil slice into an empty one so the DTO always serialises as `[]`, never
// `null` — a client doing `page.lineage.length` on null would panic.
func nonNil(s []any) []any {
	if s == nil {
		return []any{}
	}
	return s
}

// oldestActivityMs scans fleet-graph/ for the earliest activity-<day>.jsonl that exists
// and holds at least one line, returning that line's own timestamp (not just midnight of
// its day, which would overstate coverage by up to 24h).
func oldestActivityMs() (int64, bool) {
	entries, err := os.ReadDir(dir())
	if err != nil {
		return 0, false
	}
	var days []string
	for _, e := range entries {
		n := e.Name()
		if len(n) == len("activity-YYYY-MM-DD.jsonl") && n[:9] == "activity-" {
			days = append(days, n[9:19])
		}
	}
	if len(days) == 0 {
		return 0, false
	}
	sort.Strings(days)
	b, err := os.ReadFile(activityPath(days[0]))
	if err != nil {
		return 0, false
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 {
			continue
		}
		var a activityLine
		if json.Unmarshal(raw, &a) != nil {
			continue
		}
		if ms := parseMillis(a.Ts); ms > 0 {
			return ms, true // same reasoning as oldestLineageMs: skip past an unparsable ts
		}
	}
	return 0, false
}
