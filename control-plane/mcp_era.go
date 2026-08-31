package main

// MCP 2026-07-28（ステートレス版）の受理層（docs/log/49 + ADR0032）。
//
// この版は initialize ハンドシェイクと Mcp-Session-Id を廃止し、版・クライアント情報・
// クライアント能力を **毎リクエストの `_meta`** で運ぶ。af の /mcp は元から純粋な switch で
// セッション状態を持たないので、移行の重い部分（state 撤去・sticky routing）は不要だった。
// ここで足すのは「新版の作法で来た要求を正しく受けて検証する」ことだけ。
//
// 旧クライアント（initialize を送る 2025-* 系）はそのまま通す。spec も
// 「両対応したいサーバーは旧 initialize を実装し続けてよい」と明示している（SEP-2575
// Backward Compatibility）。era の判定は「`_meta` に protocolVersion があるか」だけで行う。

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
)

const (
	// mcpVersionStateless is the revision this server prefers and advertises first.
	mcpVersionStateless = "2026-07-28"
	// mcpVersionLegacy is what an initialize-era client gets echoed when it asks for
	// nothing in particular. Kept at the value the server has always answered with.
	mcpVersionLegacy = "2025-06-18"
)

// mcpSupportedVersions is what server/discover advertises and what an
// UnsupportedProtocolVersionError lists, newest first.
var mcpSupportedVersions = []string{mcpVersionStateless, "2025-11-25", mcpVersionLegacy}

// Per-request `_meta` keys of the stateless era (SEP-2575).
const (
	metaProtocolVersion = "io.modelcontextprotocol/protocolVersion"
	metaClientInfo      = "io.modelcontextprotocol/clientInfo"
	metaClientCaps      = "io.modelcontextprotocol/clientCapabilities"
	metaServerInfo      = "io.modelcontextprotocol/serverInfo"
)

// Protocol-defined JSON-RPC error codes (draft schema; the -320xx block is the range
// the MCP spec reserves for itself).
const (
	rpcHeaderMismatch     = -32020
	rpcMissingClientCap   = -32021
	rpcUnsupportedVersion = -32022
	rpcMethodNotFound     = -32601
	rpcInvalidParams      = -32602
)

// mcpEra describes how one request was framed.
type mcpEra struct {
	// Stateless is true when the request carries the per-request `_meta` envelope,
	// i.e. it speaks 2026-07-28 or later. False = the initialize handshake era.
	Stateless bool
	Version   string
}

// paramsMeta is the slice of params this layer cares about: the `_meta` envelope and
// the `name` that the Mcp-Name header mirrors.
type paramsMeta struct {
	Meta map[string]json.RawMessage `json:"_meta"`
	Name string                     `json:"name"`
	URI  string                     `json:"uri"`
}

func parseParamsMeta(raw json.RawMessage) paramsMeta {
	var p paramsMeta
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &p)
	}
	return p
}

// eraOf classifies a request. The discriminator is the presence of the protocol
// version in `_meta` — an initialize-era client never sends it, and a stateless-era
// client MUST send it on every request.
func eraOf(p paramsMeta) mcpEra {
	raw, ok := p.Meta[metaProtocolVersion]
	if !ok {
		return mcpEra{}
	}
	var v string
	if json.Unmarshal(raw, &v) != nil || v == "" {
		return mcpEra{}
	}
	return mcpEra{Stateless: true, Version: v}
}

func versionSupported(v string) bool {
	for _, s := range mcpSupportedVersions {
		if s == v {
			return true
		}
	}
	return false
}

// rpcErrData builds an error response carrying a `data` payload (the stateless-era
// errors are defined with one).
func rpcErrData(id json.RawMessage, code int, msg string, data any) *rpcResponse {
	return &rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg, Data: data}}
}

// validateStateless applies the checks the spec requires of a stateless-era request,
// and reports the HTTP status to answer with. A nil response means "carry on".
//
// Order matters: version first (so an old/new mismatch reports the actionable
// UnsupportedProtocolVersionError rather than a confusing header complaint), then the
// required `_meta` fields, then the header/body mirroring.
func validateStateless(r *http.Request, req rpcRequest, p paramsMeta, era mcpEra) (*rpcResponse, int) {
	if !versionSupported(era.Version) {
		return rpcErrData(req.ID, rpcUnsupportedVersion,
			"unsupported protocol version: "+era.Version,
			map[string]any{"supported": mcpSupportedVersions, "requested": era.Version},
		), http.StatusBadRequest
	}
	// clientInfo / clientCapabilities are REQUIRED on every stateless request; a
	// request missing one is malformed (INVALID_PARAMS + 400).
	for _, k := range []string{metaClientInfo, metaClientCaps} {
		if _, ok := p.Meta[k]; !ok {
			return rpcErr(req.ID, rpcInvalidParams, "missing required _meta field: "+k), http.StatusBadRequest
		}
	}
	if resp := validateMirroredHeaders(r, req, p, era); resp != nil {
		return resp, http.StatusBadRequest
	}
	return nil, http.StatusOK
}

// validateMirroredHeaders enforces the header↔body agreement of the Streamable HTTP
// transport. The point is security, not pedantry: intermediaries route on the header
// while the server executes on the body, so a disagreement between the two is exactly
// the confusion an attacker wants. The spec's answer is to refuse (-32020).
func validateMirroredHeaders(r *http.Request, req rpcRequest, p paramsMeta, era mcpEra) *rpcResponse {
	if r == nil { // not an HTTP request (unit tests / non-HTTP transports)
		return nil
	}
	if got := r.Header.Get("MCP-Protocol-Version"); got != era.Version {
		return rpcErr(req.ID, rpcHeaderMismatch,
			"header mismatch: MCP-Protocol-Version "+quoteHdr(got)+" does not match _meta "+quoteHdr(era.Version))
	}
	if got := r.Header.Get("Mcp-Method"); got != req.Method {
		return rpcErr(req.ID, rpcHeaderMismatch,
			"header mismatch: Mcp-Method "+quoteHdr(got)+" does not match body method "+quoteHdr(req.Method))
	}
	// Mcp-Name mirrors params.name / params.uri, and is required only for the methods
	// that carry one. tools/list and server/discover have no name, so no header.
	want, needs := mirroredName(req.Method, p)
	if !needs {
		return nil
	}
	got, err := decodeHeaderValue(r.Header.Get("Mcp-Name"))
	if err != nil {
		return rpcErr(req.ID, rpcHeaderMismatch, "header mismatch: Mcp-Name is not valid base64 sentinel encoding")
	}
	if got != want {
		return rpcErr(req.ID, rpcHeaderMismatch,
			"header mismatch: Mcp-Name "+quoteHdr(got)+" does not match body value "+quoteHdr(want))
	}
	return nil
}

// mirroredName returns the value Mcp-Name must carry for this method, and whether the
// header is required at all.
func mirroredName(method string, p paramsMeta) (string, bool) {
	switch method {
	case "tools/call", "prompts/get":
		return p.Name, true
	case "resources/read":
		return p.URI, true
	}
	return "", false
}

// decodeHeaderValue undoes the base64 sentinel encoding (`=?base64?...?=`) clients
// MUST use when a value is not safely representable as a plain ASCII header.
func decodeHeaderValue(v string) (string, error) {
	const pre, suf = "=?base64?", "?="
	if !strings.HasPrefix(v, pre) || !strings.HasSuffix(v, suf) {
		return v, nil
	}
	b, err := base64.StdEncoding.DecodeString(v[len(pre) : len(v)-len(suf)])
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func quoteHdr(s string) string {
	if s == "" {
		return "(absent)"
	}
	return `"` + s + `"`
}

// mcpDiscoverResult answers server/discover. Servers MUST implement it; it is also
// what a stdio client probes with to decide which era to speak.
//
// serverInfo is emitted BOTH top-level (SEP-2575's DiscoverResult schema) and under
// `_meta` (the draft docs' example) because the two disagree and extra fields are
// harmless — a client reading either shape finds it.
func mcpDiscoverResult(name, version, instructions string) map[string]any {
	info := map[string]any{"name": name, "version": version}
	res := map[string]any{
		"resultType":        "complete",
		"supportedVersions": mcpSupportedVersions,
		"capabilities":      map[string]any{"tools": map[string]any{}},
		"serverInfo":        info,
		"_meta":             map[string]any{metaServerInfo: info},
	}
	if instructions != "" {
		res["instructions"] = instructions
	}
	return res
}
