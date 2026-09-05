package main

// GET /usage/series — the per-feature usage time series (docs/log/46 §4 / ADR0029 §8).
//
// Aggregated on the server (raw logs never reach the Console). Raw rows carry session names and
// conversation ids, so aggregating before returning is a privacy requirement, not just a matter
// of shape.
//
// Read order is rollup → raw. For an already folded day the rollup is the only truth, so the
// same consumption is never counted twice (usage_rollup.go).
//
// A new REST route has to be registered in both workspace/agent/routes.go and
// control-plane/routes.go (the CP uses an explicit allowlist; miss one side and the Console
// gets a 404 while the backend is healthy).

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// The vocabulary of aggregation dimensions. by / split / filter accept these keys and no
// others (letting an unknown dimension through silently produces "I specified it and nothing
// happened").
const (
	usageDimFeature    = "feature"
	usageDimKind       = "kind"
	usageDimModel      = "model"
	usageDimTrigger    = "trigger"
	usageDimOrigin     = "origin"
	usageDimOriginConv = "origin_conv"
	usageDimVerb       = "verb"
	usageDimModelSrc   = "model_src"
	usageDimMeasured   = "measured"
)

// usageDimValue takes one dimension's value out of the dimension tuple. Reject unknown
// dimensions with validUsageDim before calling, so they fail rather than read as "".
func usageDimValue(k usageKey, dim string) string {
	switch dim {
	case usageDimFeature:
		return k.Feature
	case usageDimKind:
		return k.Kind
	case usageDimModel:
		return k.Model
	case usageDimTrigger:
		return k.Trigger
	case usageDimOrigin:
		return k.Origin
	case usageDimOriginConv:
		return k.OriginConv
	case usageDimVerb:
		return k.Verb
	case usageDimModelSrc:
		return k.ModelSrc
	case usageDimMeasured:
		return k.Measured
	}
	return ""
}

func validUsageDim(dim string) bool {
	switch dim {
	case usageDimFeature, usageDimKind, usageDimModel, usageDimTrigger, usageDimOrigin,
		usageDimOriginConv, usageDimVerb, usageDimModelSrc, usageDimMeasured:
		return true
	}
	return false
}

// usageFilter maps dim -> patterns. The same dimension ORs, different dimensions AND
// (filter=kind:claude,kind:codex is "claude or codex"; filter=kind:claude,feature:title.* is
// "claude and the title family"). Only a trailing * is treated as a prefix match.
type usageFilter map[string][]string

func parseUsageFilter(s string) (usageFilter, string) {
	f := usageFilter{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		dim, pat, ok := strings.Cut(part, ":")
		if !ok || !validUsageDim(dim) {
			return nil, part
		}
		f[dim] = append(f[dim], pat)
	}
	return f, ""
}

func (f usageFilter) match(k usageKey) bool {
	for dim, pats := range f {
		v := usageDimValue(k, dim)
		hit := false
		for _, p := range pats {
			if strings.HasSuffix(p, "*") {
				hit = strings.HasPrefix(v, strings.TrimSuffix(p, "*"))
			} else {
				hit = v == p
			}
			if hit {
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

type usageBucketWire struct {
	T      string              `json:"t"`
	Series map[string]usageAgg `json:"series"`
}

// usageCoverage is a kind's self-report of what it reports and how far. Generated from the
// data (a hand-written table drifts) — the UI's "not measured" banner is written from this.
type usageCoverage struct {
	Tokens string `json:"tokens"` // exact | partial | none
	Model  string `json:"model"`  // reported | requested | none
}

type usageSeriesResp struct {
	From    string            `json:"from"`
	To      string            `json:"to"`
	Bucket  string            `json:"bucket"`
	By      string            `json:"by"`
	Split   string            `json:"split,omitempty"`
	Buckets []usageBucketWire `json:"buckets"`
	Totals  usageAgg          `json:"totals"`
	// Matrix is present only when split is given (a table such as feature x model).
	Matrix          map[string]map[string]usageAgg `json:"matrix,omitempty"`
	Coverage        map[string]usageCoverage       `json:"coverage"`
	UnmeasuredCalls int                            `json:"unmeasured_calls"`
	// PricedSpend / UnpricedSpend report how much of the consumption the estimate could be
	// derived from (usage_price.go). Showing only the estimate would present a total that
	// silently omits models missing from the price table as "API-equivalent cost".
	PricedSpend   int `json:"priced_spend"`
	UnpricedSpend int `json:"unpriced_spend"`
	// Prices holds the unit prices used for the models appearing in this response (model name ->
	// effective price and its source). Checking an amount needs to know which price produced it.
	// Not carried on the aggregates (folding on a dimension would mix them).
	Prices map[string]usagePriceWire `json:"prices,omitempty"`
	// Catalog is the catalog's self-report (omitted when there is none).
	Catalog *usageCatalogMeta `json:"catalog,omitempty"`
	// Truncated says part of the requested range is older than the raw retention and could not
	// be reconstructed at hour granularity. Returning a shorter series silently would look like
	// "there was no consumption in that period".
	Truncated bool `json:"truncated,omitempty"`
	// Folding says the session-body fold is running as of this read, i.e. this response may not
	// include the most recent turns yet. The Console re-fetches until it clears.
	Folding bool `json:"folding,omitempty"`
}

// usagePriceWire is prices[model] in the response. The value is the effective unit price (after
// the multiplier has filled in cache prices the source does not carry), so the amount shown on
// screen can be checked as is.
type usagePriceWire struct {
	Src        string  `json:"src"` // builtin | catalog:<provider>/<model>
	In         float64 `json:"in"`  // $/1M
	Out        float64 `json:"out"` // $/1M
	CacheRead  float64 `json:"cread"`
	CacheWrite float64 `json:"cwrite"`
	// Ambiguous means the same model name is priced differently depending on kind (codex
	// converts gpt-5.6-luna at openai list price, opencode at gateway price). Only one price
	// fits in a row, so at least say that they differ.
	Ambiguous bool `json:"ambiguous,omitempty"`
	// spend is the consumption of the side taken as representative (the larger one — making the
	// smaller side's price representative makes the table's amount and price look inconsistent).
	spend int
}

// usageSample is the unit of aggregation input (one rollup entry, or raw rows folded within a
// bucket).
type usageSample struct {
	T   string
	Key usageKey
	Agg usageAgg
}

func handleUsageSeries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	bucket := q.Get("bucket")
	if bucket == "" {
		bucket = "day"
	}
	if bucket != "day" && bucket != "hour" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_bucket", "bucket must be day or hour")
		return
	}
	to, dateOnly, err := parseUsageTime(q.Get("to"), time.Now().UTC())
	if err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_to", "to must be RFC3339 or YYYY-MM-DD")
		return
	}
	if dateOnly {
		// A to given as a bare date means "through the end of that day". Reading it as
		// midnight makes a from=to=today hour query empty, which is almost never intended.
		to = to.AddDate(0, 0, 1).Add(-time.Nanosecond)
	}
	from, _, err := parseUsageTime(q.Get("from"), to.AddDate(0, 0, -7))
	if err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_from", "from must be RFC3339 or YYYY-MM-DD")
		return
	}
	if from.After(to) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_range", "from must not be after to")
		return
	}
	by := q.Get("by")
	if by == "" {
		by = usageDimFeature
	}
	if !validUsageDim(by) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_by", "unknown by dimension: "+by)
		return
	}
	split := q.Get("split")
	if split != "" && !validUsageDim(split) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_split", "unknown split dimension: "+split)
		return
	}
	filter, badPart := parseUsageFilter(q.Get("filter"))
	if badPart != "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_filter", "bad filter term: "+badPart)
		return
	}
	inclSession, inclAux := parseUsageInclude(q.Get("include"))
	if !inclSession && !inclAux {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_include", "include must name session and/or aux")
		return
	}

	// fold-on-read (docs/log/46 §3-b): take this request for a series as the occasion to fold
	// the session bodies. It is asynchronous, so this response does not wait for it (the most
	// recent turns land on the next read). Saying nothing about not waiting produces a screen
	// that only goes current after a few presses of refresh, so folding is set while it runs
	// and the Console re-fetches once it finishes.
	// fold=force skips the 60s throttle (only on the path where the user pressed refresh).
	folding := startFoldSessionUsage(q.Get("fold") == "force")

	samples, truncated := collectUsageSamples(from, to, bucket)
	resp := usageSeriesResp{
		From: from.UTC().Format(time.RFC3339), To: to.UTC().Format(time.RFC3339),
		Bucket: bucket, By: by, Split: split,
		Coverage: map[string]usageCoverage{}, Truncated: truncated, Folding: folding,
	}
	byBucket := map[string]map[string]usageAgg{}
	var order []string
	for _, s := range samples {
		if !filter.match(s.Key) {
			continue
		}
		isSession := s.Key.Feature == usagex.FeatureSession
		if (isSession && !inclSession) || (!isSession && !inclAux) {
			continue
		}
		if _, ok := byBucket[s.T]; !ok {
			byBucket[s.T] = map[string]usageAgg{}
			order = append(order, s.T)
		}
		// The estimate is attached here and never stored. The sample still carries the model
		// dimension, and this point, before folding, is the only one where the sums still add
		// up whichever dimension is folded on. The price varies by kind too (the same model
		// may have gone through a different provider), so look it up here, before kind is
		// dropped.
		agg := s.Agg
		if est, src, priced := usageEstCostUSD(s.Key.Kind, s.Key.Model, agg); priced {
			agg.CostEstUSD = est
			resp.PricedSpend += agg.Spend
			resp.notePrice(s.Key, src, agg.Spend)
		} else {
			resp.UnpricedSpend += agg.Spend
		}
		k := usageDimValue(s.Key, by)
		a := byBucket[s.T][k]
		a.add(agg)
		byBucket[s.T][k] = a

		resp.Totals.add(agg)

		if split != "" {
			if resp.Matrix == nil {
				resp.Matrix = map[string]map[string]usageAgg{}
			}
			if _, ok := resp.Matrix[k]; !ok {
				resp.Matrix[k] = map[string]usageAgg{}
			}
			sv := usageDimValue(s.Key, split)
			m := resp.Matrix[k][sv]
			m.add(agg)
			resp.Matrix[k][sv] = m
		}
		if s.Key.Measured == usagex.MeasuredNone {
			resp.UnmeasuredCalls += agg.Calls
		}
		observeUsageCoverage(resp.Coverage, s.Key)
	}
	resp.Catalog = usageCatalogInfo()
	sort.Strings(order)
	// Buckets with no consumption are filled with zero. Dropping them draws two distant days as
	// adjacent bars and the picture stops reading as a time axis (a four-day gap becomes
	// indistinguishable from consecutive days).
	order = fillUsageBuckets(order, from, to, bucket)
	resp.Buckets = make([]usageBucketWire, 0, len(order))
	for _, t := range order {
		s := byBucket[t]
		if s == nil {
			s = map[string]usageAgg{}
		}
		resp.Buckets = append(resp.Buckets, usageBucketWire{T: t, Series: s})
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// notePrice collects the unit prices used in this response, per model. The same model name is
// priced differently across kinds (codex looks gpt-5.6-luna up at openai list price, opencode at
// gateway price), so on a collision the larger consumption becomes the representative and the
// difference is reported through ambiguous (the model row on screen carries no kind, so only one
// price fits).
func (r *usageSeriesResp) notePrice(k usageKey, src string, spend int) {
	if k.Model == "" || src == "" {
		return
	}
	if r.Prices == nil {
		r.Prices = map[string]usagePriceWire{}
	}
	cur, seen := r.Prices[k.Model]
	if seen && cur.Src == src {
		cur.spend += spend
		r.Prices[k.Model] = cur
		return
	}
	p, _, ok := usagePriceOf(k.Kind, k.Model)
	if !ok {
		return
	}
	next := usagePriceWire{
		Src: src, In: p.In, Out: p.Out, CacheRead: p.cacheRead(), CacheWrite: p.cacheWrite(),
		Ambiguous: seen, spend: spend,
	}
	if seen {
		if cur.spend >= spend { // keep the representative, only raise ambiguous
			cur.Ambiguous = true
			r.Prices[k.Model] = cur
			return
		}
		next.spend = spend
	}
	r.Prices[k.Model] = next
}

// usageMaxFilledBuckets caps zero-filling. A series denser than this is unreadable as a bar
// chart (90 days x hour = 2,160 bars), so only the real data is returned — filling stops rather
// than the data being truncated at the cap (never drop data silently).
const usageMaxFilledBuckets = 1000

// fillUsageBuckets builds the bucket sequence of the requested range, keeping buckets with no
// real data as positions. It returns the bucket keys in ascending order (existing keys are
// always included).
func fillUsageBuckets(have []string, from, to time.Time, bucket string) []string {
	step := 24 * time.Hour
	cur := from.UTC().Truncate(24 * time.Hour)
	if bucket == "hour" {
		step = time.Hour
		cur = from.UTC().Truncate(time.Hour)
	}
	if to.Before(cur) || int(to.Sub(cur)/step) >= usageMaxFilledBuckets {
		return have
	}
	seen := make(map[string]bool, len(have))
	out := make([]string, 0, len(have))
	for ; !cur.After(to); cur = cur.Add(step) {
		key := cur.Format(time.RFC3339)
		seen[key] = true
		out = append(out, key)
	}
	// Real data that fell at the edge of the range (a bucket before from's time of day, say) is
	// always kept.
	for _, t := range have {
		if !seen[t] {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

// collectUsageSamples gathers the samples inside the range. Buckets are cut by the row's ts (the
// time the consumption happened), not by the day of the file it was appended to (see the top of
// usage_rollup.go).
//
//   - day: the folded part from the rollup, the rest from the unfolded raw (normally a single
//     file for today). Each raw file is either folded or unfolded, never both, so nothing is
//     counted twice.
//   - hour: the rollup is day-granular and unusable here, so the raw still on disk is read
//     directly (each row lives in exactly one file, so this does not double count either). If
//     part of the range was pruned and cannot be read, truncated says so honestly.
//
// Both paths go through the (ref, idx) dedup. Only hour cannot drop the shape where the original
// is in a pruned file and the duplicate remains (its watermark is built up from empty), but the
// fold retries within minutes, so an original and its duplicate straddling the retention window
// (90 days by default) does not happen in practice.
func collectUsageSamples(from, to time.Time, bucket string) (samples []usageSample, truncated bool) {
	ensureUsageRollups()
	st := readUsageRollupState()
	fromDay, toDay := from.UTC().Format("2006-01-02"), to.UTC().Format("2006-01-02")
	inDayRange := func(day string) bool { return day >= fromDay && day <= toDay }

	add := func(t string, agg map[usageKey]usageAgg) {
		for k, a := range agg {
			samples = append(samples, usageSample{T: t, Key: k, Agg: a})
		}
	}

	// (ref, idx) dedup (usage_dedup.go). If a fold dies between "append the rows" and "write the
	// watermark", a few turns of that session get appended again on the next pass.
	//
	//   - day: folded files are not read, so their watermark is carried over from the rollup
	//     state (to drop the shape where the original is in the rollup and only the duplicate
	//     is left in unfolded raw).
	//   - hour: the rollup is not used and all raw is re-read, folded or not, so the watermark
	//     is rebuilt from empty. Carrying the state's watermark over here would drop the
	//     originals as well.
	dd := usageDedupIndex{}
	if bucket == "day" {
		dd = st.Folded.clone()
	}

	if bucket == "day" {
		for day, entries := range rolledUpEntries(from, to) {
			if !inDayRange(day) {
				continue
			}
			for _, e := range entries {
				samples = append(samples, usageSample{T: day + "T00:00:00Z", Key: e.Key, Agg: e.Agg})
			}
		}
	}
	for _, fileDay := range usagex.RawDays() {
		_, rolled := st.Rolled[fileDay]
		if bucket == "day" && rolled {
			continue // the rollup is the truth; reading raw would double count
		}
		rows := usagex.ReadDay(fileDay)
		seen := map[string]bool{} // call dedup is per file (one call lives inside one file)
		byBucket := map[string][]usagex.Record{}
		for _, r := range rows {
			ts := usageRowTime(r, fileDay)
			// Pass this before narrowing by range. If which row counts as "the first one"
			// changed with the query range, the same day's total would move just because the
			// range was changed.
			if !dd.accept(r, ts) {
				continue
			}
			day := ts.Format("2006-01-02")
			if !inDayRange(day) {
				continue
			}
			key := day + "T00:00:00Z"
			if bucket == "hour" {
				// At hour granularity from/to apply down to the time of day, so that "the
				// last 24 hours" comes out directly.
				if ts.Before(from) || ts.After(to) {
					continue
				}
				key = ts.Truncate(time.Hour).Format(time.RFC3339)
			}
			byBucket[key] = append(byBucket[key], r)
		}
		for key, brows := range byBucket {
			add(key, aggregateUsageRows(brows, seen))
		}
	}
	if bucket == "hour" {
		// Raw pruned after folding cannot be reconstructed at hour granularity. Returning a
		// shorter series silently would look like "there was no consumption in that period",
		// so say it whenever the ranges overlap.
		onDisk := map[string]bool{}
		for _, d := range usagex.RawDays() {
			onDisk[d] = true
		}
		for fileDay, mark := range st.Rolled {
			if !onDisk[fileDay] && mark.MinDay <= toDay && mark.MaxDay >= fromDay {
				truncated = true
				break
			}
		}
	}
	return samples, truncated
}

// observeUsageCoverage accumulates each kind's measurement ability from the observed dimensions.
// It takes the best that kind can produce: dropping to none on a single failed turn would read
// as "this agent cannot measure at all", while what actually went unmeasured is counted by
// unmeasured_calls.
func observeUsageCoverage(cov map[string]usageCoverage, k usageKey) {
	if k.Kind == "" {
		return
	}
	c := cov[k.Kind]
	c.Tokens = betterOf(c.Tokens, k.Measured, usagex.MeasuredExact, usagex.MeasuredPartial, usagex.MeasuredNone)
	c.Model = betterOf(c.Model, coverageModelSrc(k.ModelSrc), usagex.ModelReported, usagex.ModelRequest, "none")
	cov[k.Kind] = c
}

// coverageModelSrc maps the ledger's model_src onto the coverage vocabulary
// (reported|requested|none).
func coverageModelSrc(src string) string {
	if src == usagex.ModelReported || src == usagex.ModelRequest {
		return src
	}
	return "none" // default_unknown / empty = the resolved model is not known
}

// betterOf returns the better of a and b in ranked's order (best first).
func betterOf(a, b string, ranked ...string) string {
	rank := func(s string) int {
		for i, r := range ranked {
			if s == r {
				return i
			}
		}
		return len(ranked) // unknown/empty ranks last
	}
	if rank(b) < rank(a) {
		return b
	}
	return a
}

// parseUsageTime accepts both RFC3339 and YYYY-MM-DD. dateOnly reports that it was the latter;
// the caller uses it to decide whether to stretch the to side to the end of that day.
func parseUsageTime(s string, def time.Time) (t time.Time, dateOnly bool, err error) {
	if strings.TrimSpace(s) == "" {
		return def, false, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), false, nil
	}
	t, err = time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, false, err
	}
	return t.UTC(), true, nil
}

// parseUsageInclude parses include=session,aux. The default is both; narrowing is left to the
// feature filter (the decision in §9-3).
func parseUsageInclude(s string) (session, aux bool) {
	if strings.TrimSpace(s) == "" {
		return true, true
	}
	for _, p := range strings.Split(s, ",") {
		switch strings.TrimSpace(p) {
		case "session":
			session = true
		case "aux":
			aux = true
		}
	}
	return session, aux
}
