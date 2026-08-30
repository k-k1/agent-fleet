package copilot

// copilot の起動時モデルカタログ — プラン連動のライブ取得（docs/log/36 追補）。
//
// copilot CLI に列挙口が無く（/model は TUI 専用・ACP configOptions にも model は
// 無い — 実測）、しかも**モデルの可否はプラン依存**（Copilot Free は Auto のみで、
// 正しい ID でも --model は "not available" で起動失敗 — 実測）。静的リストは
// Free プランで「選べるのに必ず失敗する」選択肢を並べてしまうため、TUI /model
// ピッカーを PTY でスクレイプして**そのアカウントで実際に選べる一覧**を返す:
//   - Free 系バナー（"currently includes only Auto"）→ 空リスト＝ピッカーは
//     既定（auto ルーティング）だけを出す
//   - バナー無し → ピッカーの行がそのままカタログ（プラン反映済み）
// agy の /usage スクレイプと同じ agents.Flow 基盤・キャッシュ（stale-if-error）。
// プローブは使い捨ての COPILOT_HOME で行うので実セッション一覧を汚さない。
// 認証は gh 由来トークンの明示注入（隔離 HOME では ambient 認証が切れる — 実測）。

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// Efforts は `--effort` の CLI 受理値（v1.0.73 --help）。DefaultEffort は空 =
// CLI 既定に任せる。
var copilotEfforts = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

// modelsTTL: ピッカーは起動モーダルを開くたびに叩かれるが、プローブは TUI 起動
// 込みで数秒かかる。プランやカタログの変化は稀なので長めに持つ。
const modelsTTL = 10 * time.Minute

var modelsMu sync.Mutex
var modelsAt time.Time
var modelsList []agents.ModelChoice // nil = 未取得/失敗, 非nil空 = Auto のみ（確定）

func Models() []agents.ModelChoice {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	if modelsList != nil && time.Since(modelsAt) < modelsTTL {
		return modelsList
	}
	list, err := probeModels()
	if err != nil {
		return modelsList // stale-if-error: 失敗時は前回値（無ければ nil = 既定のみ）
	}
	modelsList = list
	modelsAt = time.Now()
	return modelsList
}

// probeModels launches a throwaway copilot TUI, opens /model and parses the
// picker. The whole probe runs against a temp COPILOT_HOME.
func probeModels() ([]agents.ModelChoice, error) {
	tok := Token()
	if tok == "" {
		return nil, errNoAuth
	}
	home, err := os.MkdirTemp("", "copilot-models-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(home)
	work, err := os.MkdirTemp("", "copilot-models-cwd-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	// 事前 trust: 使い捨て HOME なので trust ダイアログが必ず出る側。
	ensureFolderTrustedIn(home, work)

	bin := os.Getenv("AGENT_COPILOT_BIN")
	if bin == "" {
		bin = "copilot"
	}
	cmd := exec.Command(bin, "--no-remote", "--no-remote-export")
	cmd.Dir = work
	cmd.Env = append(os.Environ(),
		"COPILOT_HOME="+home,
		"COPILOT_AUTO_UPDATE=false",
		"COPILOT_GITHUB_TOKEN="+tok,
		"TERM=xterm-256color",
	)
	f, err := agents.StartFlow(cmd)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// コンポーザ描画待ち（フッタ "/ commands" — paneMode と同じ readiness 信号）。
	if !waitFor(f, "/ commands", 30*time.Second) {
		return nil, errProbeTimeout
	}
	// スラッシュメニューの確定は入力と Enter を別 write に（実測: 同時だと
	// ペースト折り畳みに食われる — TUI 経路と同じ癖）。
	if _, err := f.Ptmx.Write([]byte("/model")); err != nil {
		return nil, err
	}
	time.Sleep(400 * time.Millisecond)
	if _, err := f.Ptmx.Write([]byte("\r")); err != nil {
		return nil, err
	}
	if !waitFor(f, "Search models", 15*time.Second) {
		return nil, errProbeTimeout
	}
	// 描画が落ち着くのを一拍待ってから最終フレームを解析する。
	time.Sleep(500 * time.Millisecond)
	return parseModelPicker(f.Clean()), nil
}

var errNoAuth = errStr("copilot models: GitHub 連携が無くプローブできません")
var errProbeTimeout = errStr("copilot models: /model ピッカーの描画を検出できません")

type errStr string

func (e errStr) Error() string { return string(e) }

func waitFor(f *agents.Flow, marker string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(f.Clean(), marker) {
			return true
		}
		time.Sleep(300 * time.Millisecond)
	}
	return false
}

// freePlanRe matches the /model banner shown when explicit model selection is
// plan-gated（実測 v1.0.73: "Your Copilot Free plan currently includes only
// Auto, which automatically selects ..."）。将来の文言微修正に備えプラン名は
// 固定しない。
var freePlanRe = regexp.MustCompile(`plan currently includes only Auto`)

// modelRowRe matches one picker row's model id（実測の語彙: gpt-5.6-sol /
// claude-sonnet-4.6 / gemini-3.1-pro-preview / kimi-k2.7-code …）。ピッカーの
// 装飾（❯ / ✓ / 罫線）を除いた行が完全に id 形であることを要求し、会話文や
// フッタの混入を防ぐ。
var modelRowRe = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[.-][a-z0-9]+)*$`)

// parseModelPicker extracts the selectable model list from the cleaned PTY
// stream. Free 系バナーがあれば「Auto のみ」＝空リスト（非 nil）を返す。
func parseModelPicker(clean string) []agents.ModelChoice {
	if freePlanRe.MatchString(clean) {
		return []agents.ModelChoice{}
	}
	seen := map[string]bool{}
	var list []agents.ModelChoice
	for _, ln := range strings.Split(clean, "\n") {
		t := strings.TrimSpace(ln)
		// 行頭のカーソル/選択マーカーと行末の選択チェック・スクロールバー描画を剥がす。
		t = strings.TrimPrefix(t, "❯")
		t = strings.TrimSuffix(t, "✓")
		for _, glyph := range []string{"█", "┃", "│"} {
			t = strings.ReplaceAll(t, glyph, "")
		}
		t = strings.TrimSpace(t)
		if t == "" || strings.EqualFold(t, "auto") || seen[t] {
			continue
		}
		if !modelRowRe.MatchString(t) {
			continue
		}
		seen[t] = true
		list = append(list, agents.ModelChoice{ID: t, Label: t, Efforts: copilotEfforts})
	}
	if list == nil {
		// バナーも行も取れなかった（ピッカーは出たが解析空振り＝描画ドリフト）。
		// 空リストを返す — ピッカーが 既定（auto）だけになる安全側で、誤った
		// 選択肢を出すよりよい。live テストがドリフトの一次検知。
		return []agents.ModelChoice{}
	}
	return list
}
