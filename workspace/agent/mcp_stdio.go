package main

// Local stdio MCP server (docs/19 Q1). Spawned by the assistant chat's
// `claude -p --mcp-config` as `workspace-agent mcp-stdio`, it exposes READ-ONLY
// "Agent Fleet" tools over newline-delimited JSON-RPC 2.0 on stdio. Each tool calls
// the local Agent's REST (127.0.0.1:<AGENT_ADDR>, AGENT_TOKEN) so the assistant can
// inspect the user's OWN workspace with no PAT and no network egress — unlike the CP
// /mcp server (PAT + public-URL hairpin), which stays for external/admin use.
//
// Write tools (send_to_session, …) are exposed ONLY when the server is started with
// --write, which chat.go passes exclusively for conversations whose assistant granted
// af_write (docs/19 Q2). An af_read conversation's server never advertises or accepts a
// write tool — the gate is the advertised tool set, not just a permission prompt (the
// chat runs claude with --dangerously-skip-permissions, so a prompt would not gate).

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"
)

const mcpStdioProtocol = "2025-06-18"

// mcpWriteEnabled gates the write tools. Set once from the `--write` arg before the
// stdio loop starts; a global is safe because each spawn is a fresh short-lived process
// serving exactly one chat conversation.
var mcpWriteEnabled bool

// runMCPStdio is the `workspace-agent mcp-stdio` subcommand: a blocking stdio loop.
// Pass --write to additionally expose the write tools (docs/19 Q2 af_write opt-in).
func runMCPStdio(args []string) {
	for _, a := range args {
		if a == "--write" {
			mcpWriteEnabled = true
		}
	}
	r := bufio.NewReaderSize(os.Stdin, 1<<20)
	w := bufio.NewWriter(os.Stdout)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			if resp := dispatchMCPStdio(line); resp != nil {
				_, _ = w.Write(resp)
				_ = w.WriteByte('\n')
				_ = w.Flush()
			}
		}
		if err != nil {
			return // stdin closed (claude shut the server down)
		}
	}
}

type mcpReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"` // number|string for requests; absent/null for notifications
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func mcpResult(id json.RawMessage, result any) []byte {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	return b
}

func mcpError(id json.RawMessage, code int, msg string) []byte {
	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": msg},
	})
	return b
}

func dispatchMCPStdio(line []byte) []byte {
	var req mcpReq
	if err := json.Unmarshal(line, &req); err != nil {
		return nil
	}
	isNotif := len(bytes.TrimSpace(req.ID)) == 0 || string(bytes.TrimSpace(req.ID)) == "null"
	switch req.Method {
	case "initialize":
		return mcpResult(req.ID, map[string]any{
			"protocolVersion": mcpStdioProtocol,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "agent-fleet-local", "version": "q1"},
		})
	case "notifications/initialized", "notifications/cancelled":
		return nil
	case "ping":
		if isNotif {
			return nil
		}
		return mcpResult(req.ID, map[string]any{})
	case "tools/list":
		return mcpResult(req.ID, map[string]any{"tools": mcpStdioToolList()})
	case "tools/call":
		return mcpStdioCall(req)
	default:
		if isNotif {
			return nil
		}
		return mcpError(req.ID, -32601, "method not found: "+req.Method)
	}
}

// mcpStdioToolList is the advertised tool set: the read-only tools always, plus the
// write tools when the server was started with --write (docs/19 Q2 af_write opt-in).
func mcpStdioToolList() []map[string]any {
	if mcpWriteEnabled {
		return append(append([]map[string]any{}, mcpStdioTools...), mcpStdioWriteTools...)
	}
	return mcpStdioTools
}

// mcpStdioTools — read-only Agent Fleet tools (names are prefixed mcp__af__<name> by
// claude). Descriptions are prescriptive about WHEN to call (better trigger rate).
var mcpStdioTools = []map[string]any{
	{
		"name":        "list_my_sessions",
		"description": "利用者自身のワークスペースで稼働中のセッション一覧（名前・種別・状態・作業ディレクトリ）を返す。「今どのセッションが動いている?」等に答える時に呼ぶ。",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		"name":        "get_session_status",
		"description": "指定セッションのライブ状態（working/idle/入力待ち等）を返す。特定セッションが動作中か聞かれた時に呼ぶ。",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "セッション名（例: s7）"},
			},
			"required": []string{"name"},
		},
	},
	{
		"name":        "get_session_output",
		"description": "指定セッションの端末出力（任意で since バイトオフセット以降のみ）を返す。あるセッションの最近の出力/結果を要約・確認する時に呼ぶ。",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":  map[string]any{"type": "string", "description": "セッション名"},
				"since": map[string]any{"type": "integer", "description": "この出力オフセット以降のみ取得（任意）"},
			},
			"required": []string{"name"},
		},
	},
}

// mcpStdioWriteTools — Agent Fleet write/orchestrate tools, advertised only under --write
// (docs/19 af_write opt-in): drive tmux sessions (send_to_session) AND consult other
// assistants (list_assistants / ask_assistant). Consults are advisory-only by construction
// (the sub-turn runs with no tools), so they can't loop or escalate.
var mcpStdioWriteTools = []map[string]any{
	{
		"name":        "send_to_session",
		"description": "指定セッションにプロンプト（テキスト）を送信して実行させる（末尾に Enter）。すぐ返るので、応答は get_session_status で稼働確認後 get_session_output で取得する。利用者が「s7 に○○を伝えて/やらせて」等の作業依頼をした時に呼ぶ。",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":   map[string]any{"type": "string", "description": "送信先セッション名（例: s7）"},
				"prompt": map[string]any{"type": "string", "description": "送信するプロンプト本文"},
			},
			"required": []string{"name", "prompt"},
		},
	},
	{
		"name":        "list_assistants",
		"description": "利用可能なアシスタント（常設ビルトイン＋ユーザー定義）の一覧を返す。ask_assistant で誰に相談するか選ぶ前に呼ぶ。",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		"name":        "ask_assistant",
		"description": "別の専門アシスタントに助言を求める。相手は読み取り専用で1ターンだけ走り、助言テキストのみ返す（副作用なし・こちらの作業は代行しない）。例：整合性チェッカーに差分/原稿を見てもらう、用語集アシスタントに用語を確認する。まず list_assistants で相手を選ぶこと。",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"assistant": map[string]any{"type": "string", "description": "相談相手のアシスタント名または id"},
				"prompt":    map[string]any{"type": "string", "description": "相手に尋ねる内容（必要な文脈も含める）"},
			},
			"required": []string{"assistant", "prompt"},
		},
	},
}

func mcpStdioCall(req mcpReq) []byte {
	var p struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"arguments"`
	}
	_ = json.Unmarshal(req.Params, &p)
	var a struct {
		Name      string `json:"name"`
		Since     int64  `json:"since"`
		Prompt    string `json:"prompt"`
		Assistant string `json:"assistant"`
	}
	_ = json.Unmarshal(p.Args, &a)

	// Write/orchestrate tools — only when this server was started with --write.
	switch p.Name {
	case "send_to_session":
		if !mcpWriteEnabled {
			return mcpToolErr(req.ID, "このアシスタントは書き込みツールを許可されていません")
		}
		if a.Name == "" {
			return mcpToolErr(req.ID, "name（セッション名）が必要です")
		}
		if a.Prompt == "" {
			return mcpToolErr(req.ID, "prompt（送信本文）が必要です")
		}
		reqBody, _ := json.Marshal(map[string]string{"prompt": a.Prompt})
		out, err := agentPOST("/sessions/"+url.PathEscape(a.Name)+"/input", reqBody)
		if err != nil {
			return mcpToolErr(req.ID, "Agent への送信に失敗しました: "+err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "list_assistants":
		if !mcpWriteEnabled {
			return mcpToolErr(req.ID, "このアシスタントは他アシスタントへの相談を許可されていません")
		}
		out, err := agentGET("/assistants")
		if err != nil {
			return mcpToolErr(req.ID, "アシスタント一覧の取得に失敗しました: "+err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "ask_assistant":
		if !mcpWriteEnabled {
			return mcpToolErr(req.ID, "このアシスタントは他アシスタントへの相談を許可されていません")
		}
		if a.Assistant == "" || a.Prompt == "" {
			return mcpToolErr(req.ID, "assistant（相手）と prompt（相談内容）が必要です")
		}
		reqBody, _ := json.Marshal(map[string]string{"assistant": a.Assistant, "prompt": a.Prompt})
		out, err := agentPOST("/chat/ask", reqBody)
		if err != nil {
			return mcpToolErr(req.ID, "相談の実行に失敗しました: "+err.Error())
		}
		return mcpTextResult(req.ID, out)
	}

	var path string
	switch p.Name {
	case "list_my_sessions":
		path = "/sessions"
	case "get_session_status":
		if a.Name == "" {
			return mcpToolErr(req.ID, "name（セッション名）が必要です")
		}
		path = "/sessions/" + url.PathEscape(a.Name) + "/status"
	case "get_session_output":
		if a.Name == "" {
			return mcpToolErr(req.ID, "name（セッション名）が必要です")
		}
		path = "/sessions/" + url.PathEscape(a.Name) + "/output"
		if a.Since > 0 {
			path += fmt.Sprintf("?since=%d", a.Since)
		}
	default:
		return mcpError(req.ID, -32602, "unknown tool: "+p.Name)
	}

	body, err := agentGET(path)
	if err != nil {
		return mcpToolErr(req.ID, "Agent への問い合わせに失敗しました: "+err.Error())
	}
	return mcpResult(req.ID, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": body}},
	})
}

// mcpTextResult returns a tools/call RESULT carrying a single text content block.
func mcpTextResult(id json.RawMessage, text string) []byte {
	return mcpResult(id, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
	})
}

// mcpToolErr returns a tools/call RESULT with isError=true — an in-band error the
// model reads and can react to — rather than a JSON-RPC protocol error.
func mcpToolErr(id json.RawMessage, msg string) []byte {
	return mcpResult(id, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": msg}},
		"isError": true,
	})
}

// agentGET calls the local Agent REST with the shared AGENT_TOKEN.
func agentGET(path string) (string, error) { return agentDo(http.MethodGet, path, nil) }

// agentPOST calls the local Agent REST with a JSON body and the shared AGENT_TOKEN.
func agentPOST(path string, body []byte) (string, error) {
	return agentDo(http.MethodPost, path, body)
}

func agentDo(method, path string, body []byte) (string, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, agentBaseURL()+path, rdr)
	if err != nil {
		return "", err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tok := os.Getenv("AGENT_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return string(b), nil
}

// agentBaseURL derives the loopback URL of the in-container Agent from AGENT_ADDR.
func agentBaseURL() string {
	addr := envOr("AGENT_ADDR", ":7700")
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		port = "7700"
	}
	return "http://127.0.0.1:" + port
}
