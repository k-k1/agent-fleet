package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUserPromptText(t *testing.T) {
	cases := []struct {
		name, line, want string
		ok               bool
	}{
		{"string content", `{"type":"user","message":{"content":"SLOW 再レビュー"}}`, "SLOW 再レビュー", true},
		{"text blocks", `{"type":"user","message":{"content":[{"type":"text","text":"a"},{"type":"image"},{"type":"text","text":"b"}]}}`, "a\nb", true},
		{"tool result", `{"type":"user","message":{"content":[{"type":"tool_result","content":"x"}]}}`, "", false},
		{"meta line", `{"type":"user","isMeta":true,"message":{"content":"caveat"}}`, "", false},
		{"subagent", `{"type":"user","isSidechain":true,"message":{"content":"sub"}}`, "", false},
		{"assistant", `{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}`, "", false},
		{"blank", `{"type":"user","message":{"content":"  "}}`, "", false},
		{"not json", `{`, "", false},
		{"compaction summary", `{"type":"user","isCompactSummary":true,"message":{"content":"This session is being continued"}}`, "", false},
		{"task notification", `{"type":"user","message":{"content":"<task-notification>\n<task-id>b1</task-id>"}}`, "", false},
	}
	for _, c := range cases {
		got, ok := userPromptText([]byte(c.line))
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", c.name, got, ok, c.want, c.ok)
		}
	}
}

// The member's prompt is found behind the lines claude writes after it on its own.
func TestLastUserPromptInSkipsClaudesOwnLines(t *testing.T) {
	lines := []string{
		`{"type":"user","message":{"content":"an older prompt"}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}`,
		`{"type":"user","message":{"content":"SLOW 再レビュー"}}`,
		`{"type":"user","isCompactSummary":true,"message":{"content":"This session is being continued"}}`,
		`{"type":"user","message":{"content":"<task-notification>\n<task-id>b1</task-id>"}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"x"}]}}`,
		`{"type":"last-prompt"}`,
	}
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := lastUserPromptIn(p); !ok || got != "SLOW 再レビュー" {
		t.Fatalf("got (%q, %v)", got, ok)
	}
}
