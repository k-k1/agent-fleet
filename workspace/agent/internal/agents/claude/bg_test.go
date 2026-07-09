package claude

import "testing"

func TestParseStat(t *testing.T) {
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
		{"garbage", "not a stat line", 0, 0, "", false},
		{"no fields after comm", "7 (x)", 0, 0, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pi, ok := parseStat(0, c.line)
			if ok != c.wantParsed {
				t.Fatalf("parsed = %v, want %v", ok, c.wantParsed)
			}
			if !ok {
				return
			}
			if pi.ppid != c.wantPPID || pi.state != c.wantState || pi.comm != c.wantComm {
				t.Fatalf("got {ppid:%d state:%c comm:%q}, want {ppid:%d state:%c comm:%q}",
					pi.ppid, pi.state, pi.comm, c.wantPPID, c.wantState, c.wantComm)
			}
		})
	}
}
