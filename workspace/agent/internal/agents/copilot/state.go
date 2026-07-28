package copilot

// events.jsonl の末尾からの live 状態分類（working / question / idle）。copilot は
// status hook を持たないため、これが TUI ルートの唯一の状態ソース（agy の会話 DB
// probe に相当 — TUI 文字列非依存で false-idle 教訓に合致）。managed ルートでも
// 同じファイルを子プロセスが書くので整合する。
//
// 分類（v1.0.73 実測のイベント順序に基づく）:
//   - permission.requested に対応する permission.completed が無い → "question"
//     （許可メニュー/plan モードの承認待ち）
//   - user.message / assistant.turn_start の後に assistant.turn_end が無い →
//     "working"（ターン進行中。ルーティング中の turn_start 前ギャップも
//     user.message 起点で拾う）
//   - それ以外 → "idle"
//   - セッション未採番・ファイル無し → ""（起動中/不明 — 呼び出し側は状態なし扱い）

import (
	"bufio"
	"encoding/json"
	"io"
	"os"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// tailWindow bounds how much of events.jsonl the poll reads. 128KB は数ターン分
// （実測: 1 ターン 3〜60KB）— それより古い open マーカーはターン跨ぎで必ず
// 閉じられているか、そもそも表示済み。
const tailWindow = 128 * 1024

// LiveState classifies the session's live state ("" when unknowable).
func LiveState(m session.Meta) string {
	sid := SessionID(m)
	if sid == "" {
		return ""
	}
	return liveStateFromFile(EventsPath(sid))
}

func liveStateFromFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > tailWindow {
		if _, err := f.Seek(st.Size()-tailWindow, io.SeekStart); err == nil {
			// 途中から読むので最初の行は欠けている可能性が高い — 捨てる。
			br := bufio.NewReader(f)
			_, _ = br.ReadString('\n')
			return classify(br)
		}
	}
	return classify(bufio.NewReader(f))
}

func classify(r io.Reader) string {
	open := false              // user.message / turn_start 以後、turn_end 前
	perms := map[string]bool{} // requested かつ未 completed の requestId
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 256*1024), 8*1024*1024)
	for sc.Scan() {
		var ev struct {
			Type string `json:"type"`
			Data struct {
				RequestID string `json:"requestId"`
			} `json:"data"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "user.message", "assistant.turn_start":
			open = true
		case "assistant.turn_end":
			open = false
		case "permission.requested":
			if ev.Data.RequestID != "" {
				perms[ev.Data.RequestID] = true
			}
		case "permission.completed":
			delete(perms, ev.Data.RequestID)
		case "session.shutdown":
			// graceful 終了が刻まれた＝この後に走るものはない。開いたままの
			// ターンや許可はプロセス毎消えている。
			open = false
			perms = map[string]bool{}
		}
	}
	if len(perms) > 0 {
		return "question"
	}
	if open {
		return "working"
	}
	return "idle"
}
