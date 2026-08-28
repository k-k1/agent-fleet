package tmuxx

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// withFakeClock swaps the settle clock for a controllable one and clears the sightings so
// tests never inherit each other's (or a live pane's) state.
func withFakeClock(t *testing.T) *time.Time {
	t.Helper()
	now := time.Date(2026, 8, 28, 15, 0, 0, 0, time.UTC)
	orig := idleSettleNow
	idleSettleNow = func() time.Time { return now }
	sightMu.Lock()
	sights = map[string]paneSighting{}
	sightMu.Unlock()
	t.Cleanup(func() {
		idleSettleNow = orig
		sightMu.Lock()
		sights = map[string]paneSighting{}
		sightMu.Unlock()
	})
	return &now
}

func TestObserveFrameSettlesOnlyAfterTheWindow(t *testing.T) {
	now := withFakeClock(t)
	if observeFrame("s1", "frame A") {
		t.Fatal("初回観測で settled になった — 保守側（いま変わった扱い）に倒すべき")
	}
	*now = now.Add(idleSettleWindow - time.Second)
	if observeFrame("s1", "frame A") {
		t.Fatal("窓に 1 秒足りないのに settled になった")
	}
	*now = now.Add(2 * time.Second)
	if !observeFrame("s1", "frame A") {
		t.Fatal("同じ絵が窓を超えて続いたのに settled にならない — 詰まったセッションが永久に 進行中 に貼り付く")
	}
}

func TestObserveFrameResetsWhenThePaneRepaints(t *testing.T) {
	now := withFakeClock(t)
	observeFrame("s1", "frame A")
	*now = now.Add(idleSettleWindow - time.Second)
	if observeFrame("s1", "frame B") {
		t.Fatal("絵が変わったのに settled — 再描画は「生きている」証拠なので時計を巻き戻すこと")
	}
	*now = now.Add(idleSettleWindow - time.Second)
	if observeFrame("s1", "frame B") {
		t.Fatal("巻き戻した窓が効いていない")
	}
	*now = now.Add(2 * time.Second)
	if !observeFrame("s1", "frame B") {
		t.Fatal("新しい絵で窓を満たしたのに settled にならない")
	}
}

func TestObserveFrameIsPerSession(t *testing.T) {
	now := withFakeClock(t)
	observeFrame("s1", "frame A")
	*now = now.Add(idleSettleWindow / 2)
	observeFrame("s2", "frame A") // 同じ絵でもセッションが違えば時計は別
	*now = now.Add(idleSettleWindow/2 + time.Second)
	if !observeFrame("s1", "frame A") {
		t.Error("s1 は窓を超えている")
	}
	if observeFrame("s2", "frame A") {
		t.Error("s2 はまだ窓の途中 — セッション間で時計が漏れている")
	}
	ForgetPane("s1")
	if observeFrame("s1", "frame A") {
		t.Error("ForgetPane のあとも settled — 名前を再利用した別セッションが前任者の時計を継ぐ")
	}
}

// TestStreamingAnswerNeverSettles は実測列（testdata/streaming_answer）を実時刻どおりに
// 再生して、「回答本文を描いている最中」が settled にならないことを守る。
//
// 守っているのは settle 窓の値そのもの: この列の最長静止は 11.44 秒なので、
// idleSettleWindow をそれ以下に縮めるとこのテストが落ちる。落ちたときに直すべきなのは
// テストではなく窓のほうで、縮めれば「TUI に回答が流れている最中にバッジが 入力待ち へ
// 落ちる」実害（停止ボタンが消える・完了通知の早撃ち・アイドル判定への波及）が戻る。
func TestStreamingAnswerNeverSettles(t *testing.T) {
	frames := streamingFrames(t)
	if len(frames) < 10 {
		t.Fatalf("testdata/streaming_answer が痩せている（%d 枚）", len(frames))
	}
	now := withFakeClock(t)
	base := *now
	for _, f := range frames {
		// production の判定は「本文描画中」を待機と読む — それがこの窓の本体。
		if !atIdlePrompt(f.text) {
			t.Fatalf("%s: atIdlePrompt=false — 実測列の前提が変わった。testdata/streaming_answer/SOURCE.txt を読んで録り直すこと", f.name)
		}
		*now = base.Add(f.at)
		if observeFrame("streaming", f.text) {
			t.Fatalf("%s (+%s): 回答を描いている最中に settled になった — この列の最長静止 11.44s に対して idleSettleWindow=%s が短すぎる", f.name, f.at, idleSettleWindow)
		}
	}
}

type streamFrame struct {
	name string
	at   time.Duration
	text string
}

func streamingFrames(t *testing.T) []streamFrame {
	t.Helper()
	files, err := filepath.Glob("testdata/streaming_answer/*.txt")
	if err != nil || len(files) == 0 {
		t.Fatalf("testdata/streaming_answer が読めない: %v", err)
	}
	sort.Strings(files)
	var out []streamFrame
	for _, p := range files {
		name := filepath.Base(p)
		if name == "SOURCE.txt" {
			continue // 由来メモ（録り直し方はここに書いてある）
		}
		ms, err := strconv.Atoi(strings.TrimSuffix(name, ".txt"))
		if err != nil {
			t.Fatalf("%s: ファイル名は窓の先頭からの経過ミリ秒であること", name)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, streamFrame{name: name, at: time.Duration(ms) * time.Millisecond, text: string(b)})
	}
	return out
}
