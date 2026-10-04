package mcpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func withSessionSearch(t *testing.T, on bool) {
	t.Helper()
	withFleetSpawn(t, false)
	old := mcpSessionSearchEnabled
	t.Cleanup(func() { mcpSessionSearchEnabled = old })
	mcpSessionSearchEnabled = on
}

func searchCall(t *testing.T, args map[string]any) (string, bool) {
	t.Helper()
	raw, _ := json.Marshal(args)
	params, _ := json.Marshal(map[string]any{"name": mcpToolSearchSessions, "arguments": json.RawMessage(raw)})
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

// The flag is honoured only on the session surface, like every other additive capability.
func TestSessionSearchFlagNeedsSelfReport(t *testing.T) {
	old, oldSelf := mcpSessionSearchEnabled, selfReportOnly()
	t.Cleanup(func() { mcpSessionSearchEnabled = old; setSelfReportOnly(oldSelf) })
	parseStdioFlags([]string{"--session-search"})
	if mcpSessionSearchEnabled {
		t.Fatal("--session-search without --self-report enabled the tool")
	}
	parseStdioFlags([]string{"--self-report", "--session-search"})
	if !mcpSessionSearchEnabled {
		t.Fatal("--self-report --session-search did not enable the tool")
	}
	parseStdioFlags([]string{"--self-report"})
	if mcpSessionSearchEnabled {
		t.Fatal("a later parse inherited the capability")
	}
}

// Off: neither advertised nor callable by a guessed name.
func TestSearchSessionsFollowsItsSwitch(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	t.Setenv("AGENT_ADDR", u.Host)
	for _, on := range []bool{false, true} {
		withSessionSearch(t, on)
		advertised := false
		for _, tool := range mcpStdioToolList() {
			if tool["name"] == mcpToolSearchSessions {
				advertised = true
			}
		}
		if advertised != on {
			t.Errorf("switch %v: advertised = %v", on, advertised)
		}
		if got := strings.Contains(mcpStdioInstructions(), "past sessions"); got != on {
			t.Errorf("switch %v: instructions mention it = %v", on, got)
		}
	}
	withSessionSearch(t, false)
	if _, isErr := searchCall(t, map[string]any{"query": "x"}); !isErr || hit {
		t.Fatalf("switched off: isError=%v, reached Agent=%v", isErr, hit)
	}
}

// The caller is the served session, never an argument, and the two modes reach their routes.
func TestSearchSessionsRoutesBothModes(t *testing.T) {
	withSessionSearch(t, true)
	var got *url.URL
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL
		_, _ = w.Write([]byte(`{"hits":[{"session":"old1","idx":7,"snippet":"fixed by retry"}],"indexing":false}`))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	t.Setenv("AGENT_ADDR", u.Host)

	text, isErr := searchCall(t, map[string]any{"query": "  retry  ", "kind": "codex", "limit": 5})
	if isErr || !strings.Contains(text, "fixed by retry") {
		t.Fatalf("search = %q isError=%v", text, isErr)
	}
	q := got.Query()
	if got.Path != "/session-search" || q.Get("q") != "retry" || q.Get("from") != "parent1" ||
		q.Get("kind") != "codex" || q.Get("limit") != "5" || q.Has("session") {
		t.Fatalf("search request = %v", got)
	}

	if _, isErr := searchCall(t, map[string]any{"session": "old1", "idx": 0}); isErr {
		t.Fatal("read mode refused idx 0")
	}
	q = got.Query()
	if got.Path != "/session-search/turns" || q.Get("session") != "old1" || q.Get("idx") != "0" || q.Get("from") != "parent1" {
		t.Fatalf("read request = %v", got)
	}

	got = nil
	if _, isErr := searchCall(t, map[string]any{"session": "old1"}); !isErr || got != nil {
		t.Fatal("neither query nor idx must be refused without reaching the Agent")
	}
}
