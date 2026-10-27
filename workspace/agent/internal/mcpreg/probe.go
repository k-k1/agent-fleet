package mcpreg

// Connection test (docs/log/48 §10, docs/log/49). A registration that can't actually start is
// the single most likely failure, and without this the user only finds out by launching
// a session and digging the error out of the CLI's startup log. So the registry speaks
// just enough MCP itself.
//
// It speaks BOTH eras (docs/log/49 / ADR0032), because a user's registered server may be
// either:
//   - 2026-07-28 (stateless): no handshake; version + clientInfo + clientCapabilities
//     ride in `_meta` on every request, and Streamable HTTP mirrors method/version into
//     headers. Capabilities come from `server/discover`.
//   - 2025-* (initialize era): initialize → notifications/initialized → tools/list,
//     with an optional Mcp-Session-Id to echo.
//
// The probe always opens with `server/discover`, which is exactly the era-detection
// move the spec prescribes for stdio and works just as well over HTTP: a legacy server
// answers `Method not found` (-32601) and we fall back to the handshake.
//
// Deliberately minimal — a reachability/handshake check, not a client. It never calls a
// tool and never keeps a connection.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Protocol revisions this probe can speak. ProtocolVersion is what it opens with;
// ProtocolVersionLegacy is the handshake-era version it falls back to.
const (
	ProtocolVersion       = "2026-07-28"
	ProtocolVersionLegacy = "2025-06-18"
)

// Per-request `_meta` keys of the stateless era (SEP-2575).
const (
	metaProtocolVersion = "io.modelcontextprotocol/protocolVersion"
	metaClientInfo      = "io.modelcontextprotocol/clientInfo"
	metaClientCaps      = "io.modelcontextprotocol/clientCapabilities"
	metaServerInfo      = "io.modelcontextprotocol/serverInfo"
)

// Protocol-defined error codes the era detection keys off.
const (
	errHeaderMismatch     = -32020
	errMissingClientCap   = -32021
	errUnsupportedVersion = -32022
	errMethodNotFound     = -32601
)

const (
	defaultProbeTimeout = 10 * time.Second
	maxProbeTools       = 8 // names returned to the UI; a full list floods the panel
	maxProbeBody        = 4 << 20
	// eraProbeTimeout bounds the stdio era-detection step. JSON-RPC says an unknown
	// method MUST get -32601, but a server that simply ignores it would otherwise hang
	// the whole probe on its full timeout. Waiting a short beat and then treating
	// silence as "legacy era" costs one wasted second and saves an unexplained stall.
	eraProbeTimeout = 2 * time.Second
)

// sessionBoundRequests is how many tools/list calls carry the session id of a stateful
// legacy-era HTTP server. With two instances behind a balancer, 6 calls miss with
// probability (1/2)^6; each pays its own TCP/TLS handshake, so keep it inside
// defaultProbeTimeout.
const sessionBoundRequests = 6

// affinityBudget caps the extra session-bound requests (all but the first tools/list).
const affinityBudget = 5 * time.Second

// errSessionRejected is the verdict for a server that refuses the session id it just issued
// (several instances, in-memory sessions, no affinity).
const errSessionRejected = "The server rejected the session ID it just issued. It may be running as several instances without session affinity (routing a session to the same instance). Make the server stateless, or route every request of a session to the same instance"

// errOAuthRequired is the Error of a recognised OAuth-protected server. The Console keys off
// ProbeResult.OAuth, not this text; it is what an older Console shows, so it names where
// sign-in happens today (the server's row) rather than anything the Console would explain.
const errOAuthRequired = "This server requires OAuth sign-in. Choose OAuth as the authentication, save, then sign in from the server's row"

// Stable failure codes of ProbeResult.Code. The Console renders them from its catalogues
// (mcp.probe_err.<code>) with ProbeResult.Params; Error stays the English fallback for an
// older Console or a caller that has no catalogue. Adding or renaming a code means changing
// the Console's en and ja catalogues together.
const (
	codeUnsupportedTransport = "unsupported_transport" // params: transport
	codeUnreachable          = "unreachable"
	codeStartFailed          = "start_failed" // params: error
	codeProcessExited        = "process_exited"
	codeTimeout              = "timeout"      // params: timeoutMs
	codeRPCRejected          = "rpc_rejected" // params: method, message
	codeSSENoResponse        = "sse_no_response"
	codeBadResponse          = "bad_response" // params: error
	codeSessionRejected      = "session_rejected"
	codeOAuthRequired        = "oauth_required"
)

// probeErr is an error that carries its ProbeResult code and parameters, so a failure raised
// deep in an exchange keeps its identity up to the result.
type probeErr struct {
	code   string
	params map[string]string
	msg    string
}

func (e *probeErr) Error() string { return e.msg }

// errProbeTimeout distinguishes "no answer in time" from "the process died", so the
// era probe can fall back on the former and fail fast on the latter. Probe adds the
// timeoutMs parameter once it knows the budget.
var errProbeTimeout error = &probeErr{code: codeTimeout, msg: "timed out"}

// coded builds a failed result with a code, its parameters (key, value pairs) and the English
// fallback text.
func coded(code, msg string, kv ...string) ProbeResult {
	r := ProbeResult{Error: msg, Code: code}
	for i := 0; i+1 < len(kv); i += 2 {
		if r.Params == nil {
			r.Params = map[string]string{}
		}
		r.Params[kv[i]] = kv[i+1]
	}
	return r
}

// rejected is the failure for a JSON-RPC error answer to method.
func rejected(method, message string) ProbeResult {
	return coded(codeRPCRejected, fmt.Sprintf("%s was rejected: %s", method, message), "method", method, "message", message)
}

// failOf turns an exchange error into a result, keeping its code when it has one.
func failOf(err error) ProbeResult {
	var pe *probeErr
	if errors.As(err, &pe) {
		r := coded(pe.code, pe.msg)
		for k, v := range pe.params {
			if r.Params == nil {
				r.Params = map[string]string{}
			}
			r.Params[k] = v
		}
		return r
	}
	return ProbeResult{Error: err.Error()}
}

// withDetail returns r with Detail set.
func withDetail(r ProbeResult, detail string) ProbeResult {
	r.Detail = detail
	return r
}

// ProbeResult is the outcome of one connection test.
type ProbeResult struct {
	OK            bool     `json:"ok"`
	ServerName    string   `json:"serverName,omitempty"`
	ServerVersion string   `json:"serverVersion,omitempty"`
	ToolCount     int      `json:"toolCount"`
	Tools         []string `json:"tools,omitempty"`
	// Revision is the protocol era the server actually answered in. Surfaced because
	// "it works, but only via the legacy handshake" is exactly what an operator needs
	// to know while the ecosystem migrates.
	Revision string `json:"revision,omitempty"`
	// SupportedVersions is what server/discover advertised (stateless servers only).
	SupportedVersions []string `json:"supportedVersions,omitempty"`
	Error             string   `json:"error,omitempty"`
	// Code and Params are the stable, language-free form of Error: the Console renders its
	// own text from them (mcp.probe_err.<code>). Error is the English fallback for a Console
	// that does not know the code. Empty when Error is the server's or the OS's own text.
	Code   string            `json:"code,omitempty"`
	Params map[string]string `json:"params,omitempty"`
	// Detail carries the server's stderr / response body tail on failure. Truncated,
	// and shown verbatim to the user — a broken command usually explains itself there.
	Detail string `json:"detail,omitempty"`
	// OAuth is set when the server answered 401 as an OAuth-protected resource. OK stays
	// false (nothing was listed), but the Console shows the report instead of a failure.
	OAuth     *OAuthReport `json:"oauth,omitempty"`
	ElapsedMS int64        `json:"elapsedMs"`
}

type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  any             `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcErrBody     `json:"error,omitempty"`
}

type rpcErrBody struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// statelessMeta is the `_meta` envelope every stateless-era request must carry.
func statelessMeta() map[string]any {
	return map[string]any{
		metaProtocolVersion: ProtocolVersion,
		metaClientInfo:      map[string]any{"name": "agent-fleet", "version": "probe"},
		metaClientCaps:      map[string]any{},
	}
}

func statelessReq(id int, method string) rpcMsg {
	return rpcMsg{JSONRPC: "2.0", ID: id, Method: method,
		Params: map[string]any{"_meta": statelessMeta()}}
}

func legacyInitParams() map[string]any {
	return map[string]any{
		"protocolVersion": ProtocolVersionLegacy,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "agent-fleet", "version": "probe"},
	}
}

// discoverResult covers both shapes of DiscoverResult: SEP-2575 puts serverInfo at the
// top level, the draft docs' example puts it under `_meta`. Read whichever is present.
type discoverResult struct {
	SupportedVersions []string `json:"supportedVersions"`
	ServerInfo        *impl    `json:"serverInfo"`
	Meta              struct {
		ServerInfo *impl `json:"io.modelcontextprotocol/serverInfo"`
	} `json:"_meta"`
}

type impl struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func (d discoverResult) info() impl {
	if d.ServerInfo != nil {
		return *d.ServerInfo
	}
	if d.Meta.ServerInfo != nil {
		return *d.Meta.ServerInfo
	}
	return impl{}
}

// legacyInitResult is the initialize-era serverInfo.
type legacyInitResult struct {
	ServerInfo impl `json:"serverInfo"`
}

type toolsResult struct {
	Tools []struct {
		Name string `json:"name"`
	} `json:"tools"`
}

// isLegacyEraSignal reports whether an error answer means "this server predates the
// stateless revision, retry with the handshake". -32601 is the direct signal; -32022
// means a stateless-aware server that refuses our version, and if its supported list
// has no stateless entry the handshake is the only way in.
func isLegacyEraSignal(e *rpcErrBody) bool {
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
			return false // it does speak our version; something else went wrong
		}
	}
	return true
}

// isModernErr reports whether an error body is one only a stateless-era server emits.
// Per the transport spec, a 400 carrying one of these means "modern server, fix the
// request" — NOT "fall back to the handshake".
func isModernErr(e *rpcErrBody) bool {
	return e != nil && (e.Code == errHeaderMismatch || e.Code == errMissingClientCap || e.Code == errUnsupportedVersion)
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
		res = coded(codeUnsupportedTransport, fmt.Sprintf("unsupported transport: %s", d.Transport), "transport", string(d.Transport))
	}
	res.ElapsedMS = time.Since(start).Milliseconds()
	if !res.OK && res.Error == "" {
		res = coded(codeUnreachable, "could not connect")
		res.ElapsedMS = time.Since(start).Milliseconds()
	}
	if res.Code == codeTimeout {
		res.Params = map[string]string{"timeoutMs": strconv.FormatInt(timeout.Milliseconds(), 10)}
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
		return withDetail(coded(codeStartFailed, fmt.Sprintf("failed to start: %v", err), "error", err.Error()), tail(errBuf.String()))
	}
	defer func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	// ONE reader for the whole probe. Spawning a goroutine per exchange looks simpler
	// but is wrong: when a wait is abandoned (the era probe's short deadline), the
	// orphaned reader keeps consuming stdout and swallows the NEXT exchange's answer,
	// so the fallback path times out even though the server replied. A single reader
	// feeding a channel lets an abandoned wait leave the stream intact.
	msgs := make(chan rpcMsg, 8)
	readErr := make(chan error, 1)
	// done releases the reader once the probe returns: after that nobody drains msgs,
	// and a chatty server (9+ id-carrying messages) would otherwise block the send
	// forever — a goroutine leak. done is closed before the kill/Wait defer (LIFO).
	done := make(chan struct{})
	defer close(done)
	go func() {
		rd := bufio.NewReaderSize(stdout, 1<<20)
		for {
			line, err := rd.ReadBytes('\n')
			if len(bytes.TrimSpace(line)) > 0 {
				var m rpcMsg
				// Non-JSON banner lines and the server's own notifications are skipped.
				if json.Unmarshal(bytes.TrimSpace(line), &m) == nil && m.ID != nil {
					select {
					case msgs <- m:
					case <-done:
						return
					}
				}
			}
			if err != nil {
				readErr <- &probeErr{code: codeProcessExited, msg: "no response (the process exited)"}
				return
			}
		}
	}()

	fail := func(r ProbeResult) ProbeResult { return withDetail(r, tail(errBuf.String())) }
	// failErr reports a transport-level error (read, write, process exit). Once the
	// probe budget is spent the context kill is the cause of all of them, so the
	// answer is timeout whichever symptom surfaced first.
	failErr := func(err error) ProbeResult {
		if ctx.Err() != nil {
			err = errProbeTimeout
		}
		return fail(failOf(err))
	}
	exchangeCtx := func(c context.Context, id int, m rpcMsg) (rpcMsg, error) {
		if err := writeRPC(stdin, m); err != nil {
			// A dead server is the cause of the broken pipe: prefer its exit error.
			select {
			case re := <-readErr:
				return rpcMsg{}, re
			default:
			}
			return rpcMsg{}, err
		}
		return readRPCFor(c, ctx, msgs, readErr, id)
	}
	exchange := func(id int, m rpcMsg) (rpcMsg, error) { return exchangeCtx(ctx, id, m) }

	// Era probe: server/discover first. stdio has no per-request status code to key
	// off, so this RPC IS the detection mechanism the spec prescribes. It runs on a
	// short deadline of its own — see eraProbeTimeout.
	eraCtx, cancelEra := context.WithTimeout(ctx, eraProbeTimeout)
	msg, err := exchangeCtx(eraCtx, 1, statelessReq(1, "server/discover"))
	cancelEra()
	switch {
	case err == nil && msg.Error == nil:
		var dr discoverResult
		_ = json.Unmarshal(msg.Result, &dr)
		msg, err = exchange(2, statelessReq(2, "tools/list"))
		if err != nil {
			return failErr(err)
		}
		if msg.Error != nil {
			return fail(rejected("tools/list", msg.Error.Message))
		}
		res := okResult(dr.info(), msg.Result)
		res.Revision, res.SupportedVersions = ProtocolVersion, dr.SupportedVersions
		return res
	case errors.Is(err, errProbeTimeout) && ctx.Err() == nil:
		// Silence, but the overall budget is intact: a server that ignores unknown
		// methods instead of answering -32601. Treat it as the legacy era, unless the
		// server exited meanwhile: the era deadline may have beaten a pending EOF.
		select {
		case re := <-readErr:
			return failErr(re)
		default:
		}
	case err != nil:
		return failErr(err) // process died, or the whole probe timed out
	case !isLegacyEraSignal(msg.Error):
		return fail(rejected("server/discover", msg.Error.Message))
	}

	// Legacy era: the full initialize handshake.
	msg, err = exchange(3, rpcMsg{JSONRPC: "2.0", ID: 3, Method: "initialize", Params: legacyInitParams()})
	if err != nil {
		return failErr(err)
	}
	if msg.Error != nil {
		return fail(rejected("initialize", msg.Error.Message))
	}
	var ir legacyInitResult
	_ = json.Unmarshal(msg.Result, &ir)
	if err := writeRPC(stdin, rpcMsg{JSONRPC: "2.0", Method: "notifications/initialized"}); err != nil {
		return failErr(err)
	}
	msg, err = exchange(4, rpcMsg{JSONRPC: "2.0", ID: 4, Method: "tools/list", Params: map[string]any{}})
	if err != nil {
		return failErr(err)
	}
	if msg.Error != nil {
		return fail(rejected("tools/list", msg.Error.Message))
	}
	res := okResult(ir.ServerInfo, msg.Result)
	res.Revision = ProtocolVersionLegacy
	return res
}

func writeRPC(w io.Writer, m rpcMsg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// readRPCFor waits for the response with the wanted id, discarding anything else that
// arrives (late answers to an abandoned exchange, stray notifications). ctx bounds this
// wait (it may be a short sub-deadline); budget is the whole probe's context. Only the
// budget expiring turns a reader error into a timeout — an exit seen under a mere
// sub-deadline stays a process exit.
func readRPCFor(ctx, budget context.Context, msgs <-chan rpcMsg, readErr <-chan error, wantID int) (rpcMsg, error) {
	for {
		select {
		case <-ctx.Done():
			return rpcMsg{}, errProbeTimeout
		case err := <-readErr:
			// The context kill closes stdout, so after the deadline both channels are
			// ready and select picks at random: an expired context wins.
			if budget.Err() != nil {
				return rpcMsg{}, errProbeTimeout
			}
			return rpcMsg{}, err
		case m := <-msgs:
			if idEquals(m.ID, wantID) {
				return m, nil
			}
		}
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

// httpExchange is one POST plus the decoded answer. status is the HTTP status, raw the
// (truncated) body for diagnostics.
type httpExchange struct {
	msg    rpcMsg
	status int
	raw    string
	header http.Header
}

func probeHTTP(ctx context.Context, d ServerDef) ProbeResult {
	// No cookie jar, on purpose: Claude Code never sends cookies back on MCP requests, so a
	// probe honouring load-balancer cookie stickiness would pass where real sessions fail.
	client := &http.Client{}
	// Session-bound requests each open a new connection. A load balancer pins a kept-alive
	// connection to one target, so reusing it hides the instance that does not know the
	// session (measured: 10 presses of the test passed against two instances; new
	// connections were rejected about half the time).
	freshTransport := http.DefaultTransport.(*http.Transport).Clone()
	freshTransport.DisableKeepAlives = true
	fresh := &http.Client{Transport: freshTransport}
	session := "" // legacy era only: 2026-07-28 removed Mcp-Session-Id entirely

	// oauthFrom turns a 401 from any step of the test into the OAuth report when the server
	// is OAuth-protected: a server may leave server/discover public and challenge only later.
	oauthFrom := func(ex httpExchange) (ProbeResult, bool) {
		if ex.status != http.StatusUnauthorized {
			return ProbeResult{}, false
		}
		rep, ok := probeOAuth(ctx, d.URL, ex.header)
		if !ok {
			return ProbeResult{}, false
		}
		res := coded(codeOAuthRequired, errOAuthRequired)
		res.OAuth = &rep
		return res, true
	}

	reqCtx := ctx
	post := func(m rpcMsg, stateless bool) (httpExchange, error) {
		b, err := json.Marshal(m)
		if err != nil {
			return httpExchange{}, err
		}
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, d.URL, bytes.NewReader(b))
		if err != nil {
			return httpExchange{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if stateless {
			// The transport mirrors these body fields into headers so intermediaries can
			// route without parsing; a server MUST reject a disagreement (-32020), so the
			// values here have to be exactly what the body says. Mcp-Name is only required
			// for methods carrying params.name/uri — server/discover and tools/list do not.
			req.Header.Set("MCP-Protocol-Version", ProtocolVersion)
			req.Header.Set("Mcp-Method", m.Method)
		} else {
			req.Header.Set("MCP-Protocol-Version", ProtocolVersionLegacy)
			if session != "" {
				req.Header.Set("Mcp-Session-Id", session)
			}
		}
		for k, v := range d.Headers {
			req.Header.Set(k, v)
		}
		c := client
		if session != "" && !stateless {
			c = fresh
		}
		resp, err := c.Do(req)
		if err != nil {
			return httpExchange{}, err
		}
		defer resp.Body.Close()
		if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" && !stateless {
			session = sid
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxProbeBody))
		ex := httpExchange{status: resp.StatusCode, raw: string(body), header: resp.Header}
		if resp.StatusCode == http.StatusAccepted || len(bytes.TrimSpace(body)) == 0 {
			return ex, nil // notification ack, or empty 200
		}
		ex.msg, err = decodeHTTPPayload(resp.Header.Get("Content-Type"), body)
		if err != nil && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
			err = nil // a plain-text error body: callers report the status and body tail instead
		}
		return ex, err
	}

	// Era probe. A stateless server answers server/discover; a legacy one returns
	// Method not found. Per the transport spec, a 400 whose body is a recognized
	// modern error means "modern server, fix the request" — do NOT fall back then.
	ex, err := post(statelessReq(1, "server/discover"), true)
	if err != nil && ex.status == 0 {
		return withDetail(failOf(err), tail(ex.raw))
	}
	if res, ok := oauthFrom(ex); ok {
		return res
	}
	statelessServer := ex.msg.Error == nil && len(ex.msg.Result) > 0
	if !statelessServer && isModernErr(ex.msg.Error) && !isLegacyEraSignal(ex.msg.Error) {
		return withDetail(rejected("server/discover", ex.msg.Error.Message), tail(ex.raw))
	}

	if statelessServer {
		var dr discoverResult
		_ = json.Unmarshal(ex.msg.Result, &dr)
		ex, err = post(statelessReq(2, "tools/list"), true)
		if err != nil {
			return withDetail(failOf(err), tail(ex.raw))
		}
		if res, ok := oauthFrom(ex); ok {
			return res
		}
		if ex.status < 200 || ex.status >= 300 {
			return ProbeResult{Error: fmt.Sprintf("HTTP %d", ex.status), Detail: tail(ex.raw)}
		}
		if ex.msg.Error != nil {
			return withDetail(rejected("tools/list", ex.msg.Error.Message), tail(ex.raw))
		}
		res := okResult(dr.info(), ex.msg.Result)
		res.Revision, res.SupportedVersions = ProtocolVersion, dr.SupportedVersions
		return res
	}

	// Legacy era.
	ex, err = post(rpcMsg{JSONRPC: "2.0", ID: 3, Method: "initialize", Params: legacyInitParams()}, false)
	if err != nil {
		return withDetail(failOf(err), tail(ex.raw))
	}
	if res, ok := oauthFrom(ex); ok {
		return res
	}
	if ex.status < 200 || ex.status >= 300 {
		return ProbeResult{Error: fmt.Sprintf("HTTP %d", ex.status), Detail: tail(ex.raw)}
	}
	if ex.msg.Error != nil {
		return withDetail(rejected("initialize", ex.msg.Error.Message), tail(ex.raw))
	}
	var ir legacyInitResult
	_ = json.Unmarshal(ex.msg.Result, &ir)
	// A server that issued a session id is stateful. Several session-bound requests, not
	// one: behind a load balancer without affinity a single request reaches the issuing
	// instance with probability 1/N and the test would pass by luck.
	stateful := session != ""
	failStatus := func(ex httpExchange) ProbeResult {
		if res, ok := oauthFrom(ex); ok {
			return res
		}
		if stateful && sessionRejected(ex) {
			return withDetail(coded(codeSessionRejected, errSessionRejected), fmt.Sprintf("HTTP %d: %s", ex.status, tail(ex.raw)))
		}
		return ProbeResult{Error: fmt.Sprintf("HTTP %d", ex.status), Detail: tail(ex.raw)}
	}
	ex, err = post(rpcMsg{JSONRPC: "2.0", Method: "notifications/initialized"}, false)
	if err != nil {
		return ProbeResult{Error: err.Error()}
	}
	if ex.status < 200 || ex.status >= 300 {
		return failStatus(ex)
	}
	// The first tools/list is the real check and its answer is the result.
	ex, err = post(rpcMsg{JSONRPC: "2.0", ID: 4, Method: "tools/list", Params: map[string]any{}}, false)
	if err != nil {
		return withDetail(failOf(err), tail(ex.raw))
	}
	if ex.status < 200 || ex.status >= 300 {
		return failStatus(ex)
	}
	if ex.msg.Error != nil {
		return withDetail(rejected("tools/list", ex.msg.Error.Message), tail(ex.raw))
	}
	first := ex
	if stateful {
		// Affinity diagnosis on its own, smaller budget. Only a session rejection fails the
		// test; a timeout, a 429 or any other trouble here is inconclusive, so a healthy
		// but slow or rate-limited server is not reported as unreachable.
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, affinityBudget)
		defer cancel()
		for i := 1; i < sessionBoundRequests; i++ {
			ex, err = post(rpcMsg{JSONRPC: "2.0", ID: 4 + i, Method: "tools/list", Params: map[string]any{}}, false)
			if err != nil || ex.status < 200 || ex.status >= 300 {
				if err == nil && sessionRejected(ex) {
					return failStatus(ex)
				}
				break
			}
		}
	}
	ex = first
	res := okResult(ir.ServerInfo, ex.msg.Result)
	res.Revision = ProtocolVersionLegacy
	return res
}

// sessionRejected reports whether ex is a server refusing a session id it issued: 404, or
// a 400 whose body says the session is unknown.
func sessionRejected(ex httpExchange) bool {
	switch ex.status {
	case http.StatusNotFound:
		return true
	case http.StatusBadRequest:
		// Judge the error text only: a body that merely echoes a sessionId next to an
		// unrelated complaint is not an unknown session.
		msg := strings.ToLower(errorText(ex.raw))
		if !strings.Contains(msg, "session") {
			return false
		}
		for _, w := range []string{"not found", "unknown", "invalid", "expired", "no such", "not exist"} {
			if strings.Contains(msg, w) {
				return true
			}
		}
	}
	return false
}

// errorText extracts the human-readable error message(s) from a response body: the
// message-like fields of a JSON object (also under "error"), or the whole body when it
// is not JSON.
func errorText(raw string) string {
	var obj map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(raw)), &obj) != nil {
		return raw
	}
	var parts []string
	var collect func(m map[string]any)
	collect = func(m map[string]any) {
		for _, k := range []string{"message", "localizedMessage", "detail", "error", "error_description"} {
			switch v := m[k].(type) {
			case string:
				parts = append(parts, v)
			case map[string]any:
				collect(v)
			}
		}
	}
	collect(obj)
	return strings.Join(parts, " ")
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
		return rpcMsg{}, &probeErr{code: codeSSENoResponse, msg: "the SSE response contains no JSON-RPC response"}
	}
	var m rpcMsg
	if err := json.Unmarshal(bytes.TrimSpace(body), &m); err != nil {
		return rpcMsg{}, &probeErr{code: codeBadResponse, msg: fmt.Sprintf("cannot parse the response: %v", err), params: map[string]string{"error": err.Error()}}
	}
	return m, nil
}

// --- shared ----------------------------------------------------------------

func okResult(info impl, toolsRaw json.RawMessage) ProbeResult {
	var tr toolsResult
	_ = json.Unmarshal(toolsRaw, &tr)
	res := ProbeResult{
		OK:            true,
		ServerName:    info.Name,
		ServerVersion: info.Version,
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
