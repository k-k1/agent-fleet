//go:build clicontract

// claude 契約テスト（**実ターンを消費する**）: 発言時点からの分岐（docs/55）の唯一の
// ドリフト検知。
//
// なぜ必要か: claude だけは公式の分岐点 API を使えない（`--resume-session-at` は print
// モード限定で、AF の claude は TUI 起動しかない）。代わりに転写 jsonl を自分で切り詰めて
// いるので、**claude の転写スキーマや resume の解釈が動いた瞬間に静かに壊れる**。しかも
// 壊れ方は「起動はするが会話が変」なので、合成テストでは絶対に気づけない。ADR 0039 の
// 決定9はこの一本を CLI ピン更新のたびに回すことを求めている。
//
// ここで固定する契約は 2 つだけ:
//  1. 手で切り詰めた jsonl を claude が resume できる（起動が拒否されない）
//  2. 切り詰めた後の履歴だけを見ている（切り落とした発言を覚えていない）
//
// 実クレデンシャルとサブスク枠を使うので opt-in:
//
//	CLAUDE_CONTRACT_LIVE=1 go test -tags clicontract -run TestContractLiveClaudeForkAt ./internal/agents/claude/
//
// コスト: haiku で 3 ターン（各 1 行の応答）。作業用の会話は scratch なプロジェクト
// ディレクトリに作り、後片付けで転写ごと消す。
package claude

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func requireClaudeLive(t *testing.T) {
	t.Helper()
	if os.Getenv("CLAUDE_CONTRACT_LIVE") != "1" {
		t.Skip("CLAUDE_CONTRACT_LIVE!=1 — real-credential claude contract skipped")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skipf("claude not on PATH: %v", err)
	}
}

// claudeUUID mints a session id claude accepts for --session-id.
func claudeUUID(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("/proc/sys/kernel/random/uuid")
	if err != nil {
		t.Skipf("no uuid source: %v", err)
	}
	return strings.TrimSpace(string(b))
}

// claudePrint runs one headless turn and returns its output.
func claudePrint(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("claude", append(args, "--model", "haiku")...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = cmd.CombinedOutput()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(240 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("claude %v timed out", args)
	}
	if err != nil {
		t.Fatalf("claude %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestContractLiveClaudeForkAt(t *testing.T) {
	requireClaudeLive(t)

	// A scratch working dir gives the conversation its own project folder under the real
	// config dir (auth lives there, so it can't be isolated) — removed at the end.
	work := t.TempDir()
	src := claudeUUID(t)
	dst := claudeUUID(t)

	claudePrint(t, work, "--session-id", src, "-p", "Remember the codeword ALPHA. Reply exactly: OK")
	claudePrint(t, work, "--resume", src, "-p", "Forget that. The codeword is now BETA. Reply exactly: OK")

	paths := jsonlPaths(src)
	if len(paths) == 0 {
		t.Fatal("no transcript written for the source session")
	}
	projectDir := filepath.Dir(paths[0])
	t.Cleanup(func() {
		// 後片付け: この scratch 会話の転写を消す（実 config dir を汚したままにしない）。
		_ = os.RemoveAll(projectDir)
	})

	lines, _, _ := TranscriptRead(src)
	turns := CollectTurns(lines, 0, len(lines))
	var anchors []string
	for _, tn := range turns {
		if tn.Role == "user" && tn.AnchorID != "" {
			anchors = append(anchors, tn.AnchorID)
		}
	}
	if len(anchors) < 2 {
		t.Fatalf("found %d anchored user turns in a real transcript, want >= 2 — claude's uuid "+
			"field or the user-line shape moved, so 「ここから分岐」 has nothing to point at", len(anchors))
	}

	// Branch before the SECOND prompt: the branch must remember ALPHA and not BETA.
	if err := MaterializeForkAt(src, dst, anchors[1]); err != nil {
		t.Fatalf("MaterializeForkAt against a REAL transcript failed: %v — the cut-point rules in "+
			"forkat.go no longer match what claude writes", err)
	}
	out := claudePrint(t, work, "--resume", dst, "-p", "What is the codeword? Answer with one word.")
	up := strings.ToUpper(out)
	switch {
	case strings.Contains(up, "ALPHA"):
		// 契約どおり: 切り詰め後の履歴だけを見ている。
	case strings.Contains(up, "BETA"):
		t.Fatalf("the branch remembered the turn we cut away (answered %q) — claude no longer "+
			"reconstructs the conversation from the file we wrote, so every point fork silently "+
			"carries history it should not", out)
	default:
		t.Fatalf("the branch could not answer from the carried history (%q) — a truncated transcript "+
			"is no longer resumable the way docs/55 §55.2 measured", out)
	}
}
