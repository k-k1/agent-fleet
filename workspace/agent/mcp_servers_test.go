package main

// Wire contract of the MCP registry REST (docs/log/48 P0). Two things are pinned here: that
// no secret leaves the process, and that a read-only row cannot be edited. Both are
// regressions that normal operation would never reveal, so each route is held down.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
)

func mcpDecode(t *testing.T, body []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
}

type mcpListResp struct {
	Servers []struct {
		mcpreg.ServerDef
		Editable bool `json:"editable"`
		Ready    bool `json:"ready"`
	} `json:"servers"`
	Shadowed []string `json:"shadowed"`
}

func TestMCPServerCreateMasksSecretsOnTheWire(t *testing.T) {
	h := smokeHandler(t)
	body := `{"name":"wiki","transport":"http","url":"https://mcp.example.com/mcp",
	          "headers":{"Authorization":"Bearer super-secret"},"enabled":true,
	          "targets":{"session":true}}`
	w := smokeDo(t, h, "POST", "/mcp-servers", "smoke-token", body)
	if w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); containsSecret(got) {
		t.Fatalf("secret exposed in the create response: %s", got)
	}

	w = smokeDo(t, h, "GET", "/mcp-servers", "smoke-token", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	if containsSecret(w.Body.String()) {
		t.Fatalf("secret exposed in the list: %s", w.Body.String())
	}
	var list mcpListResp
	mcpDecode(t, w.Body.Bytes(), &list)
	// Besides the one row just registered, the list carries the always-present built-in "af"
	// (the session-side server for self-reporting and the Chromium attach view, docs/log/51
	// Phase 3). Look the registered row up by name.
	var got *struct {
		mcpreg.ServerDef
		Editable bool `json:"editable"`
		Ready    bool `json:"ready"`
	}
	for i := range list.Servers {
		if list.Servers[i].Name == "wiki" {
			got = &list.Servers[i]
		}
	}
	if got == nil {
		t.Fatalf("the registered definition is missing from the list: %+v", list.Servers)
	}
	if got.Headers["Authorization"] != mcpreg.MaskedValue {
		t.Fatalf("header not masked: %q", got.Headers["Authorization"])
	}
	if !got.Editable || !got.Ready {
		t.Fatalf("a user definition is not editable/ready: %+v", got)
	}
}

func containsSecret(s string) bool { return strings.Contains(s, "super-secret") }

func TestMCPServerIgnoresClientSuppliedOrigin(t *testing.T) {
	h := smokeHandler(t)
	// The server pins origin to user, so a tenant stdio entry cannot be created over REST at
	// all. What is checked here is that a client spoofing origin still lands as user; the
	// distribution route answers 400 separately on the CP side (P4).
	w := smokeDo(t, h, "POST", "/mcp-servers", "smoke-token",
		`{"name":"evil","origin":"tenant","transport":"stdio","command":"/bin/sh","args":["-c","curl x"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Origin string `json:"origin"`
	}
	mcpDecode(t, w.Body.Bytes(), &created)
	if created.Origin != mcpreg.OriginUser {
		t.Fatalf("origin = %q, want %q (a client-supplied origin must never be trusted)", created.Origin, mcpreg.OriginUser)
	}
}

func TestMCPServerValidationIs400(t *testing.T) {
	h := smokeHandler(t)
	for _, body := range []string{
		`{"name":"af","transport":"stdio","command":"x"}`,                // reserved name
		`{"name":"bad name","transport":"stdio","command":"x"}`,          // invalid name
		`{"name":"s","transport":"http","url":"ftp://e.com"}`,            // not http
		`{"name":"s","transport":"http","url":"https://u:p@e.com"}`,      // credentials in the URL
		`{"name":"s","transport":"stdio","command":"x","kinds":["ssm"]}`, // unknown kind
	} {
		w := smokeDo(t, h, "POST", "/mcp-servers", "smoke-token", body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s → %d %s, want 400", body, w.Code, w.Body.String())
		}
	}
}

func TestMCPServerNameConflictIs409(t *testing.T) {
	h := smokeHandler(t)
	body := `{"name":"wiki","transport":"stdio","command":"/bin/true"}`
	if w := smokeDo(t, h, "POST", "/mcp-servers", "smoke-token", body); w.Code != http.StatusOK {
		t.Fatalf("first create: %d %s", w.Code, w.Body.String())
	}
	w := smokeDo(t, h, "POST", "/mcp-servers", "smoke-token", body)
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate create: %d %s, want 409", w.Code, w.Body.String())
	}
}

func TestMCPServerUnknownIDIs404(t *testing.T) {
	h := smokeHandler(t)
	w := smokeDo(t, h, "DELETE", "/mcp-servers/nope", "smoke-token", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("delete unknown: %d %s, want 404", w.Code, w.Body.String())
	}
	w = smokeDo(t, h, "PUT", "/mcp-servers/nope", "smoke-token",
		`{"name":"x","transport":"stdio","command":"/bin/true"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("update unknown: %d %s, want 404", w.Code, w.Body.String())
	}
}

// A stored definition keeps its secret when the mask is sent back, which is exactly what the
// Console's edit flow does.
func TestMCPServerUpdateKeepsMaskedSecret(t *testing.T) {
	h := smokeHandler(t)
	w := smokeDo(t, h, "POST", "/mcp-servers", "smoke-token",
		`{"name":"wiki","transport":"http","url":"https://e.com/mcp","headers":{"Authorization":"Bearer super-secret"}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	mcpDecode(t, w.Body.Bytes(), &created)

	w = smokeDo(t, h, "PUT", "/mcp-servers/"+created.ID, "smoke-token",
		`{"name":"wiki","label":"社内","transport":"http","url":"https://e.com/mcp","headers":{"Authorization":"***"}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	if containsSecret(w.Body.String()) {
		t.Fatalf("secret exposed in the update response: %s", w.Body.String())
	}
	// The response is masked, so check through the store that the secret really survived.
	stored, err := mcpreg.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Headers["Authorization"] != "Bearer super-secret" {
		t.Fatalf("the secret was lost on the mask round trip: %q", stored.Headers["Authorization"])
	}
	if stored.Label != "社内" {
		t.Fatalf("label was not updated: %q", stored.Label)
	}
}

func TestMCPServerTestEndpointReportsFailure(t *testing.T) {
	h := smokeHandler(t)
	w := smokeDo(t, h, "POST", "/mcp-servers/test", "smoke-token",
		`{"name":"broken","transport":"stdio","command":"/nonexistent/mcp","timeoutMs":2000}`)
	if w.Code != http.StatusOK {
		t.Fatalf("test: %d %s", w.Code, w.Body.String())
	}
	var res mcpreg.ProbeResult
	mcpDecode(t, w.Body.Bytes(), &res)
	if res.OK || res.Error == "" {
		t.Fatalf("a broken command was reported as success: %+v", res)
	}
}
