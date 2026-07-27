package main

// MCP レジストリ REST のワイヤ契約（docs/48 P0）。ここで固定したいのは
// 「秘密が外へ出ないこと」と「読み取り専用の行が編集できないこと」の 2 点で、
// どちらも壊れても通常の動作では気づけない種類の退行なので、経路ごと押さえる。

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
		t.Fatalf("作成応答に秘密が出ている: %s", got)
	}

	w = smokeDo(t, h, "GET", "/mcp-servers", "smoke-token", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	if containsSecret(w.Body.String()) {
		t.Fatalf("一覧に秘密が出ている: %s", w.Body.String())
	}
	var list mcpListResp
	mcpDecode(t, w.Body.Bytes(), &list)
	if len(list.Servers) != 1 {
		t.Fatalf("servers = %d, want 1", len(list.Servers))
	}
	got := list.Servers[0]
	if got.Headers["Authorization"] != mcpreg.MaskedValue {
		t.Fatalf("ヘッダがマスクされていない: %q", got.Headers["Authorization"])
	}
	if !got.Editable || !got.Ready {
		t.Fatalf("user 定義が editable/ready でない: %+v", got)
	}
}

func containsSecret(s string) bool { return strings.Contains(s, "super-secret") }

func TestMCPServerIgnoresClientSuppliedOrigin(t *testing.T) {
	h := smokeHandler(t)
	// origin はサーバー側で user に固定されるので、テナント stdio は REST からは
	// そもそも作れない。ここでは「クライアントが origin を詐称しても user 扱いに
	// 落ちる」ことを確認する（配布経路は CP 側 P4 で別途 400 を返す）。
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
		t.Fatalf("origin = %q, want %q（クライアント指定の origin を信じてはいけない）", created.Origin, mcpreg.OriginUser)
	}
}

func TestMCPServerValidationIs400(t *testing.T) {
	h := smokeHandler(t)
	for _, body := range []string{
		`{"name":"af","transport":"stdio","command":"x"}`,                // 予約名
		`{"name":"bad name","transport":"stdio","command":"x"}`,          // 不正な名前
		`{"name":"s","transport":"http","url":"ftp://e.com"}`,            // 非 http
		`{"name":"s","transport":"http","url":"https://u:p@e.com"}`,      // URL に資格情報
		`{"name":"s","transport":"stdio","command":"x","kinds":["ssm"]}`, // 未知の kind
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

// 保存済みの定義はマスクを送り返しても秘密を保つ。Console の編集フローそのもの。
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
		t.Fatalf("更新応答に秘密が出ている: %s", w.Body.String())
	}
	// 秘密が本当に残っているかは store 越しに確かめる（応答はマスクされているため）。
	stored, err := mcpreg.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Headers["Authorization"] != "Bearer super-secret" {
		t.Fatalf("マスク往復で秘密が失われた: %q", stored.Headers["Authorization"])
	}
	if stored.Label != "社内" {
		t.Fatalf("label が更新されていない: %q", stored.Label)
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
		t.Fatalf("壊れたコマンドが成功扱い: %+v", res)
	}
}
