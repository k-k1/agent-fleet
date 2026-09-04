package main

// Rollup of the usage ledger (docs/log/46 §3-c / ADR0029 §8).
//
// raw expires after 90 days by default, but the rollup is kept forever. It aggregates by
// day x dimension, so it is small and ordinary queries never read raw.
//
// Buckets are cut by the row's ts (when the consumption happened), not by the file day it was
// appended to. The session fold takes past transcripts in after the fact (a backfill puts
// months of history in at once on the day it is introduced), so cutting by file day piles
// every past consumption onto the introduction day and makes the series meaningless. This was
// the first thing hit on a real machine.
//
// One invariant keeps it from double counting:
//
//	every raw file day is either folded (its rows are in the rollup) or unfolded (raw is
//	read). Today is always on the unfolded side, since rows are still being added.
//
// On top of that, each folded day keeps the file days that contributed to it (Src), so even
// when the process dies before the state can be written, a retry is skipped rather than added
// a second time.
//
// calls are counted as distinct calls (docs/log/46 §4). One claude call splits into per-model
// rows, so counting rows inflates the number of calls. Counting only one representative row
// of a call keeps the total call count intact along whichever axis it is summed.

import (
	"encoding/json"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// usageKey is the aggregation's dimension tuple. ref (session name / conversation id) and
// model_raw are deliberately left out: ref grows without bound and breaks the premise that
// the rollup is small (and must not leave through the aggregation API), while model_raw is
// not a query axis since the display groups by the canonical model. Both can still be traced
// from the rows while raw is inside its retention window.
type usageKey struct {
	Feature    string `json:"feature,omitempty"`
	Trigger    string `json:"trigger,omitempty"`
	Origin     string `json:"origin,omitempty"`
	OriginConv string `json:"origin_conv,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Model      string `json:"model,omitempty"`
	ModelSrc   string `json:"model_src,omitempty"`
	Verb       string `json:"verb,omitempty"`
	Sidechain  bool   `json:"sidechain,omitempty"`
	Measured   string `json:"measured,omitempty"`
	OK         bool   `json:"ok,omitempty"`
}

// usageAgg is the aggregated value. The JSON tags are the series element of /usage/series
// itself.
type usageAgg struct {
	Spend       int     `json:"spend"`
	In          int     `json:"in"`
	Out         int     `json:"out"`
	CacheRead   int     `json:"cread"`
	CacheCreate int     `json:"ccreate"`
	Calls       int     `json:"calls"`
	CostUSD     float64 `json:"cost_usd,omitempty"` // measured (only claude returns it)
	// CostEstUSD is the estimate derived from the price table (usage_price.go). It is a
	// separate value from the measured cost and is never added to it. It is not written to the
	// rollup: prices get revised, so it is recomputed with the current table on every read
	// (handleUsageSeries adds it per sample).
	CostEstUSD float64 `json:"cost_est_usd,omitempty"`
}

func (a *usageAgg) add(b usageAgg) {
	a.Spend += b.Spend
	a.In += b.In
	a.Out += b.Out
	a.CacheRead += b.CacheRead
	a.CacheCreate += b.CacheCreate
	a.Calls += b.Calls
	a.CostUSD += b.CostUSD
	a.CostEstUSD += b.CostEstUSD
}

type usageRollupEntry struct {
	Key usageKey `json:"k"`
	Agg usageAgg `json:"v"`
}

// usageRollupDay is one consumption day. Src is the raw file days that contributed, which is
// what keeps a second attempt to fold the same file from adding to it (a retry after a crash
// is safe).
type usageRollupDay struct {
	Src     []string           `json:"src"`
	Entries []usageRollupEntry `json:"e"`
}

// usageRollupMonth is one month. The key is the consumption day (the day of the row's ts).
type usageRollupMonth struct {
	Days map[string]usageRollupDay `json:"days"`
}

// usageRolledFile records one folded raw file day. MinDay/MaxDay is the range of consumption
// days it contributed to, used to say honestly which period can no longer be reconstructed at
// hour granularity once raw has been pruned.
type usageRolledFile struct {
	MinDay string `json:"minDay"`
	MaxDay string `json:"maxDay"`
}

// usageRollupVersion is the version of the rollup files. Raising it makes the next
// ensureUsageRollups rebuild whatever can still be rebuilt from raw
// (rebuildUsageRollupsLocked).
//
//	v1: first version (P3).
//	v2: added (ref, idx) deduplication (usage_dedup.go). A v1 aggregate may have folded in
//	    duplicates that got through the fold's crash window, and since the aggregate is
//	    already summed and cannot be subtracted from, rebuilding is the only way to drop them.
const usageRollupVersion = 2

type usageRollupState struct {
	Version int                        `json:"v"`
	Rolled  map[string]usageRolledFile `json:"rolled"`
	// Folded is the (ref, idx) watermark of what has been folded in. It stays in the state so
	// duplicates that entered the rollup can still be dropped after raw has been pruned (i.e.
	// once the original rows can no longer be read).
	Folded usageDedupIndex `json:"folded,omitempty"`
}

var usageRollupMu sync.Mutex

func usageRollupDir() string { return filepath.Join(usagex.Dir(), "rollup") }

func usageRollupPath(month string) string { return filepath.Join(usageRollupDir(), month+".json") }

func usageRollupStatePath() string { return filepath.Join(usageRollupDir(), "state.json") }

func readUsageRollup(month string) usageRollupMonth {
	m := usageRollupMonth{Days: map[string]usageRollupDay{}}
	b, err := os.ReadFile(usageRollupPath(month))
	if err != nil {
		return m
	}
	if json.Unmarshal(b, &m) != nil || m.Days == nil {
		m.Days = map[string]usageRollupDay{}
	}
	return m
}

func readUsageRollupState() usageRollupState {
	st := usageRollupState{Rolled: map[string]usageRolledFile{}, Folded: usageDedupIndex{}}
	b, err := os.ReadFile(usageRollupStatePath())
	if err != nil {
		return st
	}
	if json.Unmarshal(b, &st) != nil || st.Rolled == nil {
		st.Rolled = map[string]usageRolledFile{}
	}
	if st.Folded == nil {
		st.Folded = usageDedupIndex{}
	}
	return st
}

// writeUsageJSON writes with tmp+rename (a crash mid-write must not leave a broken
// aggregate).
func writeUsageJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// keyOf takes the dimension tuple out of one ledger row.
func keyOf(r usagex.Record) usageKey {
	return usageKey{
		Feature: r.Feature, Trigger: r.Trigger, Origin: r.Origin, OriginConv: r.OriginConv,
		Kind: r.Kind, Model: r.Model, ModelSrc: r.ModelSrc, Verb: r.Verb,
		Sidechain: r.Sidechain, Measured: r.Measured, OK: r.OK,
	}
}

// usageRowTime is the row's consumption time. A row whose ts is broken or empty is pulled to
// 00:00 of the file day it was appended to (never dropped, since dropping it makes the totals
// stop adding up).
func usageRowTime(r usagex.Record, fileDay string) time.Time {
	if t, err := time.Parse(time.RFC3339, r.TS); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse("2006-01-02", fileDay); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

// aggregateUsageRows folds a set of rows by dimension. seen is the set of calls already
// counted, shared with the caller so a call count is never counted twice.
//
// The call count goes on that call's representative row. One claude call splits into
// per-model rows, and their order is only the ascending raw id (i.e. spelling order) that
// `usageModelRows` gives them. Counting the first row makes which model gets calls=1 depend
// on the spelling of the id, so `by=model` or `split=model` shows the dominant model with 0
// calls and a bit player with 1 (which breaks the averages in the feature x model table at
// the same time). The representative is the model row with the largest spend in that call,
// and ties are decided deterministically by ascending model_raw, then model.
//
// It is not prorated (1/N or by spend ratio) because calls is frozen as an integer count
// (ADR0029 §1). The property that the total along any axis equals the number of distinct
// calls holds for the representative scheme too. Non-representative model rows end up with
// spend>0 and calls=0, so a table showing an average prints the division by zero as "—"
// (`perCall` on the console side).
func aggregateUsageRows(rows []usagex.Record, seen map[string]bool) map[usageKey]usageAgg {
	rep := usageCallRepresentatives(rows)
	out := map[usageKey]usageAgg{}
	for i, r := range rows {
		k := keyOf(r)
		a := out[k]
		a.Spend += r.Spend
		a.In += r.In
		a.Out += r.Out
		a.CacheRead += r.CacheRead
		a.CacheCreate += r.CacheCreate
		a.CostUSD += r.CostUSD
		if r.Call == "" {
			a.Calls++ // a row with no call id counts as one call in itself
		} else if rep[r.Call] == i && !seen[r.Call] {
			seen[r.Call] = true
			a.Calls++ // only that call's representative row counts as one
		}
		out[k] = a
	}
	return out
}

// usageCallRepresentatives picks, per call, the index of the row that carries the count.
func usageCallRepresentatives(rows []usagex.Record) map[string]int {
	rep := make(map[string]int, len(rows))
	for i, r := range rows {
		if r.Call == "" {
			continue
		}
		if j, ok := rep[r.Call]; !ok || usageRowOutranks(r, rows[j]) {
			rep[r.Call] = i
		}
	}
	return rep
}

// usageRowOutranks reports which of two rows represents the call. It takes the substance (the
// larger consumption) and decides ties by name: the aggregate is only reproducible if the
// same input always gives the same attribution.
func usageRowOutranks(a, b usagex.Record) bool {
	if a.Spend != b.Spend {
		return a.Spend > b.Spend
	}
	if a.ModelRaw != b.ModelRaw {
		return a.ModelRaw < b.ModelRaw
	}
	return a.Model < b.Model
}

// sortedRollupEntries turns the aggregate map into a deterministically ordered list of
// entries (unless the same input produces the same file, neither a diff review nor a test can
// be relied on).
func sortedRollupEntries(agg map[usageKey]usageAgg) []usageRollupEntry {
	out := make([]usageRollupEntry, 0, len(agg))
	for k, v := range agg {
		out = append(out, usageRollupEntry{Key: k, Agg: v})
	}
	sort.Slice(out, func(i, j int) bool { return usageKeyLess(out[i].Key, out[j].Key) })
	return out
}

func usageKeyLess(a, b usageKey) bool {
	for _, p := range [][2]string{
		{a.Feature, b.Feature}, {a.Kind, b.Kind}, {a.Model, b.Model},
		{a.Trigger, b.Trigger}, {a.Origin, b.Origin}, {a.OriginConv, b.OriginConv},
		{a.ModelSrc, b.ModelSrc}, {a.Verb, b.Verb}, {a.Measured, b.Measured},
	} {
		if p[0] != p[1] {
			return p[0] < p[1]
		}
	}
	if a.Sidechain != b.Sidechain {
		return !a.Sidechain
	}
	return !a.OK && b.OK
}

// mergeRollupDay adds a contribution to an existing day. The same raw file day is never added
// twice.
func mergeRollupDay(day usageRollupDay, srcFileDay string, agg map[usageKey]usageAgg) (usageRollupDay, bool) {
	for _, s := range day.Src {
		if s == srcFileDay {
			return day, false // already folded, so a retry is a no-op
		}
	}
	merged := map[usageKey]usageAgg{}
	for _, e := range day.Entries {
		merged[e.Key] = e.Agg
	}
	for k, v := range agg {
		a := merged[k]
		a.add(v)
		merged[k] = a
	}
	day.Src = append(append([]string{}, day.Src...), srcFileDay)
	sort.Strings(day.Src)
	day.Entries = sortedRollupEntries(merged)
	return day, true
}

// ensureUsageRollups folds the raw of completed days. Today is left alone, since rows are
// still being added. The folded aggregate stays even after raw disappears in a prune (the
// rollup is kept forever, ADR0029 §7-4).
func ensureUsageRollups() {
	usageRollupMu.Lock()
	defer usageRollupMu.Unlock()
	today := time.Now().UTC().Format("2006-01-02")
	st := readUsageRollupState()
	months := map[string]usageRollupMonth{}
	dirty := map[string]bool{}
	stateChanged := false
	if st.Version < usageRollupVersion {
		st = rebuildUsageRollupsLocked(st)
		stateChanged = true // always persist at least the version (do not retry the rebuild every time)
	}
	// The (ref, idx) dedup watermark is carried across file days, because when the fold's
	// crash window straddles a date the duplicate lands in a different file from the original
	// (usage_dedup.go).
	dd := st.Folded
	dropped := 0

	for _, fileDay := range usagex.RawDays() {
		if fileDay >= today {
			continue // today (and future days, if the clock went backwards) stay as raw
		}
		if _, done := st.Rolled[fileDay]; done {
			continue
		}
		rawRows, closed := readUsageDayForRollup(fileDay)
		if !closed {
			continue // the day can still be appended to (a race across the UTC date boundary); next time
		}
		// Sort by consumption day. The crux of this layer is that one raw file can contribute
		// to several days (months of them for a backfill).
		seen := map[string]bool{} // call dedup is shared per file
		byDay := map[string][]usagex.Record{}
		var days []string
		for _, r := range rawRows {
			ts := usageRowTime(r, fileDay)
			if !dd.accept(r, ts) {
				dropped++ // re-append of rows written before the watermark could be updated
				continue
			}
			d := ts.Format("2006-01-02")
			if _, ok := byDay[d]; !ok {
				days = append(days, d)
			}
			byDay[d] = append(byDay[d], r)
		}
		sort.Strings(days)
		mark := usageRolledFile{}
		for _, d := range days {
			month := d[:7]
			m, ok := months[month]
			if !ok {
				m = readUsageRollup(month)
				months[month] = m
			}
			if merged, ok := mergeRollupDay(m.Days[d], fileDay, aggregateUsageRows(byDay[d], seen)); ok {
				m.Days[d] = merged
				months[month] = m
				dirty[month] = true
			}
			if mark.MinDay == "" || d < mark.MinDay {
				mark.MinDay = d
			}
			if d > mark.MaxDay {
				mark.MaxDay = d
			}
		}
		if mark.MinDay == "" { // an empty file: the range is taken to be its own day
			mark.MinDay, mark.MaxDay = fileDay, fileDay
		}
		st.Rolled[fileDay] = mark
		stateChanged = true
	}
	if dropped > 0 {
		// Never drop silently: what happened always gets said (it is also a trace of a fold
		// that died partway).
		log.Printf("usage: rollup: dropped %d duplicate (ref,idx) rows from the aggregate", dropped)
	}
	if !stateChanged {
		return
	}
	st.Folded = dd
	// Write the month files before the state. The other order means a crash in between leaves
	// a day marked folded with no aggregate behind it, i.e. lost consumption. In this order
	// the worst case is trying to fold again, which Src rejects.
	//
	// If even one month file cannot be written, the state is not advanced. Advancing it leaves
	// the consumption that should have gone into that month marked folded and gone from the
	// aggregate, never to come back once raw is pruned. The months that were written are
	// remembered as folded by Src, so the next re-fold does not add them twice.
	for month := range dirty {
		if err := writeUsageJSON(usageRollupPath(month), months[month]); err != nil {
			log.Printf("usage: rollup: writing %s failed: %v (the state is not advanced, i.e. retried next time)", month, err)
			return
		}
	}
	if err := writeUsageJSON(usageRollupStatePath(), st); err != nil {
		log.Printf("usage: rollup: writing the state failed: %v", err)
	}
}

// readUsageDayForRollup reads one day in a way that does not race the appender. The point is
// that it checks whether the day can still be appended to while holding usagex.Mu.
//
// The appending side (usagex.AppendRows) takes usagex.Mu before deciding which day to append to,
// so if "today" is re-read inside the lock here and `day < today` holds, any append that
// takes the lock afterwards necessarily picks a newer day, i.e. this file can no longer grow.
// With two separate locks, an append that picked its day just before the UTC date boundary
// lands in that file after the rollup and is never read again because the file counts as
// folded (i.e. that row silently disappears). The window is tiny, but it closes silently, so
// it is closed here.
func readUsageDayForRollup(day string) (rows []usagex.Record, closed bool) {
	usagex.Mu.Lock()
	defer usagex.Mu.Unlock()
	if day >= time.Now().UTC().Format("2006-01-02") {
		return nil, false
	}
	return usagex.ReadDay(day), true
}

// rebuildUsageRollupsLocked rebuilds the rollup when the version goes up. Requires
// usageRollupMu.
//
// An aggregate that has already been folded is summed, and individual rows cannot be
// subtracted from it afterwards. Since a v1 rollup may hold duplicates originating in the
// fold's crash window, rebuilding is the only way to drop them. Only the part whose
// contributing raw is still on disk can be rebuilt, so:
//
//   - every folded file day is still there (nothing has been pruned yet): throw the month
//     files away and redo everything. The caller's loop folds them again as it is.
//   - even one is already pruned: do not rebuild. Losing the aggregate of the raw that is gone
//     weighs more than duplicates that may still be in it. Instead, restore only the dedup
//     watermark from the folded files that are still visible, so later duplicates can be
//     dropped.
//
// This feature is new enough (the rollup is not yet older than raw's retention window) that in
// practice the first branch is taken.
func rebuildUsageRollupsLocked(st usageRollupState) usageRollupState {
	onDisk := map[string]bool{}
	for _, d := range usagex.RawDays() {
		onDisk[d] = true
	}
	pruned := 0
	for fileDay := range st.Rolled {
		if !onDisk[fileDay] {
			pruned++
		}
	}
	if pruned > 0 {
		log.Printf("usage: rollup v%d: not rebuilding, %d contributing raw days are already pruned"+
			" (the existing aggregate may keep duplicates; later duplicates are dropped)", usageRollupVersion, pruned)
		fresh := usageRollupState{Version: usageRollupVersion, Rolled: st.Rolled, Folded: usageDedupIndex{}}
		for _, fileDay := range usagex.RawDays() { // ascending, i.e. append order
			if _, done := st.Rolled[fileDay]; !done {
				continue // the unfolded days are handled by the caller's loop with the same index
			}
			for _, r := range usagex.ReadDay(fileDay) {
				fresh.Folded.accept(r, usageRowTime(r, fileDay))
			}
		}
		return fresh
	}
	ents, _ := os.ReadDir(usageRollupDir())
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || filepath.Ext(n) != ".json" || n == "state.json" {
			continue
		}
		_ = os.Remove(filepath.Join(usageRollupDir(), n))
	}
	if len(st.Rolled) > 0 {
		log.Printf("usage: rebuilding the rollup to v%d (re-folding %d file days from raw)",
			usageRollupVersion, len(st.Rolled))
	}
	return usageRollupState{Version: usageRollupVersion, Rolled: map[string]usageRolledFile{}, Folded: usageDedupIndex{}}
}

// rolledUpEntries returns the per-consumption-day aggregate for the given period (the part
// the rollup is authoritative for).
func rolledUpEntries(from, to time.Time) map[string][]usageRollupEntry {
	out := map[string][]usageRollupEntry{}
	last := to.UTC().Format("2006-01")
	for month := from.UTC().Format("2006-01"); month <= last; month = nextMonth(month) {
		for day, d := range readUsageRollup(month).Days {
			out[day] = d.Entries
		}
	}
	return out
}

func nextMonth(month string) string {
	t, err := time.Parse("2006-01", month)
	if err != nil {
		return "9999-12" // do not keep the loop spinning on broken input
	}
	return t.AddDate(0, 1, 0).Format("2006-01")
}
