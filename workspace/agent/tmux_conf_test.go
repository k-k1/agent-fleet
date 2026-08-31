package main

// 出荷する tmux 設定（workspace/tmux.conf → イメージの /etc/tmux.conf）のうち、
// **体感遅延に直結する値**を固定する。
//
// escape-time は「Esc 単独か、Esc で始まるシーケンス（矢印・F キー）の先頭か」を
// 見分けるための待ち時間で、tmux 3.3a の既定は 500ms。実測（この worktree の
// コンテナ内、tmux 3.3a）:
//
//	既定 500 → Esc のエコー 501ms / 20 → 20ms
//
// つまり設定を落とすと claude・codex・vim の Esc が必ず 0.5 秒遅れる。ここでの入力元は
// xterm.js で、1 回の onData = 1 WebSocket フレームとしてシーケンスを分割せず送るため
// 長い猶予は要らない。設定ごと消える（＝既定に戻る）と、症状は「ターミナルが何となく
// 遅い」としか現れず、原因に辿り着くまでが遠い — だから値そのものを縛る。
import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

func TestShippedTmuxConfKeepsEscapeTimeLow(t *testing.T) {
	b, err := os.ReadFile("../tmux.conf")
	if err != nil {
		t.Fatalf("read workspace/tmux.conf: %v", err)
	}
	m := regexp.MustCompile(`(?m)^\s*set\s+-sg\s+escape-time\s+(\d+)\s*$`).FindSubmatch(b)
	if m == nil {
		t.Fatal("workspace/tmux.conf sets no escape-time — tmux falls back to its 500ms default, " +
			"which delays every Esc in a TUI session by half a second")
	}
	ms, err := strconv.Atoi(string(m[1]))
	if err != nil {
		t.Fatalf("escape-time is not a number: %q", m[1])
	}
	// 0 にはしない: 0 は「待たない」で、フレームが割れた稀なケースで矢印キーが
	// Esc + "[A" のリテラルに化ける。数十 ms あれば人には気付かれず、割れも吸収する。
	if ms <= 0 || ms > 50 {
		t.Fatalf("escape-time = %d ms; want 1..50 (500 = tmux's default and half a second of Esc lag)", ms)
	}
}
