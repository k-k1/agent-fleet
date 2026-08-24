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
	st, _ := liveStateDetailFromFile(path)
	return st
}

// PendingPermission は未完了の許可要求の対象（"" = 許可待ちではない / 取れなかった）。
// docs/75 P5 の持ち越しが「何を訊かれていたか」を出すために読む。
func PendingPermission(m session.Meta) (string, bool) {
	sid := SessionID(m)
	if sid == "" {
		return "", false
	}
	st, detail := liveStateDetailFromFile(EventsPath(sid))
	return detail, st == "question"
}

func liveStateDetailFromFile(path string) (string, string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > tailWindow {
		if _, err := f.Seek(st.Size()-tailWindow, io.SeekStart); err == nil {
			// 途中から読むので最初の行は欠けている可能性が高い — 捨てる。
			br := bufio.NewReader(f)
			_, _ = br.ReadString('\n')
			return classifyDetail(br)
		}
	}
	return classifyDetail(bufio.NewReader(f))
}

func classify(r io.Reader) string {
	st, _ := classifyDetail(r)
	return st
}

// classifyDetail は classify に「未完了の許可が何を求めていたか」を足したもの
// （docs/75 P5 の持ち越し用）。detail は許可待ちのときだけ埋まり、取れなければ空。
func classifyDetail(r io.Reader) (string, string) {
	open := false                 // user.message / turn_start 以後、turn_end 前
	perms := map[string]bool{}    // requested かつ未 completed の requestId
	detail := map[string]string{} // requestId → 何を求めていたか（取れた分だけ）
	last := ""                    // 最後に requested された id（表示に使うのはこれ）
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 256*1024), 8*1024*1024)
	for sc.Scan() {
		var ev struct {
			Type string `json:"type"`
			Data struct {
				RequestID string `json:"requestId"`
				// 許可の対象。events.jsonl のスキーマは版で動くので、**取れたら使う**
				// 程度に扱う（取れなくても許可待ちの判定そのものは requestId だけで
				// 成立する）。空なら持ち越しカードは事実だけを述べる。
				ToolName string `json:"toolName"`
				Tool     string `json:"tool"`
				Command  string `json:"command"`
				Title    string `json:"title"`
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
				last = ev.Data.RequestID
				if d := firstNonEmpty(ev.Data.Title, ev.Data.Command, ev.Data.ToolName, ev.Data.Tool); d != "" {
					detail[ev.Data.RequestID] = d
				}
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
		if perms[last] {
			return "question", detail[last]
		}
		for id := range perms {
			return "question", detail[id]
		}
		return "question", ""
	}
	if open {
		return "working", ""
	}
	return "idle", ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
