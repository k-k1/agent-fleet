package main

// GET /usage/series — 機能別使用量の時系列（docs/46 §4 / ADR0029 §8）。
//
// サーバ側で集計して返す（Console に生ログを流さない）。生ログにはセッション名・会話 id が
// 載るので、集計して返すことは形の都合ではなくプライバシー側の要求でもある。
//
// 読む順序は rollup → raw。畳み済みの日は rollup が唯一の正なので、同じ消費を二重に
// 数えない（usage_rollup.go）。
//
// ⚠️ 新 REST は workspace/agent/routes.go と control-plane/routes.go の**両方**に登録する
// （CP は明示許可リスト方式。片方漏れると backend 正常でも Console から 404）。

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// 集計軸の語彙。by / split / filter のキーはこれだけを受け付ける（未知の軸を通すと
// 「指定したのに効いていない」が静かに起きる）。
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

// usageDimValue は次元タプルから軸の値を取る。未知の軸は "" を返さず false を返させる
// ため、呼び出し前に validUsageDim で弾く。
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

// usageFilter は dim -> パターン群。**同じ軸は OR、違う軸は AND**（filter=kind:claude,
// kind:codex は「claude か codex」、filter=kind:claude,feature:title.* は「claude かつ
// title 系」）。末尾 * だけを前方一致として扱う。
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

// usageCoverage は「この kind は何をどこまで報告するか」の自己申告。**データから自動生成
// する**（手書きの表はドリフトする）— UI の未計測バナーはこれを読んで書く。
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
	// Matrix は split 指定時のみ（「機能 × モデル」等の表）。
	Matrix          map[string]map[string]usageAgg `json:"matrix,omitempty"`
	Coverage        map[string]usageCoverage       `json:"coverage"`
	UnmeasuredCalls int                            `json:"unmeasured_calls"`
	// PricedSpend / UnpricedSpend は「推定額をいくらぶんの消費から起こせたか」の申告
	// （usage_price.go）。推定額だけを出すと、単価表に無いモデルの消費が黙って抜けた
	// 合計を「API 換算相当額」として読ませてしまう。
	PricedSpend   int `json:"priced_spend"`
	UnpricedSpend int `json:"unpriced_spend"`
	// Prices は**この応答に出てくるモデルに使った単価**（モデル名 → 実効単価と出所）。
	// 金額の検算にはどこの単価かが要る。集計値には持たせない（軸で畳むと混ざる）。
	Prices map[string]usagePriceWire `json:"prices,omitempty"`
	// Catalog はカタログの申告（無ければ省略）。
	Catalog *usageCatalogMeta `json:"catalog,omitempty"`
	// Truncated は要求期間の一部が raw の保持期間より古く、hour バケットでは復元できな
	// かったことを示す。黙って短い系列を返すと「その期間は消費が無かった」に見える。
	Truncated bool `json:"truncated,omitempty"`
	// Folding は「セッション本体の折り込みがこの読み出しの時点で走っている」＝この応答は
	// 直近ターンをまだ含まないかもしれない、の申告。Console はこれが落ちるまで取り直す。
	Folding bool `json:"folding,omitempty"`
}

// usagePriceWire は応答の prices[model]。値は**実効単価**（キャッシュ単価が出所に無ければ
// 倍率で置いた後の値）＝画面に出した金額をそのまま検算できる形にする。
type usagePriceWire struct {
	Src        string  `json:"src"` // builtin | catalog:<provider>/<model>
	In         float64 `json:"in"`  // $/1M
	Out        float64 `json:"out"` // $/1M
	CacheRead  float64 `json:"cread"`
	CacheWrite float64 `json:"cwrite"`
	// Ambiguous は「同じモデル名でも kind によって単価が違う」（例: gpt-5.6-luna を
	// codex は openai 定価・opencode はゲートウェイ価格で換算する）。1つの行に単価を
	// 1つしか出せないので、違うことだけは言う。
	Ambiguous bool `json:"ambiguous,omitempty"`
	// Spend は代表として採った側の消費（大きい方を出す — 小さい方の単価を代表に
	// してしまうと、表の金額と単価が噛み合わなく見える）。
	spend int
}

// usageSample は集計の入力単位（rollup の1エントリ、または raw をバケット内で畳んだもの）。
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
		// 日付だけで指定された to は「その日いっぱい」。深夜 0 時と解釈すると
		// from=to=今日 の hour クエリが空になり、まず間違いなく意図と違う。
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

	// fold-on-read（docs/46 §3-b）: 系列を要求されたこの機会にセッション本体を折り込む。
	// 非同期なのでこの応答自体は待たない（＝直近ターンは次回の読み出しに乗る）。**待たない
	// ことを黙っていると「再取得を何度か押すまで最新にならない」画面になる**ので、走行中は
	// folding を立てて返し、Console が終わってから取り直す。
	// fold=force は 60 秒スロットルを飛ばす（利用者が明示的に再取得を押した経路だけ）。
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
		isSession := s.Key.Feature == usageFeatureSession
		if (isSession && !inclSession) || (!isSession && !inclAux) {
			continue
		}
		if _, ok := byBucket[s.T]; !ok {
			byBucket[s.T] = map[string]usageAgg{}
			order = append(order, s.T)
		}
		// 推定額はここで載せる（保存しない）。サンプルはまだモデル次元を持っている＝
		// どの軸で畳んだ後でも足し合わせが効く形になるのは、畳む前のこの位置だけ。
		// 単価は kind でも変わる（同じモデルでも通った provider が違う）ので、
		// **kind を落とす前**のここで引くこと。
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
		if s.Key.Measured == usageMeasuredNone {
			resp.UnmeasuredCalls += agg.Calls
		}
		observeUsageCoverage(resp.Coverage, s.Key)
	}
	resp.Catalog = usageCatalogInfo()
	sort.Strings(order)
	// 消費の無いバケットもゼロで埋める。落とすと「離れた2日」が隣り合う棒として描かれ、
	// 時間軸として読めなくなる（4日空いたのか連続なのかが絵から消える）。
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

// notePrice はこの応答で使った単価を model 単位に集める。**同じモデル名でも kind が
// 違えば単価が違う**（gpt-5.6-luna を codex は openai 定価、opencode はゲートウェイ価格で
// 引く）ので、衝突したら消費の大きい方を代表にし、違うことを ambiguous で申告する
// （画面のモデル行は kind を持たないので、単価を1つしか出せない）。
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
		if cur.spend >= spend { // 代表は据え置き。ambiguous だけ立てる
			cur.Ambiguous = true
			r.Prices[k.Model] = cur
			return
		}
		next.spend = spend
	}
	r.Prices[k.Model] = next
}

// usageMaxFilledBuckets はゼロ埋めの上限。これを超える密度の系列は棒グラフとして読めない
// （90日 × hour = 2,160 本）ので、埋めずに実データだけを返す — 上限で切り詰めるのではなく
// **埋めるのをやめる**（データを黙って落とさない）。
const usageMaxFilledBuckets = 1000

// fillUsageBuckets は要求期間のバケット並びを作り、実データの無いバケットも位置として残す。
// 返すのは昇順のバケットキー列（既存キーは必ず含む）。
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
	// 期間の端（from の時刻成分より前のバケット等）に落ちた実データは必ず残す。
	for _, t := range have {
		if !seen[t] {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

// collectUsageSamples は期間内のサンプルを集める。**バケットは行の ts（消費が起きた時刻）
// で刻む** — 追記先のファイル日ではない（usage_rollup.go 冒頭）。
//
//   - day: 畳み済みの分は rollup から、未畳みの raw（通常は当日のファイル1つ）から残りを。
//     各 raw ファイルは畳み済みか未畳みのどちらか一方なので、二重に数えない。
//   - hour: rollup は日粒度なので使えない。ディスクに残っている raw を直接読む（各行は
//     ちょうど1つのファイルにあるので、こちらも二重にならない）。prune 済みで読めない
//     期間があれば truncated で正直に言う。
//
// どちらの経路も (ref, idx) 重複排除を通す。hour だけは「原本が prune 済みファイルにあり
// 重複が残っている」形を落とせない（水位を空から積むため）が、折り込みの再試行は分単位で
// 走るので、原本と重複が保持期間（既定90日）を跨ぐことは実質起きない。
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

	// (ref, idx) 重複排除（usage_dedup.go）。折り込みが「行を追記 → watermark を書く」の
	// 間で落ちると、そのセッションの数ターン分が次のパスで再追記される。
	//
	//   - day: 畳み済みファイルは読まないので、その分の水位を rollup state から引き継ぐ
	//     （原本が rollup にあり、重複だけが未畳みの raw に残っている形を落とすため）。
	//   - hour: rollup を使わず**畳み済みも含めて raw を全部読み直す**ので、水位は空から
	//     積み直す。ここで state の水位を引き継ぐと、原本の側まで落としてしまう。
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
	for _, fileDay := range usageRawDays() {
		_, rolled := st.Rolled[fileDay]
		if bucket == "day" && rolled {
			continue // rollup が正。raw を読むと二重計上になる
		}
		rows := readUsageDay(fileDay)
		seen := map[string]bool{} // call の重複排除はファイル単位（1呼び出しは1ファイル内）
		byBucket := map[string][]usageRecord{}
		for _, r := range rows {
			ts := usageRowTime(r, fileDay)
			// **期間で絞る前に**通す。どの行を「最初の1件」とみなすかがクエリ期間で
			// 変わると、期間を変えただけで同じ日の合計が動く。
			if !dd.accept(r, ts) {
				continue
			}
			day := ts.Format("2006-01-02")
			if !inDayRange(day) {
				continue
			}
			key := day + "T00:00:00Z"
			if bucket == "hour" {
				// hour では from/to の時刻成分まで効かせる（「直近24時間」を素直に出せる）。
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
		// 畳んだ後に prune された raw があると、その期間は時間粒度で復元できない。黙って
		// 短い系列を返すと「その期間は消費が無かった」に見えるので、範囲が重なる時だけ言う。
		onDisk := map[string]bool{}
		for _, d := range usageRawDays() {
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

// observeUsageCoverage は観測した次元から kind ごとの計測能力を積み上げる。**その kind が
// 出せる最良のもの**を採る（1回の失敗ターンで none に落とすと「このエージェントは測れない」
// と誤読させる。実際に測れていない分は unmeasured_calls が数える）。
func observeUsageCoverage(cov map[string]usageCoverage, k usageKey) {
	if k.Kind == "" {
		return
	}
	c := cov[k.Kind]
	c.Tokens = betterOf(c.Tokens, k.Measured, usageMeasuredExact, usageMeasuredPartial, usageMeasuredNone)
	c.Model = betterOf(c.Model, coverageModelSrc(k.ModelSrc), usageModelReported, usageModelRequest, "none")
	cov[k.Kind] = c
}

// coverageModelSrc は台帳の model_src を coverage の語彙（reported|requested|none）へ写す。
func coverageModelSrc(src string) string {
	if src == usageModelReported || src == usageModelRequest {
		return src
	}
	return "none" // default_unknown / 空 = 解決後のモデルが分からない
}

// betterOf は ranked の並び（良い順）で a と b の良い方を返す。
func betterOf(a, b string, ranked ...string) string {
	rank := func(s string) int {
		for i, r := range ranked {
			if s == r {
				return i
			}
		}
		return len(ranked) // 未知/空は最下位
	}
	if rank(b) < rank(a) {
		return b
	}
	return a
}

// parseUsageTime は RFC3339 と YYYY-MM-DD の両方を受ける。dateOnly は後者だったことを
// 返す — to 側を「その日の終わり」へ伸ばすかの判断に使う（呼び出し側）。
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

// parseUsageInclude は include=session,aux を解く。既定は両方（feature フィルタで絞れる形に
// する、が §9-3 の決定）。
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
