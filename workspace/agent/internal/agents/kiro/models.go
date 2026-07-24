package kiro

// 起動時モデルカタログ — アカウント連動のライブ取得（docs/43 §2.6）。kiro は
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

var modelsMu sync.Mutex
var modelsAt time.Time
var modelsList []agents.ModelChoice // nil = 未取得/失敗

// listModelsOut is the shape of `kiro-cli chat --list-models -f json`（実測 2.14.1）。
type listModelsOut struct {
	Models []struct {
		ModelName   string `json:"model_name"`
		Description string `json:"description"`
		ModelID     string `json:"model_id"`
	} `json:"models"`
	DefaultModel string `json:"default_model"`
}

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

func probeModels() ([]agents.ModelChoice, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin(), "chat", "--list-models", "-f", "json").Output()
	if err != nil {
		return nil, err
	}
	return parseModels(out), nil
}

// parseModels extracts the catalog from the --list-models JSON, dropping `auto`
// (既定＝フラグ無し). Label は description（読みやすさ優先）、無ければ model_name。
func parseModels(b []byte) []agents.ModelChoice {
	var lm listModelsOut
	if json.Unmarshal(b, &lm) != nil {
		return []agents.ModelChoice{} // 非 nil 空: 描画ドリフト時も既定のみで安全側
	}
	seen := map[string]bool{}
	var list []agents.ModelChoice
	for _, m := range lm.Models {
		id := strings.TrimSpace(m.ModelID)
		if id == "" || id == "auto" || seen[id] {
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
		return []agents.ModelChoice{}
	}
	return list
}
