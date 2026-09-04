package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAgentCallsUseTheSharedClient requires every CP→Agent HTTP call to go through
// agent_dial.go's Transport (agentHTTPClient / agentLongCallClient / agentRelayClient /
// healthzClient; WebSocket via NetDialContext: dialAgent).
//
// Service Connect aliases are only written into /etc/hosts when a task starts, so a
// workspace created after the CP task NXDOMAINs on a plain dial. The re-lookup against
// Cloud Map that fixes this lives on the Transport, so any path using http.DefaultClient
// misses it and fails in a real deployment with
//
//	dial tcp: lookup af-ws-… on 10.20.0.2:53: no such host
//
// while the Console only says "is the workspace running?" — and it is, so the cause is
// unreachable from there. This has shipped twice, hence the mechanical check.
//
// The check is per file: fail when a file that builds Agent URLs (it references
// Endpoint()) uses http.DefaultClient. Outbound calls from such a file (an IdP,
// bitbucket.org) must carry their own client instead (bbHTTPClient / oidcHTTPClient).
func TestAgentCallsUseTheSharedClient(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatal(err)
		}
		body := string(src)
		if !strings.Contains(body, "Endpoint()") {
			continue
		}
		checked++
		for i, line := range strings.Split(body, "\n") {
			code := line
			if j := strings.Index(code, "//"); j >= 0 {
				code = code[:j] // naming it in a comment is fine; the call is what is banned
			}
			if strings.Contains(code, "http.DefaultClient") {
				t.Errorf("%s:%d: http.DefaultClient is used on a CP->Agent path. "+
					"Use agentHTTPClient (or agentLongCallClient for long calls) - "+
					"the fallback to Cloud Map lives on the Transport, so with a plain client "+
					"any workspace created after the CP started becomes no such host.\n\t%s",
					name, i+1, strings.TrimSpace(line))
			}
		}
	}
	if checked == 0 {
		t.Fatal("no file references Endpoint() - the check is measuring nothing")
	}
}
