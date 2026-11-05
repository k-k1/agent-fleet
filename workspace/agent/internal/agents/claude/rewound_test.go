package claude

import (
	"strings"
	"testing"
)

// rewoundFixture is the line shape claude 2.1.318 wrote for "first ok" (answered), "HOLD
// second" (stopped before its first byte, put back into the box and cleared) and "third ok",
// measured with a fake API server. Bookkeeping lines without a uuid are kept, as in the file.
var rewoundFixture = []string{
	`{"type":"permission-mode","permissionMode":"default"}`,
	`{"type":"user","uuid":"u1","parentUuid":null,"message":{"role":"user","content":"first ok"}}`,
	`{"type":"attachment","uuid":"a1","parentUuid":"u1","attachment":{"type":"skill_listing"}}`,
	`{"type":"assistant","uuid":"r1","parentUuid":"a1","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}`,
	`{"type":"system","uuid":"s1","parentUuid":"r1","subtype":"turn_duration"}`,
	`{"type":"last-prompt"}`,
	`{"type":"user","uuid":"u2","parentUuid":"s1","message":{"role":"user","content":"HOLD second"}}`,
	`{"type":"attachment","uuid":"a2","parentUuid":"u2","attachment":{"type":"todo_reminder"}}`,
	`{"type":"user","uuid":"u3","parentUuid":"s1","message":{"role":"user","content":"third ok"}}`,
	`{"type":"attachment","uuid":"a3","parentUuid":"u3","attachment":{"type":"todo_reminder"}}`,
	`{"type":"assistant","uuid":"r3","parentUuid":"a3","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}`,
}

func fixtureLines(ss []string) [][]byte {
	out := make([][]byte, len(ss))
	for i, s := range ss {
		out[i] = []byte(s)
	}
	return out
}

func userTexts(lines [][]byte) []string {
	var got []string
	for _, t := range CollectTurns(lines, 0, len(lines)) {
		if t.Role == "user" {
			got = append(got, t.Text)
		}
	}
	return got
}

func TestRewoundPromptIsLeftOutOfTheTurns(t *testing.T) {
	lines := fixtureLines(rewoundFixture)
	rw := RewoundPrompts(lines)
	if len(rw) != 1 || rw[6] != 8 {
		t.Fatalf("RewoundPrompts = %v, want {6: 8}", rw)
	}
	if got := strings.Join(userTexts(lines), "|"); got != "first ok|third ok" {
		t.Fatalf("user turns = %q", got)
	}
}

// Until the next prompt is written the stopped one cannot be told from a prompt still waiting
// for its first byte, so it stays.
func TestStoppedPromptStaysUntilReplaced(t *testing.T) {
	lines := fixtureLines(rewoundFixture[:8])
	if rw := RewoundPrompts(lines); len(rw) != 0 {
		t.Fatalf("RewoundPrompts = %v, want none", rw)
	}
	if got := strings.Join(userTexts(lines), "|"); got != "first ok|HOLD second" {
		t.Fatalf("user turns = %q", got)
	}
}

// A prompt that got an answer ran, even if a later prompt shares its parent (/rewind to before
// it): its branch stays on screen.
func TestAnsweredSiblingIsKept(t *testing.T) {
	ss := append([]string{}, rewoundFixture[:8]...)
	ss = append(ss,
		`{"type":"assistant","uuid":"r2","parentUuid":"a2","message":{"role":"assistant","content":[{"type":"text","text":"partial"}]}}`,
	)
	ss = append(ss, rewoundFixture[8:]...)
	if rw := RewoundPrompts(fixtureLines(ss)); len(rw) != 0 {
		t.Fatalf("RewoundPrompts = %v, want none", rw)
	}
}

// Tool results are user lines too, and meta lines share parents freely: neither is a prompt.
func TestToolResultsAndMetaAreNotPrompts(t *testing.T) {
	lines := fixtureLines([]string{
		`{"type":"user","uuid":"u1","parentUuid":"x","message":{"role":"user","content":"go"}}`,
		`{"type":"assistant","uuid":"r1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]}}`,
		`{"type":"user","uuid":"t1r","parentUuid":"r1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"x"}]}}`,
		`{"type":"user","uuid":"m1","parentUuid":"r1","isMeta":true,"message":{"role":"user","content":"caveat"}}`,
		`{"type":"user","uuid":"m2","parentUuid":"r1","isMeta":true,"message":{"role":"user","content":"caveat"}}`,
	})
	if rw := RewoundPrompts(lines); len(rw) != 0 {
		t.Fatalf("RewoundPrompts = %v, want none", rw)
	}
}

func TestRewoundSince(t *testing.T) {
	rw := map[int]int{6: 8}
	for _, c := range []struct {
		since int
		want  bool
	}{{6, false}, {7, true}, {8, true}, {9, false}} {
		if got := RewoundSince(rw, c.since); got != c.want {
			t.Errorf("RewoundSince(since=%d) = %v, want %v", c.since, got, c.want)
		}
	}
}

// Lines without parent links (a session's first prompt, a transcript written without them)
// prove nothing: answered prompts must not be hidden for sharing a null parent.
func TestNullParentsAreNoEvidence(t *testing.T) {
	lines := fixtureLines([]string{
		`{"type":"user","uuid":"u1","message":{"role":"user","content":"ALPHA"}}`,
		`{"type":"assistant","uuid":"a1","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}`,
		`{"type":"user","uuid":"u2","parentUuid":null,"message":{"role":"user","content":"BETA"}}`,
	})
	if rw := RewoundPrompts(lines); len(rw) != 0 {
		t.Fatalf("RewoundPrompts = %v, want none", rw)
	}
}
