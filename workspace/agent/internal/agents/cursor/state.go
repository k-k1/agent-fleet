package cursor

// JSONL 転写末尾からの live 状態分類（working / idle）。cursor の TUI/-p ルートは
// user プロンプトで行を書き、応答/ツールを流し、最後に turn_ended を刻む（実測）
// ので、「開いたターンがあるか」で判定できる（copilot の events.jsonl 分類と同型・
// TUI 文字列非依存で false-idle 教訓に合致）。managed（ACP）ルートは転写を書かない
// ため、そちらの状態は driver の runTurn 境界が持つ（Track A2）。
//
// 許可待ち（TUI の allowlist 外コマンド確認）は JSONL に痕跡が残らないため v1 では
// "question" を出さない——ターンが開いたまま＝"working" として扱う（ミラーは進行中
// ＋停止ボタン）。許可カード化は Track D（docs/40）。

import (
	"bufio"
	"encoding/json"
	"io"
	"os"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// tailWindow bounds how much of the JSONL the poll reads. 128KB は数ターン分で
// 十分（それより古い開マーカーはターン跨ぎで必ず turn_ended により閉じている）。
const tailWindow = 128 * 1024

// LiveState classifies the session's live state ("" when unknowable —— チャット
// 未採番／転写ファイル未生成＝起動直後）。
func LiveState(m session.Meta) string {
	// managed（ACP）は転写を書かないので、下の JSONL 分類は常に空を返す。turn 状態機械
	// から供給しないと一覧のチップも reaper の分類も付かない（driver.go managedLiveState）。
	if m.DriverKind() == session.DriverManaged {
		return managedLiveState(m)
	}
	chatID := ChatID(m)
	if chatID == "" {
		return ""
	}
	path := transcriptPath(m.Dir, chatID)
	return liveStateFromFile(path)
}

func liveStateFromFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return "" // 未生成 — 不明（呼び出し側は状態なし扱い）
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > tailWindow {
		if _, err := f.Seek(st.Size()-tailWindow, io.SeekStart); err == nil {
			br := bufio.NewReader(f)
			_, _ = br.ReadString('\n') // 途中開始の欠け行を捨てる
			return classify(br)
		}
	}
	return classify(bufio.NewReader(f))
}

func classify(r io.Reader) string {
	open := false // role 行以後、turn_ended 前
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 256*1024), 8*1024*1024)
	for sc.Scan() {
		var ev struct {
			Role string `json:"role"`
			Type string `json:"type"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch {
		case ev.Type == "turn_ended":
			open = false
		case ev.Role == "user" || ev.Role == "assistant":
			open = true
		}
	}
	if open {
		return "working"
	}
	return "idle"
}
