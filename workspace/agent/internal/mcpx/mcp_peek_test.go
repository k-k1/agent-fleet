package mcpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func peekCall(t *testing.T, args map[string]any) (string, bool) {
	t.Helper()
	raw, _ := json.Marshal(args)
	params, _ := json.Marshal(map[string]any{"name": "peek_session_output", "arguments": json.RawMessage(raw)})
	resp := mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: params})
	var parsed struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil || len(parsed.Result.Content) == 0 {
		t.Fatalf("resp = %s err = %v", resp, err)
	}
	return parsed.Result.Content[0].Text, parsed.Result.IsError
}

// The reader the Agent judges is the session this server serves, never an argument: a
// peek_from smuggled into the arguments must not replace it.
func TestPeekSessionOutputNamesTheServedSessionAsReader(t *testing.T) {
	withFleetSpawn(t, false)
	mcpPeerMessagingEnabled = true
	var got *url.URL
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL
		_, _ = w.Write([]byte(`{"name":"peer2","output":"done: PR #9","cursor":4}`))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	t.Setenv("AGENT_ADDR", u.Host)

	text, isErr := peekCall(t, map[string]any{"name": "peer2", "lines": 20, "since": 3, "peek_from": "someone-else"})
	if isErr || !strings.Contains(text, "done: PR #9") {
		t.Fatalf("peek result = %q (isError=%v)", text, isErr)
	}
	if got == nil || got.Path != "/sessions/peer2/output" {
		t.Fatalf("agent path = %v, want /sessions/peer2/output", got)
	}
	q := got.Query()
	if q.Get("peek_from") != "parent1" || len(q["peek_from"]) != 1 {
		t.Errorf("peek_from = %q, want the served session parent1 only", q["peek_from"])
	}
	if q.Get("lines") != "20" || q.Get("since") != "3" {
		t.Errorf("query = %v, want lines=20 since=3", q)
	}

	// Omitted lines / since are left to the Agent's defaults.
	_, _ = peekCall(t, map[string]any{"name": "peer2"})
	if q := got.Query(); q.Has("lines") || q.Has("since") || q.Get("peek_from") != "parent1" {
		t.Errorf("bare peek query = %v, want peek_from only", q)
	}
}

// Off with peer messaging: neither advertised nor callable by a guessed name.
func TestPeekSessionOutputFollowsPeerMessagingSwitch(t *testing.T) {
	withFleetSpawn(t, false)
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	t.Setenv("AGENT_ADDR", u.Host)

	for _, on := range []bool{false, true} {
		mcpPeerMessagingEnabled = on
		advertised := false
		for _, tool := range mcpStdioToolList() {
			if tool["name"] == "peek_session_output" {
				advertised = true
			}
		}
		if advertised != on {
			t.Errorf("peer messaging %v: peek_session_output advertised = %v", on, advertised)
		}
	}
	mcpPeerMessagingEnabled = false
	if _, isErr := peekCall(t, map[string]any{"name": "peer2"}); !isErr || hit {
		t.Fatalf("peek with peer messaging off: isError=%v, reached Agent=%v", isErr, hit)
	}
}
