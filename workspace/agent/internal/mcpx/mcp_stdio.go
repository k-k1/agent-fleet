package mcpx

// Local stdio MCP server (docs/log/19 Q1). Spawned by the assistant chat's
// `claude -p --mcp-config` as `workspace-agent mcp-stdio`, it exposes READ-ONLY
// "Agent Fleet" tools over newline-delimited JSON-RPC 2.0 on stdio. Each tool calls
// the local Agent's REST (127.0.0.1:<AGENT_ADDR>, AGENT_TOKEN) so the assistant can
// inspect the user's OWN workspace with no PAT and no network egress — unlike the CP
// /mcp server (PAT + public-URL hairpin), which stays for external/admin use.
//
// Write tools (send_to_session, …) are exposed ONLY when the server is started with
// --write, which chat.go passes exclusively for conversations whose assistant granted
// af_write (docs/log/19 Q2). An af_read conversation's server never advertises or accepts a
// write tool — the gate is the advertised tool set, not just a permission prompt (the
// chat runs claude with --dangerously-skip-permissions, so a prompt would not gate).

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/browserx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// MCP protocol versions (docs/log/49 + ADR0032). 2026-07-28 drops the initialize handshake
// and carries the version, client info and capabilities in every request's `_meta`. This
// stdio server holds no session state to begin with — it is a pure switch — so it can
// accept both conventions as they are.
const (
	mcpStdioProtocol = "2026-07-28" // preferred (the stateless era)
	mcpStdioLegacy   = "2025-06-18" // echoed back to old clients that send initialize
)

// mcpStdioSupportedVersions is the set server/discover advertises, newest first.
var mcpStdioSupportedVersions = []string{mcpStdioProtocol, "2025-11-25", mcpStdioLegacy}

// The stateless era's per-request `_meta` keys (SEP-2575).
const (
	mcpMetaProtocolVersion = "io.modelcontextprotocol/protocolVersion"
	mcpMetaClientInfo      = "io.modelcontextprotocol/clientInfo"
	mcpMetaClientCaps      = "io.modelcontextprotocol/clientCapabilities"
	mcpMetaServerInfo      = "io.modelcontextprotocol/serverInfo"
)

// The -320xx protocol-defined errors the spec reserves.
const (
	mcpErrUnsupportedVersion = -32022
	mcpErrMethodNotFound     = -32601
	mcpErrInvalidParams      = -32602
)

// mcpSourceSession is the Agent Fleet slot that owns a session-side MCP process.
// It is deliberately not a tool argument: native conversation ids such as
// CLAUDE_CODE_SESSION_ID are provider-specific and must never decide AF ownership.
var mcpSourceSession string

// mcpPeerMessagingEnabled adds ONLY the two session-to-session messaging tools to the
// session-side server (docs/log/58 / ADR 0041 decision 3). Enabled by `--self-report
// --peer-messaging`, the same additive shape as --chromium-attach, so `--self-report`
// alone keeps its historical contract. It is deliberately NOT implied by --write: the
// operator surface already has send_to_session, and peer messaging carries different rules
// (no arm, no shell targets, server-built envelope).
var mcpPeerMessagingEnabled bool

// mcpImageGenEnabled adds ONLY the image generation tool to the session-side server
// (ADR 0069 decision 8). Enabled by `--self-report --image-gen`, the same additive shape as
// --chromium-attach and --peer-messaging. The flag carries the user's opt-in and NOTHING
// else: which sessions actually see the tool is decided per tools/list (mcpImageGenAdvertise),
// because the answer depends on the session's agent kind and on which provider is effective,
// and neither may be frozen into the server's argv.
var mcpImageGenEnabled bool

// Fleet observation — get_session_status / get_session_usage / list_memos / add_memo /
// update_memo — is part of the session surface, unconditionally (docs/log/86 stage 1, made the
// default 2026-09-09). It was opt-in for its first weeks; the switch is gone and existing
// workspaces get it whether or not they had turned it on.
//
// Why it is not a capability worth choosing: reading the fleet you are part of and leaving your
// user a note are what a session needs to be a colleague rather than a process. Nothing here
// drives, answers for or deletes another session — that is --fleet-spawn (ADR 0073), which
// stayed a switch precisely because it spends the host.
//
// What is still NOT here: delete_memo and flush_memos. A session may add to its user's queue and
// correct what is in it; emptying it or sending it is the user's.

// mcpFleetSpawnEnabled adds the ten session-steering tools (ADR 0073), under
// `--self-report --fleet-spawn`. It is the one session-side switch left, and it is a switch
// because it is the one that spends the shared host with nobody watching. Observation above is
// not: reading the fleet and writing your user a note cost nothing and are refused by nobody,
// which is why that one stopped being a setting at all.
//
// What makes steering safe to open at all is not the flag but the CHILD RELATION. create_session
// stamps origin=session plus the caller's own name (ADR 0073 decision 1), and every driving tool
// refuses a target that is not one of the caller's own children (sessionDriveAllowed). Deletion
// stays closed even for those.
var mcpFleetSpawnEnabled bool

// mcpBrowserUnavailable is the runtime id the Agent passed in mcpreg.BrowserUnavailableFlag, ""
// when the workspace has browser features. When set, the Chromium tools leave tools/list on
// both surfaces, and a call that names one anyway (a client holding a list from an older
// config, or a guessed name) answers browser_unavailable instead of "not in tools/list", so
// the model learns why rather than looking for the tool elsewhere.
var mcpBrowserUnavailable string

// parseStdioFlags resolves the argv into this package's capability flags. It is separate from
// RunStdio because RunStdio then blocks on stdin forever: the conjunctions below are the whole
// scope boundary between the assistant and session surfaces, and they have to be reachable by
// a test without standing a server up.
//
// Every flag is reset first, so a caller cannot inherit a capability from a previous parse.
func parseStdioFlags(args []string) {
	setWriteEnabled(false)
	setSelfReportOnly(false)
	setSessionChromiumEnabled(false)
	setConvID("")
	mcpPeerMessagingEnabled = false
	mcpImageGenEnabled = false
	mcpFleetSpawnEnabled = false
	mcpSessionSearchEnabled = false
	mcpAgentMemoryEnabled = false
	mcpBrowserUnavailable = ""
	chromiumAttachRequested, peerMessagingRequested, imageGenRequested := false, false, false
	fleetSpawnRequested, sessionSearchRequested, agentMemoryRequested := false, false, false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--write":
			setWriteEnabled(true)
		case "--self-report":
			setSelfReportOnly(true)
		case "--chromium-attach":
			chromiumAttachRequested = true
		case "--peer-messaging":
			peerMessagingRequested = true
		case "--image-gen":
			imageGenRequested = true
		case "--fleet-observe":
			// Accepted and ignored: observation is unconditional now, but an MCP config
			// written before that change still passes the flag until it is re-materialized.
		case "--fleet-spawn":
			fleetSpawnRequested = true
		case "--session-search":
			sessionSearchRequested = true
		case "--agent-memory":
			agentMemoryRequested = true
		case "--conv":
			if i+1 < len(args) {
				i++
				setConvID(args[i])
			}
		case mcpreg.BrowserUnavailableFlag:
			if i+1 < len(args) {
				i++
				mcpBrowserUnavailable = strings.TrimSpace(args[i])
			}
		}
	}
	// The additive capability is valid only on the session-side server. Keeping the
	// conjunction here means a guessed/accidental --chromium-attach on an assistant
	// invocation cannot widen that assistant's scope.
	setSessionChromiumEnabled(selfReportOnly() && chromiumAttachRequested)
	mcpPeerMessagingEnabled = selfReportOnly() && peerMessagingRequested
	mcpImageGenEnabled = selfReportOnly() && imageGenRequested
	mcpFleetSpawnEnabled = selfReportOnly() && fleetSpawnRequested
	mcpSessionSearchEnabled = selfReportOnly() && sessionSearchRequested
	mcpAgentMemoryEnabled = selfReportOnly() && agentMemoryRequested
}

// RunStdio is the `workspace-agent mcp-stdio` subcommand: a blocking stdio loop.
// Pass --write to additionally expose the write tools (docs/log/19 Q2 af_write opt-in),
// or --self-report for the session-side server (docs/log/51 Phase 3). Combining
// --self-report with --chromium-attach adds the narrowly scoped Chromium Attach View
// tools without granting any other read/write tool (docs/log/53 §53.8).
func RunStdio(args []string) {
	dropUnexpandedEnv()
	mcpSourceSession = os.Getenv("AF_SESSION_NAME")
	parseStdioFlags(args)
	r := bufio.NewReaderSize(os.Stdin, 1<<20)
	stdioOut = &stdioWriter{w: bufio.NewWriter(os.Stdout)}
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			if resp := dispatchMCPStdio(line); resp != nil {
				stdioOut.writeLine(resp)
			}
		}
		if err != nil {
			return // stdin closed (claude shut the server down)
		}
	}
}

// stdioWriter is the server's ONE way onto stdout, and the mutex is load-bearing. The loop
// above used to be strictly one response per request, so a plain bufio.Writer was enough;
// generate_image changed that by emitting notifications/progress from a goroutine WHILE a
// tools/call is still in flight (ADR 0069, open question 1 — a heartbeat is what lifts
// opencode's 60 s per-call ceiling). Two writers interleaving would produce a torn JSON line,
// which every client reads as a protocol violation and drops the server for.
type stdioWriter struct {
	mu sync.Mutex
	w  *bufio.Writer
}

// stdioOut is nil until RunStdio installs it — the unit tests call dispatchMCPStdio directly
// and have no stdout to write to, so every write goes through the nil-safe helpers below.
var stdioOut *stdioWriter

func (s *stdioWriter) writeLine(b []byte) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.w.Write(b)
	_ = s.w.WriteByte('\n')
	_ = s.w.Flush()
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
	// A request from the stateless era is validated first: the required `_meta` fields and
	// the version. The old era (clients that send initialize) passes straight through and
	// keeps its existing behaviour.
	if resp, stop := mcpStdioValidate(req); stop {
		return resp
	}
	switch req.Method {
	case "server/discover":
		// A server MUST implement this in 2026-07-28. stdio has no HTTP status to tell the
		// versions apart, so a dual-era client sends this first to decide (SEP-2575).
		return mcpResult(req.ID, mcpStdioDiscoverResult())
	case "initialize":
		// For old clients only. 2026-07-28 removed it, but a server that wants to serve both
		// eras may keep it (SEP-2575 Backward Compatibility). Dropping it kills every CLI
		// that has not been updated.
		ver := mcpStdioLegacy
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if p.ProtocolVersion != "" {
			ver = p.ProtocolVersion
		}
		return mcpResult(req.ID, map[string]any{
			"protocolVersion": ver,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": true}},
			"serverInfo":      map[string]any{"name": "agent-fleet-local", "version": "q1"},
		})
	case "notifications/initialized", "notifications/cancelled":
		return nil
	case "ping":
		// 2026-07-28 removed ping in both directions (any RPC proves liveness). Kept for
		// the old era.
		if isNotif {
			return nil
		}
		return mcpResult(req.ID, map[string]any{"resultType": "complete"})
	case "tools/list":
		// ttlMs / cacheScope are required fields on 2026-07-28 list results (cacheable
		// lists). opencode 1.18.8's new-era client rejects a missing one in zod and drops
		// the whole server with "Failed to get tools"; old clients ignore them as unknown
		// keys, the same as resultType (measured on 1.18.5). The tool set is a static,
		// per-user list decided by --write, hence private.
		tools := mcpStdioToolList()
		// Remembered before it goes out, because this answer IS the promise the call-side check
		// is enforcing (see mcpAdvertised).
		rememberAdvertised(tools)
		// From here on the client is holding a snapshot, so it is worth telling it when the
		// snapshot goes stale (see mcpStartToolListWatch).
		mcpStartToolListWatch()
		return mcpResult(req.ID, map[string]any{
			"resultType": "complete",
			"ttlMs":      60000,
			"cacheScope": "private",
			"tools":      tools,
		})
	case "tools/call":
		return mcpStdioCall(req)
	default:
		if isNotif {
			return nil
		}
		return mcpError(req.ID, mcpErrMethodNotFound, "method not found: "+req.Method)
	}
}

// mcpStdioValidate checks a stateless-era request's `_meta`. stop=true means the
// caller must return resp immediately (resp is nil for a malformed notification,
// which gets no answer). An initialize-era request passes straight through.
func mcpStdioValidate(req mcpReq) (resp []byte, stop bool) {
	var p struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &p)
	}
	raw, ok := p.Meta[mcpMetaProtocolVersion]
	if !ok {
		return nil, false // initialize era — nothing to validate here
	}
	var ver string
	if json.Unmarshal(raw, &ver) != nil || ver == "" {
		return nil, false
	}
	isNotif := len(bytes.TrimSpace(req.ID)) == 0 || string(bytes.TrimSpace(req.ID)) == "null"
	fail := func(code int, msg string, data any) ([]byte, bool) {
		if isNotif {
			return nil, true
		}
		if data == nil {
			return mcpError(req.ID, code, msg), true
		}
		b, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": req.ID,
			"error": map[string]any{"code": code, "message": msg, "data": data},
		})
		return b, true
	}
	if !mcpStdioVersionSupported(ver) {
		return fail(mcpErrUnsupportedVersion, "unsupported protocol version: "+ver,
			map[string]any{"supported": mcpStdioSupportedVersions, "requested": ver})
	}
	// clientInfo / clientCapabilities are REQUIRED on every stateless request.
	for _, k := range []string{mcpMetaClientInfo, mcpMetaClientCaps} {
		if _, ok := p.Meta[k]; !ok {
			return fail(mcpErrInvalidParams, "missing required _meta field: "+k, nil)
		}
	}
	return nil, false
}

func mcpStdioVersionSupported(v string) bool {
	for _, s := range mcpStdioSupportedVersions {
		if s == v {
			return true
		}
	}
	return false
}

// mcpStdioDiscoverResult answers server/discover. serverInfo is emitted BOTH top-level
// (SEP-2575's DiscoverResult) and under `_meta` (the draft documentation's example): the
// two disagree, and an extra field is harmless, so a client reading either one finds it.
func mcpStdioDiscoverResult() map[string]any {
	info := map[string]any{"name": "agent-fleet-local", "version": "q1"}
	return map[string]any{
		"resultType":        "complete",
		"supportedVersions": mcpStdioSupportedVersions,
		"capabilities":      map[string]any{"tools": map[string]any{"listChanged": true}},
		"serverInfo":        info,
		"_meta":             map[string]any{mcpMetaServerInfo: info},
		"instructions":      mcpStdioInstructions(),
	}
}

// mcpStdioInstructions summarises the surface mcpStdioToolList advertises, from the same
// capability flags, so a model reading it is not told a narrower story than the tool list. It
// reads flags only: image generation and the studio are decided by a live loopback call per
// tools/list, so they are named as conditional rather than probed here. Keep it short — it is
// in every session's context.
func mcpStdioInstructions() string {
	if !selfReportOnly() {
		if writeEnabled() {
			return "Agent Fleet local MCP for the assistant: observe and steer the sessions in your own Workspace."
		}
		return "Agent Fleet local MCP for the assistant: observe the sessions in your own Workspace."
	}
	parts := []string{"completion report", "handoff proposal", "stop after this turn", "session status and usage", "memos to your user", "branch names from your naming rules"}
	if mcpAgentMemoryEnabled {
		parts = append(parts, "agent memory shared by every agent kind")
	}
	if sessionChromiumEnabled() && mcpBrowserUnavailable == "" {
		parts = append(parts, "Chromium hand-off to the user")
	}
	if mcpPeerMessagingEnabled {
		parts = append(parts, "messages to and read-only peeks at peer sessions")
	}
	if mcpSessionSearchEnabled {
		parts = append(parts, "search over past sessions' conversations")
	}
	if mcpFleetSpawnEnabled {
		parts = append(parts, "starting and steering your own child sessions")
	}
	return "Agent Fleet local MCP for this session's own tools: " + strings.Join(parts, ", ") +
		"; image generation and image-studio tools when available to this session."
}

// mcpCallArgs is the union of every argument the union-decoded tools take. Each field's json
// tag must be advertised by some tool schema and each advertised property of those tools must
// have a field here (TestMCPArgsSchemaAndDecoderAgree).
type mcpCallArgs struct {
	Name string `json:"name"`
	// Since is a pointer: an explicit since:0 (re-read from the start) has to be
	// distinguished from an omitted one (continue from the previous cursor —
	// mcpSessionOutput).
	Since *int64 `json:"since"`
	// Lines is peek_session_output's trailing-line count (the Agent clamps it).
	Lines     int    `json:"lines"`
	Prompt    string `json:"prompt"`
	Assistant string `json:"assistant"`
	// create_session args
	Dir           string `json:"dir"`
	Title         string `json:"title"`
	Kind          string `json:"kind"`
	Model         string `json:"model"`
	Effort        string `json:"effort"`
	InitialPrompt string `json:"initial_prompt"`
	// Worktree is a POINTER because the default differs by surface: false for the
	// operator, true for a session (ADR 0073 decision 7). A plain bool cannot tell
	// "worktree=false, work right here" from "not mentioned".
	Worktree *bool `json:"worktree"`
	// AllowSharedWorkingCopy is a session's explicit opt-in to share its own working copy
	// with the child (needs worktree=false; the Agent enforces which directory).
	AllowSharedWorkingCopy bool   `json:"allow_shared_working_copy"`
	Branch                 string `json:"branch"`
	NewBranch              string `json:"new_branch"`
	Subdir                 string `json:"subdir"`
	// ReportBack asks a spawned child to send one intent=answer peer message home when it
	// finishes (ADR 0073 decision 9). A pointer for the same reason as On: omitted means
	// on, and a plain bool would silently turn the report off for every caller that did
	// not think about it.
	ReportBack *bool `json:"report_back"`
	// SpendCapUSD is create_session's per-child budget (#1054). A pointer: omitted means the
	// user's default, which the Agent applies; an explicit 0 means none.
	SpendCapUSD *float64 `json:"spend_cap_usd"`
	// answer_session_question args: 1-based choice numbers, in question order.
	Choices []int `json:"choices"`
	// respond_session_plan args
	Decision string `json:"decision"`
	Feedback string `json:"feedback"`
	// set_chat_plan args (docs/log/33 stage 5, option D): the full work plan pinned to
	// the conversation.
	Plan string `json:"plan"`
	// memo args (id in the path; the rest are forwarded verbatim via p.Args).
	// ID doubles as the cleanup-archive id (restore/purge). Repo names the branch's repo.
	ID   string `json:"id"`
	Repo string `json:"repo"`
	// agent-memory args (docs/log/39 P4). Rev/At pick the snapshot; All/Kinds/Projects
	// are the restore scope. Limit/Path narrow the read tools.
	// af_report (docs/log/51 Phase 3): the reporting session's name. Kept separate from
	// Name because this tool carries "who I am", not "which session to observe".
	// af_stop_after_turn (docs/log/85) uses the same field for the same reason.
	Session string `json:"session"`
	// On is af_stop_after_turn's arm / release. A POINTER because the zero value of the
	// arming flag has to mean "arm": decoded into a plain bool, a call that omitted it
	// would silently release the arm it was meant to set.
	On       *bool    `json:"on"`
	Rev      string   `json:"rev"`
	At       string   `json:"at"`
	Path     string   `json:"path"`
	Limit    int      `json:"limit"`
	All      bool     `json:"all"`
	Kinds    []string `json:"kinds"`
	Projects []string `json:"projects"`
	// Chromium Attach View (docs/log/53). MCP is snake_case while the Agent REST is
	// camelCase, so the conversion is explicit at this boundary. Neither a host nor a
	// CDP WebSocket URL is accepted as input.
	Port              int    `json:"port"`
	TargetID          string `json:"target_id"`
	ExpectedBrowserID string `json:"expected_browser_id"`
	AttachmentID      string `json:"attachment_id"`
	Label             string `json:"label"`
	Message           string `json:"message"`
	CompletionLabel   string `json:"completion_label"`
	AllowCancel       *bool  `json:"allow_cancel"`
	ControlMode       string `json:"control_mode"`
	// send_to_peer_session args (docs/log/58 §58.14): the message kind. The Agent derives
	// the reply policy from it, so this layer passes it through untouched.
	Intent string `json:"intent"`
	// generate_image args (ADR 0069). Op/Size/Background/Count are passed through as the
	// caller wrote them: what a provider cannot honour is REPORTED in the result's
	// warnings, so narrowing them here would hide exactly what the user needs to see.
	Op             string   `json:"op"`
	Provider       string   `json:"provider"`
	Size           string   `json:"size"`
	AspectRatio    string   `json:"aspect_ratio"`
	Background     string   `json:"background"`
	Count          int      `json:"count"`
	Inputs         []string `json:"inputs"`
	Mask           string   `json:"mask"`
	Seed           *int64   `json:"seed"`
	NegativePrompt string   `json:"negative_prompt"`
	// Strength is a pointer so that 0 arrives at the Agent as the request it is and is
	// refused there by value, instead of reading here as "not given" and becoming the default.
	Strength *float64 `json:"strength"`
	// Loras (ADR 0072 decision 5, phase P3). Passed on as written for the same reason as the
	// rest: the Agent refuses an unknown name or a family that does not match the checkpoint
	// BY NAME, which is a better answer than a silently shortened list.
	Loras []imageGenLoraArg `json:"loras"`
	// Params is the sampler overlay, and a POINTER for the same reason Strength is: an absent
	// object and an empty one are not the same request, and which of the four a family reads
	// is answered downstream in warnings rather than guessed at here.
	Params *imageGenParamsArg `json:"params"`
}

func mcpStdioCall(req mcpReq) []byte {
	var p struct {
		Name string                     `json:"name"`
		Args json.RawMessage            `json:"arguments"`
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	_ = json.Unmarshal(req.Params, &p)
	// Taken out before anything decodes or forwards p.Args (the memo tools relay it verbatim).
	p.Args, mcpCallerSID = takeCallerSID(p.Args)
	mcpCallerFromMeta = false
	if mcpCallerSID == "" { // the stamp wins: it is what opencode 1 delivers and what was trusted first
		if mcpCallerSID = metaCallerSID(p.Meta); mcpCallerSID != "" {
			mcpCallerFromMeta = true
		}
	}
	defer func() { mcpCallerSID, mcpCallerFromMeta = "", false }()
	// The session-side advertised set IS the scope boundary (see
	// selfReportOnly()/sessionChromiumEnabled()). Refuse every unadvertised name here
	// too, or a client that guesses names could reach fleet read/write handlers from any
	// interactive session.
	// Ahead of the advertised-set check, which would otherwise answer a withheld browser tool
	// with "not in tools/list". Only for a name this server would have offered on a workspace
	// with a browser, so the scope boundary is unchanged.
	if mcpBrowserUnavailable != "" && mcpChromiumToolInScope(p.Name) {
		return mcpToolErr(req.ID, chromiumUnavailableTextFor(browserx.UnavailableMessage(mcpBrowserUnavailable)))
	}
	if selfReportOnly() && !mcpStdioToolAdvertised(p.Name) {
		// The reason is "this name was not in tools/list", not "you lack permission" — those call
		// for opposite responses, and the old wording sent a model looking for a settings page
		// when the honest answer is that it named something this server never offered.
		return mcpToolErr(req.ID, "このサーバーの tools/list に無いツール名です（広告されていないものは呼べません）: "+p.Name)
	}
	var a mcpCallArgs
	if msg := mcpCheckCallArgs(p.Name, p.Args, &a); msg != "" {
		return mcpToolErr(req.ID, msg)
	}

	// The mutating Chromium attachment tools are refused on the call side too, not just in
	// the advertised set: a read-only client that guesses a name and issues tools/call must
	// not reach the Agent REST.
	if !mcpChromiumWriteEnabled() && isChromiumWriteTool(p.Name) {
		return mcpToolErr(req.ID, "このアシスタントはChromium attachmentの変更を許可されていません")
	}

	// Memo-queue tools relay to the CP's /internal/memos bridge (the queue lives in the
	// CP store, not the Agent), authenticated by AF_MEMO_TOKEN. list_memos is read-only
	// (available to af_read too); the mutating ones require --write. The tool args match
	// the CP wire shape, so p.Args is forwarded as the request body verbatim.
	// The peer tools are refused on the call side too, for the same reason as the mutating
	// Chromium ones: a guessed name in tools/call must not reach the Agent REST.
	if !mcpPeerMessagingEnabled && isPeerTool(p.Name) {
		return mcpToolErr(req.ID, "このセッションはセッション間メッセージを許可されていません")
	}
	// Same reason again for image generation: the advertised set is the scope boundary, and a
	// guessed name in tools/call must not reach a route that spends the user's plan quota.
	if !mcpImageGenEnabled && p.Name == mcpToolGenerateImage {
		return mcpToolErr(req.ID, "このセッションは画像生成を許可されていません（設定 > エージェント）")
	}

	switch p.Name {
	case "get_image_studio", "set_image_draft", "run_image_trial", "add_image_knowledge":
		return mcpStudioCall(req, p.Name, p.Args)
	case mcpToolBranchName:
		return mcpBranchName(req.ID, p.Args)
	case mcpToolMemoryIndex, mcpToolMemorySearch, mcpToolMemoryRead, mcpToolMemorySave, mcpToolMemoryForget:
		return mcpMemoryCall(req.ID, p.Name, p.Args)
	case mcpToolGenerateImage:
		return mcpGenerateImage(req, imageGenArgs{
			op: a.Op, provider: a.Provider, prompt: a.Prompt, size: a.Size,
			aspectRatio: a.AspectRatio, background: a.Background, count: a.Count,
			inputs: a.Inputs, mask: a.Mask, model: a.Model, loras: a.Loras, seed: a.Seed,
			negativePrompt: a.NegativePrompt, strength: a.Strength, params: a.Params,
		})
	case mcpToolSearchSessions:
		return mcpSearchSessions(req.ID, p.Args)
	case "list_child_sessions":
		// The only one of the nine that is NOT also an operator tool: the operator has
		// list_my_sessions, which sees every session and needs no lineage filter. So the gate
		// is the flag alone, refused on the call side for the same reason as the peer and
		// image tools — a guessed name in tools/call must not reach a route this server does
		// not advertise.
		if !mcpFleetSpawnEnabled {
			return mcpToolErr(req.ID, "このセッションはセッションの起動・操縦を許可されていません（設定 > エージェント）")
		}
		self, err := mcpOwningSession()
		if err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		return mcpListChildSessions(req.ID, self)
	case "list_peer_sessions":
		self, err := mcpOwningSession()
		if err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		peers := peerReachableSessions(self)
		out := make([]any, 0, len(peers))
		for _, m := range peers {
			row := map[string]any{"name": m.Name, "kind": m.Kind, "dir": m.Dir}
			if m.Title != "" {
				row["title"] = m.Title
			}
			// Ask the Agent for the state. The meta carries no live state, and a made-up
			// idle here leads to the wrong call ("it looks quiet, so I can send"). When it
			// cannot be read, omit state rather than guessing at it.
			if st, err := agentPeerSessionState(m.Name); err == nil && st != "" {
				row["state"] = st
			}
			out = append(out, row)
		}
		return mcpStructuredResult(req.ID, map[string]any{"sessions": out})
	case "send_to_peer_session":
		self, err := mcpOwningSession()
		if err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		if a.Name == "" {
			return mcpToolErr(req.ID, "name（宛先セッション名）が必要です")
		}
		if strings.TrimSpace(a.Message) == "" {
			return mcpToolErr(req.ID, "message（送信本文）が必要です")
		}
		if strings.TrimSpace(a.Intent) == "" {
			return mcpToolErr(req.ID, "intent（本文の種別）が必要です: "+strings.Join(peerIntentNames, " / "))
		}
		// The envelope, the recipient policy, the rate limit and leaving the arm alone all
		// belong to the Agent (session_peer.go): building them here would let anyone bypass
		// them by replacing this thin layer. Validating the intent and deriving the reply
		// policy stay on the Agent (peerResolveIntent) for the same reason — this layer
		// passes them through.
		reqBody, _ := json.Marshal(map[string]any{
			"prompt": a.Message, "peer_from": self, "peer_intent": a.Intent,
		})
		out, resumed, err := AgentSendToSession(a.Name, reqBody)
		if err != nil {
			return mcpToolErr(req.ID, "メッセージを届けられませんでした: "+err.Error())
		}
		// A Managed peer in the middle of a turn only queues the message (the Agent says so
		// with held). Reported as delivered, it lets the sender wait on a peer that cannot
		// see the message until its current turn ends.
		// A peer waiting on its user's answer queues it too (blocked_on, #1031): it is delivered
		// after the answer, once that turn ends.
		var sent struct {
			Held       bool   `json:"held"`
			HeldReason string `json:"held_reason"`
			BlockedOn  string `json:"blocked_on"`
		}
		_ = json.Unmarshal([]byte(out), &sent)
		queued := sent.Held || sent.BlockedOn != ""
		result := map[string]any{"delivered": !queued, "resumed": resumed, "session": a.Name, "from": self}
		switch {
		case sent.BlockedOn == "starting":
			result["queued"] = true
			result["blocked_on"] = sent.BlockedOn
			result["note"] = peerStartingNote
		case sent.BlockedOn != "":
			result["queued"] = true
			result["blocked_on"] = sent.BlockedOn
			result["note"] = peerBlockedNote
		case sent.Held && sent.HeldReason != "":
			result["queued"] = true
			result["reason"] = sent.HeldReason
			result["note"] = peerRejectedNote
		case sent.Held:
			result["queued"] = true
			result["note"] = peerQueuedNote
		}
		if json.Valid([]byte(out)) {
			result["agent_result"] = json.RawMessage(out)
		}
		b, _ := json.Marshal(result)
		return mcpTextResult(req.ID, string(b))
	case "peek_session_output":
		// The reader is the session this server serves, never an argument: the Agent applies
		// the peer policy, the caps, the rate limit and the audit to that name.
		self, err := mcpOwningSession()
		if err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		if a.Name == "" {
			return mcpToolErr(req.ID, "name（読むセッション名）が必要です")
		}
		q := url.Values{"peek_from": {self}}
		if a.Lines > 0 {
			q.Set("lines", strconv.Itoa(a.Lines))
		}
		if a.Since != nil && *a.Since >= 0 {
			q.Set("since", strconv.FormatInt(*a.Since, 10))
		}
		body, err := agentGET("/sessions/" + url.PathEscape(a.Name) + "/output?" + q.Encode())
		if err != nil {
			return mcpToolErr(req.ID, "出力を読めませんでした: "+err.Error())
		}
		return mcpTextResult(req.ID, body)
	case "propose_session_handoff":
		if !selfReportOnly() {
			return mcpToolErr(req.ID, "propose_session_handoff はセッション側の Agent Fleet サーバー専用です")
		}
		if strings.TrimSpace(a.Prompt) == "" {
			return mcpToolErr(req.ID, "prompt（次セッションへの引き継ぎ本文）が必要です")
		}
		if strings.TrimSpace(a.Title) == "" {
			return mcpToolErr(req.ID, "title（新規セッションの表示名）が必要です")
		}
		// The display name becomes the session name verbatim, so refuse it at proposal time
		// by the same rule the create API uses (at most 80 runes, no control characters).
		// Let it through and the long name is stored, only to fail the moment the user
		// presses launch — by which time the caller is long gone.
		if _, ok := cleanTitle(a.Title); !ok {
			return mcpToolErr(req.ID, fmt.Sprintf("title は %d 文字以内・改行なしにしてください（そのままセッションの表示名になります）", sessionTitleMaxRunes))
		}
		name, err := mcpOwningSession()
		if err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		body, _ := json.Marshal(map[string]string{"prompt": a.Prompt, "title": a.Title})
		if _, err := agentDo(http.MethodPost, "/sessions/"+url.PathEscape(name)+"/handoff-proposal", body); err != nil {
			return mcpToolErr(req.ID, "引き継ぎ提案の保存に失敗しました: "+err.Error())
		}
		return mcpTextResult(req.ID, "引き継ぎ案を利用者へ提示しました。利用者が内容、次のエージェント、モデルを確認してから新規セッションを起動します。")
	case "list_chromium_targets":
		return mcpListChromiumTargets(req.ID, a.Port)
	case "get_chromium_attachment":
		return mcpGetChromiumAttachment(req.ID, a.AttachmentID)
	case "attach_chromium":
		return mcpAttachChromium(req.ID, a.Port, a.TargetID, a.ExpectedBrowserID, a.Label)
	case "detach_chromium":
		return mcpDetachChromium(req.ID, a.AttachmentID)
	case "request_browser_action":
		return mcpRequestBrowserAction(req.ID, a.AttachmentID, a.Message, a.CompletionLabel, a.AllowCancel, a.ControlMode)
	case "get_browser_action_result":
		return mcpGetBrowserActionResult(req.ID, a.AttachmentID)
	case "set_chromium_control_mode":
		return mcpSetChromiumControlMode(req.ID, a.AttachmentID, a.ControlMode)
	case "af_report":
		// The self-report fast path (docs/log/51 Phase 3), for the session-side server
		// started with --self-report only. The assistant's af_read/af_write never advertise
		// this tool, so a call arriving through an unadvertised route is refused — the same
		// convention as elsewhere: the advertised set is the scope boundary (docs/log/19 Q2).
		if !selfReportOnly() {
			return mcpToolErr(req.ID, "af_report はセッション側の Agent Fleet サーバー専用です")
		}
		name := a.Session
		if !session.ValidName(name) {
			// The argument only ever means "me", and a Managed model has no shell variable to
			// read it from; the server knows whom it serves (mcpOwningSession).
			resolved, err := mcpOwningSession()
			if err != nil {
				return mcpToolErr(req.ID, err.Error())
			}
			name = resolved
		}
		body, _ := json.Marshal(map[string]string{"name": name, "kind": reportKindSelfReport})
		if _, err := AgentPOST("/chat/report", body); err != nil {
			return mcpToolErr(req.ID, "完了の申告に失敗しました: "+err.Error())
		}
		// The call only makes the report EARLIER; the server still decides and delivers the
		// report itself. Telling the model "reported" here would hide the recovery paths for
		// a forgotten or premature call (the reconciler).
		return mcpTextResult(req.ID, "完了を申告しました（報告は Agent Fleet 側が状態を確認して配信します）。")
	case "af_stop_after_turn":
		// Arming only (docs/log/85). This call runs INSIDE the turn, so stopping here would
		// kill the session while the answer is still being written and the tool result would
		// never come back; the Agent stops it once the turn has demonstrably ended.
		if !selfReportOnly() {
			return mcpToolErr(req.ID, "af_stop_after_turn はセッション側の Agent Fleet サーバー専用です")
		}
		name := a.Session
		if !session.ValidName(name) {
			// Unlike af_report the session name may be omitted: this tool is called off a
			// plain sentence from the user, with no [agent-fleet] note to copy the name from.
			resolved, err := mcpOwningSession()
			if err != nil {
				return mcpToolErr(req.ID, err.Error())
			}
			name = resolved
		}
		on := a.On == nil || *a.On
		body, _ := json.Marshal(map[string]bool{"on": on})
		if _, err := AgentPOST("/sessions/"+url.PathEscape(name)+"/stop-after-turn", body); err != nil {
			return mcpToolErr(req.ID, "停止予約の更新に失敗しました: "+err.Error())
		}
		if !on {
			return mcpTextResult(req.ID, "ターン終了後の停止予約を解除しました。")
		}
		return mcpTextResult(req.ID,
			"ターン終了後に停止するよう予約しました（この回答は最後まで出し切ってから停止します。"+
				"再開はいつでもできます。新しい指示が届いた場合は予約が解除されます）。")
	case "get_agent_usage":
		// Read-only merge of the two WsBar usage endpoints (5h/weekly windows captured
		// locally from statusline / rollout — no network call). opencode has no usage
		// source, so it is intentionally absent from the result (said in the tool desc).
		cl, err := agentGET("/claude/usage")
		if err != nil {
			return mcpToolErr(req.ID, "使用量の取得に失敗しました: "+err.Error())
		}
		cx, err := agentGET("/codex/usage")
		if err != nil {
			return mcpToolErr(req.ID, "使用量の取得に失敗しました: "+err.Error())
		}
		// agy usage lives under the connections path and has a different shape
		// ({account, plan, groups}); it self-reports authed=false when signed out
		// and never 500s, so a merge is safe. Absent when unsupported on this host.
		ag, err := agentGET("/connections/agy/usage")
		if err != nil {
			return mcpToolErr(req.ID, "使用量の取得に失敗しました: "+err.Error())
		}
		// muse's reading is the host's own last observation off the wire (ADR 0095 decision
		// 10), so unlike the three above it can be authed with nothing to report — a
		// workspace whose muse sessions have all just started has seen no completion yet.
		// It answers {ok:false, authed:...} in that case and never 500s, so the merge is safe.
		ms, err := agentGET("/muse/usage")
		if err != nil {
			return mcpToolErr(req.ID, "使用量の取得に失敗しました: "+err.Error())
		}
		b, _ := json.Marshal(map[string]any{
			"claude": json.RawMessage(cl),
			"codex":  json.RawMessage(cx),
			"agy":    json.RawMessage(ag),
			"muse":   json.RawMessage(ms),
		})
		return mcpTextResult(req.ID, string(b))
	case "list_models":
		// Advertised in the write set, so the call is refused at the same boundary: the
		// advertised set is the scope boundary (docs/log/19 Q2). --fleet-spawn advertises it
		// too (ADR 0073 §3-b) — create_session's own description sends the caller here first,
		// so a gate left at writeEnabled() breaks the spawn path at its first step.
		if !writeEnabled() && !mcpFleetSpawnEnabled {
			return mcpToolErr(req.ID, "このアシスタントはモデル一覧の取得を許可されていません")
		}
		if a.Kind != "claude" && a.Kind != "codex" && a.Kind != "opencode" && a.Kind != "agy" && a.Kind != "copilot" && a.Kind != "cursor" && a.Kind != "kiro" && a.Kind != "lcpp" && a.Kind != "muse" {
			return mcpToolErr(req.ID, "kind には claude / codex / opencode / agy / copilot / cursor / kiro / lcpp / muse のいずれかを指定してください")
		}
		out, err := agentGET("/agents/" + url.PathEscape(a.Kind) + "/models")
		if err != nil {
			return mcpToolErr(req.ID, "モデル一覧の取得に失敗しました: "+err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "list_memos":
		out, err := cpMemoDo(http.MethodGet, "/internal/memos", nil)
		if err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "add_memo":
		if !memoWriteAllowed() {
			return mcpToolErr(req.ID, "このアシスタントはメモの追加を許可されていません")
		}
		out, err := cpMemoDo(http.MethodPost, "/internal/memos", []byte(p.Args))
		if err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "update_memo":
		if !memoWriteAllowed() {
			return mcpToolErr(req.ID, "このアシスタントはメモの編集を許可されていません")
		}
		if a.ID == "" {
			return mcpToolErr(req.ID, "id（メモ id）が必要です")
		}
		out, err := cpMemoDo(http.MethodPatch, "/internal/memos/"+url.PathEscape(a.ID), []byte(p.Args))
		if err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "delete_memo":
		if !writeEnabled() {
			return mcpToolErr(req.ID, "このアシスタントはメモの削除を許可されていません")
		}
		if a.ID == "" {
			return mcpToolErr(req.ID, "id（メモ id）が必要です")
		}
		out, err := cpMemoDo(http.MethodDelete, "/internal/memos/"+url.PathEscape(a.ID), nil)
		if err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "flush_memos":
		if !writeEnabled() {
			return mcpToolErr(req.ID, "このアシスタントはメモの一括送信を許可されていません")
		}
		out, err := cpMemoDo(http.MethodPost, "/internal/memos/flush", []byte(p.Args))
		if err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "list_schedules":
		out, err := CPScheduleDo(http.MethodGet, "/internal/schedules", nil)
		if err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "get_schedule_runs":
		if a.ID == "" {
			return mcpToolErr(req.ID, "id（スケジュール id）が必要です")
		}
		out, err := CPScheduleDo(http.MethodGet, "/internal/schedules/"+url.PathEscape(a.ID)+"/runs", nil)
		if err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "create_schedule":
		if !writeEnabled() {
			return mcpToolErr(req.ID, "このアシスタントはスケジュールの登録を許可されていません")
		}
		// Route completion reports back to THIS operator conversation (docs/log/30): stamp
		// owner_conv = the operator's own conv id, overriding any client-supplied value.
		out, err := CPScheduleDo(http.MethodPost, "/internal/schedules", WithOwnerConv(p.Args, convID()))
		if err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "update_schedule":
		if !writeEnabled() {
			return mcpToolErr(req.ID, "このアシスタントはスケジュールの編集を許可されていません")
		}
		if a.ID == "" {
			return mcpToolErr(req.ID, "id（スケジュール id）が必要です")
		}
		out, err := CPScheduleDo(http.MethodPatch, "/internal/schedules/"+url.PathEscape(a.ID), []byte(p.Args))
		if err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "delete_schedule":
		if !writeEnabled() {
			return mcpToolErr(req.ID, "このアシスタントはスケジュールの削除を許可されていません")
		}
		if a.ID == "" {
			return mcpToolErr(req.ID, "id（スケジュール id）が必要です")
		}
		out, err := CPScheduleDo(http.MethodDelete, "/internal/schedules/"+url.PathEscape(a.ID), nil)
		if err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "pause_schedule", "resume_schedule", "run_schedule_now":
		if !writeEnabled() {
			return mcpToolErr(req.ID, "このアシスタントはスケジュールの操作を許可されていません")
		}
		if a.ID == "" {
			return mcpToolErr(req.ID, "id（スケジュール id）が必要です")
		}
		action := map[string]string{"pause_schedule": "pause", "resume_schedule": "resume", "run_schedule_now": "run-now"}[p.Name]
		out, err := CPScheduleDo(http.MethodPost, "/internal/schedules/"+url.PathEscape(a.ID)+"/"+action, nil)
		if err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		return mcpTextResult(req.ID, out)
	}

	// Write/orchestrate tools — only when this server was started with --write.
	switch p.Name {
	case "get_chat_plan", "set_chat_plan":
		// The work plan (docs/log/33 stage 5, option D). The target is ALWAYS this server's
		// own conversation (convID()) and there is no conversation-id argument — the same
		// convention as create_schedule's owner_conv override, making "an operator only
		// writes to itself" a property of the wiring.
		//
		// The read is under the --write gate too: both tools are advertised only in the
		// write set, so this follows the existing rule that the advertised set is the scope
		// boundary (af_report, docs/log/19 Q2).
		if !writeEnabled() {
			return mcpToolErr(req.ID, "このアシスタントは書き込みツールを許可されていません")
		}
		if convID() == "" {
			return mcpToolErr(req.ID, "この経路には会話が結び付いていないため、作業計画は扱えません")
		}
		path := "/chat/conversations/" + url.PathEscape(convID()) + "/plan"
		if p.Name == "get_chat_plan" {
			out, err := agentGET(path)
			if err != nil {
				return mcpToolErr(req.ID, "作業計画の取得に失敗しました: "+err.Error())
			}
			return mcpTextResult(req.ID, out)
		}
		// Never let an empty value clear it: discarding the plan is the user's decision, made
		// in the Console's work-plan panel. A whole-text replacement that is empty or only
		// whitespace is usually an accident (a failed summary, truncated output).
		if strings.TrimSpace(a.Plan) == "" {
			return mcpToolErr(req.ID, "plan（作業計画の全文）が空です。計画を消したい場合は利用者に Console の作業計画パネルから操作してもらってください")
		}
		// notice=true: this is the only route that moves the plan while the user is not
		// watching, so leave a card in the conversation.
		body, _ := json.Marshal(map[string]any{"plan": a.Plan, "notice": true})
		if _, err := agentDo(http.MethodPut, path, body); err != nil {
			return mcpToolErr(req.ID, "作業計画の更新に失敗しました: "+err.Error())
		}
		// Do not return the whole conversation: that would feed its full text back to the
		// model on every plan write.
		return mcpTextResult(req.ID, "作業計画を更新しました（以降の新しいセッションへ原文のまま引き継がれます）。")
	case "create_session":
		if !writeEnabled() && !mcpFleetSpawnEnabled {
			return mcpToolErr(req.ID, "このアシスタントはセッションの作成を許可されていません")
		}
		// Before the shell approval gate, the effort check and the idempotency key: an unknown
		// kind is refused here rather than after those have treated it as something it is not.
		if err := validateCreateKind(a.Kind); err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		// Who is launching decides the provenance, the idempotency scope, the report route and
		// the worktree default. Everything below branches on this one answer (ADR 0073).
		parent := ""
		if selfReportOnly() {
			self, err := mcpOwningSession()
			if err != nil {
				return mcpToolErr(req.ID, err.Error())
			}
			parent = self
		}
		// P3: a raw shell session executes arbitrary commands (no agent guardrails) — gate
		// its creation on a Discord approval when this is an unattended operator turn. A
		// session never reaches it: the Agent refuses kind=shell for origin=session outright,
		// because this gate is a no-op without a conversation (ADR 0073 decision 8).
		if a.Kind == "shell" && parent == "" {
			if err := bridgeApprovalGate(approvalLabel("create_session_shell"), shellCreateTarget(a.Dir, a.InitialPrompt)); err != nil {
				return mcpToolErr(req.ID, err.Error())
			}
		}
		driver := ""
		// An MCP caller has no way to drive a tui pane, so it asks for managed wherever the
		// registry says that is the default (sessionx.ManagedDefaultKind). claude and agy are
		// absent because no managed route exists for them. lcpp and muse have no tui route
		// at all; the Agent's create handler defaults them to managed anyway, so naming it
		// here is the same launch spelled out.
		if managedDefaultKind(a.Kind) {
			driver = "managed"
		}
		// The worktree default differs by surface: the operator's is false (a person can say
		// "work right here"), a session's is true. Two agents in one working copy is the
		// accident the workspace policy forbids by name, and a session launching with defaults
		// would otherwise put its child in its own checkout.
		worktree := a.Worktree != nil && *a.Worktree
		if parent != "" && a.Worktree == nil {
			worktree = true
		}
		// The opt-in only means something against a worktree=false launch from a session; with
		// the default worktree it would be silently ignored, so say so instead.
		shared := parent != "" && a.AllowSharedWorkingCopy
		if shared && worktree {
			return mcpToolErr(req.ID, "allow_shared_working_copy は worktree=false と一緒に指定してください（既定の worktree=true では作業コピーは共有されません）")
		}
		initialPrompt := a.InitialPrompt
		if parent != "" {
			initialPrompt = spawnPromptFor(parent, initialPrompt, a.ReportBack)
		}
		// Deterministic idempotency key (caller + launch intent): an LLM re-issuing the same
		// create_session reproduces it, so a timed-out-then-retried create collapses onto the
		// first session instead of spawning a duplicate. The caller scope is the conversation
		// for an operator and the session's own name for a session — never empty, or two
		// sessions launching the same thing would collapse into one (ADR 0073 decision 2).
		scope := convID()
		origin, originConv := session.OriginOperator, convID()
		if parent != "" {
			scope, origin, originConv = parent, session.OriginSession, ""
		}
		// Checked before anything is created: the Agent stores an effort without validating it,
		// and an unknown value would otherwise surface only when the child's first turn fails.
		// Trimmed once and used everywhere below: the check resolves a padded id to its row, so
		// forwarding the raw value would launch a model string the check never saw, and a
		// blank model would slip past its "effort needs model" refusal.
		model, effort := strings.TrimSpace(a.Model), strings.TrimSpace(a.Effort)
		if effort != "" {
			if err := checkCreateEffort(a.Kind, model, effort); err != nil {
				return mcpToolErr(req.ID, err.Error())
			}
		}
		idemKey := CreateSessionKey(scope, a.Dir, a.Subdir, a.Kind, model, effort, initialPrompt, worktree, a.Branch, a.NewBranch, shared)
		body := map[string]any{
			"dir":             a.Dir,
			"subdir":          a.Subdir,
			"title":           a.Title,
			"kind":            a.Kind,
			"model":           model,
			"effort":          effort,
			"initial_prompt":  initialPrompt,
			"worktree":        worktree,
			"branch":          a.Branch,
			"new_branch":      a.NewBranch,
			"driver":          driver,
			"report_to":       convID(), // docs/log/30: send the completion report to this conversation (empty = off)
			"idempotency_key": idemKey,
			// ADR 0029 §6: record explicitly who started this session. That is the axis usage
			// accounting needs to separate unattended spend (autopilot, schedules and now
			// session-spawned children) from sessions a human opened. origin_session names the
			// parent and is filled from the server's own $AF_SESSION_NAME, never from an
			// argument — the model cannot claim a parent it is not (ADR 0073 decision 1).
			"origin":         origin,
			"origin_conv":    originConv,
			"origin_session": parent,
		}
		if shared {
			body["allow_shared_working_copy"] = true
		}
		if a.SpendCapUSD != nil {
			body["spend_cap_usd"] = *a.SpendCapUSD
		}
		reqBody, _ := json.Marshal(body)
		// A create costs 40s + 45s at worst, over opencode's 60s per-call ceiling. The
		// heartbeat resets that clock (measured, ADR 0069); claude ignores progress for
		// timeouts and codex has tool_timeout_sec=600.
		stop := startProgressHeartbeat(req, "セッションを起動しています…")
		out, err := agentCreateSession(reqBody, idemKey)
		stop()
		if err != nil {
			return mcpToolErr(req.ID, "セッションの作成に失敗しました: "+agentErrDetail(err))
		}
		if parent != "" && strings.TrimSpace(a.InitialPrompt) == "" {
			out = withNoTaskWarning(out)
		}
		return mcpTextResult(req.ID, out)
	case "send_to_session":
		if !writeEnabled() {
			return mcpToolErr(req.ID, "このアシスタントは書き込みツールを許可されていません")
		}
		if a.Name == "" {
			return mcpToolErr(req.ID, "name（セッション名）が必要です")
		}
		if a.Prompt == "" {
			return mcpToolErr(req.ID, "prompt（送信本文）が必要です")
		}
		// P3: sending to a raw shell session runs an arbitrary command — gate it like a shell
		// create when this is an unattended operator turn (agent sessions keep their own guardrails).
		if sessionIsShell(a.Name) {
			if err := bridgeApprovalGate(approvalLabel("send_to_session_shell"), shellSendTarget(a.Name, a.Prompt)); err != nil {
				return mcpToolErr(req.ID, err.Error())
			}
		}
		// confirm (docs/log/38 delivery verification): an operator send is an unattended
		// route, so wait for proof that the turn actually started rather than the 200 that
		// only means the keystrokes went out. If the input was swallowed the Agent repairs
		// itself (re-sends Enter / retypes), and if it is still unconfirmed the tool fails
		// with delivery_unconfirmed — the fix for instructions silently lost to a stopped
		// session (bc5d685e).
		reqBody, _ := json.Marshal(map[string]any{"prompt": a.Prompt, "report_to": convID(), "confirm": true})
		out, resumed, err := AgentSendToSession(a.Name, reqBody)
		if err != nil {
			return mcpToolErr(req.ID, "Agent への送信に失敗しました: "+err.Error())
		}
		result := map[string]any{"sent": true, "resumed": resumed, "session": a.Name}
		if json.Valid([]byte(out)) {
			result["agent_result"] = json.RawMessage(out)
		}
		b, _ := json.Marshal(result)
		return mcpTextResult(req.ID, string(b))
	case "answer_session_question":
		if !writeEnabled() {
			return mcpToolErr(req.ID, "このアシスタントは書き込みツールを許可されていません")
		}
		if a.Name == "" {
			return mcpToolErr(req.ID, "name（セッション名）が必要です")
		}
		if len(a.Choices) == 0 {
			return mcpToolErr(req.ID, "choices（質問順の 1-based 選択肢番号の配列）が必要です")
		}
		reqBody, _ := json.Marshal(map[string]any{"choices": a.Choices})
		out, err := AgentPOST("/sessions/"+url.PathEscape(a.Name)+"/answer-question", reqBody)
		if err != nil {
			return mcpToolErr(req.ID, "質問への回答に失敗しました: "+err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "respond_session_plan":
		if !writeEnabled() {
			return mcpToolErr(req.ID, "このアシスタントは書き込みツールを許可されていません")
		}
		if a.Name == "" {
			return mcpToolErr(req.ID, "name（セッション名）が必要です")
		}
		if a.Decision != "approve" && a.Decision != "reject" {
			return mcpToolErr(req.ID, "decision は approve か reject を指定してください")
		}
		reqBody, _ := json.Marshal(map[string]string{"decision": a.Decision, "feedback": a.Feedback})
		out, err := AgentPOST("/sessions/"+url.PathEscape(a.Name)+"/plan-respond", reqBody)
		if err != nil {
			return mcpToolErr(req.ID, "プランへの応答に失敗しました: "+err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "stop_session":
		if !writeEnabled() && !mcpFleetSpawnEnabled {
			return mcpToolErr(req.ID, "このアシスタントはセッションの停止を許可されていません")
		}
		if a.Name == "" {
			return mcpToolErr(req.ID, "name（セッション名）が必要です")
		}
		if err := sessionDriveAllowed(a.Name); err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		// disarm_report: an operator stop means the instruction is cancelled, so swallow the
		// armed one-shot report — otherwise a later resume's completion delivers a stale one.
		//
		// A SESSION's stop does not send it (ADR 0073 decision 10). Disarming says "the
		// instruction is withdrawn", and a parent folding up a child withdraws nothing the
		// operator asked for; if the operator had steered this child, its report is still owed.
		// (The stop still delays that report until the child is resumed — leaving the arm alone
		// is not the same as having no effect.)
		reqBody, _ := json.Marshal(map[string]bool{"disarm_report": !selfReportOnly()})
		out, err := AgentPOST("/sessions/"+url.PathEscape(a.Name)+"/halt", reqBody)
		if err != nil {
			return mcpToolErr(req.ID, "セッションの停止に失敗しました: "+err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "stop_session_after_turn":
		// Deliberately does NOT disarm the report, which is the whole difference from
		// stop_session: this stop lets the instruction finish, so the report it owes is
		// still owed — and the Agent delivers it before folding the session away
		// (docs/log/85).
		if !writeEnabled() && !mcpFleetSpawnEnabled {
			return mcpToolErr(req.ID, "このアシスタントはセッションの停止を許可されていません")
		}
		if a.Name == "" {
			return mcpToolErr(req.ID, "name（セッション名）が必要です")
		}
		if err := sessionDriveAllowed(a.Name); err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		on := a.On == nil || *a.On
		armBody, _ := json.Marshal(map[string]bool{"on": on})
		out, err := AgentPOST("/sessions/"+url.PathEscape(a.Name)+"/stop-after-turn", armBody)
		if err != nil {
			return mcpToolErr(req.ID, "停止予約の更新に失敗しました: "+err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "get_memory_snapshot":
		// Return the tree (what was there at that point) and the diff (what that snapshot
		// changed) in one call. A restore scope can only be built from the tree: offering
		// the CURRENT roots as the choices makes an already-deleted project unselectable,
		// which defeats the main case, restoring memory that was deleted by mistake (why
		// docs/log/39 ③ added the tree).
		if a.Rev == "" && a.At == "" {
			return mcpToolErr(req.ID, "rev（snapshot id）か at（日時）のどちらかが必要です")
		}
		q := url.Values{}
		if a.Rev != "" {
			q.Set("rev", a.Rev)
		}
		if a.At != "" {
			q.Set("at", a.At)
		}
		tree, err := agentGET("/agents/memory/tree?" + q.Encode())
		if err != nil {
			return mcpToolErr(req.ID, "メモリの時点情報の取得に失敗しました: "+err.Error())
		}
		q.Del("rev")
		if a.Rev != "" {
			q.Set("to", a.Rev)
		}
		if a.Path != "" {
			q.Set("path", a.Path)
		}
		// claude's and codex's memories only: the AF memory's history (af/) is not this tool's,
		// and it is withheld from sessions and the assistant alike while its switch is off.
		q.Set("native", "1")
		diff, err := agentGET("/agents/memory/diff?" + q.Encode())
		if err != nil {
			return mcpToolErr(req.ID, "メモリの差分の取得に失敗しました: "+err.Error())
		}
		out, _ := json.Marshal(map[string]any{
			"tree": json.RawMessage(tree), "diff": json.RawMessage(diff),
		})
		return mcpTextResult(req.ID, string(out))
	case "restore_memory_snapshot":
		if !writeEnabled() {
			return mcpToolErr(req.ID, "このアシスタントはメモリの復元を許可されていません")
		}
		if a.Rev == "" && a.At == "" {
			return mcpToolErr(req.ID, "rev（snapshot id）か at（日時）のどちらかが必要です")
		}
		// An omitted scope is NOT read as "everything". Killing that structurally at the
		// argument level stops the model dropping a field and rolling the whole memory back:
		// the user's approval was given for a specific scope, so widening it silently is a
		// betrayal of that approval.
		if !a.All && len(a.Kinds) == 0 && len(a.Projects) == 0 {
			return mcpToolErr(req.ID, "戻す範囲が必要です（all=true か projects / kinds を指定してください）")
		}
		reqBody, _ := json.Marshal(map[string]any{
			"rev": a.Rev, "at": a.At,
			"scope": map[string]any{"all": a.All, "kinds": a.Kinds, "projects": a.Projects},
		})
		out, err := AgentPOST("/agents/memory/restore", reqBody)
		if err != nil {
			return mcpToolErr(req.ID, "メモリの復元に失敗しました: "+err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "resume_session":
		if !writeEnabled() && !mcpFleetSpawnEnabled {
			return mcpToolErr(req.ID, "このアシスタントはセッションの再開を許可されていません")
		}
		if a.Name == "" {
			return mcpToolErr(req.ID, "name（セッション名）が必要です")
		}
		if err := sessionDriveAllowed(a.Name); err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		out, err := AgentPOST("/sessions/"+url.PathEscape(a.Name)+"/start", nil)
		if err != nil {
			return mcpToolErr(req.ID, "セッションの再開に失敗しました: "+err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "rename_child_session":
		// The flag ALONE, like list_child_sessions and unlike the stop/resume pair: those are
		// also operator tools, this one is not advertised on the operator surface at all. An
		// assistant that guessed the name would otherwise reach a route this server does not
		// advertise — and rename every session in the workspace, since sessionDriveAllowed waves
		// the operator through by design.
		if !mcpFleetSpawnEnabled {
			return mcpToolErr(req.ID, "このセッションはセッションの起動・操縦を許可されていません（設定 > エージェント）")
		}
		if a.Name == "" {
			return mcpToolErr(req.ID, "name（セッション名）が必要です")
		}
		// An empty title means "revert to the auto label", which is the user's affordance in the
		// rename dialog and not something a parent asks for. Refused here as well as in the Agent
		// so the caller is told what to send instead of reading it back as an API error.
		if strings.TrimSpace(a.Title) == "" {
			return mcpToolErr(req.ID, "title（新しい題名）が必要です")
		}
		if _, ok := cleanTitle(a.Title); !ok {
			return mcpToolErr(req.ID, fmt.Sprintf("title は %d 文字以内・改行なしにしてください", sessionTitleMaxRunes))
		}
		if err := sessionDriveAllowed(a.Name); err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		// title_set_by is what separates this write from the Console's on the same endpoint, and
		// the refusal that reads it (a title the user chose) lives in the Agent — not here, where
		// replacing this layer would walk around it.
		renameBody, _ := json.Marshal(map[string]string{"title": a.Title, "title_set_by": session.TitleSetByParent})
		out, err := AgentPOST("/sessions/"+url.PathEscape(a.Name)+"/title/set", renameBody)
		if err != nil {
			return mcpToolErr(req.ID, "題名の変更に失敗しました: "+agentErrDetail(err))
		}
		return mcpTextResult(req.ID, out)
	case "archive_session":
		if !writeEnabled() {
			return mcpToolErr(req.ID, "このアシスタントはセッションのアーカイブを許可されていません")
		}
		if a.Name == "" {
			return mcpToolErr(req.ID, "name（セッション名）が必要です")
		}
		out, err := AgentPOST("/sessions/"+url.PathEscape(a.Name)+"/archive", nil)
		if err != nil {
			return mcpToolErr(req.ID, "セッションのアーカイブに失敗しました: "+err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "delete_worktree":
		if !writeEnabled() {
			return mcpToolErr(req.ID, "このアシスタントは worktree の削除を許可されていません")
		}
		if a.Name == "" {
			return mcpToolErr(req.ID, "name（worktree 名）が必要です")
		}
		if err := bridgeApprovalGate(approvalLabel("delete_worktree"), a.Name); err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		// The Agent shelves the stopped AI sessions in it and trashes its shell / ssm on every
		// delete (ADR 0101 decision 4), so no flag is sent. No force: a dirty or ahead worktree
		// stays protected and is refused by the Agent, which returns the reason (push first,
		// or force it from the Console).
		out, err := agentDo(http.MethodDelete, "/repos/"+url.PathEscape(a.Name), nil)
		if err != nil {
			return mcpToolErr(req.ID, "worktree の削除に失敗しました: "+err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "delete_session":
		if !writeEnabled() {
			return mcpToolErr(req.ID, "このアシスタントはセッションの削除を許可されていません")
		}
		if a.Name == "" {
			return mcpToolErr(req.ID, "name（セッション名）が必要です")
		}
		if err := bridgeApprovalGate(approvalLabel("delete_session"), a.Name); err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		// Every delete moves the meta and jsonl to the gz trash before removing them (ADR 0101),
		// so it stays restorable. A running session is refused (no stop=1: stopping is a
		// separate, confirmed step).
		out, err := agentDo(http.MethodDelete, "/sessions/"+url.PathEscape(a.Name), nil)
		if err != nil {
			return mcpToolErr(req.ID, "セッションの削除に失敗しました: "+err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "delete_branch":
		if !writeEnabled() {
			return mcpToolErr(req.ID, "このアシスタントはブランチの削除を許可されていません")
		}
		if a.Repo == "" || a.Branch == "" {
			return mcpToolErr(req.ID, "repo（リポジトリ名）と branch（ブランチ名）が必要です")
		}
		if err := bridgeApprovalGate(approvalLabel("delete_branch"), a.Repo+" / "+a.Branch); err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		out, err := agentDo(http.MethodDelete,
			"/repos/"+url.PathEscape(a.Repo)+"/branch?branch="+url.QueryEscape(a.Branch), nil)
		if err != nil {
			return mcpToolErr(req.ID, "ブランチの削除に失敗しました: "+err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "restore_cleanup_archive":
		if !writeEnabled() {
			return mcpToolErr(req.ID, "このアシスタントはアーカイブの復元を許可されていません")
		}
		if a.ID == "" {
			return mcpToolErr(req.ID, "id（アーカイブ id）が必要です")
		}
		out, err := AgentPOST("/cleanup/archives/"+url.PathEscape(a.ID)+"/restore", nil)
		if err != nil {
			return mcpToolErr(req.ID, "アーカイブの復元に失敗しました: "+err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "purge_cleanup_archive":
		if !writeEnabled() {
			return mcpToolErr(req.ID, "このアシスタントはアーカイブの完全削除を許可されていません")
		}
		if a.ID == "" {
			return mcpToolErr(req.ID, "id（アーカイブ id）が必要です")
		}
		if err := bridgeApprovalGate(approvalLabel("purge_cleanup_archive"), a.ID); err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		out, err := agentDo(http.MethodDelete, "/cleanup/archives/"+url.PathEscape(a.ID), nil)
		if err != nil {
			return mcpToolErr(req.ID, "アーカイブの完全削除に失敗しました: "+err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "list_assistants":
		if !writeEnabled() {
			return mcpToolErr(req.ID, "このアシスタントは他アシスタントへの相談を許可されていません")
		}
		out, err := agentGET("/assistants")
		if err != nil {
			return mcpToolErr(req.ID, "アシスタント一覧の取得に失敗しました: "+err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "ask_assistant":
		if !writeEnabled() {
			return mcpToolErr(req.ID, "このアシスタントは他アシスタントへの相談を許可されていません")
		}
		if a.Assistant == "" || a.Prompt == "" {
			return mcpToolErr(req.ID, "assistant（相手）と prompt（相談内容）が必要です")
		}
		reqBody, _ := json.Marshal(map[string]string{"assistant": a.Assistant, "prompt": a.Prompt})
		out, err := AgentPOST("/chat/ask", reqBody)
		if err != nil {
			return mcpToolErr(req.ID, "相談の実行に失敗しました: "+err.Error())
		}
		return mcpTextResult(req.ID, out)
	}

	var path string
	switch p.Name {
	case "list_my_sessions":
		path = "/sessions"
	case "list_repos":
		path = "/repos"
	case "get_session_status":
		name := a.Name
		if name == "" && selfReportOnly() {
			// A session asking about itself: this is also how a Managed model, which has no
			// $AF_SESSION_NAME in its shell, learns its own name.
			resolved, err := mcpOwningSession()
			if err != nil {
				return mcpToolErr(req.ID, err.Error())
			}
			name = resolved
		}
		if name == "" {
			return mcpToolErr(req.ID, "name（セッション名）が必要です")
		}
		path = "/sessions/" + url.PathEscape(name) + "/status"
	case "get_session_output":
		if a.Name == "" {
			return mcpToolErr(req.ID, "name（セッション名）が必要です")
		}
		// Unlike the other reads a session is given, this one names a target and returns
		// another session's raw output — so it is children only (ADR 0073 decision 4).
		if err := sessionDriveAllowed(a.Name); err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		return mcpSessionOutput(req.ID, a.Name, a.Since)
	case "get_session_usage":
		path = "/sessions/usage"
		if a.Name != "" {
			path += "?name=" + url.QueryEscape(a.Name)
		}
	case "list_memory_snapshots":
		limit := a.Limit
		if limit <= 0 {
			limit = 20
		}
		// claude's and codex's history only, like get_memory_snapshot's diff: commits that touch
		// nothing but the AF memory are left out before the limit applies (ADR 0108).
		path = "/agents/memory/snapshots?native=1&limit=" + strconv.Itoa(limit)
	case "list_cleanup_candidates":
		path = "/sessions/cleanup"
	case "list_cleanup_archives":
		path = "/cleanup/archives"
	default:
		return mcpError(req.ID, -32602, "unknown tool: "+p.Name)
	}

	body, err := agentGET(path)
	if err != nil {
		return mcpToolErr(req.ID, "Agent への問い合わせに失敗しました: "+err.Error())
	}
	if p.Name == "get_session_status" && selfReportOnly() {
		body = withoutPendingInteraction(body)
	}
	return mcpTextResult(req.ID, body)
}

// withoutPendingInteraction drops `questions` and `plan` from a session status before a
// SESSION sees it (the operator keeps both — it can act on them).
//
// A session gets get_session_status to learn whether a peer is busy, and stage 1 withholds
// answer_session_question / respond_session_plan on purpose: answering another session's
// question, or approving its plan, is standing in for the user's approval. Handing over the
// pending text anyway would leave the model holding exactly the input it needs to do that
// through some other route — relaying the plan into a peer message, say — while the tool that
// would have done it honestly is missing. These two fields are also the largest and the most
// attacker-influenced part of the payload (they are another session's output, which docs/30
// treats as attacker-influenced), and a plan body is unbounded.
//
// Unparseable input is returned untouched rather than blanked: this is a display trim, and a
// status the caller cannot read at all is worse than one carrying a field it must not act on.
func withoutPendingInteraction(body string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return body
	}
	_, hasQuestions := m["questions"]
	_, hasPlan := m["plan"]
	if !hasQuestions && !hasPlan {
		return body
	}
	delete(m, "questions")
	delete(m, "plan")
	trimmed, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return string(trimmed)
}

// mcpStructuredResult deliberately duplicates the same JSON object in text and
// structuredContent. P0 measured CLIs that never hand structuredContent to the model, so
// text always holds a short JSON fallback every CLI can read, not a prose description.
func mcpStructuredResult(id json.RawMessage, value map[string]any) []byte {
	text, _ := json.Marshal(value)
	return mcpResult(id, map[string]any{
		"resultType":        "complete",
		"content":           []any{map[string]any{"type": "text", "text": string(text)}},
		"structuredContent": value,
	})
}

// mcpTextResult returns a tools/call RESULT carrying a single text content block. Every
// tools/call success goes through here or mcpStructuredResult: a 2026-07-28 client rejects a
// result without resultType outright, so the model never sees the tool's answer.
func mcpTextResult(id json.RawMessage, text string) []byte {
	return mcpResult(id, map[string]any{
		"resultType": "complete",
		"content":    []any{map[string]any{"type": "text", "text": text}},
	})
}

// mcpToolErr returns a tools/call RESULT with isError=true — an in-band error the
// model reads and can react to — rather than a JSON-RPC protocol error.
func mcpToolErr(id json.RawMessage, msg string) []byte {
	return mcpResult(id, map[string]any{
		"resultType": "complete",
		"content":    []any{map[string]any{"type": "text", "text": msg}},
		"isError":    true,
	})
}
