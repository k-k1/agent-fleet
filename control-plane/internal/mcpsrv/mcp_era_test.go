package mcpsrv

// The /mcp era contract (docs/log/49 + ADR0032). Both eras are served at once, so two
// things are pinned here: the new era's validation actually bites, and a legacy client
// still gets through unchanged. These call dispatchMCPHTTP directly — PAT auth is a
// separate layer and only era handling is under test.

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func eraReq(t *testing.T, body string, hdr map[string]string) (*http.Request, rpcRequest) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	var req rpcRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("bad test body: %v", err)
	}
	return r, req
}

// statelessBody builds a well-formed stateless request with the full _meta envelope.
func statelessBody(id int, method, extraParams, version string) string {
	meta := `"_meta":{` +
		`"io.modelcontextprotocol/protocolVersion":"` + version + `",` +
		`"io.modelcontextprotocol/clientInfo":{"name":"t","version":"1"},` +
		`"io.modelcontextprotocol/clientCapabilities":{}}`
	if extraParams != "" {
		meta = extraParams + "," + meta
	}
	return `{"jsonrpc":"2.0","id":` + itoa(id) + `,"method":"` + method + `","params":{` + meta + `}}`
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func statelessHdr(method string) map[string]string {
	return map[string]string{"MCP-Protocol-Version": mcpVersionStateless, "Mcp-Method": method}
}

func dispatch(t *testing.T, body string, hdr map[string]string) (*rpcResponse, int) {
	t.Helper()
	r, req := eraReq(t, body, hdr)
	return API{}.dispatchMCPHTTP(r, &mcpPrincipal{patID: "p"}, req)
}

// validate runs only the era/header checks. Used where dispatching would reach a tool
// implementation (and therefore the store), which is not what these tests are about.
func validate(t *testing.T, body string, hdr map[string]string) (*rpcResponse, int) {
	t.Helper()
	r, req := eraReq(t, body, hdr)
	p := parseParamsMeta(req.Params)
	return validateStateless(r, req, p, eraOf(p))
}

func TestMCPDiscoverAdvertisesBothEras(t *testing.T) {
	resp, status := dispatch(t, statelessBody(1, "server/discover", "", mcpVersionStateless),
		statelessHdr("server/discover"))
	if status != http.StatusOK || resp == nil || resp.Error != nil {
		t.Fatalf("discover: status=%d resp=%+v", status, resp)
	}
	res, _ := resp.Result.(map[string]any)
	if res["resultType"] != "complete" {
		t.Fatalf("resultType = %v (new-era clients read it as required)", res["resultType"])
	}
	vers, _ := res["supportedVersions"].([]string)
	if len(vers) == 0 || vers[0] != mcpVersionStateless {
		t.Fatalf("supportedVersions = %v, want newest-first starting %q", vers, mcpVersionStateless)
	}
	var sawLegacy bool
	for _, v := range vers {
		if v == mcpVersionLegacy {
			sawLegacy = true
		}
	}
	if !sawLegacy {
		t.Fatalf("the legacy version disappeared from the advertisement: %v (keep listing it as long as legacy clients are served)", vers)
	}
	// SEP and the draft document disagree on where serverInfo belongs, so it goes in both.
	if _, ok := res["serverInfo"]; !ok {
		t.Fatal("serverInfo is missing at top level (the SEP-2575 DiscoverResult shape)")
	}
	meta, _ := res["_meta"].(map[string]any)
	if _, ok := meta[metaServerInfo]; !ok {
		t.Fatal("serverInfo is missing from _meta (the draft document shape)")
	}
}

// TestMCPLegacyInitializeStillWorks: a legacy client (no _meta) passes through unchanged,
// and the header rules do not apply to it.
func TestMCPLegacyInitializeStillWorks(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`
	resp, status := dispatch(t, body, nil)
	if status != http.StatusOK || resp == nil || resp.Error != nil {
		t.Fatalf("initialize: status=%d resp=%+v", status, resp)
	}
	res, _ := resp.Result.(map[string]any)
	if res["protocolVersion"] != "2025-06-18" {
		t.Fatalf("protocolVersion = %v, want echo", res["protocolVersion"])
	}
}

func TestMCPHeaderMismatchIs400(t *testing.T) {
	cases := map[string]map[string]string{
		"version header disagrees with _meta": {"MCP-Protocol-Version": "2025-06-18", "Mcp-Method": "server/discover"},
		"version header missing":              {"Mcp-Method": "server/discover"},
		"Mcp-Method missing":                  {"MCP-Protocol-Version": mcpVersionStateless},
		"Mcp-Method differs":                  {"MCP-Protocol-Version": mcpVersionStateless, "Mcp-Method": "tools/list"},
	}
	for label, hdr := range cases {
		resp, status := validate(t, statelessBody(1, "server/discover", "", mcpVersionStateless), hdr)
		if status != http.StatusBadRequest || resp.Error == nil || resp.Error.Code != rpcHeaderMismatch {
			t.Errorf("%s: status=%d err=%+v, want 400/-32020", label, status, resp.Error)
		}
	}
}

// TestMCPNameHeaderMirrorsBody: Mcp-Name mirrors params.name, decoded first when it
// arrives as a base64 sentinel.
func TestMCPNameHeaderMirrorsBody(t *testing.T) {
	body := statelessBody(1, "tools/call", `"name":"list_my_sessions"`, mcpVersionStateless)
	hdr := statelessHdr("tools/call")

	hdr["Mcp-Name"] = "list_my_sessions"
	if resp, _ := validate(t, body, hdr); resp != nil {
		t.Fatalf("rejected even though it matches: %+v", resp.Error)
	}

	hdr["Mcp-Name"] = "other_tool"
	resp, status := validate(t, body, hdr)
	if status != http.StatusBadRequest || resp.Error == nil || resp.Error.Code != rpcHeaderMismatch {
		t.Fatalf("a mismatch got through: status=%d err=%+v", status, resp.Error)
	}

	// A name with non-ASCII characters travels as a base64 sentinel.
	jp := "検索"
	bodyJP := statelessBody(1, "tools/call", `"name":"`+jp+`"`, mcpVersionStateless)
	hdr["Mcp-Name"] = "=?base64?" + base64.StdEncoding.EncodeToString([]byte(jp)) + "?="
	if resp, _ := validate(t, bodyJP, hdr); resp != nil {
		t.Fatalf("the base64 sentinel was not decoded: %+v", resp.Error)
	}
}

// TestMCPNameNotRequiredWithoutName: a method that carries no name (server/discover,
// tools/list) does not need Mcp-Name.
func TestMCPNameNotRequiredWithoutName(t *testing.T) {
	if _, status := dispatch(t, statelessBody(1, "server/discover", "", mcpVersionStateless),
		statelessHdr("server/discover")); status != http.StatusOK {
		t.Fatalf("status=%d, want 200", status)
	}
}

func TestMCPUnsupportedVersionIs400WithSupportedList(t *testing.T) {
	hdr := map[string]string{"MCP-Protocol-Version": "1999-01-01", "Mcp-Method": "server/discover"}
	resp, status := dispatch(t, statelessBody(1, "server/discover", "", "1999-01-01"), hdr)
	if status != http.StatusBadRequest || resp.Error == nil || resp.Error.Code != rpcUnsupportedVersion {
		t.Fatalf("status=%d err=%+v, want 400/-32022", status, resp.Error)
	}
	data, _ := resp.Error.Data.(map[string]any)
	if data["requested"] != "1999-01-01" {
		t.Fatalf("requested = %v", data["requested"])
	}
	if sup, _ := data["supported"].([]string); len(sup) == 0 {
		t.Fatal("without the supported list a client cannot renegotiate")
	}
}

// TestMCPMissingRequiredMetaIs400: clientInfo / clientCapabilities are required on every
// request; without them it is malformed.
func TestMCPMissingRequiredMetaIs400(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{` +
		`"io.modelcontextprotocol/protocolVersion":"` + mcpVersionStateless + `"}}}`
	resp, status := dispatch(t, body, statelessHdr("server/discover"))
	if status != http.StatusBadRequest || resp.Error == nil || resp.Error.Code != rpcInvalidParams {
		t.Fatalf("status=%d err=%+v, want 400/-32602", status, resp.Error)
	}
}

// TestMCPUnknownMethodIs404: in the new era an unknown method is 404, which is what lets a
// client tell "new-era server without that method" from "legacy server" without reading
// the body.
func TestMCPUnknownMethodIs404(t *testing.T) {
	resp, status := dispatch(t, statelessBody(1, "does/notexist", "", mcpVersionStateless),
		statelessHdr("does/notexist"))
	if status != http.StatusNotFound || resp.Error == nil || resp.Error.Code != rpcMethodNotFound {
		t.Fatalf("status=%d err=%+v, want 404/-32601", status, resp.Error)
	}
}

// TestMCPLegacyUnknownMethodStays200: an unknown method from a legacy client keeps its old
// 200 + -32601 answer.
func TestMCPLegacyUnknownMethodStays200(t *testing.T) {
	resp, status := dispatch(t, `{"jsonrpc":"2.0","id":1,"method":"does/notexist"}`, nil)
	if status != http.StatusOK || resp.Error == nil || resp.Error.Code != rpcMethodNotFound {
		t.Fatalf("status=%d err=%+v, want 200/-32601", status, resp.Error)
	}
}

func TestMCPEndpointRejectsRemovedTransportVerbs(t *testing.T) {
	// The 2026-07-28 era dropped the GET stream and DELETE session teardown.
	for _, m := range []string{http.MethodGet, http.MethodDelete} {
		w := httptest.NewRecorder()
		API{}.HandleMCP(w, httptest.NewRequest(m, "/mcp", nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /mcp = %d, want 405", m, w.Code)
		}
	}
}
