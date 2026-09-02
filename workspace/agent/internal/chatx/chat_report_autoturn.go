package chatx

// 完了報告の自動ターンの束ね（デバウンス）。
//
// リコンサイラの配送（deliverReportCard）は報告カードを会話へ**即時**追記するが、
// オペレーターの自動ターンは報告1件ごとに回さず、短い窓で束ねてから1回だけ回す。
// 自動ターンは毎回、会話の全コンテキスト（システムプロンプト・ツールスキーマ・
// 履歴）をプロバイダに再読させる高価な呼び出しで、しかも runReportAutoTurn は
// もともと**未配信の報告を全部まとめて1ターンに載せる**設計（undeliveredReports）
// — 束ねる仕組みは既にあり、足りなかったのは「少し待つ」ことだけだった。
// 複数セッションが近接して完了する典型場面（並行指示の収束・sweep の同一 tick）で、
// ターン数＝コンテキスト再読の回数が報告数から窓数へ落ちる。
//
// 遅れるのは**オペレーターの追撃ターンだけ**: 報告カード自体は即時に会話と通知
// センターへ出るので、利用者から見える完了通知は遅れない。窓の間に利用者が発話
// すれば報告はそのターンに相乗りし（injectPendingReports）、後から発火するタイマー
// は未配信ゼロを見て no-op になる。
//
// interim（question / plan-approval）の即時ターンはここを通らない（chat_report.go
// deliverSessionReport）: 質問への回答はレイテンシがそのまま利用者体験になる経路
// なので、束ねの対象にしない（docs/log/30）。

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
	"os"
	"strconv"
	"sync"
	"time"
)

// chatAutoTurnDelayDefault is the bundling window. リコンサイラの settle は
// tick(15s)×2 のデバウンスを持つため、並行セッションの完了は数十秒の幅に散って
// 届く — 窓はそれを1ターンに畳める長さにする。設定（設定 > アシスタント「自動応答の
// 束ね時間」・ui-prefs assistantAutoTurnDelay 秒）または AF_CHAT_AUTOTURN_DELAY（秒）
// で上書き可、0 で即時（従来挙動）。
const ChatAutoTurnDelayDefault = 60 * time.Second

// chatAutoTurnDelayMax caps the configurable window: これ以上遅らせても束ね効果は
// 頭打ちで、報告への追撃だけが遅くなる。
const ChatAutoTurnDelayMax = 10 * time.Minute

// chatAutoTurnDelay returns the effective bundling window（設定 → env → 既定）。
func ChatAutoTurnDelay() time.Duration {
	if v, ok := uiprefs.Read()["assistantAutoTurnDelay"].(float64); ok && v >= 0 {
		d := time.Duration(v) * time.Second
		if d > ChatAutoTurnDelayMax {
			return ChatAutoTurnDelayMax
		}
		return d
	}
	if v := os.Getenv("AF_CHAT_AUTOTURN_DELAY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
	}
	return ChatAutoTurnDelayDefault
}

// autoTurnScheduler runs one deferred turn per conversation per window.
type autoTurnScheduler struct {
	delay func() time.Duration
	run   func(convID string)

	mu      sync.Mutex
	pending map[string]*time.Timer
}

func newAutoTurnScheduler(delay func() time.Duration, run func(convID string)) *autoTurnScheduler {
	return &autoTurnScheduler{delay: delay, run: run, pending: map[string]*time.Timer{}}
}

// reportAutoTurns is the process-wide scheduler (プロセスが落ちれば窓は消えるが、
// 未配信の報告は次のターン投入時に injectPendingReports が拾う — 即時起動時代の
// go runReportAutoTurn が失われるのと同じ縮退で、消失にはならない)。
var reportAutoTurns = newAutoTurnScheduler(ChatAutoTurnDelay, runReportAutoTurn)

// schedule requests one operator turn for the conversation after the bundling
// window. 窓は最初の報告が開き、以後の報告は同じ発火に相乗りする（届くたびの
// リセットはしない — リセット式だと報告が窓より短い間隔で届き続ける限りターンが
// 飢える。固定窓なら遅延の上限＝窓長が保証される）。
func (s *autoTurnScheduler) schedule(convID string) {
	d := s.delay()
	if d <= 0 {
		go s.run(convID)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.pending[convID]; ok {
		return
	}
	s.pending[convID] = time.AfterFunc(d, func() {
		s.mu.Lock()
		delete(s.pending, convID)
		s.mu.Unlock()
		s.run(convID)
	})
}
