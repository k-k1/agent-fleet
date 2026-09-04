package mcpx

// The version contract of the local stdio MCP (docs/log/49 + ADR0032). Like the CP's /mcp it
// serves both eras at once, but stdio has neither an HTTP status nor headers, so the only
// evidence available is `_meta` and the server/discover response.

import (
	"encoding/json"
	"testing"
)

func stdioDispatch(t *testing.T, body string) map[string]any {
	t.Helper()
	out := dispatchMCPStdio([]byte(body))
	if out == nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	return m
}

func stdioMeta(version string) string {
	return `"_meta":{` +
		`"io.modelcontextprotocol/protocolVersion":"` + version + `",` +
		`"io.modelcontextprotocol/clientInfo":{"name":"t","version":"1"},` +
		`"io.modelcontextprotocol/clientCapabilities":{}}`
}

func stdioErr(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	e, ok := m["error"].(map[string]any)
	if !ok {
		t.Fatalf("no error in the answer: %+v", m)
	}
	return e
}

func TestStdioDiscoverAdvertisesBothEras(t *testing.T) {
	m := stdioDispatch(t, `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{`+stdioMeta(mcpStdioProtocol)+`}}`)
	res, ok := m["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result in the answer: %+v", m)
	}
	if res["resultType"] != "complete" {
		t.Fatalf("resultType = %v", res["resultType"])
	}
	vers, _ := res["supportedVersions"].([]any)
	if len(vers) == 0 || vers[0] != mcpStdioProtocol {
		t.Fatalf("supportedVersions = %v, want newest-first starting %q", vers, mcpStdioProtocol)
	}
	var sawLegacy bool
	for _, v := range vers {
		if v == mcpStdioLegacy {
			sawLegacy = true
		}
	}
	if !sawLegacy {
		t.Fatalf("the old version has disappeared from the advertisement: %v", vers)
	}
	if _, ok := res["serverInfo"]; !ok {
		t.Fatal("no top-level serverInfo (the SEP-2575 shape)")
	}
	meta, _ := res["_meta"].(map[string]any)
	if _, ok := meta[mcpMetaServerInfo]; !ok {
		t.Fatal("no serverInfo in _meta (the draft document's shape)")
	}
}

// An old client (no _meta) still gets through with initialize as before.
func TestStdioLegacyInitializeStillWorks(t *testing.T) {
	m := stdioDispatch(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	res, ok := m["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result in the answer: %+v", m)
	}
	if res["protocolVersion"] != "2025-06-18" {
		t.Fatalf("protocolVersion = %v, want echo", res["protocolVersion"])
	}
}

// tools/list works in both eras and carries resultType for new-era clients. ttlMs and
// cacheScope are required fields of a 2026-07-28 list result: without them a new-era client
// (measured with opencode 1.18.8) fails validation and disconnects the whole server.
func TestStdioToolsListBothEras(t *testing.T) {
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{` + stdioMeta(mcpStdioProtocol) + `}}`,
	} {
		m := stdioDispatch(t, body)
		res, ok := m["result"].(map[string]any)
		if !ok {
			t.Fatalf("%s → no result in the answer: %+v", body, m)
		}
		if res["resultType"] != "complete" {
			t.Fatalf("%s → resultType = %v", body, res["resultType"])
		}
		if ms, ok := res["ttlMs"].(float64); !ok || ms < 0 {
			t.Fatalf("%s → ttlMs = %v", body, res["ttlMs"])
		}
		if res["cacheScope"] != "private" {
			t.Fatalf("%s → cacheScope = %v", body, res["cacheScope"])
		}
		if tools, _ := res["tools"].([]any); len(tools) == 0 {
			t.Fatalf("%s → tools is empty", body)
		}
	}
}

func TestStdioUnsupportedVersionCarriesSupportedList(t *testing.T) {
	m := stdioDispatch(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{`+stdioMeta("1999-01-01")+`}}`)
	e := stdioErr(t, m)
	if int(e["code"].(float64)) != mcpErrUnsupportedVersion {
		t.Fatalf("code = %v, want %d", e["code"], mcpErrUnsupportedVersion)
	}
	data, _ := e["data"].(map[string]any)
	if data["requested"] != "1999-01-01" {
		t.Fatalf("requested = %v", data["requested"])
	}
	if sup, _ := data["supported"].([]any); len(sup) == 0 {
		t.Fatal("without the supported list a client cannot renegotiate")
	}
}

func TestStdioMissingRequiredMetaIsInvalidParams(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{` +
		`"io.modelcontextprotocol/protocolVersion":"` + mcpStdioProtocol + `"}}}`
	e := stdioErr(t, stdioDispatch(t, body))
	if int(e["code"].(float64)) != mcpErrInvalidParams {
		t.Fatalf("code = %v, want %d", e["code"], mcpErrInvalidParams)
	}
}

// An unknown method is -32601. That is how a dual-era client concludes "this is an old-version
// server"; swallowing it makes the client wait out its era-detection timeout instead.
func TestStdioUnknownMethodIsMethodNotFound(t *testing.T) {
	e := stdioErr(t, stdioDispatch(t, `{"jsonrpc":"2.0","id":1,"method":"does/notexist"}`))
	if int(e["code"].(float64)) != mcpErrMethodNotFound {
		t.Fatalf("code = %v, want %d", e["code"], mcpErrMethodNotFound)
	}
}

// A notification (no id) is never answered, not even when version validation rejects it.
func TestStdioNotificationsGetNoAnswer(t *testing.T) {
	for _, body := range []string{
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","method":"does/notexist"}`,
		`{"jsonrpc":"2.0","method":"tools/list","params":{` + stdioMeta("1999-01-01") + `}}`,
	} {
		if out := dispatchMCPStdio([]byte(body)); out != nil {
			t.Fatalf("answered the notification %s: %s", body, out)
		}
	}
}
