package claude

import "testing"

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
	}
	for _, c := range cases {
		got, ok := userPromptText([]byte(c.line))
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", c.name, got, ok, c.want, c.ok)
		}
	}
}
