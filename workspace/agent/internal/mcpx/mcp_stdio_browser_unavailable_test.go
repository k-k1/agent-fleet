package mcpx

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/browserx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
)

// withParsedStdioFlags runs parseStdioFlags on args and restores every flag it resets.
func withParsedStdioFlags(t *testing.T, args []string) {
	t.Helper()
	ow, os, oc, conv := writeEnabled(), selfReportOnly(), sessionChromiumEnabled(), convID()
	op, oi, of, ob := mcpPeerMessagingEnabled, mcpImageGenEnabled, mcpFleetSpawnEnabled, mcpBrowserUnavailable
	t.Cleanup(func() {
		setFlags(ow, os, oc)
		setConvID(conv)
		mcpPeerMessagingEnabled, mcpImageGenEnabled, mcpFleetSpawnEnabled, mcpBrowserUnavailable = op, oi, of, ob
	})
	parseStdioFlags(args)
}

func listedToolNames() map[string]bool {
	names := map[string]bool{}
	for _, tool := range mcpStdioToolList() {
		names[tool["name"].(string)] = true
	}
	return names
}

// On a workspace without browser features the Agent passes --browser-unavailable, and the af
// server stops listing the seven Chromium tools on every surface that had them (#1614). Without
// the flag the list is exactly what it was, and nothing else leaves it with the flag.
//
// The process env is set to the unavailable value throughout, so the no-flag cases also pin
// that the server takes the answer from its argv only: the env does not reach a codex or muse
// MCP child, and two sources could disagree.
func TestMCPBrowserUnavailableHidesChromiumTools(t *testing.T) {
	t.Setenv(browserx.UnavailableEnv, "kubernetes")
	// Nothing may reach the Agent: the refusal for a withheld tool is answered by the server.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a withheld browser tool reached the Agent: %s %s", r.Method, r.URL)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	t.Setenv("AGENT_ADDR", u.Host)

	allChromium := append(append([]string{}, chromiumReadToolNames...), chromiumWriteToolNames...)
	for _, tc := range []struct {
		name string
		args []string
		// offered is the Chromium tools this surface lists with browser features.
		offered []string
	}{
		{"session", []string{"--self-report", "--chromium-attach"}, allChromium},
		{"assistant read", nil, chromiumReadToolNames},
		{"assistant write", []string{"--write", "--conv", "c1"}, allChromium},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withParsedStdioFlags(t, tc.args)
			before := listedToolNames()
			for _, name := range tc.offered {
				if !before[name] {
					t.Fatalf("without the flag %s is not listed: %v", name, sortedBoolKeys(before))
				}
			}
			beforeInstructions := mcpStdioInstructions()

			withParsedStdioFlags(t, append(append([]string{}, tc.args...), mcpreg.BrowserUnavailableFlag, "kubernetes"))
			after := listedToolNames()
			for name := range before {
				hidden := false
				for _, c := range allChromium {
					hidden = hidden || c == name
				}
				if after[name] == hidden {
					t.Errorf("with the flag %s listed=%v, want %v", name, after[name], !hidden)
				}
			}
			if len(after) != len(before)-len(tc.offered) {
				t.Errorf("with the flag %d tools, want %d: %v", len(after), len(before)-len(tc.offered), sortedBoolKeys(after))
			}
			if strings.Contains(mcpStdioInstructions(), "Chromium") {
				t.Errorf("instructions still offer Chromium: %q", mcpStdioInstructions())
			}
			if tc.name != "session" && mcpStdioInstructions() != beforeInstructions {
				t.Errorf("assistant instructions changed: %q", mcpStdioInstructions())
			}

			// The backstop: calling a withheld tool by name says why, rather than "unknown tool".
			for _, name := range tc.offered {
				resp := string(callChromiumMCP(t, name, map[string]any{"port": 9222, "attachment_id": "a1"}))
				for _, want := range []string{`"isError":true`, "code=browser_unavailable", "(kubernetes)", "Do not start Chromium"} {
					if !strings.Contains(resp, want) {
						t.Errorf("%s: answer lacks %q: %s", name, want, resp)
					}
				}
			}
		})
	}
}

// The refusal does not widen anyone's scope: a Chromium tool the surface would not offer even
// with a browser keeps its old refusal.
func TestMCPBrowserUnavailableKeepsScopeRefusals(t *testing.T) {
	withParsedStdioFlags(t, []string{"--self-report", mcpreg.BrowserUnavailableFlag, "kubernetes"})
	if resp := string(callChromiumMCP(t, "list_chromium_targets", map[string]any{"port": 9222})); !strings.Contains(resp, "tools/list に無いツール名") {
		t.Errorf("session without --chromium-attach: %s", resp)
	}
	withParsedStdioFlags(t, []string{mcpreg.BrowserUnavailableFlag, "kubernetes"})
	if resp := string(callChromiumMCP(t, "attach_chromium", map[string]any{})); !strings.Contains(resp, "変更を許可されていません") {
		t.Errorf("read-only assistant: %s", resp)
	}
}

func sortedBoolKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return sortedStringsCopy(out)
}
