package main

import (
	"encoding/json"
	"testing"
)

// extractClaudeAudits records only assistant change/exec tool_use, mapping tool names
// to actions and picking file-or-info as the target; reads / text / user turns are out.
func TestExtractClaudeAudits(t *testing.T) {
	// A transcript window as the Agent's /messages returns it.
	raw := `{"messages":[
	  {"role":"user","parts":[{"kind":"text","text":"do it"}],"ts":"2026-07-05T10:00:00Z"},
	  {"role":"assistant","ts":"2026-07-05T10:00:01Z","parts":[
	     {"kind":"thinking","text":"..."},
	     {"kind":"tool","tool":"Read","info":"a.txt"},
	     {"kind":"tool","tool":"Write","info":"write foo","file":"src/foo.go"},
	     {"kind":"tool","tool":"Bash","info":"go test ./..."},
	     {"kind":"text","text":"done"}
	  ]},
	  {"role":"assistant","ts":"2026-07-05T10:00:02Z","parts":[
	     {"kind":"tool","tool":"MultiEdit","file":"src/bar.go"}
	  ]}
	],"cursor":42,"reset":false}`
	var resp ctResp
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Cursor != 42 || resp.Reset {
		t.Fatalf("resp meta: cursor=%d reset=%v", resp.Cursor, resp.Reset)
	}
	got := extractClaudeAudits(resp.Messages, "t1", "sess1")
	if len(got) != 3 {
		t.Fatalf("want 3 audits (Write/Bash/MultiEdit), got %d: %+v", len(got), got)
	}
	want := []struct{ action, target string }{
		{"claude.write", "src/foo.go"},   // file preferred over info
		{"claude.bash", "go test ./..."}, // info (no file)
		{"claude.edit", "src/bar.go"},    // MultiEdit -> claude.edit
	}
	for i, w := range want {
		if got[i].Action != w.action || got[i].Target != w.target {
			t.Errorf("audit[%d] = (%q,%q) want (%q,%q)", i, got[i].Action, got[i].Target, w.action, w.target)
		}
		if got[i].ActorKind != "claude" || got[i].TenantID != "t1" || got[i].ActorID != "sess1" {
			t.Errorf("audit[%d] attribution: %+v", i, got[i])
		}
	}
	// TS carried from the turn.
	if got[0].At != "2026-07-05T10:00:01Z" {
		t.Errorf("ts not carried: %q", got[0].At)
	}
}
