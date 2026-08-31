package main

// 使用量台帳の rollup（docs/log/46 §3-c / ADR0029 §8）。
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
// calls は **distinct call** で数える（docs/log/46 §4）。claude は1呼び出しがモデル別行に
// 割れるので、行数で数えると呼び出し回数が水増しされる。「その call の最初の行」だけを
// 1回として数えることで、どの軸で足し合わせても合計の呼び出し回数が壊れない。

import (
	"encoding/json"
	"log"
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
	CostUSD     float64 `json:"cost_usd,omitempty"` // 実測（claude だけが返す）
	// CostEstUSD は単価表から起こした推定（usage_price.go）。**実測とは別値**で、足さない。
	// **rollup には書かない** — 単価は改定されるので、読み出しのたびに今の表で掛け直す
	// （handleUsageSeries がサンプル単位で載せる）。
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

// usageRollupVersion は rollup ファイルの版。上げると次回の ensureUsageRollups が
// 「raw から作り直せる分だけ」作り直す（rebuildUsageRollupsLocked）。
//
//	v1: 初版（P3）。
//	v2: (ref, idx) 重複排除を入れた（usage_dedup.go）。v1 の集計には折り込みの
//	    クラッシュ窓で入り込んだ重複が畳み込まれている可能性があり、集計は加算済みで
//	    引き算できないので作り直す以外に落とす方法が無い。
const usageRollupVersion = 2

type usageRollupState struct {
	Version int                        `json:"v"`
	Rolled  map[string]usageRolledFile `json:"rolled"`
	// Folded は畳み込み済みの (ref, idx) 水位。rollup へ入った分の重複を、raw が prune
	// された後（＝原本の行がもう読めない後）でも落とせるようにするため state に残す。
	Folded usageDedupIndex `json:"folded,omitempty"`
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
//
// **呼び出し回数はその call の「代表行」に付ける。** claude は1呼び出しがモデル別行に
// 割れ、行の並びは `usageModelRows` が付ける **生 id の昇順**（＝綴り順）でしかない。
// 「最初の行」で数えると、calls=1 が付くモデルが id の綴り次第で決まり、`by=model` や
// `split=model` で**主力モデルが 0 回・脇役が 1 回**と出る（機能×モデル表の平均も同時に
// 壊れる）。代表は **その呼び出しで最も spend の大きいモデル行**とし、同点は model_raw →
// model の昇順で決定的に選ぶ。
//
// 按分（1/N や spend 比）にしないのは calls を整数の「回数」として凍結してあるから
// （ADR0029 §1）。どの軸で足しても合計が distinct call 数に一致する性質は代表方式でも
// 保たれる。代表以外のモデル行は spend>0 / calls=0 になるので、平均を出す表は 0 除算を
// 「—」で出す（console 側 `perCall`）。
func aggregateUsageRows(rows []usageRecord, seen map[string]bool) map[usageKey]usageAgg {
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
			a.Calls++ // 呼び出し ID の無い行は行＝呼び出しとみなす
		} else if rep[r.Call] == i && !seen[r.Call] {
			seen[r.Call] = true
			a.Calls++ // その呼び出しの代表行だけを1回として数える
		}
		out[k] = a
	}
	return out
}

// usageCallRepresentatives は call ごとに「回数を数える行」の添字を選ぶ。
func usageCallRepresentatives(rows []usageRecord) map[string]int {
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

// usageRowOutranks は「どちらの行がその呼び出しの代表か」。実態（消費の大きい方）を採り、
// 同点は名前で決める — 同じ入力から常に同じ帰属になることが集計の再現性に要る。
func usageRowOutranks(a, b usageRecord) bool {
	if a.Spend != b.Spend {
		return a.Spend > b.Spend
	}
	if a.ModelRaw != b.ModelRaw {
		return a.ModelRaw < b.ModelRaw
	}
	return a.Model < b.Model
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
	if st.Version < usageRollupVersion {
		st = rebuildUsageRollupsLocked(st)
		stateChanged = true // 版だけでも必ず残す（毎回作り直しを試みない）
	}
	// (ref, idx) 重複排除の水位は **ファイル日を跨いで持ち回る**。折り込みのクラッシュ窓が
	// 日付を跨ぐと、重複は原本と別のファイルへ落ちるため（usage_dedup.go）。
	dd := st.Folded
	dropped := 0

	for _, fileDay := range usageRawDays() {
		if fileDay >= today {
			continue // 当日（と、時計が巻き戻った場合の未来日）は raw のまま扱う
		}
		if _, done := st.Rolled[fileDay]; done {
			continue
		}
		rawRows, closed := readUsageDayForRollup(fileDay)
		if !closed {
			continue // まだ追記されうる日（UTC 日跨ぎの競合）— 次回に回す
		}
		// 消費日ごとに仕分ける。1つの raw ファイルが複数の日（バックフィルなら数か月）へ
		// 寄与しうるのがこの層の肝。
		seen := map[string]bool{} // call の重複排除はファイル単位で共有する
		byDay := map[string][]usageRecord{}
		var days []string
		for _, r := range rawRows {
			ts := usageRowTime(r, fileDay)
			if !dd.accept(r, ts) {
				dropped++ // 追記済み・watermark 未更新で落ちた分の再追記
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
		if mark.MinDay == "" { // 空ファイル: 範囲は自身の日とみなす
			mark.MinDay, mark.MaxDay = fileDay, fileDay
		}
		st.Rolled[fileDay] = mark
		stateChanged = true
	}
	if dropped > 0 {
		// 黙って落とさない — 起きたことは必ず言う（折り込みが途中で落ちた痕跡でもある）。
		log.Printf("usage: rollup: (ref,idx) 重複 %d 行を集計から落とした", dropped)
	}
	if !stateChanged {
		return
	}
	st.Folded = dd
	// 月ファイル → state の順で書く。逆順だと、間で落ちた時に「畳み済み扱いだが集計が無い」
	// = 消費の取りこぼしになる。この順なら最悪もう一度畳もうとするだけで、Src が弾く。
	//
	// **1つでも月ファイルが書けなければ state を進めない。** 進めると、その月へ寄与する
	// はずだった消費は「畳み済み」扱いのまま集計から消え、raw が prune された時点で
	// 二度と戻らない。書けた月の分は Src が「畳み済み」を覚えているので、次回の畳み直しで
	// 二重に足されることもない。
	for month := range dirty {
		if err := writeUsageJSON(usageRollupPath(month), months[month]); err != nil {
			log.Printf("usage: rollup %s の書き込みに失敗: %v（state は進めない＝次回やり直す）", month, err)
			return
		}
	}
	if err := writeUsageJSON(usageRollupStatePath(), st); err != nil {
		log.Printf("usage: rollup state の書き込みに失敗: %v", err)
	}
}

// readUsageDayForRollup は追記と競合しない形で1日分を読む。**usageMu を保持したまま
// 「その日はもう追記されないか」を確かめて読む**のが要点。
//
// 追記側（appendUsageRows）は usageMu を取ってから追記先の日を決めるので、ここでロック内
// に「今日」を取り直して `day < today` を確認できれば、その後にロックを取る追記は必ず
// もっと新しい日を選ぶ＝このファイルはもう伸びない。両者が別ロックだと、UTC 日跨ぎの直前に
// 日を決めた追記がロールアップの後にそのファイルへ着地し、「畳み済み」判定で二度と読まれない
// （＝その行が黙って消える）。窓は極小だが、消え方が静かなので塞ぐ。
func readUsageDayForRollup(day string) (rows []usageRecord, closed bool) {
	usageMu.Lock()
	defer usageMu.Unlock()
	if day >= time.Now().UTC().Format("2006-01-02") {
		return nil, false
	}
	return readUsageDay(day), true
}

// rebuildUsageRollupsLocked は版が上がった時に rollup を作り直す。usageRollupMu 保持前提。
//
// 既に畳んである集計は加算済みで、後から特定の行だけ引き算できない。v1 の rollup へ
// 折り込みのクラッシュ窓由来の重複が入っている可能性がある以上、**作り直す以外に落とす
// 手が無い**。作り直せるのは寄与元の raw がまだディスクにある分だけなので、
//
//   - 畳んだファイル日が全部残っている（＝まだ何も prune されていない）: 月ファイルを
//     捨てて全部やり直す。呼び出し元のループがそのまま畳み直す。
//   - 1つでも prune 済み: **作り直さない**。消えた raw の分の集計を失う方が、残っている
//     かもしれない重複より重い。代わりに、見えている畳み済みファイルから重複排除の水位
//     だけ復元して、以後に入る重複は落とせるようにする。
//
// 本機能は導入直後（rollup が raw の保持期間より古くなる前）なので、実際には前者を通る。
func rebuildUsageRollupsLocked(st usageRollupState) usageRollupState {
	onDisk := map[string]bool{}
	for _, d := range usageRawDays() {
		onDisk[d] = true
	}
	pruned := 0
	for fileDay := range st.Rolled {
		if !onDisk[fileDay] {
			pruned++
		}
	}
	if pruned > 0 {
		log.Printf("usage: rollup v%d: 寄与元 raw が %d 日分 prune 済みのため作り直さない"+
			"（既存集計に重複が残る可能性あり・以後の重複は落とす）", usageRollupVersion, pruned)
		fresh := usageRollupState{Version: usageRollupVersion, Rolled: st.Rolled, Folded: usageDedupIndex{}}
		for _, fileDay := range usageRawDays() { // 昇順＝追記順
			if _, done := st.Rolled[fileDay]; !done {
				continue // 未畳みの分は呼び出し元のループが同じ索引で処理する
			}
			for _, r := range readUsageDay(fileDay) {
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
		log.Printf("usage: rollup を v%d へ作り直す（%d ファイル日を raw から畳み直す）",
			usageRollupVersion, len(st.Rolled))
	}
	return usageRollupState{Version: usageRollupVersion, Rolled: map[string]usageRolledFile{}, Folded: usageDedupIndex{}}
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
