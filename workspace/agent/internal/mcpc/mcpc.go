// Package mcpc is the MCP CLIENT this harness build speaks with attached external MCP
// servers — ADR 0093 phase 1 segment F (see internal/harness/types.go's package doc for
// the D/E/F/G split). It supplies segment E's tool loop with a []harness.ToolDef and
// the ability to execute a tools/call against whichever server advertised the name.
//
// This is deliberately a NEW package rather than growing mcpreg/probe.go in place:
// probe.go is a one-shot reachability check (initialize/discover → tools/list, then
// drop the connection — see its own package doc) used by the registry's "test this
// definition" button, and several other kinds' materializers depend on it staying
// exactly that. mcpc borrows its era-detection knowledge (both transports speak both
// protocol revisions, docs/log/49 / ADR0031) but keeps the connection open for
// tools/call and notifications/tools/list_changed, and reconnects/kills processes on
// its own schedule instead of a single probe timeout.
//
// It is also NOT part of internal/harness: harness/types.go is the vocabulary segments
// D/E/F/G code against, not an implementation, and mcpc is one of the packages that
// implements against it (it returns []harness.ToolDef, never imports anything else from
// harness).
package mcpc

import (
	"encoding/json"
	"strconv"
)

// Protocol revisions this client can speak — the same two eras mcpreg/probe.go detects.
const (
	ProtocolVersion       = "2026-07-28"
	ProtocolVersionLegacy = "2025-06-18"
)

// Per-request `_meta` keys of the stateless era (SEP-2575).
const (
	metaProtocolVersion = "io.modelcontextprotocol/protocolVersion"
	metaClientInfo      = "io.modelcontextprotocol/clientInfo"
	metaClientCaps      = "io.modelcontextprotocol/clientCapabilities"
)

// Protocol-defined error codes the era detection keys off (mirrors mcpreg/probe.go).
const (
	errMethodNotFound     = -32601
	errUnsupportedVersion = -32022
)

const clientName = "agent-fleet-lcpp"

// rpcMsg is one JSON-RPC 2.0 message, request/response/notification alike.
type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  any             `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcErr         `json:"error,omitempty"`
}

type rpcErr struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcErr) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// isRequest reports whether m carries an id — a call needing a reply, as opposed to a
// fire-and-forget notification.
func isRequest(m rpcMsg) bool {
	id := string(m.ID)
	return id != "" && id != "null"
}

func idJSON(id int64) json.RawMessage {
	return json.RawMessage(strconv.FormatInt(id, 10))
}

func statelessMeta() map[string]any {
	return map[string]any{
		metaProtocolVersion: ProtocolVersion,
		metaClientInfo:      map[string]any{"name": clientName, "version": "1"},
		metaClientCaps:      map[string]any{},
	}
}

// withMeta wraps params with the stateless-era `_meta` envelope. params may be nil.
func withMeta(params map[string]any) map[string]any {
	if params == nil {
		params = map[string]any{}
	}
	params["_meta"] = statelessMeta()
	return params
}

func legacyInitParams() map[string]any {
	return map[string]any{
		"protocolVersion": ProtocolVersionLegacy,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": clientName, "version": "1"},
	}
}

// isLegacyEraSignal reports whether an error answer means "this server predates the
// stateless revision, retry with the handshake" (mirrors mcpreg/probe.go's function of
// the same name — duplicated rather than exported from there, see package doc).
func isLegacyEraSignal(e *rpcErr) bool {
	if e == nil {
		return false
	}
	if e.Code == errMethodNotFound {
		return true
	}
	if e.Code != errUnsupportedVersion {
		return false
	}
	var d struct {
		Supported []string `json:"supported"`
	}
	_ = json.Unmarshal(e.Data, &d)
	for _, v := range d.Supported {
		if v == ProtocolVersion {
			return false
		}
	}
	return true
}

// toolInfo is one entry of a tools/list result, MCP's own wire shape (inputSchema is
// passed through verbatim into harness.ToolDef.Parameters).
type toolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type toolsListResult struct {
	Tools []toolInfo `json:"tools"`
}
