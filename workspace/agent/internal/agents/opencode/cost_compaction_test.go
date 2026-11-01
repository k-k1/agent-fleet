package opencode

import (
	"testing"
)

// Each assistant message carries its own cost (measured 1.18.34: per message, the session row
// holds the running total), and it reaches the turn so the usage fold can sum it once.
func TestOpencodeReadSessionCost(t *testing.T) {
	db := newOpencodeTestDB(t)
	ses := "ses_cost"
	insMsg(t, db, "m1", ses, 1000, `{"role":"user","time":{"created":1000}}`)
	insPart(t, db, "p1", "m1", ses, 1, `{"type":"text","text":"hi"}`)
	insMsg(t, db, "m2", ses, 2000, `{"role":"assistant","modelID":"big-pickle","cost":0.0187629,`+
		`"tokens":{"input":5858,"output":41,"cache":{"read":1913,"write":0}},"time":{"created":2000,"completed":2100}}`)
	insPart(t, db, "p2", "m2", ses, 1, `{"type":"text","text":"hello"}`)
	insPart(t, db, "p3", "m2", ses, 2, `{"type":"step-finish","cost":0.0187629}`)
	insMsg(t, db, "m3", ses, 3000, `{"role":"assistant","modelID":"big-pickle","cost":0,"time":{"created":3000,"completed":3100}}`)
	insPart(t, db, "p4", "m3", ses, 1, `{"type":"text","text":"free"}`)

	turns := renderSession(t, db, ses)
	if len(turns) != 3 {
		t.Fatalf("turns = %d, want 3", len(turns))
	}
	if turns[0].CostUSD != 0 || turns[1].CostUSD != 0.0187629 || turns[2].CostUSD != 0 {
		t.Fatalf("CostUSD = %v / %v / %v, want 0 / 0.0187629 / 0", turns[0].CostUSD, turns[1].CostUSD, turns[2].CostUSD)
	}
}
