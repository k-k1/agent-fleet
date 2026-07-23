package cursor

// 起動時モデルカタログ — アカウント連動のライブ取得（docs/40 決定 6）。cursor は
// `cursor-agent models` が公式にあり（copilot の TUI スクレイプ不要）、`id - 表示名`
// の行を返す（実測 v2026.07.20）。effort はモデル ID 自体に畳まれている
// （例 gpt-5.3-codex-high / claude-opus-4-8-thinking-high）ので別 Efforts は付けない。
// `auto`（既定・フラグ無し）はカタログから外す。10 分キャッシュ・stale-if-error。

import (
	"context"
	"os/exec"
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
	modelsList = list
	modelsAt = time.Now()
	return modelsList
}

// modelRowRe matches one `id - Display Name` catalog row（実測）。id は英小文字
// 始まりの [a-z0-9.-] 語彙。行末の "(current, default)" 等の注記は label から剥がす。
var modelRowRe = regexp.MustCompile(`^([a-z][a-z0-9.\-]*)\s+-\s+(.+)$`)
var annotationRe = regexp.MustCompile(`\s*\((?:current|default)[^)]*\)\s*$`)

func probeModels() ([]agents.ModelChoice, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin(), "models").Output()
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
