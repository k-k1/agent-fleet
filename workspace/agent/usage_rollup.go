package main

// 使用量台帳の rollup（docs/46 §3-c / ADR0029 §8）。
//
// raw は既定90日で消えるが、rollup は無期限に残る。日 × 次元の集計なので小さく、
// 通常のクエリは raw を読まない。
//
// **バケットは行の ts（消費が起きた時刻）で刻む。追記先のファイル日ではない。** セッション
// 本体の折り込みは過去の転写を後から取り込む（バックフィルは導入日に数か月分が一度に入る）
// ので、ファイル日で刻むと過去の消費が全部「導入日」に積み上がり、時系列として無意味になる。
// 実機で最初に踏んだのがこれ。
//
// 二重計上しないための不変条件は1つ:
//
//	raw の各ファイル日は「畳み済み（行は rollup にある）」か「未畳み（raw を読む）」の
//	どちらか一方。当日は行がまだ増えるので必ず未畳み側。
//
// さらに、畳んだ日ごとに寄与元のファイル日（Src）を残してあるので、途中でプロセスが落ちて
// state を書けなくても、やり直しは skip されるだけで二重に足されない。
//
// calls は **distinct call** で数える（docs/46 §4）。claude は1呼び出しがモデル別行に
// 割れるので、行数で数えると呼び出し回数が水増しされる。「その call の最初の行」だけを
// 1回として数えることで、どの軸で足し合わせても合計の呼び出し回数が壊れない。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// usageKey は集計の次元タプル。**ref（セッション名/会話 id）と model_raw は入れない** —
// ref は際限なく増えて rollup が「小さい」前提を壊し（かつ集計 API から出したくない）、
// model_raw は表示が canonical で束ねる以上クエリ軸にならない。どちらも raw の保持期間内
// なら行から追える。
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

// usageAgg は集計値。JSON タグは /usage/series の series 要素そのもの。
type usageAgg struct {
	Spend       int     `json:"spend"`
	In          int     `json:"in"`
	Out         int     `json:"out"`
	CacheRead   int     `json:"cread"`
	CacheCreate int     `json:"ccreate"`
	Calls       int     `json:"calls"`
	CostUSD     float64 `json:"cost_usd,omitempty"`
}

func (a *usageAgg) add(b usageAgg) {
	a.Spend += b.Spend
	a.In += b.In
	a.Out += b.Out
	a.CacheRead += b.CacheRead
	a.CacheCreate += b.CacheCreate
	a.Calls += b.Calls
	a.CostUSD += b.CostUSD
}

type usageRollupEntry struct {
	Key usageKey `json:"k"`
	Agg usageAgg `json:"v"`
}

// usageRollupDay は消費日1日分。Src は寄与した raw ファイル日 — これがあるので、
// 同じファイルを二度畳もうとしても足し込まない（クラッシュ後のやり直しが安全）。
type usageRollupDay struct {
	Src     []string           `json:"src"`
	Entries []usageRollupEntry `json:"e"`
}

// usageRollupMonth は1か月分。キーは**消費日**（行の ts の日）。
type usageRollupMonth struct {
	Days map[string]usageRollupDay `json:"days"`
}

// usageRolledFile は畳み終えた raw ファイル1日分の記録。MinDay/MaxDay は寄与した消費日の
// 範囲で、raw が prune された後に「時間粒度では復元できない期間」を正直に言うために使う。
type usageRolledFile struct {
	MinDay string `json:"minDay"`
	MaxDay string `json:"maxDay"`
}

type usageRollupState struct {
	Rolled map[string]usageRolledFile `json:"rolled"`
}

var usageRollupMu sync.Mutex

func usageRollupDir() string { return filepath.Join(usageDir(), "rollup") }

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
	st := usageRollupState{Rolled: map[string]usageRolledFile{}}
	b, err := os.ReadFile(usageRollupStatePath())
	if err != nil {
		return st
	}
	if json.Unmarshal(b, &st) != nil || st.Rolled == nil {
		st.Rolled = map[string]usageRolledFile{}
	}
	return st
}

// writeUsageJSON は tmp+rename で書く（途中で落ちても壊れた集計を残さない）。
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

// keyOf は台帳1行から次元タプルを取る。
func keyOf(r usageRecord) usageKey {
	return usageKey{
		Feature: r.Feature, Trigger: r.Trigger, Origin: r.Origin, OriginConv: r.OriginConv,
		Kind: r.Kind, Model: r.Model, ModelSrc: r.ModelSrc, Verb: r.Verb,
		Sidechain: r.Sidechain, Measured: r.Measured, OK: r.OK,
	}
}

// usageRowTime は行の消費時刻。ts が壊れている/空の行は追記先のファイル日の 0 時へ寄せる
// （捨てない — 捨てると合計が合わなくなる）。
func usageRowTime(r usageRecord, fileDay string) time.Time {
	if t, err := time.Parse(time.RFC3339, r.TS); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse("2006-01-02", fileDay); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

// aggregateUsageRows は行群を次元別に畳む。seen は「既に数えた call」— 呼び出し回数の
// 二重計上を防ぐために呼び出し側と共有する。
func aggregateUsageRows(rows []usageRecord, seen map[string]bool) map[usageKey]usageAgg {
	out := map[usageKey]usageAgg{}
	for _, r := range rows {
		k := keyOf(r)
		a := out[k]
		a.Spend += r.Spend
		a.In += r.In
		a.Out += r.Out
		a.CacheRead += r.CacheRead
		a.CacheCreate += r.CacheCreate
		a.CostUSD += r.CostUSD
		if r.Call == "" {
			a.Calls++ // 呼び出し ID の無い行は行＝呼び出しとみなす
		} else if !seen[r.Call] {
			seen[r.Call] = true
			a.Calls++ // その呼び出しの最初の行だけを1回として数える
		}
		out[k] = a
	}
	return out
}

// sortedRollupEntries は集計マップを決定的な並びのエントリ列にする（同じ入力から同じ
// ファイルが出ないと、差分レビューもテストも当てにならない）。
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

// mergeRollupDay は既存の日へ寄与を足す。同じ raw ファイル日を二度足さない。
func mergeRollupDay(day usageRollupDay, srcFileDay string, agg map[usageKey]usageAgg) (usageRollupDay, bool) {
	for _, s := range day.Src {
		if s == srcFileDay {
			return day, false // 畳み済み — やり直しは no-op
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

// ensureUsageRollups は「完了した日」の raw を畳む。当日はまだ行が増えるので触らない。
// raw が prune で消えても、畳んだ集計は残る（rollup 無期限・ADR0029 §7-4）。
func ensureUsageRollups() {
	usageRollupMu.Lock()
	defer usageRollupMu.Unlock()
	today := time.Now().UTC().Format("2006-01-02")
	st := readUsageRollupState()
	months := map[string]usageRollupMonth{}
	dirty := map[string]bool{}
	stateChanged := false

	for _, fileDay := range usageRawDays() {
		if fileDay >= today {
			continue // 当日（と、時計が巻き戻った場合の未来日）は raw のまま扱う
		}
		if _, done := st.Rolled[fileDay]; done {
			continue
		}
		// 消費日ごとに仕分ける。1つの raw ファイルが複数の日（バックフィルなら数か月）へ
		// 寄与しうるのがこの層の肝。
		seen := map[string]bool{} // call の重複排除はファイル単位で共有する
		byDay := map[string][]usageRecord{}
		var days []string
		for _, r := range readUsageDay(fileDay) {
			d := usageRowTime(r, fileDay).Format("2006-01-02")
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
		if mark.MinDay == "" { // 空ファイル: 範囲は自身の日とみなす
			mark.MinDay, mark.MaxDay = fileDay, fileDay
		}
		st.Rolled[fileDay] = mark
		stateChanged = true
	}
	if !stateChanged {
		return
	}
	// 月ファイル → state の順で書く。逆順だと、間で落ちた時に「畳み済み扱いだが集計が無い」
	// = 消費の取りこぼしになる。この順なら最悪もう一度畳もうとするだけで、Src が弾く。
	for month := range dirty {
		_ = writeUsageJSON(usageRollupPath(month), months[month])
	}
	_ = writeUsageJSON(usageRollupStatePath(), st)
}

// rolledUpEntries は指定期間の消費日ごとの集計を返す（rollup が正である分）。
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
		return "9999-12" // 壊れた入力でループを回し続けない
	}
	return t.AddDate(0, 1, 0).Format("2006-01")
}
