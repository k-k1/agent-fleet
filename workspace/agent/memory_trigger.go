package main

// エージェントメモリの版管理（docs/39 / ADR 0022）— 自動 snapshot の契機。
//
// docs/39 は契機を「claude 全セッションの idle 遷移 + debounce」と書いているが、その遷移は
// `workspace-agent session-status` という**フックの別プロセス**で観測される（session_status.go）
// ため、常駐プロセス側に debounce タイマーを置けない。マーカーファイルで渡す手もあるが、
// フックの取りこぼしが即「snapshot が積まれない」に直結する。そこで同じ意味論を
// **常駐側のポーリング**で表現する:
//
//	毎 tick（既定 1 分）: ルート配下の最新 mtime を見る
//	  → 前回 snapshot 以降に変更があり
//	  → その変更が debounce（既定 5 分）以上静穏で
//	  → 対象 kind のセッションが誰も working でない
//	なら snapshot する。
//
// 走査は projects/*/memory に glob で限定するので、同じマウントにある 883MB の transcript は
// 一切 stat しない。ポーリングなのでフックの取りこぼしで履歴が欠けることがなく、docs/39 の
// 「15 分 tick の保険」も兼ねる。
//
// busy による先送りには上限（MaxDefer・既定 30 分）を置く。false-idle 対策で分かっている
// とおり状態マーカーは壊れ得る（停止済みセッションに working が残る等）ので、busy 判定の
// 誤りが「履歴が永久に積まれない」という最悪の壊れ方に化けないようにする。

import (
	"log"
	"os"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

const (
	memoryDefaultInterval = time.Minute
	memoryDefaultDebounce = 5 * time.Minute
	memoryDefaultMaxDefer = 30 * time.Minute
)

// memoryAutoEnabled は自動 snapshot の既定 ON（docs/39 決着 #1）。AF_MEMORY_SNAPSHOT=off
// で止められる（UI トグルは P2）。
func memoryAutoEnabled() bool {
	v := strings.TrimSpace(os.Getenv("AF_MEMORY_SNAPSHOT"))
	return !(v == "0" || strings.EqualFold(v, "off") || strings.EqualFold(v, "false"))
}

// memoryEnvDuration は AF_MEMORY_* の duration 上書きを読む（不正値は既定へフォールバック）。
func memoryEnvDuration(key string, def, min time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < min {
		log.Printf("memory-snapshot: invalid %s %q; using %v", key, v, def)
		return def
	}
	return d
}

func memorySnapshotInterval() time.Duration {
	return memoryEnvDuration("AF_MEMORY_SNAPSHOT_INTERVAL", memoryDefaultInterval, 5*time.Second)
}

func memorySnapshotDebounce() time.Duration {
	return memoryEnvDuration("AF_MEMORY_SNAPSHOT_DEBOUNCE", memoryDefaultDebounce, 0)
}

func memorySnapshotMaxDefer() time.Duration {
	return memoryEnvDuration("AF_MEMORY_SNAPSHOT_MAX_DEFER", memoryDefaultMaxDefer, time.Minute)
}

// memoryTriggerInput は契機判定に要る事実だけを束ねたもの。判定を純関数に切り出して
// おくことで、実時間を待たずにテストできる。
type memoryTriggerInput struct {
	Now          time.Time
	NewestChange time.Time     // ルート配下の最新 mtime（ゼロ = 対象ファイルなし）
	LastSnapshot time.Time     // 直近 snapshot の時刻（ゼロ = まだ 1 件も無い）
	Busy         bool          // 対象 kind のセッションが 1 つでも working
	Debounce     time.Duration // 静穏を要求する時間
	MaxDefer     time.Duration // busy による先送りの上限
}

// memoryShouldSnapshot は今 snapshot を積むべきかを返す。
func memoryShouldSnapshot(in memoryTriggerInput) bool {
	if in.NewestChange.IsZero() {
		return false // 対象ファイルが 1 つも無い
	}
	if !in.LastSnapshot.IsZero() && !in.NewestChange.After(in.LastSnapshot) {
		return false // 前回 snapshot 以降に変更なし（git 側の無変更 skip より手前で弾く）
	}
	if in.Now.Before(in.NewestChange.Add(in.Debounce)) {
		return false // まだ書き終わっていないかもしれない
	}
	if in.Busy {
		// 実行中セッションがあるうちは待つ。ただし待ち続けて履歴が永久に欠けるのは
		// 避けたいので、変更から MaxDefer 経つと busy を押し切って積む。
		return in.MaxDefer > 0 && !in.Now.Before(in.NewestChange.Add(in.MaxDefer))
	}
	return true
}

// memoryNewestChange は全ルートの allowlist 対象ファイルの最新 mtime を返す。
func memoryNewestChange() time.Time {
	var newest time.Time
	for _, r := range memoryRoots() {
		for _, f := range memoryCollect(r) {
			if t := time.Unix(f.MTime, 0); t.After(newest) {
				newest = t
			}
		}
	}
	return newest
}

// memoryKindsBusy は版管理対象 kind のセッションに working のものがあるかを返す。
// 状態は既存の状態検知（status ストア）から読む — snapshot のために新しい観測経路を
// 増やさない。
func memoryKindsBusy() bool {
	kinds := map[string]bool{}
	for _, r := range memoryRoots() {
		kinds[r.Kind] = true
	}
	for _, m := range session.ListMetas() {
		if !kinds[m.Kind] {
			continue
		}
		if status.LiveState(session.UUID(m.Dir, m.Name)) == "working" {
			return true
		}
	}
	return false
}

// startMemorySnapshotLoop は自動 snapshot のポーリングループを起こす（無効なら no-op）。
func startMemorySnapshotLoop() {
	if !memoryAutoEnabled() {
		log.Printf("memory-snapshot: auto snapshot disabled (AF_MEMORY_SNAPSHOT)")
		return
	}
	interval, debounce, maxDefer := memorySnapshotInterval(), memorySnapshotDebounce(), memorySnapshotMaxDefer()
	go func() {
		time.Sleep(45 * time.Second) // 起動直後の混雑（reconcile / cred seeding）を避ける
		for {
			memorySnapshotTick(time.Now(), debounce, maxDefer)
			time.Sleep(interval)
		}
	}()
}

// lastMemorySnapshotErr は同じ失敗の連投ログを抑える（1 分周期でログを埋めない）。
var lastMemorySnapshotErr string

// memorySnapshotTick は 1 周期分の判定と実行。ループ本体から切り出してテスト可能にしてある。
func memorySnapshotTick(now time.Time, debounce, maxDefer time.Duration) bool {
	if len(memoryRoots()) == 0 {
		return false
	}
	in := memoryTriggerInput{
		Now: now, NewestChange: memoryNewestChange(), LastSnapshot: memoryHeadTime(),
		Busy: memoryKindsBusy(), Debounce: debounce, MaxDefer: maxDefer,
	}
	if !memoryShouldSnapshot(in) {
		return false
	}
	res, err := memorySnapshot(memoryTriggerAuto, now)
	if err != nil {
		if msg := err.Error(); lastMemorySnapshotErr != msg {
			lastMemorySnapshotErr = msg
			log.Printf("memory-snapshot: %v", err)
		}
		return false
	}
	lastMemorySnapshotErr = ""
	if res.Committed {
		log.Printf("memory-snapshot: %s (%d files changed)", res.Rev[:8], len(res.Changed))
	}
	return res.Committed
}
