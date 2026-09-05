package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// writeUsageDay writes a given day's raw file directly (so "yesterday or earlier" can be
// built without moving the clock).
func writeUsageDay(t *testing.T, day string, rows ...usagex.Record) {
	t.Helper()
	dir := usagex.RawDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, day+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			t.Fatal(err)
		}
	}
}

func row(call, feature, kind, model string, spend int) usagex.Record {
	return usagex.Record{
		Call: call, Feature: feature, Kind: kind, Model: model, ModelSrc: usagex.ModelReported,
		Trigger: usagex.TriggerUser, In: spend, Spend: spend, OK: true, Measured: usagex.MeasuredExact,
	}
}

// at puts the row's consumption time at 12:00 on the given day (buckets are cut by ts).
func at(r usagex.Record, day string) usagex.Record {
	r.TS = day + "T12:00:00Z"
	return r
}

func daysAgo(n int) string { return time.Now().UTC().AddDate(0, 0, -n).Format("2006-01-02") }

// One claude call splits into per-model rows. Counting rows inflates the number of calls, so
// they are counted as distinct calls (docs/log/46 §4). This also checks that the total holds
// along whichever axis it is summed.
func TestAggregateUsageRowsCountsDistinctCalls(t *testing.T) {
	rows := []usagex.Record{
		row("c1", usagex.FeatureTitleSession, session.KindClaude, "claude-haiku-4-5", 100),
		row("c1", usagex.FeatureTitleSession, session.KindClaude, "claude-sonnet-4-6", 50), // second model of the same call
		row("c2", usagex.FeatureTitleSession, session.KindClaude, "claude-haiku-4-5", 20),
	}
	agg := aggregateUsageRows(rows, map[string]bool{})
	if len(agg) != 2 {
		t.Fatalf("keys = %d, want 2 (one per model)", len(agg))
	}
	total, calls := 0, 0
	for _, a := range agg {
		total += a.Spend
		calls += a.Calls
	}
	if total != 170 {
		t.Fatalf("spend total = %d, want 170", total)
	}
	if calls != 2 {
		t.Fatalf("calls total = %d, want 2 (distinct calls, not the 3 rows)", calls)
	}
}

// Only completed days are folded. Today stays raw, since rows are still being added.
func TestEnsureUsageRollupsOnlyFoldsCompletedDays(t *testing.T) {
	useIsolatedUsageDir(t)
	yesterday, today := daysAgo(1), daysAgo(0)
	writeUsageDay(t, yesterday, at(row("a", usagex.FeatureTitleSession, session.KindClaude, "haiku", 10), yesterday))
	writeUsageDay(t, today, at(row("b", usagex.FeatureTitleSession, session.KindClaude, "haiku", 20), today))

	ensureUsageRollups()
	m := readUsageRollup(yesterday[:7])
	if _, ok := m.Days[yesterday]; !ok {
		t.Fatalf("yesterday was not folded: %+v", m.Days)
	}
	if _, ok := m.Days[today]; ok {
		t.Fatal("today was folded (rows are still being added to it)")
	}

	// Idempotent: a second pass changes nothing.
	before, _ := json.Marshal(readUsageRollup(yesterday[:7]))
	ensureUsageRollups()
	after, _ := json.Marshal(readUsageRollup(yesterday[:7]))
	if string(before) != string(after) {
		t.Fatalf("the second pass changed the rollup:\n%s\n%s", before, after)
	}
}

// The rollup stays after raw expires out of its retention window (kept forever, ADR0029 §7-4).
func TestRollupSurvivesRawPrune(t *testing.T) {
	dir := useIsolatedUsageDir(t)
	old := daysAgo(2)
	writeUsageDay(t, old, at(row("a", usagex.FeatureSession, session.KindClaude, "haiku", 500), old))
	ensureUsageRollups()
	if err := os.Remove(filepath.Join(dir, "raw", old+".jsonl")); err != nil {
		t.Fatal(err)
	}
	entries := readUsageRollup(old[:7]).Days[old].Entries
	if len(entries) != 1 || entries[0].Agg.Spend != 500 {
		t.Fatalf("aggregate after deleting raw = %+v", entries)
	}
	// It must be visible through a query too (a day the rollup owns does not read raw).
	samples, _ := collectUsageSamples(time.Now().UTC().AddDate(0, 0, -3), time.Now().UTC(), "day")
	sum := 0
	for _, s := range samples {
		sum += s.Agg.Spend
	}
	if sum != 500 {
		t.Fatalf("spend through the query = %d, want 500", sum)
	}
}

// A folded day is not read from raw as well (regression for the state where both the rollup
// and raw are present).
func TestCollectUsageSamplesDoesNotDoubleCount(t *testing.T) {
	useIsolatedUsageDir(t)
	yesterday := daysAgo(1)
	writeUsageDay(t, yesterday, at(row("a", usagex.FeatureTitleSession, session.KindClaude, "haiku", 300), yesterday))
	ensureUsageRollups() // raw is left in place
	samples, _ := collectUsageSamples(time.Now().UTC().AddDate(0, 0, -2), time.Now().UTC(), "day")
	sum, calls := 0, 0
	for _, s := range samples {
		sum += s.Agg.Spend
		calls += s.Agg.Calls
	}
	if sum != 300 || calls != 1 {
		t.Fatalf("spend = %d / calls = %d, want 300 / 1 (double counted across rollup and raw?)", sum, calls)
	}
}

// Regression for the first gap hit on a real machine. A session-fold backfill writes months of
// past rows into today's raw file at once. Cutting buckets by the file day appended to piles
// every past consumption onto the introduction day and makes the series meaningless. Cut by
// the row's ts.
func TestUsageSeriesBucketsByConsumptionTimeNotFileDay(t *testing.T) {
	useIsolatedUsageDir(t)
	today := daysAgo(0)
	old1, old2 := daysAgo(40), daysAgo(41)
	// All three rows are appended to today's file, but the consumption happened on other days.
	writeUsageDay(t, today,
		at(row("c1", usagex.FeatureSession, session.KindClaude, "haiku", 100), old1),
		at(row("c2", usagex.FeatureSession, session.KindClaude, "haiku", 200), old2),
		at(row("c3", usagex.FeatureSession, session.KindClaude, "haiku", 300), today),
	)
	got := getSeries(t, "from="+old2+"&to="+today)
	hit := nonEmptyBuckets(got)
	if len(hit) != 3 {
		t.Fatalf("buckets with consumption = %d, want 3 (they should split per consumption day): %+v", len(hit), got.Buckets)
	}
	// The period comes back zero-filled (the 41 day gap does not vanish from the picture).
	if len(got.Buckets) != 42 {
		t.Fatalf("buckets = %d, want 42 (zero-filled from %s to %s)", len(got.Buckets), old2, today)
	}
	want := map[string]int{
		old2 + "T00:00:00Z":  200,
		old1 + "T00:00:00Z":  100,
		today + "T00:00:00Z": 300,
	}
	for _, b := range hit {
		if b.Series[usagex.FeatureSession].Spend != want[b.T] {
			t.Fatalf("bucket %s = %d, want %d", b.T, b.Series[usagex.FeatureSession].Spend, want[b.T])
		}
	}
	if got.Totals.Spend != 600 {
		t.Fatalf("totals = %+v", got.Totals)
	}
}

// The same shape as above: the consumption day survives folding (the rollup key is the day of
// the row's ts).
func TestRollupKeysByConsumptionDay(t *testing.T) {
	useIsolatedUsageDir(t)
	fileDay, consumed := daysAgo(1), daysAgo(30)
	writeUsageDay(t, fileDay, at(row("c1", usagex.FeatureSession, session.KindClaude, "haiku", 700), consumed))
	ensureUsageRollups()
	m := readUsageRollup(consumed[:7])
	day, ok := m.Days[consumed]
	if !ok {
		t.Fatalf("no key for consumption day %s: %+v", consumed, m.Days)
	}
	if len(day.Entries) != 1 || day.Entries[0].Agg.Spend != 700 {
		t.Fatalf("entries = %+v", day.Entries)
	}
	if len(day.Src) != 1 || day.Src[0] != fileDay {
		t.Fatalf("the contributing file day was not recorded: %+v", day.Src)
	}
	// A retry does not add again (Src rejects it, so a rerun after a crash is safe).
	if merged, ok := mergeRollupDay(day, fileDay, map[usageKey]usageAgg{{Kind: "x"}: {Spend: 1}}); ok {
		t.Fatalf("the same file day was added twice: %+v", merged)
	}
}

func TestParseUsageFilter(t *testing.T) {
	f, bad := parseUsageFilter("kind:claude,feature:title.*")
	if bad != "" {
		t.Fatalf("bad = %q", bad)
	}
	// different axes are ANDed
	if !f.match(usageKey{Kind: "claude", Feature: "title.session"}) {
		t.Fatal("claude AND title.* did not match")
	}
	if f.match(usageKey{Kind: "codex", Feature: "title.session"}) {
		t.Fatal("matched even though the kind differs")
	}
	if f.match(usageKey{Kind: "claude", Feature: "compact"}) {
		t.Fatal("matched even though the feature differs")
	}
	// the same axis is ORed
	f2, _ := parseUsageFilter("kind:claude,kind:codex")
	if !f2.match(usageKey{Kind: "codex"}) || !f2.match(usageKey{Kind: "claude"}) {
		t.Fatal("several values on one axis are not ORed")
	}
	if f2.match(usageKey{Kind: "cursor"}) {
		t.Fatal("a value outside the enumeration matched")
	}
	// an unknown axis is an error (ignoring it silently gives "I set it and it does nothing")
	if _, bad := parseUsageFilter("nope:1"); bad != "nope:1" {
		t.Fatalf("an unknown axis was not rejected: %q", bad)
	}
}

// nonEmptyBuckets returns only the buckets carrying real data. The response zero-fills the
// requested period (dropping the empty buckets draws days that are far apart as adjacent bars
// and makes the time axis unreadable), so a test asking "how many days had consumption" uses
// this.
func nonEmptyBuckets(r usageSeriesResp) []usageBucketWire {
	var out []usageBucketWire
	for _, b := range r.Buckets {
		if len(b.Series) > 0 {
			out = append(out, b)
		}
	}
	return out
}

func getSeries(t *testing.T, query string) usageSeriesResp {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/usage/series?"+query, nil)
	rec := httptest.NewRecorder()
	handleUsageSeries(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got usageSeriesResp
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v — body=%s", err, rec.Body.String())
	}
	return got
}

func TestUsageSeriesAggregation(t *testing.T) {
	useIsolatedUsageDir(t)
	day := daysAgo(1)
	writeUsageDay(t, day,
		at(row("c1", usagex.FeatureTitleSession, session.KindClaude, "claude-haiku-4-5", 100), day),
		at(row("c2", usagex.FeatureAssistantChat, session.KindClaude, "claude-sonnet-4-6", 900), day),
		at(row("c3", usagex.FeatureSession, session.KindCodex, "", 5000), day),
		// a CLI that reports neither model nor tokens: only the number of calls is counted
		usagex.Record{TS: day + "T12:00:00Z", Call: "c4", Feature: usagex.FeatureTitleSession,
			Kind: session.KindAgy, ModelSrc: usagex.ModelUnknown, OK: true, Measured: usagex.MeasuredNone},
	)

	got := getSeries(t, "from="+day+"&to="+day+"&by=feature")
	if len(got.Buckets) != 1 || got.Buckets[0].T != day+"T00:00:00Z" {
		t.Fatalf("buckets = %+v", got.Buckets)
	}
	if got.Totals.Spend != 6000 || got.Totals.Calls != 4 {
		t.Fatalf("totals = %+v", got.Totals)
	}
	s := got.Buckets[0].Series
	if s[usagex.FeatureSession].Spend != 5000 || s[usagex.FeatureAssistantChat].Spend != 900 {
		t.Fatalf("series = %+v", s)
	}
	if s[usagex.FeatureTitleSession].Calls != 2 { // claude 1 + agy 1
		t.Fatalf("title.session calls = %d", s[usagex.FeatureTitleSession].Calls)
	}
	// Do not confuse "0" with "unmeasured".
	if got.UnmeasuredCalls != 1 {
		t.Fatalf("unmeasured_calls = %d, want 1", got.UnmeasuredCalls)
	}
	// coverage is generated from the data (a hand-written table drifts).
	if got.Coverage[session.KindClaude].Tokens != usagex.MeasuredExact ||
		got.Coverage[session.KindClaude].Model != usagex.ModelReported {
		t.Fatalf("claude coverage = %+v", got.Coverage[session.KindClaude])
	}
	if got.Coverage[session.KindAgy].Tokens != usagex.MeasuredNone ||
		got.Coverage[session.KindAgy].Model != "none" {
		t.Fatalf("agy coverage = %+v", got.Coverage[session.KindAgy])
	}

	// include=aux drops the session bodies (§9-3's "include it and narrow with a filter" shape).
	aux := getSeries(t, "from="+day+"&to="+day+"&include=aux")
	if aux.Totals.Spend != 1000 {
		t.Fatalf("include=aux totals = %+v", aux.Totals)
	}
	only := getSeries(t, "from="+day+"&to="+day+"&include=session")
	if only.Totals.Spend != 5000 {
		t.Fatalf("include=session totals = %+v", only.Totals)
	}

	// The feature x model table (the view this is really for).
	mx := getSeries(t, "from="+day+"&to="+day+"&by=feature&split=model")
	if mx.Matrix[usagex.FeatureAssistantChat]["claude-sonnet-4-6"].Spend != 900 {
		t.Fatalf("matrix = %+v", mx.Matrix)
	}
	if _, ok := mx.Matrix[usagex.FeatureTitleSession][""]; !ok {
		t.Fatalf("the unknown-model (agy) slot is missing from the table: %+v", mx.Matrix[usagex.FeatureTitleSession])
	}

	// Filter (prefix match).
	f := getSeries(t, "from="+day+"&to="+day+"&filter=kind:claude")
	if f.Totals.Spend != 1000 || len(f.Coverage) != 1 {
		t.Fatalf("filter=kind:claude → totals %+v coverage %+v", f.Totals, f.Coverage)
	}
}

func TestUsageSeriesHourBucket(t *testing.T) {
	useIsolatedUsageDir(t)
	day := daysAgo(0) // today stays raw, so hour granularity is available
	r1 := row("c1", usagex.FeatureAssistantChat, session.KindClaude, "haiku", 10)
	r1.TS = day + "T01:30:00Z"
	r2 := row("c2", usagex.FeatureAssistantChat, session.KindClaude, "haiku", 20)
	r2.TS = day + "T01:45:00Z"
	r3 := row("c3", usagex.FeatureAssistantChat, session.KindClaude, "haiku", 40)
	r3.TS = day + "T05:00:00Z"
	writeUsageDay(t, day, r1, r2, r3)

	got := getSeries(t, "from="+day+"&to="+day+"&bucket=hour")
	hit := nonEmptyBuckets(got)
	if len(hit) != 2 {
		t.Fatalf("buckets with consumption = %+v", got.Buckets)
	}
	if hit[0].T != day+"T01:00:00Z" || hit[0].Series[usagex.FeatureAssistantChat].Spend != 30 {
		t.Fatalf("bucket0 = %+v", hit[0])
	}
	if hit[1].T != day+"T05:00:00Z" {
		t.Fatalf("bucket1 = %+v", hit[1])
	}
	// One day of hour buckets is zero-filled to 24 (the idle hours do not vanish from the picture).
	if len(got.Buckets) != 24 {
		t.Fatalf("buckets = %d, want 24 (zero-filled)", len(got.Buckets))
	}
	if got.Totals.Spend != 70 {
		t.Fatalf("totals = %+v", got.Totals)
	}
}

// Asking for a folded day at hour granularity must say truncated, not "there was no
// consumption".
func TestUsageSeriesHourReportsTruncationAfterPrune(t *testing.T) {
	dir := useIsolatedUsageDir(t)
	old := daysAgo(2)
	writeUsageDay(t, old, at(row("a", usagex.FeatureAssistantChat, session.KindClaude, "haiku", 100), old))
	ensureUsageRollups()
	if err := os.Remove(filepath.Join(dir, "raw", old+".jsonl")); err != nil {
		t.Fatal(err)
	}
	got := getSeries(t, "from="+old+"&to="+old+"&bucket=hour")
	if !got.Truncated {
		t.Fatal("asked for hour granularity over a period whose raw is gone, but truncated is not set")
	}
	if hit := nonEmptyBuckets(got); len(hit) != 0 {
		t.Fatalf("buckets with consumption came back: %+v", hit)
	}
}

func TestUsageSeriesRejectsBadParams(t *testing.T) {
	useIsolatedUsageDir(t)
	for _, q := range []string{
		"bucket=week", "by=nope", "split=nope", "filter=nope:1",
		"from=2026-13-99", "to=nope", "from=2026-07-10&to=2026-07-01", "include=nothing",
	} {
		req := httptest.NewRequest(http.MethodGet, "/usage/series?"+q, nil)
		rec := httptest.NewRecorder()
		handleUsageSeries(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%q: status = %d, want 400 (body=%s)", q, rec.Code, rec.Body.String())
		}
	}
}

// The aggregation API returns no raw log. Bodies were never recorded in the first place, and
// session names and conversation ids do not leave the bucket either.
func TestUsageSeriesDoesNotLeakRefs(t *testing.T) {
	useIsolatedUsageDir(t)
	day := daysAgo(0)
	r := at(row("c1", usagex.FeatureTitleSession, session.KindClaude, "haiku", 10), day)
	r.Ref = "slot-secret"
	writeUsageDay(t, day, r)
	req := httptest.NewRequest(http.MethodGet, "/usage/series?from="+day+"&to="+day, nil)
	rec := httptest.NewRecorder()
	handleUsageSeries(rec, req)
	if body := rec.Body.String(); strings.Contains(body, "slot-secret") {
		t.Fatalf("the ref leaked into the response: %s", body)
	}
}

// The session fold is asynchronous, so a response sent while it runs does not include the most
// recent turns. That fact is declared through folding. Returning stale numbers silently makes
// the Console draw them as current, and the user hammers "refresh" until it catches up (this
// was an actual complaint).
func TestUsageSeriesReportsFolding(t *testing.T) {
	useIsolatedUsageDir(t)
	resetUsageFold(t)
	day := daysAgo(0)

	// The first read starts the fold, i.e. this response has not caught up yet.
	if got := getSeries(t, "from="+day+"&to="+day); !got.Folding {
		t.Fatal("the response that started the fold did not set folding")
	}
	waitUsageFoldIdle(t)

	// A read after the fold has finished hits the throttle, i.e. nothing is running. Keeping
	// folding set here never lets the Console stop re-fetching.
	if got := getSeries(t, "from="+day+"&to="+day); got.Folding {
		t.Fatal("folding is set even though the fold has finished")
	}

	// An explicit refresh (fold=force) skips the throttle and always runs.
	if got := getSeries(t, "from="+day+"&to="+day+"&fold=force"); !got.Folding {
		t.Fatal("fold=force hit the throttle and did not start")
	}
	waitUsageFoldIdle(t)
}

// --- P2/P3 review regressions --------------------------------------------------

// modelRow is one row of a claude call that was split into per-model rows.
func modelRow(call, modelRaw, model string, spend int) usagex.Record {
	r := row(call, usagex.FeatureAssistantChat, session.KindClaude, model, spend)
	r.ModelRaw = modelRaw
	return r
}

// P2-5: the call count goes on the model row that consumed the most in that call. The order of
// the rows is only the spelling order of the raw id, so counting the first row shows the
// dominant model with calls=0.
func TestCallsGoToTheDominantModelRow(t *testing.T) {
	day := daysAgo(0)
	rows := []usagex.Record{
		// a-model comes first in spelling order, but z-model is what actually consumed.
		at(modelRow("c1", "a-model-20260101", "a-model", 100), day),
		at(modelRow("c1", "z-model-20260101", "z-model", 9000), day),
	}
	agg := aggregateUsageRows(rows, map[string]bool{})
	byModel := map[string]usageAgg{}
	calls := 0
	for k, a := range agg {
		byModel[k.Model] = a
		calls += a.Calls
	}
	if calls != 1 {
		t.Fatalf("calls total = %d, want 1 (distinct call)", calls)
	}
	if byModel["z-model"].Calls != 1 || byModel["a-model"].Calls != 0 {
		t.Fatalf("the count is not attached to the dominant model: %+v", byModel)
	}
	// Ties are decided deterministically (same input, same attribution, so the aggregate
	// reproduces).
	tie := []usagex.Record{
		at(modelRow("c2", "b-raw", "b", 50), day),
		at(modelRow("c2", "a-raw", "a", 50), day),
	}
	for i := 0; i < 3; i++ {
		got := aggregateUsageRows(tie, map[string]bool{})
		for k, a := range got {
			if a.Calls == 1 && k.Model != "a" {
				t.Fatalf("the tie's representative came out as model=%q (it should be decided by ascending raw id)", k.Model)
			}
		}
	}
}

// The same thing through the API: with `by=model` the dominant model must not look like calls 0.
func TestUsageSeriesCallsFollowDominantModel(t *testing.T) {
	useIsolatedUsageDir(t)
	day := daysAgo(0)
	writeUsageDay(t, day,
		at(modelRow("c1", "a-model-20260101", "a-model", 100), day),
		at(modelRow("c1", "z-model-20260101", "z-model", 9000), day),
	)
	got := getSeries(t, "from="+day+"&to="+day+"&by=model")
	series := nonEmptyBuckets(got)[0].Series
	if series["z-model"].Calls != 1 || series["a-model"].Calls != 0 {
		t.Fatalf("by=model calls = %+v", series)
	}
	if got.Totals.Calls != 1 || got.Totals.Spend != 9100 {
		t.Fatalf("totals = %+v, want calls 1 / spend 9100", got.Totals)
	}
}

// P2-4: if a month file could not be written, the state is not advanced. Advancing it leaves
// the consumption that should have gone into that month marked folded and gone from the
// aggregate, never to come back once raw is pruned.
func TestRollupKeepsStateWhenMonthWriteFails(t *testing.T) {
	useIsolatedUsageDir(t)
	day := daysAgo(1)
	writeUsageDay(t, day, at(row("c1", usagex.FeatureSession, session.KindClaude, "haiku", 500), day))
	// Block the month file's path with a directory (the rename then always fails).
	blocked := usageRollupPath(day[:7])
	if err := os.MkdirAll(filepath.Join(blocked, "x"), 0o700); err != nil {
		t.Fatal(err)
	}

	ensureUsageRollups()
	if st := readUsageRollupState(); len(st.Rolled) != 0 {
		t.Fatalf("marked folded even though the month file was not written: %+v", st.Rolled)
	}

	// Removing the block lets the next pass fold again (what was missed is recovered).
	if err := os.RemoveAll(blocked); err != nil {
		t.Fatal(err)
	}
	ensureUsageRollups()
	entries := readUsageRollup(day[:7]).Days[day].Entries
	if len(entries) != 1 || entries[0].Agg.Spend != 500 {
		t.Fatalf("aggregate after recovery = %+v, want spend 500", entries)
	}
	if _, ok := readUsageRollupState().Rolled[day]; !ok {
		t.Fatal("not recorded as folded after recovery")
	}
}

// P3-12: appending and folding hold separate locks, so an append that picked its day just
// before the UTC date boundary can land after the fold. The folding side holds usageMu and
// checks that the day can no longer grow before reading it.
func TestRollupRefusesToReadTheOpenDay(t *testing.T) {
	useIsolatedUsageDir(t)
	today, yesterday := daysAgo(0), daysAgo(1)
	writeUsageDay(t, today, at(row("c1", usagex.FeatureSession, session.KindClaude, "haiku", 10), today))
	writeUsageDay(t, yesterday, at(row("c2", usagex.FeatureSession, session.KindClaude, "haiku", 20), yesterday))

	if rows, closed := readUsageDayForRollup(today); closed || len(rows) != 0 {
		t.Fatalf("today's file was read as a fold target (rows=%d closed=%v)", len(rows), closed)
	}
	rows, closed := readUsageDayForRollup(yesterday)
	if !closed || len(rows) != 1 {
		t.Fatalf("a completed day was not read (rows=%d closed=%v)", len(rows), closed)
	}
}

// P3-9: days with no consumption come back zero-filled too. Dropping them turns "two days far
// apart" into adjacent bars and the empty stretch vanishes from the picture.
func TestUsageSeriesFillsEmptyBuckets(t *testing.T) {
	useIsolatedUsageDir(t)
	from, gap, to := daysAgo(4), daysAgo(2), daysAgo(0)
	writeUsageDay(t, to,
		at(row("c1", usagex.FeatureSession, session.KindClaude, "haiku", 100), from),
		at(row("c2", usagex.FeatureSession, session.KindClaude, "haiku", 200), to),
	)
	got := getSeries(t, "from="+from+"&to="+to)
	if len(got.Buckets) != 5 {
		t.Fatalf("buckets = %d, want 5 (every day from %s to %s)", len(got.Buckets), from, to)
	}
	if len(nonEmptyBuckets(got)) != 2 {
		t.Fatalf("buckets with consumption = %+v", nonEmptyBuckets(got))
	}
	var mid *usageBucketWire
	for i, b := range got.Buckets {
		if b.T == gap+"T00:00:00Z" {
			mid = &got.Buckets[i]
		}
	}
	if mid == nil || len(mid.Series) != 0 {
		t.Fatalf("the empty day did not keep its position: %+v", got.Buckets)
	}
	// Filling does not move the totals.
	if got.Totals.Spend != 300 || got.Totals.Calls != 2 {
		t.Fatalf("totals = %+v", got.Totals)
	}
}

// At a density where filling is pointless (90 days x hour) it is not filled. It stops filling
// rather than truncating, so all the real data always comes back.
func TestUsageSeriesDoesNotFillAbsurdRanges(t *testing.T) {
	useIsolatedUsageDir(t)
	day := daysAgo(0)
	writeUsageDay(t, day, at(row("c1", usagex.FeatureSession, session.KindClaude, "haiku", 10), day))
	got := getSeries(t, "from="+daysAgo(89)+"&to="+day+"&bucket=hour")
	if len(got.Buckets) != 1 {
		t.Fatalf("buckets = %d, want 1 (a density past the limit is not filled)", len(got.Buckets))
	}
	if got.Totals.Spend != 10 {
		t.Fatalf("totals = %+v", got.Totals)
	}
}
