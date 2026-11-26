package main

import (
	"testing"
)

// TestElicitationAnswerIsAuditedFromTheAgentsAnswer drives the answer route through the proxy: the
// row names the server, the tool, the choice and the session the Agent reports, and nothing of the
// call's arguments.
func TestElicitationAnswerIsAuditedFromTheAgentsAnswer(t *testing.T) {
	const route = "POST /api/mcp-elicitations/answer"
	only := func(r string) bool { return r == route }
	for _, tc := range []struct {
		name, answer, target, detail string
	}{
		{"accept", `{"id":"e1","serverId":"mcp-gmail1","tool":"send_mail","session":"fix-mail","action":"accept"}`,
			"mcp-gmail1 send_mail", "accept session=fix-mail"},
		{"decline unattributed", `{"id":"e1","serverId":"mcp-gmail1","tool":"gmail/send","action":"decline"}`,
			"mcp-gmail1 gmail/send", "decline"},
		// Out of shape is left out, never recorded as given.
		{"out of shape", `{"serverId":"https://u:pw@h/mcp","tool":"a b\nc","session":"../x","action":"allow"}`, "", "unknown"},
		{"not json", `ok`, "", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probes := probeRelaysWith(t, only, map[string]string{"POST /mcp-elicitations/answer": tc.answer})
			if len(probes) != 1 {
				t.Fatalf("probed %d routes", len(probes))
			}
			p := probes[0]
			if !p.relayed() {
				t.Fatal("not relayed to the Agent")
			}
			if len(p.rows) != 1 || p.rows[0].Action != "mcp.elicitation" {
				t.Fatalf("audit rows %+v", p.rows)
			}
			if r := p.rows[0]; r.Target != tc.target || r.Detail != tc.detail || r.ActorKind != "user" || r.ActorID == "" {
				t.Fatalf("row target %q detail %q actor %s/%s, want %q %q", r.Target, r.Detail, r.ActorKind, r.ActorID, tc.target, tc.detail)
			}
		})
	}
}

func TestElicitationListIsNotAudited(t *testing.T) {
	probes := probeRelaysWith(t, func(r string) bool { return r == "GET /api/mcp-elicitations" }, nil)
	if len(probes) != 1 || !probes[0].relayed() || len(probes[0].audits) != 0 {
		t.Fatalf("%+v", probes)
	}
}
