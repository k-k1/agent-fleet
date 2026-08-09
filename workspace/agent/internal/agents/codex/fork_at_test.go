package codex

// 発言時点からの分岐（docs/55）の codex 側ユニットテスト。
//
// codex はアンカーを**そのままは送らない**唯一の kind。Console の意味は排他（この発言の
// 手前まで）で、`thread/fork` の lastTurnId は包含（この turn まで残す）なので、答えは
// 1 つ前の turn になる。ここを取り違えると、打ち直したかった発言ごと引き継いだ会話が
// できあがり、しかもミラー上は「分岐できた」に見える。

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// rollout3Turns writes a rollout with 3 turns under an isolated HOME and maps the slot to
// it, so ResolveForkAt resolves exactly like it does at runtime.
func rollout3Turns(t *testing.T) (session.Meta, []string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const cxid = "019f9830-6b4f-7a70-9834-6d5247150090"
	ids := []string{
		"019f9830-aaaa-7b43-a606-f61767644baa",
		"019f9830-bbbb-7b43-a606-f61767644bbb",
		"019f9830-cccc-7b43-a606-f61767644ccc",
	}
	var b strings.Builder
	b.WriteString(`{"type":"session_meta","payload":{"cwd":"` + dir + `"}}` + "\n")
	for i, id := range ids {
		b.WriteString(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"` + id + `"}}` + "\n")
		b.WriteString(`{"type":"turn_context","payload":{"turn_id":"` + id + `","model":"gpt-5.5"}}` + "\n")
		b.WriteString(`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"prompt ` + string(rune('A'+i)) + `"}]}}` + "\n")
		b.WriteString(`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"reply ` + string(rune('A'+i)) + `"}]}}` + "\n")
		b.WriteString(`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"` + id + `"}}` + "\n")
	}
	sessDir := filepath.Join(home, ".codex", "sessions", "2026", "08", "08")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(sessDir, "rollout-2026-08-08T00-00-00-"+cxid+".jsonl")
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	// 分岐点を渡せるのは app-server 経由＝managed だけ（ResolveForkAt が経路も見る）。
	m := session.Meta{Dir: dir, Name: "cx", Kind: session.KindCodex, Driver: session.DriverManaged}
	sids.Write(session.UUID(dir, "cx"), cxid)
	return m, ids
}

// CLI(TUI) ルートは分岐点を渡す口が無い。「アンカーが悪い」ではなく「この経路ではできない」
// なので、ハンドラが区別できるよう ErrForkAtRoute で答える。
func TestResolveForkAtRefusesCLIRoute(t *testing.T) {
	m, ids := rollout3Turns(t)
	m.Driver = session.DriverTUI
	_, err := (agentImpl{}).ResolveForkAt(m, agents.ForkPoint{Anchor: ids[1]})
	if err == nil {
		t.Fatal("ResolveForkAt on the CLI route = nil error; want a refusal")
	}
	if !errors.Is(err, agents.ErrForkAtRoute) {
		t.Fatalf("error = %v; want it to wrap ErrForkAtRoute", err)
	}
}

// 転写の各ターンが、自分が属する rollout の turn id をアンカーとして持つこと。
// Console はこれを掴んで送り返すので、空だと導線が出ない。
func TestTranscriptCarriesTurnAnchor(t *testing.T) {
	m, ids := rollout3Turns(t)
	td, _ := readTranscript(m)
	if len(td.Turns) == 0 {
		t.Fatal("no turns parsed")
	}
	var userAnchors []string
	for _, tn := range td.Turns {
		if tn.Role == "user" {
			userAnchors = append(userAnchors, tn.AnchorID)
		}
	}
	if len(userAnchors) != 3 {
		t.Fatalf("user turns = %d, want 3", len(userAnchors))
	}
	for i, got := range userAnchors {
		if got != ids[i] {
			t.Errorf("user turn %d anchor = %q, want %q", i, got, ids[i])
		}
	}
}

// 本丸: アンカーは 1 つ前の turn id に変換される（lastTurnId は包含）。
func TestResolveForkAtReturnsPreviousTurn(t *testing.T) {
	m, ids := rollout3Turns(t)
	got, err := (agentImpl{}).ResolveForkAt(m, agents.ForkPoint{Anchor: ids[2]})
	if err != nil {
		t.Fatalf("ResolveForkAt: %v", err)
	}
	if got != ids[1] {
		t.Fatalf("ResolveForkAt(turn3) = %q; want turn2 (%q) — lastTurnId は inclusive", got, ids[1])
	}
	if got, err := (agentImpl{}).ResolveForkAt(m, agents.ForkPoint{Anchor: ids[1]}); err != nil || got != ids[0] {
		t.Fatalf("ResolveForkAt(turn2) = %q, %v; want turn1 (%q)", got, err, ids[0])
	}
}

// 「この発言の続きから」（Include）: codex は lastTurnId が包含なので、そのターン自身を
// 渡せばよい。ずらしが要るのは排他側だけ。
func TestResolveForkAtIncludeKeepsAnchorTurn(t *testing.T) {
	m, ids := rollout3Turns(t)
	for i, id := range ids {
		got, err := (agentImpl{}).ResolveForkAt(m, agents.ForkPoint{Anchor: id, Include: true})
		if err != nil {
			t.Fatalf("ResolveForkAt(turn%d, include): %v", i+1, err)
		}
		if got != id {
			t.Errorf("ResolveForkAt(turn%d, include) = %q; want the turn itself (%q)", i+1, got, id)
		}
	}
	// 最初のやり取りも「続きから」なら成立する（排他のときだけ表現できない）。
	if _, err := (agentImpl{}).ResolveForkAt(m, agents.ForkPoint{Anchor: ids[0], Include: true}); err != nil {
		t.Errorf("include on the first turn should be representable: %v", err)
	}
}

// 最初のやり取りから分岐すると lastTurnId が空になり、codex には「会話まるごと」を
// 意味してしまう（正反対）。送らずに断る。
func TestResolveForkAtRefusesFirstTurn(t *testing.T) {
	m, ids := rollout3Turns(t)
	got, err := (agentImpl{}).ResolveForkAt(m, agents.ForkPoint{Anchor: ids[0]})
	if err == nil {
		t.Fatalf("ResolveForkAt(first turn) = %q, nil; want a refusal (empty lastTurnId = 会話まるごと)", got)
	}
	if got != "" {
		t.Fatalf("ResolveForkAt(first turn) returned %q alongside the error", got)
	}
}

func TestResolveForkAtRejectsUnknownAnchor(t *testing.T) {
	m, _ := rollout3Turns(t)
	for _, anchor := range []string{"", "019f9830-9999-7b43-a606-f61767644999"} {
		if _, err := (agentImpl{}).ResolveForkAt(m, agents.ForkPoint{Anchor: anchor}); err == nil {
			t.Errorf("ResolveForkAt(%q) = nil error; want one", anchor)
		}
	}
}

// thread/fork の params に lastTurnId が載ること／分岐点なしでは載らないこと。
// 空文字で送ると codex には「会話まるごと」を意味するので、キーごと落ちている必要がある。
func TestThreadForkSendsLastTurnID(t *testing.T) {
	m, cl := newMockCodexServer(t)

	_, _ = threadFork(cl, "src-thread", "/dir", "turn-2")
	p, ok := m.lastCall("thread/fork")
	if !ok {
		t.Fatal("thread/fork was never called")
	}
	var got map[string]any
	if err := json.Unmarshal(p, &got); err != nil {
		t.Fatalf("params: %v", err)
	}
	if got["threadId"] != "src-thread" {
		t.Errorf("threadId = %v", got["threadId"])
	}
	if got["lastTurnId"] != "turn-2" {
		t.Errorf("lastTurnId = %v; want turn-2", got["lastTurnId"])
	}

	_, _ = threadFork(cl, "src-thread", "/dir", "")
	p, _ = m.lastCall("thread/fork")
	got = nil
	if err := json.Unmarshal(p, &got); err != nil {
		t.Fatalf("params: %v", err)
	}
	if _, present := got["lastTurnId"]; present {
		t.Errorf("whole-conversation fork sent lastTurnId=%v; want the key absent", got["lastTurnId"])
	}
}

func TestBuildLaunchRefusesForkAtOnCLIRoute(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	m := session.Meta{Dir: dir, Name: "cx2", Kind: session.KindCodex, ForkFrom: "src-id", ForkAt: "turn-id"}
	if _, err := (agentImpl{}).BuildLaunch(m, agents.LaunchOpts{}); err == nil {
		t.Fatal("BuildLaunch with ForkAt on the CLI route = nil error; want a refusal")
	}
}
