//go:build clicontract

// copilot 契約テスト（**実ターンを消費する**）: 発言時点からの分岐（docs/55）のドリフト検知。
//
// copilot にも公式の分岐口が無く、session-state ディレクトリをコピーして events.jsonl を
// 切り詰めている。この手術が成立するのは **復元元が events.jsonl だから**で、それは実測で
// しか分からない（session.db が隣にあり、そちらが正なら「ミラーでは切れているのに
// エージェントは全部覚えている」になる）。合成テストはどちらでも緑のままなので、ここが
// 唯一の警報になる。
//
//	COPILOT_CONTRACT_LIVE=1 go test -tags clicontract -run TestContractLiveCopilotForkAt ./internal/agents/copilot/
//
// コスト: 実ターン 3 回（各 1 行の応答）。COPILOT_HOME を隔離するので実 ~/.copilot は
// 一切触らない（認証は環境の GitHub トークン / 保存済み資格情報を使う）。
package copilot

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func requireCopilotLive(t *testing.T) {
	t.Helper()
	if os.Getenv("COPILOT_CONTRACT_LIVE") != "1" {
		t.Skip("COPILOT_CONTRACT_LIVE!=1 — real-credential copilot contract skipped")
	}
	if _, err := exec.LookPath("copilot"); err != nil {
		t.Skipf("copilot not on PATH: %v", err)
	}
}

func copilotUUID(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("/proc/sys/kernel/random/uuid")
	if err != nil {
		t.Skipf("no uuid source: %v", err)
	}
	return strings.TrimSpace(string(b))
}

// copilotPrompt runs one headless turn in home/dir and returns its output.
func copilotPrompt(t *testing.T, home, dir, sid, prompt string) string {
	t.Helper()
	cmd := exec.Command("copilot", "--session-id", sid, "-p", prompt)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "COPILOT_HOME="+home)
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
		t.Fatalf("copilot -p timed out (%q)", prompt)
	}
	if err != nil {
		t.Fatalf("copilot -p %q: %v\n%s", prompt, err, out)
	}
	return string(out)
}

func TestContractLiveCopilotForkAt(t *testing.T) {
	requireCopilotLive(t)

	home := t.TempDir() // 隔離: 実 ~/.copilot には触れない
	work := t.TempDir()
	t.Setenv("COPILOT_HOME", home)
	// HOME は差し替えない — copilot の認証がそこから来るため（差し替えると exit 1）。
	// 代わりに sids へ書いた 1 件だけを後始末する。

	src := copilotUUID(t)
	copilotPrompt(t, home, work, src, "Remember the codeword ALPHA. Reply exactly: OK")
	copilotPrompt(t, home, work, src, "Forget that. The codeword is now BETA. Reply exactly: OK")

	// アンカーは production の転写経路から取る（イベントの id 形が変わればここで落ちる）。
	m := session.Meta{Dir: work, Name: "cp-contract", Kind: session.KindCopilot}
	slot := session.UUID(work, "cp-contract")
	sids.Write(slot, src)
	t.Cleanup(func() { sids.Remove(slot) })
	td, _ := (agentImpl{}).Transcript(m)
	var anchors []string
	for _, tn := range td.Turns {
		if tn.Role == "user" && tn.AnchorID != "" {
			anchors = append(anchors, tn.AnchorID)
		}
	}
	if len(anchors) < 2 {
		t.Fatalf("found %d anchored user turns in a real transcript, want >= 2 — events.jsonl's "+
			"id field or the user.message shape moved, so 「ここから分岐」 has nothing to point at", len(anchors))
	}

	resolved, err := (agentImpl{}).ResolveForkAt(m, agents.ForkPoint{Anchor: anchors[1]})
	if err != nil {
		t.Fatalf("ResolveForkAt: %v", err)
	}
	dst := copilotUUID(t)
	if err := MaterializeForkAt(src, dst, resolved); err != nil {
		t.Fatalf("MaterializeForkAt against a REAL session failed: %v", err)
	}
	// session.db は無改変のまま運ばれている＝両ターンが入っている。それでも切り詰めた
	// events.jsonl のほうが勝つ、というのがこのテストの主張。
	if _, err := os.Stat(filepath.Join(home, "session-state", dst, "session.db")); err != nil {
		t.Fatalf("branch has no session.db (the copy is incomplete): %v", err)
	}

	out := copilotPrompt(t, home, work, dst, "What is the codeword? Answer with one word.")
	up := strings.ToUpper(out)
	switch {
	case strings.Contains(up, "ALPHA"):
		// 契約どおり: events.jsonl が復元元。
	case strings.Contains(up, "BETA"):
		t.Fatalf("the branch remembered the turn we cut away — copilot no longer restores from "+
			"events.jsonl (session.db, which we copy verbatim, now wins). Every point fork would "+
			"silently carry history the mirror shows as removed.\n%s", out)
	default:
		t.Fatalf("the branch could not answer from the carried history — a truncated events.jsonl "+
			"is no longer resumable the way docs/55 §55.5 measured.\n%s", out)
	}
}
