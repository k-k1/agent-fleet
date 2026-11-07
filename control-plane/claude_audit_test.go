package main

import (
	"encoding/json"
	"strings"
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
	got := extractClaudeAudits(resp.Messages, "t1", "sess1", true)
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

// Default: a Bash audit row names only the program (and a plain subcommand), never the
// arguments, so a token written on a command line cannot reach audit_log.
func TestBashAuditTargetDefaultProgramOnly(t *testing.T) {
	const tok = "ghp_" + "0123456789abcdefghijklmnop"
	for _, c := range []struct{ info, want string }{
		{"go test ./...", "go test"},
		{"curl -H 'Authorization: Bearer " + tok + "' https://x.test", "curl"},
		{"API_KEY=" + tok + " npm run build", "npm run"},
		{"/usr/bin/git push origin main", "git push"},
		{"deploy --token=" + tok, "deploy"},
		{"", ""},
	} {
		got := bashAuditTarget(c.info, false)
		if got != c.want {
			t.Errorf("bashAuditTarget(%q,false) = %q, want %q", c.info, got, c.want)
		}
		if strings.Contains(got, tok) {
			t.Errorf("token leaked: %q", got)
		}
	}
	msgs := []ctTurn{{Role: "assistant", Parts: []ctPart{{Kind: "tool", Tool: "Bash", Info: "curl -u me:" + tok + " https://x.test"}}}}
	got := extractClaudeAudits(msgs, "t", "s", false)
	if len(got) != 1 || got[0].Target != "curl" {
		t.Fatalf("extract default: %+v", got)
	}
}

// Opt-in full lines are still masked.
func TestBashAuditTargetFullIsMasked(t *testing.T) {
	const tok = "ghp_" + "0123456789abcdefghijklmnop"
	for _, c := range []struct{ info, want string }{
		{"API_KEY=" + tok + " npm run build", "API_KEY=*** npm run build"},
		{"curl -H 'Authorization: Bearer " + tok + "' https://x.test", "curl -H 'Authorization: Bearer ***' https://x.test"},
		{"deploy --token " + tok + " --env prod", "deploy --token *** --env prod"},
		{"deploy --password=hunter2", "deploy --password=***"},
		{"git clone https://me:" + tok + "@host.test/r.git", "git clone https://me:***@host.test/r.git"},
		{"echo " + tok, "echo ***"},
		{"go test ./...", "go test ./..."},
	} {
		got := bashAuditTarget(c.info, true)
		if got != c.want {
			t.Errorf("bashAuditTarget(%q,true) = %q, want %q", c.info, got, c.want)
		}
		if strings.Contains(got, tok) || strings.Contains(got, "hunter2") {
			t.Errorf("secret leaked: %q", got)
		}
	}
}
