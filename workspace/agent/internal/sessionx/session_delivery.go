package sessionx

// 配達検証（docs/log/38 追補・sbk7oej 再発 2026-07-24 の恒久対策）。
//
// tmux send-keys の成功は「キーがペインに届いた」ことしか意味しない。CLI 側の一瞬の
// 受け付け不能 — resume 直後でスラッシュコマンド登録が終わる前・ペースト折り畳みに
// Enter が食われる・モーダルがタイプ文字を無視する等 — に当たると、タイプ済みの
// プロンプトは無音で消え、/input は 200 を返す。人が見ている Console 送信なら気付いて
// 打ち直せるが、無人経路（CP スケジューラの reuse 送信）はこの 200 をもって「fired」を
// 台帳に刻む — 実行されていないのに成功履歴になる偽陽性の級で、readiness ゲート
// （sbk7oej 修正第1弾）だけでは塞げなかった穴。
//
// ここでは成功の定義を「ターンが実際に始まった証拠」に引き上げる:
//   証拠 = claude の会話 jsonl に user ターンが追記された（提出の一次記録）
//        ∨ ペインが実行中スピナーを示す（jsonl フラッシュ遅延の保険）
// 証拠が出るまで待ち、出なければ Enter 再送（下書きが残っている＝Enter だけ食われた）
// → 全文再タイプ（下書きごと飲まれた）の自己修復を 1 巡試み、それでも出なければ
// delivery_unconfirmed を返す。CP スケジューラはこれを error: として記録し通知する。
//
// 検証は呼び出し元が confirm を明示したときだけ走る（オプトイン）。/model のような
// ターンを生まない UI スラッシュコマンドや、人が見ている Console 送信の意味論を
// 変えないため。検証手段が無い kind（非 claude の TUI）では no-op — 「検証できない」を
// 「配達失敗」と混同しない。

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

const (
	// deliveryConfirmWindow is how long the first evidence wait runs. A healthy claude
	// appends the user line in <1s; the window is generous for a cold resume on a busy
	// host, because a premature retry is worse than a slow confirm.
	deliveryConfirmWindow = 12 * time.Second
	// deliveryRetryWindow is the evidence wait after the one self-heal attempt.
	deliveryRetryWindow = 12 * time.Second
	deliveryPoll        = 500 * time.Millisecond
)

// deliverySnapshot is the pre-typing evidence baseline. logs == nil = この kind には
// 検証プリミティブが無い（confirmPromptDelivery は no-op で成功扱い）。
type deliverySnapshot struct {
	logs      map[string]int64 // 会話 jsonl のサイズ（提出の一次記録を差分で見る）
	subagents map[string]int64 // BG エージェントの転写（誤配達の検出用）
	paneBusy  bool             // **打つ前から**ペインが動いていたか
}

// deliveryBaseline snapshots the session's conversation log sizes before typing, so
// "a user turn was appended" is checkable afterward. claude only for now — the other
// TUI kinds have no equally cheap submit ground-truth, and today's unattended senders
// (the CP scheduler reuse send) target claude sessions.
//
// paneBusy も基線に含める: スピナーは「自分のプロンプトでターンが始まった」証拠として
// 使うものなので、打つ前から回っていた（前のターン・BG エージェント）なら証拠にならない。
func deliveryBaseline(m session.Meta) deliverySnapshot {
	if NormalizeKind(m.Kind) != session.KindClaude {
		return deliverySnapshot{}
	}
	sid := session.UUID(m.Dir, m.Name)
	return deliverySnapshot{
		logs:      claude.TranscriptSnapshot(sid),
		subagents: claude.SubagentSnapshot(sid),
		paneBusy:  tmuxx.IsBusy(m.Name),
	}
}

// deliveryEvidenced reports whether the prompt provably reached the session. The jsonl
// half also catches a turn that already FINISHED between polls (the user line persists)
// and the queued case (typed mid-turn: claude holds the prompt and replays it later).
//
// スピナーは jsonl のフラッシュ遅れを埋める保険としてだけ残す。**基線で既に動いていた
// 場合は数えない** — その回転は自分のプロンプトとは無関係で、実測ではペインに映っていた
// のが BG サブエージェントのスピナーだったせいで、メイン会話に届かなかった指示が
// 「配達済み」と判定されていた（2026-07-30 sannme2）。
func deliveryEvidenced(m session.Meta, base deliverySnapshot, prompt string) bool {
	if claude.PromptAcceptedSince(session.UUID(m.Dir, m.Name), base.logs, prompt) {
		return true
	}
	return !base.paneBusy && tmuxx.IsBusy(m.Name)
}

func awaitDeliveryEvidence(m session.Meta, base deliverySnapshot, prompt string, window time.Duration) bool {
	deadline := time.Now().Add(window)
	for {
		if deliveryEvidenced(m, base, prompt) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(deliveryPoll)
	}
}

// confirmPromptDelivery blocks until the just-typed prompt provably started a turn,
// self-healing once if it did not. nil = confirmed (or unverifiable kind).
func confirmPromptDelivery(m session.Meta, pane, prompt string, base deliverySnapshot) error {
	if base.logs == nil {
		return nil
	}
	if awaitDeliveryEvidence(m, base, prompt, deliveryConfirmWindow) {
		return nil
	}
	// 誤配達の検出: プロンプトが BG エージェントの転写に入っていたら、ペインの入力欄は
	// メイン会話ではなくそのエージェントに紐づいていた（agents 表示）。ここで自己修復に
	// 進むと同じ割り込みをもう一度エージェントへ撃ち込むだけなので、再送せずに落とす。
	// 通常はタイプ前のガード（session_io.go の agents_view）で弾かれるが、レールの表示が
	// 変わってガードが素通りしたときの最後の砦。
	if claude.SubagentReceivedSince(session.UUID(m.Dir, m.Name), base.subagents, prompt) {
		return fmt.Errorf("prompt was delivered to a background agent, not the session " +
			"(the pane's input box was bound to an agent); return the pane to the main conversation and resend")
	}
	// 自己修復: 下書きがまだコンポーザに見えている＝Enter だけ食われた → Enter 再送
	// （提出済みなら空コンポーザへの Enter は no-op なので安全）。下書きも消えている
	// ＝行ごと飲まれた → 全文を再タイプして提出し直す（証拠が無い＝何も提出されて
	// いないので二重実行にはならない）。
	if promptDraftVisible(tmuxx.CapturePane(session.TmuxName(m.Name)), prompt) {
		log.Printf("delivery: %s composer still holds the prompt — resending Enter", m.Name)
		_ = tmuxx.Cmd("send-keys", "-t", pane, "Enter").Run()
	} else {
		log.Printf("delivery: %s prompt vanished without a turn — retyping", m.Name)
		if err := typeLineAndSubmit(m.Name, pane, prompt); err != nil {
			return fmt.Errorf("delivery retry: %v", err)
		}
	}
	if awaitDeliveryEvidence(m, base, prompt, deliveryRetryWindow) {
		return nil
	}
	return fmt.Errorf("prompt did not become a turn (no user turn appended, pane not working)")
}

// promptDraftVisible reports whether the captured pane still shows the typed prompt as
// an unsubmitted composer draft. Best-effort pane heuristics: match the first line's
// head against the tail region where the composer sits. The head is kept short
// (12 runes, rune-safe) because the composer WRAPS long lines at pane width — a longer
// needle can straddle a wrap point and false-negative. A false positive only costs a
// harmless extra Enter (no-op on an empty composer); a false negative costs a retype,
// which is safe because this path is only reached when no turn evidence exists.
func promptDraftVisible(captured, prompt string) bool {
	first := strings.TrimSpace(strings.SplitN(prompt, "\n", 2)[0])
	if first == "" || captured == "" {
		return false
	}
	if r := []rune(first); len(r) > 12 {
		first = string(r[:12])
	}
	return strings.Contains(paneTail(captured, 6), first)
}
