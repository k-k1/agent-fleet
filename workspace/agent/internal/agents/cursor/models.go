package cursor

// 起動時モデルカタログ — アカウント連動のライブ取得（docs/40 決定 6）。cursor は
// `cursor-agent models` が公式にあり（copilot の TUI スクレイプ不要）、`id - 表示名`
// の行を返す（実測 v2026.07.20）。effort はモデル ID 自体に畳まれている
// （例 gpt-5.3-codex-high / claude-opus-4-8-thinking-high）ので別 Efforts は付けない。
// `auto`（既定・フラグ無し）はカタログから外す。10 分キャッシュ・stale-if-error。
//
// **Free プラン絞り込み（docs/40 §Free・session2 実測）**: `cursor-agent models` は
// プランに関係なく全モデルを列挙するが、**Free プランは named model を一切使えない**
// （実測: `ActionRequiredError: Named models unavailable Free plans can only use Auto.`）。
// 使えるのは Auto（＝ピッカーの 既定・カタログから除外済み）と Composer 系のみ
// （実測: composer-2.5 は result:"ok"）。よって Free プラン（`cursor-agent about` の
// `Subscription Tier` で判定）のときはカタログを composer 系だけに絞り、選ぶと壁に
// 当たる named を隠す（ユーザーが GLM-5.2 を選んで Upgrade 要求に当たった問題の解消）。
// 有料プラン／判定不能時は全カタログのまま（過剰制限しない安全側）。

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

const modelsTTL = 10 * time.Minute

var modelsMu sync.Mutex
var modelsAt time.Time
var modelsList []agents.ModelChoice // nil = 未取得/失敗

// Models returns the account's selectable launch models (empty ⇒ picker offers 既定
// [auto] only).
func Models() []agents.ModelChoice {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	if modelsList != nil && time.Since(modelsAt) < modelsTTL {
		return modelsList
	}
	list, err := probeModels()
	if err != nil {
		return modelsList // stale-if-error
	}
	if freePlan() {
		list = freeUsableModels(list)
	}
	modelsList = list
	modelsAt = time.Now()
	return modelsList
}

// freeUsableModels keeps only the models a Free plan can actually launch — the
// Composer family（Auto はピッカーの 既定として別枠なのでここには元々居ない）。named
// model は Free では `ActionRequiredError` になるので隠す。有料は Models() が呼ばない。
func freeUsableModels(list []agents.ModelChoice) []agents.ModelChoice {
	out := make([]agents.ModelChoice, 0, len(list))
	for _, m := range list {
		if strings.HasPrefix(m.ID, "composer") {
			out = append(out, m)
		}
	}
	return out
}

// --- Free プラン判定（`cursor-agent about` の Subscription Tier）------------------

var tierMu sync.Mutex
var tierAt time.Time
var tierFree, tierKnown bool

// freePlan reports whether the signed-in account is on the Free plan. Cached with
// the same TTL as the model catalog（アップグレード反映は最大 10 分）。判定不能時は
// 直近既知値（無ければ false＝過剰制限しない）。
func freePlan() bool {
	tierMu.Lock()
	defer tierMu.Unlock()
	if tierKnown && time.Since(tierAt) < modelsTTL {
		return tierFree
	}
	free, ok := probeFreePlan()
	if !ok {
		return tierFree // stale-if-error
	}
	tierFree, tierKnown, tierAt = free, true, time.Now()
	return tierFree
}

// aboutTierRe matches the `Subscription Tier   Free` row of `cursor-agent about`
// （列は空白揃え・実測 v2026.07.20）。
var aboutTierRe = regexp.MustCompile(`(?mi)^\s*Subscription Tier\s+(\S+)`)

func probeFreePlan() (free bool, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := probeCmd(ctx, disableAutoUpdateFlag, "about").Output()
	if err != nil {
		return false, false
	}
	m := aboutTierRe.FindStringSubmatch(string(out))
	if m == nil {
		return false, false // 書式ドリフト — 未知として扱う（絞り込まない）
	}
	return strings.EqualFold(strings.TrimSpace(m[1]), "free"), true
}

// modelRowRe matches one `id - Display Name` catalog row（実測）。id は英小文字
// 始まりの [a-z0-9.-] 語彙。行末の "(current, default)" 等の注記は label から剥がす。
var modelRowRe = regexp.MustCompile(`^([a-z][a-z0-9.\-]*)\s+-\s+(.+)$`)
var annotationRe = regexp.MustCompile(`\s*\((?:current|default)[^)]*\)\s*$`)

func probeModels() ([]agents.ModelChoice, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// --disable-auto-update を前置（models は最大 20s 走り得て、起動2秒後の背景更新
	// トリガに掛かる可能性がある — root option なのでサブコマンドの前）。
	out, err := probeCmd(ctx, disableAutoUpdateFlag, "models").Output()
	if err != nil {
		return nil, err
	}
	return parseModels(string(out)), nil
}

// parseModels extracts the model catalog from `cursor-agent models` output.
func parseModels(s string) []agents.ModelChoice {
	seen := map[string]bool{}
	var list []agents.ModelChoice
	for _, ln := range strings.Split(s, "\n") {
		m := modelRowRe.FindStringSubmatch(strings.TrimSpace(ln))
		if m == nil {
			continue
		}
		id := m[1]
		if id == "auto" || seen[id] { // auto は既定＝フラグ無し（カタログから除外）
			continue
		}
		seen[id] = true
		label := strings.TrimSpace(annotationRe.ReplaceAllString(m[2], ""))
		if label == "" {
			label = id
		}
		list = append(list, agents.ModelChoice{ID: id, Label: label})
	}
	if list == nil {
		return []agents.ModelChoice{} // 非 nil 空: 描画ドリフト時も既定のみで安全側
	}
	return list
}
