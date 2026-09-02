package agents

// 共有デーモン（codex app-server / opencode serve）の「需要ゼロで畳む」監視。
//
// どちらのデーモンも常駐コストが重い（実測 RSS: codex app-server 約 110 MB＝
// native 62 MB＋node シム 48 MB、opencode serve 約 305 MB）のに対し、冷起動は
// codex で 217 ms しかかからない。つまり「起動時から上げっぱなし」に見合う理由は
// なく、需要（managed ハンドル、および codex では --remote で動いている TUI
// セッション）が無い間は落としておくのが正しい。
//
// 監視は supervisor 側が Ensure 成功時に 1 本だけ張り、停止したら自分で終わる
// （次に必要になった Ensure が張り直す）。判定と停止の競合は stopIfIdle 側が
// ロック内で needs を再確認して潰す — ここは「生きているデーモンを引き抜く」に
// 直結するので、監視ループ側の判定だけを信じない。

import (
	"log"
	"os"
	"strconv"
	"time"
)

// idleTick は需要の観測間隔。停止までの猶予より十分細かければよい（var なのは
// テストが縮めるため）。
var idleTick = 15 * time.Second

// IdleGrace reads a "<n> 秒間ゼロなら停止" knob from env. 0 で自動停止を無効化
// （一度上げたら Agent が死ぬまで畳まない）。不正値は既定にフォールバックする。
func IdleGrace(env string, def time.Duration) time.Duration {
	v := os.Getenv(env)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	return time.Duration(n) * time.Second
}

// WatchIdle runs the observation loop: needs() が 0 の状態が grace 続いたら
// stopIfIdle() を呼ぶ。stopIfIdle が true（停止した／既に停止していた）を返したら
// ループを終える。false は「ロック内の再確認で需要が復活していた」の意味なので、
// 猶予を数え直して監視を続ける。grace<=0 なら監視自体を張らない。
func WatchIdle(name string, needs func() int, stopIfIdle func() bool, grace time.Duration) {
	if grace <= 0 {
		return
	}
	var idleSince time.Time
	for {
		time.Sleep(idleTick)
		if needs() > 0 {
			idleSince = time.Time{}
			continue
		}
		if idleSince.IsZero() {
			idleSince = time.Now()
			continue
		}
		if time.Since(idleSince) < grace {
			continue
		}
		if stopIfIdle() {
			return
		}
		// ロック内の再確認で需要が戻っていた（畳む直前にセッションが立った）。
		// 結果は supervisor 側が記録するので、ここでは数え直すだけ。
		log.Printf("%s: 需要ゼロ %s のあと停止を見送りました（需要が戻った）", name, grace)
		idleSince = time.Time{}
	}
}
