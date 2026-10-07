package procx

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		name       string
		line       string
		wantPPID   int
		wantState  byte
		wantComm   string
		wantParsed bool
	}{
		{"simple", "1234 (bash) S 1000 1234 1000 0 -1 4194560 100", 1000, 'S', "bash", true},
		{"running", "42 (go) R 7 42 7 0 -1 0 0", 7, 'R', "go", true},
		{"comm has space", "5 (cc1 plus) D 3 5 3", 3, 'D', "cc1 plus", true},
		{"comm has parens", "9 (a) b) S 2 9 2", 2, 'S', "a) b", true},
		{"session fields", "1234 (bash) S 1000 1234 1100 0 -1 4194560 100", 1000, 'S', "bash", true},
		{"garbage", "not a stat line", 0, 0, "", false},
		{"no fields after comm", "7 (x)", 0, 0, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pi, ok := Parse(0, c.line)
			if ok != c.wantParsed {
				t.Fatalf("parsed = %v, want %v", ok, c.wantParsed)
			}
			if !ok {
				return
			}
			if pi.PPID != c.wantPPID || pi.State != c.wantState || pi.Comm != c.wantComm {
				t.Fatalf("got {PPID:%d State:%c Comm:%q}, want {PPID:%d State:%c Comm:%q}",
					pi.PPID, pi.State, pi.Comm, c.wantPPID, c.wantState, c.wantComm)
			}
		})
	}
}

func TestParseSession(t *testing.T) {
	pi, ok := Parse(0, "1234 (bash) S 1000 1234 1100 0 -1 4194560 100")
	if !ok || pi.Pgrp != 1234 || pi.Sid != 1100 {
		t.Fatalf("got %+v ok=%v, want Pgrp 1234 Sid 1100", pi, ok)
	}
}

// Shapes measured on the real agy (docs/log/32): pane root agy (sid 100), its MCP server in
// the same session, a run_command as its own session leader.
func TestToolProcessIn(t *testing.T) {
	const root = 100
	idle := func() map[int]Info {
		return map[int]Info{
			root: {PPID: 1, State: 'S', Comm: "agy", Pgrp: root, Sid: root},
			101:  {PPID: root, State: 'S', Comm: "workspace-agent", Pgrp: root, Sid: root},
		}
	}
	if ToolProcessIn(root, idle()) {
		t.Fatal("agy's MCP server alone read as a tool process")
	}
	tab := idle()
	tab[200] = Info{PPID: root, State: 'S', Comm: "bash", Pgrp: 200, Sid: 200}
	tab[201] = Info{PPID: 200, State: 'S', Comm: "sleep", Pgrp: 200, Sid: 200}
	if !ToolProcessIn(root, tab) {
		t.Fatal("a run_command (own-session bash > sleep) was not seen")
	}
	tab[200] = Info{PPID: root, State: 'Z', Comm: "bash", Pgrp: 200, Sid: 200}
	delete(tab, 201)
	if ToolProcessIn(root, tab) {
		t.Fatal("a zombie tool counted as running")
	}
	// Wrapper shell as the pane root with agy beneath it, same session.
	wrapped := map[int]Info{
		50:  {PPID: 1, State: 'S', Comm: "sh", Pgrp: 50, Sid: 50},
		100: {PPID: 50, State: 'S', Comm: "agy", Pgrp: 100, Sid: 50},
		101: {PPID: 100, State: 'S', Comm: "workspace-agent", Pgrp: 50, Sid: 50},
	}
	if ToolProcessIn(50, wrapped) {
		t.Fatal("agy under a wrapper shell read as a tool process")
	}
	if ToolProcessIn(999, wrapped) {
		t.Fatal("unknown root read as a tool process")
	}
}
