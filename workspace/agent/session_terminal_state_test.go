package main

import "testing"

func TestParseCompactProgress(t *testing.T) {
	cases := []struct {
		name    string
		pane    string
		wantPct int
		wantEl  string
	}{
		{
			name:    "percent and elapsed",
			pane:    "✳ Compacting conversation… (2m 3s)\n  ▐███████░░░░░ 74%\n  L Tip: Use /btw ...\n",
			wantPct: 74,
			wantEl:  "2m 3s",
		},
		{
			name:    "seconds-only elapsed, no percent yet",
			pane:    "✳ Compacting conversation… (45s)\n  starting up\n",
			wantPct: -1,
			wantEl:  "45s",
		},
		{
			name:    "percent but no elapsed rendered",
			pane:    "Compacting conversation…\n  ▐██░░░░ 12%\n",
			wantPct: 12,
			wantEl:  "",
		},
		{
			name:    "a stray parenthetical elsewhere is not mistaken for elapsed",
			pane:    "some earlier line (9s) done\n✳ Compacting conversation… (1m 0s)\n 100%\n",
			wantPct: 100,
			wantEl:  "1m 0s",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseCompactProgress(c.pane)
			if got.Pct != c.wantPct {
				t.Errorf("pct = %d, want %d", got.Pct, c.wantPct)
			}
			if got.Elapsed != c.wantEl {
				t.Errorf("elapsed = %q, want %q", got.Elapsed, c.wantEl)
			}
		})
	}
}
