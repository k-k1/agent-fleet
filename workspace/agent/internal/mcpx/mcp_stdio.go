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
	"os"
	"strings"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/browserx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
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

	spec, ok := toolNamed(p.Name)
	if !ok {
		return mcpError(req.ID, -32602, "unknown tool: "+p.Name)
	}
	if spec.refuse != nil {
		if msg := spec.refuse(); msg != "" {
			return mcpToolErr(req.ID, msg)
		}
	}
	return spec.handle(req, toolCallParams{Name: p.Name, Args: p.Args}, &a)
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
