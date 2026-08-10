package copilot

// 発言時点からの分岐（docs/55）の copilot 側ユニットテスト。
//
// copilot の分岐は「session-state ディレクトリごとコピーして events.jsonl だけ切り詰める」。
// **session.db は無改変で運ぶ**のが肝で（復元元は events.jsonl だと実測済み）、テストも
// そこを固定する — 我々が意味を知らないファイルを書き換え始めた瞬間に、この手術は
// 「読めるものを同じ形で書き直す」から「別プロダクトの内部状態を owns する」に変わる。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func ev(t *testing.T, typ, id, parent, content string) []byte {
	t.Helper()
	obj := map[string]any{"type": typ, "id": id, "parentId": parent, "timestamp": "2026-08-09T00:00:00Z"}
	if content != "" {
		obj["data"] = map[string]any{"content": content, "sessionId": "SRC"}
	}
	b, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// events: start → prompt1/reply1 → prompt2/reply2
func copilotEvents(t *testing.T) [][]byte {
	return [][]byte{
		ev(t, "session.start", "e0", "", ""),
		ev(t, "system.message", "e1", "e0", "you are copilot"),
		ev(t, "user.message", "u1", "e1", "ALPHA"),
		ev(t, "assistant.turn_start", "a1", "u1", ""),
		ev(t, "assistant.message", "a2", "a1", "ok"),
		ev(t, "assistant.turn_end", "a3", "a2", ""),
		ev(t, "user.message", "u2", "a3", "BETA"),
		ev(t, "assistant.turn_start", "b1", "u2", ""),
		ev(t, "assistant.message", "b2", "b1", "ok"),
		ev(t, "assistant.turn_end", "b3", "b2", ""),
	}
}

func TestForkEventLinesKeepsPrefixOnly(t *testing.T) {
	out, err := forkEventLines(copilotEvents(t), "u2", "SRC", "DST")
	if err != nil {
		t.Fatalf("forkEventLines: %v", err)
	}
	if len(out) != 6 {
		t.Fatalf("kept %d events, want 6 (everything before the anchored prompt)", len(out))
	}
	joined := string(strings.Join(func() []string {
		s := make([]string, len(out))
		for i, b := range out {
			s[i] = string(b)
		}
		return s
	}(), "\n"))
	if !strings.Contains(joined, "ALPHA") {
		t.Error("the exchange before the anchor is missing")
	}
	if strings.Contains(joined, "BETA") {
		t.Error("the anchored prompt was carried into the branch")
	}
	if strings.Contains(joined, "SRC") || !strings.Contains(joined, "DST") {
		t.Error("the session id was not retargeted")
	}
}

func TestForkEventLinesWholeConversation(t *testing.T) {
	// アンカー無し = 会話まるごと（copilot はこれもネイティブ口が無いので同じ経路）。
	out, err := forkEventLines(copilotEvents(t), "", "SRC", "DST")
	if err != nil {
		t.Fatalf("forkEventLines(whole): %v", err)
	}
	if len(out) != 10 {
		t.Fatalf("kept %d events, want all 10", len(out))
	}
}

func TestCutIndexAtRefusesNonPrompts(t *testing.T) {
	lines := copilotEvents(t)
	for _, tc := range []struct{ name, anchor, want string }{
		{"assistant", "a2", "ユーザーの発言"},
		{"session event", "e0", "ユーザーの発言"},
		{"unknown", "nope", "見つかりません"},
		{"empty", "", "指定されていません"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := cutIndexAt(lines, tc.anchor)
			if err == nil {
				t.Fatalf("cutIndexAt(%q) = nil error; want a refusal", tc.anchor)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v; want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestForkEventLinesRefusesEmptyPrefix(t *testing.T) {
	// 最初の発言から分岐しても中身が無い。
	if _, err := forkEventLines(copilotEvents(t), "u1", "SRC", "DST"); err == nil {
		t.Fatal("branching at the first prompt = nil error; want a refusal")
	}
}

func TestNextPromptID(t *testing.T) {
	lines := copilotEvents(t)
	got, err := nextPromptID(lines, "u1")
	if err != nil || got != "u2" {
		t.Fatalf("nextPromptID(u1) = %q, %v; want u2", got, err)
	}
	if got, err := nextPromptID(lines, "u2"); err != nil || got != "" {
		t.Fatalf("nextPromptID(last) = %q, %v; want \"\", nil", got, err)
	}
}

// copyTree が session.db 等を**無改変で**運ぶこと。ここが崩れると、我々が意味を知らない
// ファイルを書き換えていることになる。
func TestMaterializeForkAtCopiesStateUntouched(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COPILOT_HOME", home)
	const srcSid, dstSid = "src-session", "dst-session"
	src := filepath.Join(home, "session-state", srcSid)
	if err := os.MkdirAll(filepath.Join(src, "checkpoints"), 0o755); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	for _, ln := range copilotEvents(t) {
		buf.Write(ln)
		buf.WriteByte('\n')
	}
	write := func(rel, content string) {
		if err := os.WriteFile(filepath.Join(src, rel), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("events.jsonl", buf.String())
	write("workspace.yaml", "sessionId: "+srcSid+"\n")
	write("session.db", "BINARY-DB-WITH-BETA")
	write(filepath.Join("checkpoints", "c1.json"), "{}")

	if err := MaterializeForkAt(srcSid, dstSid, "u2"); err != nil {
		t.Fatalf("MaterializeForkAt: %v", err)
	}
	dst := filepath.Join(home, "session-state", dstSid)
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(dst, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		return string(b)
	}
	if e := read("events.jsonl"); strings.Contains(e, "BETA") || !strings.Contains(e, "ALPHA") {
		t.Error("events.jsonl was not truncated at the anchor")
	}
	if db := read("session.db"); db != "BINARY-DB-WITH-BETA" {
		t.Errorf("session.db = %q; it must be copied VERBATIM — events.jsonl is the restore "+
			"source (実測), so we never rewrite a file whose format we don't own", db)
	}
	if w := read("workspace.yaml"); !strings.Contains(w, dstSid) || strings.Contains(w, srcSid) {
		t.Error("workspace.yaml still points at the source session")
	}
	if read(filepath.Join("checkpoints", "c1.json")) != "{}" {
		t.Error("nested state was not copied")
	}
	// 元は読むだけ。
	if b, _ := os.ReadFile(filepath.Join(src, "events.jsonl")); !strings.Contains(string(b), "BETA") {
		t.Error("the source session was modified")
	}
	// 既存の宛先は上書きしない。
	if err := MaterializeForkAt(srcSid, dstSid, "u2"); err == nil {
		t.Error("MaterializeForkAt overwrote an existing branch")
	}
}

func TestCopilotResolveForkAt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home) // sids ストア（$HOME/.config/agent-fleet）
	t.Setenv("COPILOT_HOME", filepath.Join(home, ".copilot"))
	dir := filepath.Join(home, "repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const sid = "sess-1"
	if err := os.MkdirAll(filepath.Join(home, ".copilot", "session-state", sid), 0o755); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	for _, ln := range copilotEvents(t) {
		buf.Write(ln)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(home, ".copilot", "session-state", sid, "events.jsonl"),
		[]byte(buf.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	m := session.Meta{Dir: dir, Name: "cp", Kind: session.KindCopilot}
	sids.Write(session.UUID(dir, "cp"), sid)

	got, err := (agentImpl{}).ResolveForkAt(m, agents.ForkPoint{Anchor: "u2"})
	if err != nil || got != "u2" {
		t.Fatalf("ResolveForkAt(u2) = %q, %v; want u2 (copilot passes the anchor through)", got, err)
	}
	// 「続きから」: 最後のやり取りなら会話まるごと（""）。
	if got, err := (agentImpl{}).ResolveForkAt(m, agents.ForkPoint{Anchor: "u2", Include: true}); err != nil || got != "" {
		t.Fatalf("ResolveForkAt(u2, include) = %q, %v; want \"\"", got, err)
	}
	if got, err := (agentImpl{}).ResolveForkAt(m, agents.ForkPoint{Anchor: "u1", Include: true}); err != nil || got != "u2" {
		t.Fatalf("ResolveForkAt(u1, include) = %q, %v; want u2", got, err)
	}
	if _, err := (agentImpl{}).ResolveForkAt(m, agents.ForkPoint{Anchor: "a2"}); err == nil {
		t.Error("ResolveForkAt(assistant event) = nil error; want a refusal")
	}
}
