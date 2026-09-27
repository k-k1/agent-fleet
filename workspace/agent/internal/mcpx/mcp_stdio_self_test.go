package mcpx

// Tools whose session argument only ever means "me": a Managed model has no $AF_SESSION_NAME in
// its shell, so when it leaves the argument out the server fills in the session it serves.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// selfTestAgent records the path and body of every request the tool makes to the Agent.
func selfTestAgent(t *testing.T) *[]string {
	t.Helper()
	old := selfReportOnly()
	setSelfReportOnly(true)
	oldSource := mcpSourceSession
	t.Cleanup(func() { setSelfReportOnly(old); mcpSourceSession = oldSource })

	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = append(seen, r.Method+" "+r.URL.Path+" "+string(b))
		_, _ = w.Write([]byte(`{"name":"x"}`))
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_ADDR", u.Host)
	return &seen
}

func callSelfTool(t *testing.T, name string, args map[string]any) string {
	t.Helper()
	raw, _ := json.Marshal(args)
	params, _ := json.Marshal(map[string]any{"name": name, "arguments": json.RawMessage(raw)})
	return string(mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: params}))
}

func TestAfReportFillsTheServedSessionWhenOmitted(t *testing.T) {
	seen := selfTestAgent(t)
	mcpSourceSession = "owner01"

	if out := callSelfTool(t, "af_report", map[string]any{}); strings.Contains(out, `"isError":true`) {
		t.Fatalf("af_report without session failed: %s", out)
	}
	if len(*seen) != 1 || !strings.Contains((*seen)[0], `"name":"owner01"`) {
		t.Fatalf("Agent saw %q, want a report for the served session", *seen)
	}

	// An explicit name is still taken as given: the [agent-fleet] note spells it out.
	*seen = nil
	callSelfTool(t, "af_report", map[string]any{"session": "noted01"})
	if len(*seen) != 1 || !strings.Contains((*seen)[0], `"name":"noted01"`) {
		t.Fatalf("Agent saw %q, want the named session", *seen)
	}
}

func TestAfReportWithoutAnOwnerIsRefused(t *testing.T) {
	seen := selfTestAgent(t)
	ownerTestEnv(t, nil) // no AF_SESSION_NAME, and no session in this folder

	out := callSelfTool(t, "af_report", map[string]any{})
	if !strings.Contains(out, `"isError":true`) {
		t.Fatalf("af_report with no owner succeeded: %s", out)
	}
	for _, s := range *seen {
		if strings.Contains(s, "/chat/report") {
			t.Fatalf("a report was sent for nobody: %q", *seen)
		}
	}
}

func TestGetSessionStatusWithoutNameIsTheServedSession(t *testing.T) {
	seen := selfTestAgent(t)
	mcpSourceSession = "owner01"

	callSelfTool(t, "get_session_status", map[string]any{})
	if len(*seen) != 1 || !strings.HasPrefix((*seen)[0], "GET /sessions/owner01/status") {
		t.Fatalf("Agent saw %q, want the served session's status", *seen)
	}

	// The operator's server serves no session: there, a name is still required.
	setSelfReportOnly(false)
	*seen = nil
	if out := callSelfTool(t, "get_session_status", map[string]any{}); !strings.Contains(out, `"isError":true`) {
		t.Fatalf("operator get_session_status without a name succeeded: %s", out)
	}
	if len(*seen) != 0 {
		t.Fatalf("operator call reached the Agent: %q", *seen)
	}
}
