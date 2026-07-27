package mcpreg

// Connection test (docs/48 §10). A registration that can't actually start is the
// single most likely failure, and without this the user only finds out by launching a
// session and digging the error out of the CLI's startup log. So the registry speaks
// just enough MCP itself: initialize → notifications/initialized → tools/list.
//
// Deliberately minimal — this is a reachability/handshake check, not a client. It
// never calls a tool and never keeps a connection.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ProtocolVersion is the MCP revision announced during the handshake. Kept equal to
// the one af's own servers speak (control-plane/mcp.go, mcp_stdio.go).
const ProtocolVersion = "2025-06-18"

const (
	defaultProbeTimeout = 10 * time.Second
	maxProbeTools       = 8 // names returned to the UI; a full list floods the panel
	maxProbeBody        = 4 << 20
)

// ProbeResult is the outcome of one connection test.
type ProbeResult struct {
	OK            bool     `json:"ok"`
	ServerName    string   `json:"serverName,omitempty"`
	ServerVersion string   `json:"serverVersion,omitempty"`
	ToolCount     int      `json:"toolCount"`
	Tools         []string `json:"tools,omitempty"`
	Error         string   `json:"error,omitempty"`
	// Detail carries the server's stderr / response body tail on failure. Truncated,
	// and shown verbatim to the user — a broken command usually explains itself there.
	Detail    string `json:"detail,omitempty"`
	ElapsedMS int64  `json:"elapsedMs"`
}

type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  any             `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func initializeParams() map[string]any {
	return map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "agent-fleet", "version": "probe"},
	}
}

type initResult struct {
	ServerInfo struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
}

type toolsResult struct {
	Tools []struct {
		Name string `json:"name"`
	} `json:"tools"`
}

// Probe runs the handshake against d and reports what came back.
func Probe(ctx context.Context, d ServerDef) ProbeResult {
	timeout := defaultProbeTimeout
	if d.TimeoutMS > 0 {
		timeout = time.Duration(d.TimeoutMS) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	var res ProbeResult
	switch d.Transport {
	case TransportStdio:
		res = probeStdio(ctx, d)
	case TransportHTTP:
		res = probeHTTP(ctx, d)
	default:
		res = ProbeResult{Error: fmt.Sprintf("未対応のトランスポートです: %s", d.Transport)}
	}
	res.ElapsedMS = time.Since(start).Milliseconds()
	if !res.OK && res.Error == "" {
		res.Error = "接続できませんでした"
	}
	return res
}

// --- stdio -----------------------------------------------------------------

func probeStdio(ctx context.Context, d ServerDef) ProbeResult {
	cmd := exec.CommandContext(ctx, d.Command, d.Args...)
	cmd.Env = os.Environ()
	for k, v := range d.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return ProbeResult{Error: err.Error()}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return ProbeResult{Error: err.Error()}
	}
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		return ProbeResult{Error: fmt.Sprintf("起動できませんでした: %v", err), Detail: tail(errBuf.String())}
	}
	defer func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	rd := bufio.NewReaderSize(stdout, 1<<20)
	fail := func(msg string) ProbeResult {
		return ProbeResult{Error: msg, Detail: tail(errBuf.String())}
	}

	if err := writeRPC(stdin, rpcMsg{JSONRPC: "2.0", ID: 1, Method: "initialize", Params: initializeParams()}); err != nil {
		return fail(err.Error())
	}
	msg, err := readRPCFor(ctx, rd, 1)
	if err != nil {
		return fail(err.Error())
	}
	if msg.Error != nil {
		return fail(fmt.Sprintf("initialize が拒否されました: %s", msg.Error.Message))
	}
	var ir initResult
	_ = json.Unmarshal(msg.Result, &ir)

	if err := writeRPC(stdin, rpcMsg{JSONRPC: "2.0", Method: "notifications/initialized"}); err != nil {
		return fail(err.Error())
	}
	if err := writeRPC(stdin, rpcMsg{JSONRPC: "2.0", ID: 2, Method: "tools/list", Params: map[string]any{}}); err != nil {
		return fail(err.Error())
	}
	msg, err = readRPCFor(ctx, rd, 2)
	if err != nil {
		return fail(err.Error())
	}
	if msg.Error != nil {
		return fail(fmt.Sprintf("tools/list が拒否されました: %s", msg.Error.Message))
	}
	return okResult(ir, msg.Result)
}

func writeRPC(w io.Writer, m rpcMsg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// readRPCFor reads newline-delimited JSON until the response with the wanted id
// arrives, skipping the server's own notifications and any non-JSON banner lines
// (some servers print a startup line before speaking protocol).
func readRPCFor(ctx context.Context, rd *bufio.Reader, wantID int) (rpcMsg, error) {
	type read struct {
		msg rpcMsg
		err error
	}
	ch := make(chan read, 1)
	go func() {
		for {
			line, err := rd.ReadBytes('\n')
			if len(bytes.TrimSpace(line)) > 0 {
				var m rpcMsg
				if json.Unmarshal(bytes.TrimSpace(line), &m) == nil && idEquals(m.ID, wantID) {
					ch <- read{msg: m}
					return
				}
			}
			if err != nil {
				ch <- read{err: fmt.Errorf("応答がありませんでした（プロセスが終了しました）")}
				return
			}
		}
	}()
	select {
	case <-ctx.Done():
		return rpcMsg{}, fmt.Errorf("タイムアウトしました")
	case r := <-ch:
		return r.msg, r.err
	}
}

func idEquals(got any, want int) bool {
	switch v := got.(type) {
	case float64:
		return int(v) == want
	case int:
		return v == want
	case string:
		return v == fmt.Sprint(want)
	}
	return false
}

// --- streamable http -------------------------------------------------------

func probeHTTP(ctx context.Context, d ServerDef) ProbeResult {
	client := &http.Client{}
	session := ""

	post := func(m rpcMsg) (rpcMsg, string, error) {
		b, err := json.Marshal(m)
		if err != nil {
			return rpcMsg{}, "", err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.URL, bytes.NewReader(b))
		if err != nil {
			return rpcMsg{}, "", err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", ProtocolVersion)
		for k, v := range d.Headers {
			req.Header.Set(k, v)
		}
		if session != "" {
			req.Header.Set("Mcp-Session-Id", session)
		}
		resp, err := client.Do(req)
		if err != nil {
			return rpcMsg{}, "", err
		}
		defer resp.Body.Close()
		if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
			session = sid
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxProbeBody))
		if resp.StatusCode == http.StatusAccepted || len(bytes.TrimSpace(body)) == 0 {
			return rpcMsg{}, string(body), nil // notification ack, or empty 200
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return rpcMsg{}, string(body), fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		out, err := decodeHTTPPayload(resp.Header.Get("Content-Type"), body)
		return out, string(body), err
	}

	msg, raw, err := post(rpcMsg{JSONRPC: "2.0", ID: 1, Method: "initialize", Params: initializeParams()})
	if err != nil {
		return ProbeResult{Error: err.Error(), Detail: tail(raw)}
	}
	if msg.Error != nil {
		return ProbeResult{Error: fmt.Sprintf("initialize が拒否されました: %s", msg.Error.Message), Detail: tail(raw)}
	}
	var ir initResult
	_ = json.Unmarshal(msg.Result, &ir)

	if _, raw, err = post(rpcMsg{JSONRPC: "2.0", Method: "notifications/initialized"}); err != nil {
		return ProbeResult{Error: err.Error(), Detail: tail(raw)}
	}
	msg, raw, err = post(rpcMsg{JSONRPC: "2.0", ID: 2, Method: "tools/list", Params: map[string]any{}})
	if err != nil {
		return ProbeResult{Error: err.Error(), Detail: tail(raw)}
	}
	if msg.Error != nil {
		return ProbeResult{Error: fmt.Sprintf("tools/list が拒否されました: %s", msg.Error.Message), Detail: tail(raw)}
	}
	return okResult(ir, msg.Result)
}

// decodeHTTPPayload handles both shapes a Streamable HTTP server may answer with:
// a plain JSON body, or an SSE stream whose `data:` lines carry the JSON-RPC
// messages. For SSE we take the first line that parses as a response.
func decodeHTTPPayload(contentType string, body []byte) (rpcMsg, error) {
	if strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		sc := bufio.NewScanner(bytes.NewReader(body))
		sc.Buffer(make([]byte, 0, 64<<10), maxProbeBody)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			var m rpcMsg
			if json.Unmarshal([]byte(strings.TrimSpace(line[len("data:"):])), &m) == nil && m.ID != nil {
				return m, nil
			}
		}
		return rpcMsg{}, fmt.Errorf("SSE 応答に JSON-RPC 応答が含まれていません")
	}
	var m rpcMsg
	if err := json.Unmarshal(bytes.TrimSpace(body), &m); err != nil {
		return rpcMsg{}, fmt.Errorf("応答を解釈できません: %v", err)
	}
	return m, nil
}

// --- shared ----------------------------------------------------------------

func okResult(ir initResult, toolsRaw json.RawMessage) ProbeResult {
	var tr toolsResult
	_ = json.Unmarshal(toolsRaw, &tr)
	res := ProbeResult{
		OK:            true,
		ServerName:    ir.ServerInfo.Name,
		ServerVersion: ir.ServerInfo.Version,
		ToolCount:     len(tr.Tools),
	}
	for i, t := range tr.Tools {
		if i >= maxProbeTools {
			break
		}
		res.Tools = append(res.Tools, t.Name)
	}
	return res
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	const max = 600
	if len(s) <= max {
		return s
	}
	return "…" + s[len(s)-max:]
}
