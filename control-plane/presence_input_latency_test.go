package main

// noteInput は端末の**打鍵中継の上**で呼ばれる（proxy.go relay: onInput() を通してから
// キーを転送する）。したがってここで待った時間は、そのキーがエコーバックされるまでの
// 遅延にそのまま乗る。以前はここで recordWorkspaceActivity を同期呼び出ししていたので、
// 畳み込みの切れる 5 秒に 1 回、ある打鍵だけが DB 1 往復ぶん止まっていた。
//
// 端末は 1 文字の往復で品質が決まる面なので、在席の記録は打鍵を待たせてはいけない。
// この表が壊っても機能は動いてしまい（在席も記録されるし、キーも届く）、症状は
// 「ときどき入力が引っかかる」としか出ないので、時間そのものを検査する。
import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// slowActivityStore は DB が遅い日を再現する。ほかの Store メソッドは使わないので
// 埋め込みの nil のまま（呼ばれたら panic して気付ける）。
type slowActivityStore struct {
	Store
	delay time.Duration
	calls atomic.Int32
}

func (s *slowActivityStore) RecordWorkspaceActivity(context.Context, string, string, string, string) (bool, error) {
	s.calls.Add(1)
	time.Sleep(s.delay)
	return true, nil
}

func TestTerminalNoteInputDoesNotBlockOnTheStore(t *testing.T) {
	const wsID = "ws-latency"
	st := &slowActivityStore{delay: 300 * time.Millisecond}
	m := &manager{conns: newConnRegistry(), store: st}

	release, noteInput, err := m.trackWorkspaceTerminal(context.Background(), wsID, "s1")
	if err != nil {
		t.Fatalf("trackWorkspaceTerminal: %v", err)
	}
	defer release()

	// アタッチ時の 1 回（force）で 5 秒の畳み込みが張られる。打鍵が実際に書き込みへ
	// 進む状態を作るため、その保護を明示的に切る。
	m.mu.Lock()
	m.activityProtectedUntil[wsID] = time.Time{}
	m.mu.Unlock()
	before := st.calls.Load()

	// 連続打鍵。★ここが 1 回でも store の応答時間（300ms）を待ったら、その打鍵は
	// 300ms 遅れて画面に出る。
	start := time.Now()
	for i := 0; i < 50; i++ {
		noteInput()
	}
	elapsed := time.Since(start)
	if elapsed > 50*time.Millisecond {
		t.Fatalf("50 打鍵の中継に %v かかった（store の 1 往復 = %v）— 打鍵経路で "+
			"在席の書き込みを待っている", elapsed, st.delay)
	}

	// 待たせないだけでなく、記録は落とさない（畳まれて非同期に 1 回は走る）。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if st.calls.Load() > before {
			// 在席の in-memory 側は即時であること — reaper の判定はこちらを読む。
			if !m.conns.watched(wsID, time.Minute, time.Now()) {
				t.Fatal("打鍵したのに在席と数えられていない")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("打鍵しても共有ウォーターマークが一度も進まなかった（非同期化で書き込みごと落ちている）")
}
