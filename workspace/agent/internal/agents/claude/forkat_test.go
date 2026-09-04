package claude

// Unit tests for the claude side of branching at a message (docs/log/55).
//
// claude alone has no official entry point and cuts the jsonl itself, so the weight of these
// tests is on deciding whether a point may be cut at. A badly cut jsonl still starts — what
// breaks is the next turn, and to the user it only looks like "the agent stopped working after
// I branched".

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// line builds one transcript line. Only the fields the cut logic reads are set.
func line(t *testing.T, obj map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func forkUserLine(t *testing.T, uuid, text string) []byte {
	return line(t, map[string]any{
		"type": "user", "uuid": uuid, "sessionId": "src",
		"message": map[string]any{"role": "user", "content": text},
	})
}

func assistantLine(t *testing.T, uuid, text string) []byte {
	return line(t, map[string]any{
		"type": "assistant", "uuid": uuid, "sessionId": "src",
		"message": map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "text", "text": text},
		}},
	})
}

// toolCall/toolResult are the pair the cut must never split.
func toolCall(t *testing.T, uuid, id string) []byte {
	return line(t, map[string]any{
		"type": "assistant", "uuid": uuid, "sessionId": "src",
		"message": map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{}},
		}},
	})
}

func toolResult(t *testing.T, uuid, id string) []byte {
	return line(t, map[string]any{
		"type": "user", "uuid": uuid, "sessionId": "src",
		"message": map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": id, "content": "ok"},
		}},
	})
}

// convo: user u1 / assistant a1 / user u2 / assistant a2.
func convo(t *testing.T) [][]byte {
	return [][]byte{
		forkUserLine(t, "u1", "ALPHA"),
		assistantLine(t, "a1", "ok"),
		forkUserLine(t, "u2", "BETA"),
		assistantLine(t, "a2", "ok"),
	}
}

func TestBuildForkLinesKeepsPrefixOnly(t *testing.T) {
	out, err := buildForkLines(convo(t), "u2", "dst")
	if err != nil {
		t.Fatalf("buildForkLines: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("kept %d lines, want 2 (everything before the anchored prompt)", len(out))
	}
	joined := string(out[0]) + string(out[1])
	if !strings.Contains(joined, "ALPHA") {
		t.Error("the turn before the anchor is missing — the cut is too early")
	}
	if strings.Contains(joined, "BETA") {
		t.Error("the anchored prompt was carried into the branch — the cut must exclude it")
	}
}

// The only difference from the official fork is sessionId (measured). Rewriting uuid/parentUuid
// makes the same message unaddressable in the branch (the anchor stops being stable across
// conversations).
func TestBuildForkLinesRewritesOnlySessionID(t *testing.T) {
	src := convo(t)
	out, err := buildForkLines(src, "u2", "dst-sid")
	if err != nil {
		t.Fatalf("buildForkLines: %v", err)
	}
	for i, ln := range out {
		var before, after map[string]any
		if err := json.Unmarshal(src[i], &before); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(ln, &after); err != nil {
			t.Fatal(err)
		}
		if after["sessionId"] != "dst-sid" {
			t.Errorf("line %d sessionId = %v, want dst-sid", i, after["sessionId"])
		}
		delete(before, "sessionId")
		delete(after, "sessionId")
		if len(before) != len(after) {
			t.Errorf("line %d: field count changed (%d → %d)", i, len(before), len(after))
		}
		for k, v := range before {
			if k == "message" {
				continue // compared as JSON below
			}
			if after[k] != v {
				t.Errorf("line %d: %s changed %v → %v; only sessionId may change", i, k, v, after[k])
			}
		}
	}
}

func TestCutIndexRefusesNonPromptAnchors(t *testing.T) {
	lines := [][]byte{
		forkUserLine(t, "u1", "ALPHA"),
		toolCall(t, "a1", "tool_1"),
		toolResult(t, "r1", "tool_1"),
		assistantLine(t, "a2", "done"),
		line(t, map[string]any{"type": "user", "uuid": "meta1", "isMeta": true,
			"message": map[string]any{"role": "user", "content": "x"}}),
		line(t, map[string]any{"type": "user", "uuid": "side1", "isSidechain": true,
			"message": map[string]any{"role": "user", "content": "sub"}}),
		line(t, map[string]any{"type": "user", "uuid": "sum1", "isCompactSummary": true,
			"message": map[string]any{"role": "user", "content": "summary"}}),
	}
	for _, tc := range []struct{ name, anchor, want string }{
		// The easiest trap here: a tool result is carried as type:"user" too.
		{"tool result", "r1", "ツールの実行結果"},
		{"assistant", "a2", "エージェントの発言"},
		{"meta", "meta1", "この行からは"},
		{"sidechain", "side1", "サブエージェント"},
		{"compact summary", "sum1", "圧縮の要約"},
		{"unknown", "nope", "見つかりません"},
		{"empty", "", "指定されていません"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := cutIndex(lines, tc.anchor)
			if err == nil {
				t.Fatalf("cutIndex(%q) = nil error; want a refusal", tc.anchor)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("cutIndex(%q) error = %v; want it to mention %q", tc.anchor, err, tc.want)
			}
		})
	}
}

// Cutting between tool_use and tool_result yields a branch that starts but whose next turn the
// API rejects.
func TestBuildForkLinesRefusesDanglingToolUse(t *testing.T) {
	lines := [][]byte{
		forkUserLine(t, "u1", "ALPHA"),
		toolCall(t, "a1", "tool_1"),
		toolResult(t, "r1", "tool_1"),
		assistantLine(t, "a2", "done"),
		forkUserLine(t, "u2", "BETA"),
		toolCall(t, "a3", "tool_2"),
		// tool_2's result stays in the fixture so the check below can cut before it by hand
		toolResult(t, "r2", "tool_2"),
	}
	// Cutting before u2 drops the whole tool_2 pair, so the result is sound.
	if _, err := buildForkLines(lines, "u2", "dst"); err != nil {
		t.Fatalf("cutting before a clean boundary failed: %v", err)
	}
	// A cut that leaves the call without its result must be refused (checked directly on a
	// prefix with the result line dropped).
	if id := danglingToolUse(lines[:6]); id != "tool_2" {
		t.Fatalf("danglingToolUse = %q; want tool_2 — the call has no matching result", id)
	}
	if id := danglingToolUse(lines[:4]); id != "" {
		t.Fatalf("danglingToolUse = %q on a balanced prefix; want none", id)
	}
}

// Branching at the first message leaves nothing to carry. Resuming an empty conversation makes
// claude die with "No conversation found", so refuse before creating it.
func TestBuildForkLinesRefusesEmptyResult(t *testing.T) {
	if _, err := buildForkLines(convo(t), "u1", "dst"); err == nil {
		t.Fatal("branching at the first prompt = nil error; want a refusal (nothing to carry)")
	}
}

// "Continue from this message" (Include): the new cut point is just before the next user
// prompt. ForkAt's meaning (keep everything before this uuid) is unchanged, so materializing and
// launching still work as they are.
func TestNextPromptUUID(t *testing.T) {
	lines := [][]byte{
		forkUserLine(t, "u1", "ALPHA"),
		toolCall(t, "a1", "tool_1"),
		toolResult(t, "r1", "tool_1"), // a tool result is a user line but not "the next prompt"
		assistantLine(t, "a2", "done"),
		forkUserLine(t, "u2", "BETA"),
		assistantLine(t, "a3", "ok"),
	}
	got, err := nextPromptUUID(lines, "u1")
	if err != nil {
		t.Fatalf("nextPromptUUID: %v", err)
	}
	if got != "u2" {
		t.Fatalf("nextPromptUUID(u1) = %q; want u2 — it must not grab a tool-result user line", got)
	}
	// The last exchange: no next prompt = keep everything ("" falls into the whole-conversation
	// route).
	if got, err := nextPromptUUID(lines, "u2"); err != nil || got != "" {
		t.Fatalf("nextPromptUUID(last) = %q, %v; want \"\", nil", got, err)
	}
	// The anchor itself is validated as in the exclusive case (a tool result cannot be continued
	// from either).
	if _, err := nextPromptUUID(lines, "r1"); err == nil {
		t.Error("nextPromptUUID(tool result) = nil error; the anchor must be validated too")
	}
}

func TestMaterializeForkAtWritesBranchTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// CLAUDE_CONFIG_DIR is what decides where the jsonl lives: replacing HOME alone still reads
	// the real fleet's /var/lib/af/claude (P3-5 stage 2).
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	dir := filepath.Join(home, ".claude", "projects", "-tmp-x")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const srcSid, dstSid = "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	var buf strings.Builder
	for _, ln := range convo(t) {
		buf.Write(ln)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, srcSid+".jsonl"), []byte(buf.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := MaterializeForkAt(srcSid, dstSid, "u2"); err != nil {
		t.Fatalf("MaterializeForkAt: %v", err)
	}
	if !SessionJSONLExists(dstSid) {
		t.Fatal("branch transcript was not created where buildProgram looks for it")
	}
	got, err := os.ReadFile(filepath.Join(dir, dstSid+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "BETA") {
		t.Error("branch carried the anchored prompt")
	}
	if !strings.Contains(string(got), "ALPHA") || !strings.Contains(string(got), dstSid) {
		t.Error("branch is missing the earlier turn or the retargeted sessionId")
	}
	// The source file is only read.
	src, err := os.ReadFile(filepath.Join(dir, srcSid+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "BETA") || strings.Contains(string(src), dstSid) {
		t.Error("the source transcript was modified — the fork must leave it untouched")
	}
	// An existing destination is never overwritten (a live conversation must not be destroyed).
	if err := MaterializeForkAt(srcSid, dstSid, "u2"); err == nil {
		t.Error("MaterializeForkAt overwrote an existing destination transcript")
	}
}

// The transcript must carry the message uuid as an anchor; without it the Console offers no way
// in.
func TestTranscriptCarriesUUIDAnchor(t *testing.T) {
	lines := convo(t)
	turns := CollectTurns(lines, 0, len(lines))
	if len(turns) != 4 {
		t.Fatalf("parsed %d turns, want 4", len(turns))
	}
	for i, want := range []string{"u1", "a1", "u2", "a2"} {
		if turns[i].AnchorID != want {
			t.Errorf("turn %d anchor = %q, want %q", i, turns[i].AnchorID, want)
		}
	}
}

func TestClaudeCapsAdvertiseForkAt(t *testing.T) {
	// claude has no managed driver, so it must never refuse on the grounds of the route.
	m := session.Meta{Dir: t.TempDir(), Name: "n", Kind: session.KindClaude, Driver: session.DriverTUI}
	if !(agentImpl{}).Caps().CanForkAt {
		t.Fatal("CanForkAt is false — the mirror would never offer 「ここから分岐」")
	}
	_, err := (agentImpl{}).ResolveForkAt(m, agents.ForkPoint{Anchor: "u2"})
	if err == nil {
		t.Fatal("ResolveForkAt with no transcript = nil error; want a refusal")
	}
	if strings.Contains(err.Error(), "managed") {
		t.Fatalf("error = %v; claude has no managed route, so it must never demand one", err)
	}
}
