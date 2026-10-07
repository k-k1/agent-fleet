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
