package main

// /mcp の版契約（docs/log/49 + ADR0032）。両 era を同時に serve するので、固定したいのは
// 「新版の検証が効くこと」と「旧クライアントが従来どおり通ること」の 2 点。
// dispatchMCPHTTP を直接叩く（PAT 認証は別レイヤで、ここでは版の扱いだけを見る）。

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
	return mcpAPI{}.dispatchMCPHTTP(r, &mcpPrincipal{patID: "p"}, req)
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
		t.Fatalf("resultType = %v（新版クライアントは必須として読む）", res["resultType"])
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
		t.Fatalf("旧版が広告から消えている: %v（旧クライアントを serve し続ける限り載せる）", vers)
	}
	// serverInfo は SEP と draft ドキュメントで置き場が食い違うので両方に出す。
	if _, ok := res["serverInfo"]; !ok {
		t.Fatal("serverInfo が top-level に無い（SEP-2575 の DiscoverResult 形）")
	}
	meta, _ := res["_meta"].(map[string]any)
	if _, ok := meta[metaServerInfo]; !ok {
		t.Fatal("serverInfo が _meta に無い（draft ドキュメントの形）")
	}
}

// 旧クライアント（_meta 無し）は従来どおり素通り。ヘッダ規則も適用されない。
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
		"版ヘッダが _meta と食い違う": {"MCP-Protocol-Version": "2025-06-18", "Mcp-Method": "server/discover"},
		"版ヘッダが無い":           {"Mcp-Method": "server/discover"},
		"Mcp-Method が無い":    {"MCP-Protocol-Version": mcpVersionStateless},
		"Mcp-Method が違う":    {"MCP-Protocol-Version": mcpVersionStateless, "Mcp-Method": "tools/list"},
	}
	for label, hdr := range cases {
		resp, status := validate(t, statelessBody(1, "server/discover", "", mcpVersionStateless), hdr)
		if status != http.StatusBadRequest || resp.Error == nil || resp.Error.Code != rpcHeaderMismatch {
			t.Errorf("%s: status=%d err=%+v, want 400/-32020", label, status, resp.Error)
		}
	}
}

// Mcp-Name は params.name を写す。base64 sentinel で来たら復号してから比較する。
func TestMCPNameHeaderMirrorsBody(t *testing.T) {
	body := statelessBody(1, "tools/call", `"name":"list_my_sessions"`, mcpVersionStateless)
	hdr := statelessHdr("tools/call")

	hdr["Mcp-Name"] = "list_my_sessions"
	if resp, _ := validate(t, body, hdr); resp != nil {
		t.Fatalf("一致しているのに拒否された: %+v", resp.Error)
	}

	hdr["Mcp-Name"] = "other_tool"
	resp, status := validate(t, body, hdr)
	if status != http.StatusBadRequest || resp.Error == nil || resp.Error.Code != rpcHeaderMismatch {
		t.Fatalf("不一致が通ってしまった: status=%d err=%+v", status, resp.Error)
	}

	// 非 ASCII を含む名前は base64 sentinel で運ばれる。
	jp := "検索"
	bodyJP := statelessBody(1, "tools/call", `"name":"`+jp+`"`, mcpVersionStateless)
	hdr["Mcp-Name"] = "=?base64?" + base64.StdEncoding.EncodeToString([]byte(jp)) + "?="
	if resp, _ := validate(t, bodyJP, hdr); resp != nil {
		t.Fatalf("base64 sentinel が復号されていない: %+v", resp.Error)
	}
}

// name を持たないメソッドに Mcp-Name は要らない（server/discover / tools/list）。
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
		t.Fatal("supported 一覧が無いとクライアントは再交渉できない")
	}
}

// clientInfo / clientCapabilities は毎リクエスト必須。欠けたら malformed。
func TestMCPMissingRequiredMetaIs400(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{` +
		`"io.modelcontextprotocol/protocolVersion":"` + mcpVersionStateless + `"}}}`
	resp, status := dispatch(t, body, statelessHdr("server/discover"))
	if status != http.StatusBadRequest || resp.Error == nil || resp.Error.Code != rpcInvalidParams {
		t.Fatalf("status=%d err=%+v, want 400/-32602", status, resp.Error)
	}
}

// 新版では未知メソッドが 404。これがあるから、クライアントは本文を読まずとも
// 「新版サーバーだがそのメソッドが無い」と「旧版サーバー」を区別できる。
func TestMCPUnknownMethodIs404(t *testing.T) {
	resp, status := dispatch(t, statelessBody(1, "does/notexist", "", mcpVersionStateless),
		statelessHdr("does/notexist"))
	if status != http.StatusNotFound || resp.Error == nil || resp.Error.Code != rpcMethodNotFound {
		t.Fatalf("status=%d err=%+v, want 404/-32601", status, resp.Error)
	}
}

// 旧クライアントの未知メソッドは従来どおり 200 + -32601（挙動を変えない）。
func TestMCPLegacyUnknownMethodStays200(t *testing.T) {
	resp, status := dispatch(t, `{"jsonrpc":"2.0","id":1,"method":"does/notexist"}`, nil)
	if status != http.StatusOK || resp.Error == nil || resp.Error.Code != rpcMethodNotFound {
		t.Fatalf("status=%d err=%+v, want 200/-32601", status, resp.Error)
	}
}

func TestMCPEndpointRejectsRemovedTransportVerbs(t *testing.T) {
	// 2026-07-28 は GET ストリームと DELETE によるセッション破棄を廃止した。
	for _, m := range []string{http.MethodGet, http.MethodDelete} {
		w := httptest.NewRecorder()
		mcpAPI{}.handleMCP(w, httptest.NewRequest(m, "/mcp", nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /mcp = %d, want 405", m, w.Code)
		}
	}
}
