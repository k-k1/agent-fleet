// runtime_health.go — Agent 到達待ち（/healthz）の共通部品。
//
// この面の契約はひとつだけ: **「Agent がまだ応答しない」は起動の失敗ではない**。
// Start はコンテナ/プロセスの起動を確定させるところまでを引き受け、Agent が応答する
// までの窓は State() の "starting" として表に出す。ECS アダプタは最初からそう作られて
// いて（watchReady・docs/log/62 §62.5「A readiness failure must still NEVER fail Start」）、
// ローカルの docker / native 2 つだけが「予算内に /healthz が 200 を返さなければ起動
// 失敗」だった。その不揃いが 3 つの実害を生んでいた:
//
//   - 実障害（docs/log/38 ★6）: 定時実行の wake が `agent did not become healthy within 15s`
//     で落ちた。真因は entrypoint の CLI 自己更新（実測 約60 秒）で、コンテナは正常。
//   - 同じ 15 秒が人手の Start にも効く。予算が 300 秒に伸びるのは自己更新 opt-in が
//     ON の起動だけなので、**OFF の利用者だけ**が lean/cold な起動・遅い回線・ネット
//     ワーク home で赤いトーストを踏み、しかも数秒後には普通に使える（起動は続いて
//     いたのだから）。「一部の人だけ、たまに」に見えるのはこれ。
//   - 失敗扱いそのものが状態の嘘になる。ensureWorkspaceStartedRTLocked は Start が
//     エラーなら DB を running にせず idle 時計も触らずに返すので、コンテナは走って
//     いるのに DB は stopped、reaper の in-memory lastSeen も古いまま、が残っていた。
//
// なので予算の数字をいじるのではなく、予算の**意味**を変える: 予算は「同期で待って
// あげる猶予」であって期限ではない。超えたら starting を名乗り、ポーラーが収束させる。
package runtime

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// agentBootBudget は「まだ起動途中だ」と名乗ってよい上限。同期猶予（startHealthWait）を
// 超えたあとも、この期限までは State() が "starting" を返す。
//
// 300 秒なのは native の rootfs 経路・ECS の startTimeout と同じ根拠（cold pull ＋
// entrypoint の boot-install ＋ CLI 自己更新の実測 約60 秒を包む）。**必ず時限式**に
// するのが肝で、収束しない "starting" は Console から停止も再作成もできない箱になる
// （docs/log/70 §70.14.6 の実害）。期限が切れたら素直に running（コンテナは在る）へ落ちる。
const agentBootBudget = 300 * time.Second

// agentReadyWait は「Agent に用がある API」がその場で待ってよい上限（AF_AGENT_READY_WAIT_SEC）。
// 既定 55 秒は ALB の idle timeout 60 秒（deploy/aws/ecs/cfn/30-ingress.yaml）の内側に
// 収めるため — HTTP ハンドラの中で待つ以上、ここを超えた瞬間に応答そのものが 504 で
// 消える（docs/log/62 §62.5 で計測済み）。待ちきれなければ 409 workspace_starting を返す。
// 起動は裏で続くので、利用者/Console の再試行が次に通る。
func agentReadyWait() time.Duration {
	if n := envInt("AF_AGENT_READY_WAIT_SEC", 0); n > 0 {
		return time.Duration(n) * time.Second
	}
	return 55 * time.Second
}

// agentHealthWait returns the Start health-wait budget: the adapter default,
// overridable via AF_AGENT_HEALTH_WAIT_SEC. Lean (boot-install) deployments
// need more than the classic 15s on FIRST start — the entrypoint downloads the
// pinned CLIs before the agent listens (docs/log/35 §35.4.1); the native rootfs
// adapter defaults higher for the same reason.
//
// ★ これは「これを過ぎたら失敗」ではなく「これを過ぎたら starting を名乗って返る」。
func agentHealthWait(def time.Duration) time.Duration {
	if n := envInt("AF_AGENT_HEALTH_WAIT_SEC", 0); n > 0 {
		return time.Duration(n) * time.Second
	}
	return def
}

// agentNotReadyError は「時間内に /healthz が 200 を返さなかった」だけを意味する。
// 文言は従来と 1 文字も変えていない（スケジュール実行履歴や運用の grep が拾っている）
// が、型で区別できるようにして、呼び出し側が **失敗と取り違えない**ようにする。
type agentNotReadyError struct{ timeout time.Duration }

func (e agentNotReadyError) Error() string {
	return fmt.Sprintf("agent did not become healthy within %s", e.timeout)
}

// agentHealthy は /healthz を 1 回だけ叩く。healthzClient は 5s cap（agent_client.go）。
func agentHealthy(ctx context.Context, endpoint string) bool {
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint+"/healthz", nil)
	if err != nil {
		return false
	}
	resp, err := healthzClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// waitAgentHealthy polls the Agent's /healthz until it answers 200 or the
// timeout lapses. Shared by the docker and native local adapters (ECS has its
// own converge loop) and by ensureWorkspaceReady.
//
// 返るエラーは 2 種類で、意味がまるで違う:
//   - agentNotReadyError … まだ来ていないだけ。起動は続いている。
//   - ctx のキャンセル   … 呼び出し側が去った（リクエスト打ち切り・lease 喪失）。
func waitAgentHealthy(ctx context.Context, endpoint string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		// キャンセル済み ctx で最大タイムアウトまでポーリングし続けない
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("agent health wait canceled: %w", err)
		}
		if agentHealthy(ctx, endpoint) {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return agentNotReadyError{timeout: timeout}
}

// agentStartingMarker は「この起動はまだ Agent の応答待ちだ」という印を workspace の
// dataDir に置く。プロセス内の変数ではなくファイルなのは 2 つの理由による:
//
//   - State() を呼ぶのは Start を走らせた goroutine とは限らない。/api/workspace の
//     4 秒ポーリング・SSE・proxy・reaper はそれぞれ別に Runtime を組み立てるので、
//     in-memory の印はそもそも見えない（見えなければ「running なのに Agent 不在」の
//     ままターミナルを開いてしまう、というのが直したい症状そのもの）。
//   - CP が起動の途中で落ちても、残るのはファイル 1 つだけにしたい。中身は期限なので、
//     期限切れでも /healthz 200 でも消える（自己修復）。
type agentStartingMarker struct{ path string }

// agentStartingMarkerIn は dataDir 直下の印。dataDir 未設定（テストの最小 struct 等）
// では無効な印を返す＝常に非 starting。
func agentStartingMarkerIn(dataDir string) agentStartingMarker {
	if dataDir == "" {
		return agentStartingMarker{}
	}
	return agentStartingMarker{path: filepath.Join(dataDir, ".agent-starting")}
}

// arm records "starting until <deadline>". Best-effort: 書けなければ従来どおり
// running に見えるだけで、壊れる方向には倒れない。
func (m agentStartingMarker) arm(until time.Time) {
	if m.path == "" {
		return
	}
	_ = os.WriteFile(m.path, []byte(strconv.FormatInt(until.Unix(), 10)+"\n"), 0o644)
}

func (m agentStartingMarker) clear() {
	if m.path == "" {
		return
	}
	_ = os.Remove(m.path)
}

// active reports whether this workspace is still inside its boot window. 印が在る間は
// 呼ばれるたびに /healthz を 1 回だけ叩き、上がっていれば印を落として running に戻す
// ので、Start を走らせた CP が居なくなっていても収束する。
func (m agentStartingMarker) active(ctx context.Context, endpoint string) bool {
	if m.path == "" {
		return false
	}
	b, err := os.ReadFile(m.path)
	if err != nil {
		return false
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil || !time.Now().Before(time.Unix(sec, 0)) {
		m.clear() // 期限切れ／壊れた印: これ以上 starting を名乗らない
		return false
	}
	if agentHealthy(ctx, endpoint) {
		m.clear()
		return false
	}
	return true
}
