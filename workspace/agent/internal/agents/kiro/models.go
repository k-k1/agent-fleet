package kiro

// 起動時モデルカタログ — アカウント連動のライブ取得（docs/log/43 §2.6）。kiro は
// `kiro-cli chat --list-models -f json` が完全機械可読の JSON を返す（cursor の行
// スクレイプ不要）。`auto`（既定・1M ctx・フラグ無し）はカタログから外す。**Free
// プランでも named モデル指定可**（実測）なので cursor のような Free 絞り込みは不要。
// 10 分キャッシュ・stale-if-error。effort はモデルと独立の別フラグ（--effort）なので
// ここでは畳まない（program.go が m.Effort をそのまま渡す）。

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

const modelsTTL = 10 * time.Minute

// kiroDefaultWindow is the fallback context window (tokens) used when the catalog
// hasn't been fetched or the running model id isn't in it. It only affects the token
// COUNT shown next to kiro's live context %; the % itself round-trips exactly because
// the window is passed explicitly to the ContextBar (see context.go / session_usage.go).
const kiroDefaultWindow = 200_000

var modelsMu sync.Mutex
var modelsAt time.Time
var modelsList []agents.ModelChoice // nil = 未取得/失敗
var modelWindows map[string]int     // model_id → context_window_tokens（auto 含む・nil=未取得）

// listModelsOut is the shape of `kiro-cli chat --list-models -f json`（実測 2.14.1）。
type listModelsOut struct {
	Models []struct {
		ModelName           string `json:"model_name"`
		Description         string `json:"description"`
		ModelID             string `json:"model_id"`
		ContextWindowTokens int    `json:"context_window_tokens"`
	} `json:"models"`
	DefaultModel string `json:"default_model"`
}

// Models returns the account's selectable launch models (empty ⇒ picker offers 既定
// [auto] only).
func Models() []agents.ModelChoice {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	return ensureCatalogLocked()
}

// ModelWindow returns the context-window token count for a model id (incl "auto"), from
// the cached `--list-models` catalog (Track D — pct→token 変換用)。未取得/不明は 0。
func ModelWindow(id string) int {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	ensureCatalogLocked()
	return modelWindows[id]
}

// ensureCatalogLocked refreshes the model list + window map when stale, returning the
// current list (stale-if-error). Caller holds modelsMu.
func ensureCatalogLocked() []agents.ModelChoice {
	if modelsList != nil && time.Since(modelsAt) < modelsTTL {
		return modelsList
	}
	list, windows, err := probeModels()
	if err != nil {
		return modelsList // stale-if-error（windows も前回値を温存）
	}
	modelsList, modelWindows, modelsAt = list, windows, time.Now()
	return modelsList
}

func probeModels() ([]agents.ModelChoice, map[string]int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin(), "chat", "--list-models", "-f", "json").Output()
	if err != nil {
		return nil, nil, err
	}
	list, windows := parseModels(out)
	return list, windows, nil
}

// parseModels extracts the catalog from the --list-models JSON. The picker list drops
// `auto`（既定＝フラグ無し）; the window map keeps EVERY model incl auto (the pct→token
// conversion needs auto's 1M window too). Label は description（読みやすさ優先）、無ければ
// model_name。
func parseModels(b []byte) ([]agents.ModelChoice, map[string]int) {
	windows := map[string]int{}
	var lm listModelsOut
	if json.Unmarshal(b, &lm) != nil {
		return []agents.ModelChoice{}, windows // 非 nil 空: 描画ドリフト時も既定のみで安全側
	}
	seen := map[string]bool{}
	var list []agents.ModelChoice
	for _, m := range lm.Models {
		id := strings.TrimSpace(m.ModelID)
		if id == "" {
			continue
		}
		if m.ContextWindowTokens > 0 {
			windows[id] = m.ContextWindowTokens
		}
		if id == "auto" || seen[id] {
			continue
		}
		seen[id] = true
		label := strings.TrimSpace(m.Description)
		if label == "" {
			label = m.ModelName
		}
		if label == "" {
			label = id
		}
		list = append(list, agents.ModelChoice{ID: id, Label: label})
	}
	if list == nil {
		list = []agents.ModelChoice{}
	}
	return list, windows
}
