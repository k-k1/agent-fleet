package main

// ローカル stdio MCP の版契約（docs/log/49 + ADR0032）。CP の /mcp と同じく両 era を
// 同時に serve するが、stdio には HTTP ステータスもヘッダも無いので、判別材料は
// `_meta` と server/discover の応答だけになる。

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
		t.Fatalf("error が無い: %+v", m)
	}
	return e
}

func TestStdioDiscoverAdvertisesBothEras(t *testing.T) {
	m := stdioDispatch(t, `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{`+stdioMeta(mcpStdioProtocol)+`}}`)
	res, ok := m["result"].(map[string]any)
	if !ok {
		t.Fatalf("result が無い: %+v", m)
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
		t.Fatalf("旧版が広告から消えている: %v", vers)
	}
	if _, ok := res["serverInfo"]; !ok {
		t.Fatal("serverInfo が top-level に無い（SEP-2575 の形）")
	}
	meta, _ := res["_meta"].(map[string]any)
	if _, ok := meta[mcpMetaServerInfo]; !ok {
		t.Fatal("serverInfo が _meta に無い（draft ドキュメントの形）")
	}
}

// 旧クライアント（_meta 無し）は従来どおり initialize で通る。
func TestStdioLegacyInitializeStillWorks(t *testing.T) {
	m := stdioDispatch(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	res, ok := m["result"].(map[string]any)
	if !ok {
		t.Fatalf("result が無い: %+v", m)
	}
	if res["protocolVersion"] != "2025-06-18" {
		t.Fatalf("protocolVersion = %v, want echo", res["protocolVersion"])
	}
}

// tools/list は両 era で通り、新版クライアント向けに resultType を持つ。ttlMs/cacheScope
// は 2026-07-28 の list 系結果の必須フィールドで、欠くと新 era クライアント（opencode
// 1.18.8 実測）が検証で弾いてサーバーごと切断する。
func TestStdioToolsListBothEras(t *testing.T) {
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{` + stdioMeta(mcpStdioProtocol) + `}}`,
	} {
		m := stdioDispatch(t, body)
		res, ok := m["result"].(map[string]any)
		if !ok {
			t.Fatalf("%s → result が無い: %+v", body, m)
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
			t.Fatalf("%s → tools が空", body)
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
		t.Fatal("supported 一覧が無いとクライアントは再交渉できない")
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

// 未知メソッドは -32601。これが両対応クライアントの「旧版サーバーだ」という判別材料で、
// 黙殺すると相手は era 判定のタイムアウトを待つことになる。
func TestStdioUnknownMethodIsMethodNotFound(t *testing.T) {
	e := stdioErr(t, stdioDispatch(t, `{"jsonrpc":"2.0","id":1,"method":"does/notexist"}`))
	if int(e["code"].(float64)) != mcpErrMethodNotFound {
		t.Fatalf("code = %v, want %d", e["code"], mcpErrMethodNotFound)
	}
}

// 通知（id 無し）は何があっても応答しない — 版検証で弾く場合も含めて。
func TestStdioNotificationsGetNoAnswer(t *testing.T) {
	for _, body := range []string{
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","method":"does/notexist"}`,
		`{"jsonrpc":"2.0","method":"tools/list","params":{` + stdioMeta("1999-01-01") + `}}`,
	} {
		if out := dispatchMCPStdio([]byte(body)); out != nil {
			t.Fatalf("%s に応答してしまった: %s", body, out)
		}
	}
}
