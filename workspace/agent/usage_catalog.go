package main

// 単価カタログ（docs/log/46 §5-c・P2a）。内蔵表（usage_price.go）は Anthropic の一次単価しか
// 持っていないので、codex / opencode の消費が「値付け不可」で残っていた。これを埋める。
//
// ★ 単価表を自分で育てない。**models.dev のカタログが既にワークスペースの中にある**
// （`~/.cache/opencode/models.json` — opencode が自動更新する 212 provider / 7,493 モデル。
// 単位は $/1M でこちらと同じ。ライセンス MIT）。実測: この台帳の消費のうち値付けできる割合が
// 内蔵表のみ 92.7% → カタログ併用 99.3%（残りは `<synthetic>` と上流も価格を持たないモデル）。
//
// ★ **同じモデル id が 20〜34 provider に載っていて価格が違う**（再販・ルータ経由。ある
// 再販業者の claude-opus-4-8 は $1.5/$9.25 で、一次の $5/$25 と別物）。名前だけで引くと
// 静かに嘘の金額が出るので、**引く provider を台帳の `kind` から決める**（claude→anthropic /
// codex→openai / opencode→opencode = 実際に通ったゲートウェイの価格 / copilot→github-copilot）。
// 既知の provider だけを索引に入れ、順序表に無い provider は**絶対に引かない**。
//
// ★ 上流の内部ファイルに依存するので **best-effort に徹する**（ファイルが無い・形が変わった・
// 壊れている、のどれでも「カタログ無し」に落ちるだけで、推定も系列も壊さない）。上流の
// ファイル名で検査を書いて壊れた前例がある（memory: copilot-session-db-moved）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// usageCatalogMaxBytes はカタログとして読む上限。上流が肥大化しても、こちらの
// メモリを道連れにしない（共有ホストは memory constrained）。
const usageCatalogMaxBytes = 64 << 20

// usageCatalogProviders は kind ごとに「その消費が実際に通った provider」の優先順。
// **ここに無い kind は usageCatalogFallback だけを見る。**
var usageCatalogProviders = map[string][]string{
	session.KindClaude: {"anthropic"},
	session.KindCodex:  {"openai"},
	// opencode はゲートウェイ経由＝利用者が実際に払うのは opencode の価格（決定 2026-08-31）。
	session.KindOpencode: {"opencode"},
	session.KindCopilot:  {"github-copilot"},
	// cursor は models.dev に provider が無い。kiro / agy はモデル名から落とす。
	session.KindKiro: {"amazon-bedrock", "anthropic"},
	session.KindAgy:  {"google", "google-vertex"},
}

// usageCatalogFallback は kind から決まらなかった時に見る一次 provider の順。
// **再販・ルータ系（openrouter / nano-gpt / …）は入れない** — 価格が本家と違う。
var usageCatalogFallback = []string{
	"anthropic", "openai", "google", "google-vertex", "amazon-bedrock",
	"alibaba", "zhipuai", "moonshotai", "deepseek", "xai", "mistral", "sakana",
	"minimax", "cohere", "github-copilot", "opencode",
}

// usageCatalogIndexed は索引に入れる provider の集合（順序表の和集合）。
func usageCatalogIndexed() map[string]bool {
	m := make(map[string]bool, len(usageCatalogFallback)+len(usageCatalogProviders))
	for _, p := range usageCatalogFallback {
		m[p] = true
	}
	for _, ps := range usageCatalogProviders {
		for _, p := range ps {
			m[p] = true
		}
	}
	return m
}

// usageCatalog は読み込み済みカタログ。price のキーは "provider/正規化モデル名"。
type usageCatalog struct {
	price   map[string]usagePrice
	origin  string    // opencode | file | env（Console が文言を持つ・パスは出さない）
	modTime time.Time // 元ファイルの mtime = 「いつ時点の単価か」
	models  int
}

// usageCatalogStatTTL は「ファイルを見に行き直す」間隔。単価は1系列の集計で数千回引かれる
// ので、毎回 stat すると無駄な syscall が数千回になる。カタログの更新が数秒遅れて効くのは
// 実害が無い（更新するのは opencode で、こちらの読み出しとは無関係のタイミング）。
const usageCatalogStatTTL = 5 * time.Second

var (
	usageCatalogMu      sync.Mutex
	usageCatalogCache   *usageCatalog
	usageCatalogSrc     string    // 読んだファイル
	usageCatalogAt      time.Time // その時の mtime（変わったら読み直す）
	usageCatalogChecked time.Time // 最後に stat した時刻（TTL 内は見に行かない）
)

// usageCatalogFiles は探す順。前のものが読めればそれを使う。
//   - AF_USAGE_CATALOG: テストと、運用者が自前スナップショットを差す口
//   - usageDir()/catalog.json: opencode を使っていないワークスペース向けに置ける場所
//   - opencode のキャッシュ: 既にあるものを読むだけ（こちらから更新はしない）
func usageCatalogFiles() []struct{ path, origin string } {
	out := []struct{ path, origin string }{}
	if v := strings.TrimSpace(os.Getenv("AF_USAGE_CATALOG")); v != "" {
		out = append(out, struct{ path, origin string }{v, "env"})
	}
	out = append(out, struct{ path, origin string }{filepath.Join(usageDir(), "catalog.json"), "file"})
	cache := os.Getenv("XDG_CACHE_HOME")
	if cache == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cache = filepath.Join(home, ".cache")
		}
	}
	if cache != "" {
		out = append(out, struct{ path, origin string }{filepath.Join(cache, "opencode", "models.json"), "opencode"})
	}
	return out
}

// loadUsageCatalog は最初に読めたカタログを返す（無ければ nil）。mtime が変わるまで再利用。
func loadUsageCatalog() *usageCatalog {
	usageCatalogMu.Lock()
	defer usageCatalogMu.Unlock()
	// TTL 内は前回の結論（カタログ有り／無しの両方）をそのまま使う。
	if !usageCatalogChecked.IsZero() && time.Since(usageCatalogChecked) < usageCatalogStatTTL {
		return usageCatalogCache
	}
	usageCatalogChecked = time.Now()
	for _, f := range usageCatalogFiles() {
		st, err := os.Stat(f.path)
		if err != nil || st.IsDir() || st.Size() == 0 || st.Size() > usageCatalogMaxBytes {
			continue
		}
		if usageCatalogCache != nil && usageCatalogSrc == f.path && usageCatalogAt.Equal(st.ModTime()) {
			return usageCatalogCache
		}
		b, err := os.ReadFile(f.path)
		if err != nil {
			continue
		}
		cat := parseUsageCatalog(b, f.origin, st.ModTime())
		if cat == nil {
			continue // 形が違う／壊れている = このファイルは無かったことにする
		}
		usageCatalogCache, usageCatalogSrc, usageCatalogAt = cat, f.path, st.ModTime()
		return cat
	}
	usageCatalogCache, usageCatalogSrc, usageCatalogAt = nil, "", time.Time{}
	return nil
}

// catalogFile は models.dev の api.json のうち**こちらが使う分だけ**の形。
// 上流が他のフィールドを増減させてもここは動く（decoder が読み飛ばす）。
type catalogFile map[string]struct {
	Models map[string]struct {
		Cost *struct {
			Input      *float64 `json:"input"`
			Output     *float64 `json:"output"`
			CacheRead  *float64 `json:"cache_read"`
			CacheWrite *float64 `json:"cache_write"`
			// tiers / context_over_200k（長文の割増）は**使わない**。台帳はターンごとの
			// 入力長を持たないので当てられない＝下段固定＝推定は下振れしうる（docs/log/46 §5-c）。
		} `json:"cost"`
	} `json:"models"`
}

// parseUsageCatalog は索引に入れる provider だけを取り出す。1つも取れなければ nil
// （＝「読めたが中身が知らない形」を「カタログ有り」と言わない）。
func parseUsageCatalog(b []byte, origin string, mod time.Time) *usageCatalog {
	var f catalogFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil
	}
	want := usageCatalogIndexed()
	price := map[string]usagePrice{}
	// 同じ provider 内で正規化名が衝突したら、モデル id の辞書順で小さい方を採る
	// （map の反復順で結果が揺れないように — 実測 3.3M の額が読むたび変わるのは論外）。
	winner := map[string]string{}
	for pid, p := range f {
		if !want[pid] {
			continue
		}
		for mid, m := range p.Models {
			if m.Cost == nil || m.Cost.Input == nil || m.Cost.Output == nil {
				continue // 上流が価格を持たないモデル（= 値付け不可のまま）
			}
			key := pid + "/" + usageNormalizeModel(mid)
			if cur, ok := winner[key]; ok && cur <= mid {
				continue
			}
			winner[key] = mid
			price[key] = usagePrice{
				In: *m.Cost.Input, Out: *m.Cost.Output,
				CacheRead: derefPrice(m.Cost.CacheRead), CacheWrite: derefPrice(m.Cost.CacheWrite),
			}
		}
	}
	if len(price) == 0 {
		return nil
	}
	return &usageCatalog{price: price, origin: origin, modTime: mod, models: len(price)}
}

func derefPrice(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

// usageCatalogOrder は kind に対して引く provider の順（kind 固有 → 一次 provider）。
func usageCatalogOrder(kind string) []string {
	head := usageCatalogProviders[kind]
	out := make([]string, 0, len(head)+len(usageCatalogFallback))
	seen := map[string]bool{}
	for _, p := range append(append([]string{}, head...), usageCatalogFallback...) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// usageCatalogLookup はカタログから単価を引く。ref は「どの provider の値を採ったか」
// （画面に出す — どこの単価かが分からない金額は検算できない）。
func usageCatalogLookup(kind, model string) (usagePrice, string, bool) {
	cat := loadUsageCatalog()
	if cat == nil {
		return usagePrice{}, "", false
	}
	base := usageNormalizeModel(model)
	if base == "" {
		return usagePrice{}, "", false
	}
	for _, pid := range usageCatalogOrder(kind) {
		if p, ok := cat.price[pid+"/"+base]; ok {
			return p, pid + "/" + base, true
		}
	}
	return usagePrice{}, "", false
}

// usageCatalogMeta は応答に載せるカタログの申告（無ければ nil）。**取得日を必ず出す**：
// カタログが更新されると過去の推定額も変わる（保存していないので）ので、どの時点の単価かを
// 言わずに額だけ変えるのは黙って嘘をつくのに近い。
type usageCatalogMeta struct {
	Origin  string `json:"origin"`            // opencode | file | env
	Models  int    `json:"models"`            // 索引に入った（＝一次 provider の）モデル数
	Fetched string `json:"fetched,omitempty"` // 元ファイルの mtime（RFC3339）
}

func usageCatalogInfo() *usageCatalogMeta {
	cat := loadUsageCatalog()
	if cat == nil {
		return nil
	}
	m := &usageCatalogMeta{Origin: cat.origin, Models: cat.models}
	if !cat.modTime.IsZero() {
		m.Fetched = cat.modTime.UTC().Format(time.RFC3339)
	}
	return m
}

// usageSortedModels はテスト・デバッグ用（索引のキーを決定的に並べる）。
func (c *usageCatalog) keys() []string {
	out := make([]string, 0, len(c.price))
	for k := range c.price {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
