package chatx

// The deny-all one-shot route (ADR 0117 decision 3): a helper turn that reads a third party's
// text must run with no tool, no MCP server, no file and no network access, and a backend that
// cannot be shown to do that does not run it. Fail closed, never "run with the default".
//
// Only claude qualifies today. A kind qualifies when it has an entry in denyAllBackends: a runner
// that probes the very run it will make, and checks the real run's own output again. The others
// have none, because none of them prints what the run has (denyAllLimits says why, per kind).
// Fail closed: a kind without a runner is skipped, and a pin on it fails the call.
//
// A flag in the argv is not proof. Measured on claude 2.1.302: --strict-mcp-config with an
// empty config still leaves Claude in Chrome connected (22 browser tools) when the inherited
// environment sets CLAUDE_CODE_ENABLE_CFC=1. So the route checks what the run EFFECTIVELY has,
// twice: a probe with the very same argv and environment before the transcript is handed over,
// and the init event of the real run, which discards the reply if it ever lists a tool.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routehealth"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// ErrDenyAllUnavailable is returned when no backend that can run deny-all is usable, or the
// one that is could not be verified. Nothing was sent to any model.
var ErrDenyAllUnavailable = errors.New("no backend that can run without tools is available")

// denyAllRunner makes one deny-all call on one kind, model already chosen. It verifies the
// posture with a probe first, then checks the real run's output, and returns an error wrapping
// ErrDenyAllUnavailable when the probe could not show the posture. The runner writes the ledger
// row itself, and only once the transcript is about to be sent: a refused probe reached no model
// and must leave no row.
type denyAllRunner func(ctx context.Context, persona, prompt, model string) (string, error)

// denyAllBackends are the kinds with a deny-all one-shot that is verified at run time. This table
// is the only way in: DenyAllKind, the resolution, the warm-up and the dispatch in OneShotDenyAll
// all read it, so a kind cannot qualify without bringing its own probe and re-check.
var denyAllBackends = map[string]denyAllRunner{
	session.KindClaude: runClaudeDenyAll,
}

// denyAllLimits says, for each one-shot kind that does not qualify, why it was not qualified. Each
// line is "no inventory shown", not "cannot be done": it names what was and was not found, and the
// evidence behind it (help text, a debug command, an earlier measurement, or not run) is in ADR
// 0117 (note of 2026-10-19). A kind leaves this table only by gaining a runner above.
// TestDenyAllLimitsCoverEveryOneShotKind keeps the two in step with oneShotKinds, so a new kind has
// to be classified.
var denyAllLimits = map[string]string{
	session.KindCodex:    "no tool inventory was found: prompt-input lists messages only, exec --json was not run, and no flag removing built-in tools was found",
	session.KindOpencode: "the one-shot config denies shell, edit, write and subagent only; other built-in tools and the user's global MCP servers are not shown absent, and no output lists the run's tools",
	session.KindCursor:   "ask mode is read-only, not tool-less; the stream-json init event is documented without a tool or MCP list, and it was not run",
	session.KindAgy:      "not probed (the CLI aborts at start here); the driver reads plain text from -p, so no inventory is known, and no tool-removal switch is known",
	session.KindMuse:     "the disable flags cover shell, write and web per its help; an echo-provider event stream named no tools, and a real-provider run was not made",
}

// DenyAllKind reports whether kind has a verified-at-run-time deny-all one-shot.
func DenyAllKind(kind string) bool {
	return denyAllBackends[kind] != nil // a nil runner is no probe: fail closed
}

// OneShotSourceUnavailable is the resolution source of a deny-all feature whose pinned backend
// cannot run without tools (or none can be reached): the review would fail closed, not fall back.
const OneShotSourceUnavailable = "unavailable"

// ResolveDenyAllCached is ResolveOneShotCached for a deny-all feature: the same precedence as
// resolveDenyAllKind, answered from the availability cache alone so GET /ai-assist/resolution
// never starts a CLI. ok is false while the cache cannot say yet, which includes "nothing
// confirmed reachable"; "unavailable" is the one definite answer: the pin names a backend that
// cannot qualify, or every kind that could qualify is confirmed unreachable.
func ResolveDenyAllCached(feature string) (kind, source string, ok bool) {
	if pin := aiFeatureAgentPref(feature); oneShotRunnable(pin) {
		if !DenyAllKind(pin) {
			return "", OneShotSourceUnavailable, true
		}
		avail, known := headlessAvailableCached(pin)
		if !known {
			return "", "", false
		}
		if avail {
			return pin, OneShotSourcePin, true
		}
	}
	for _, k := range oneShotOrder() {
		if !DenyAllKind(k) {
			continue
		}
		if avail, known := headlessAvailableCached(k); known && avail {
			return k, OneShotSourceDefault, true
		}
	}
	// Definite only when every deny-all kind has a fresh "not reachable" entry; a single cold
	// entry keeps the answer unknown, because the next warm-up may still find it.
	// One read per kind: a warm-up racing the loops above may have stored "reachable" since, and
	// that must read as unknown, never as a verdict.
	for k := range denyAllBackends {
		if avail, known := headlessAvailableCached(k); !known || avail {
			return "", "", false
		}
	}
	return "", OneShotSourceUnavailable, true
}

// WarmDenyAllKind fills the availability cache for the kinds a deny-all feature can run on, the
// counterpart of WarmOneShotKind: that one warms the shared order, which may never reach claude.
func WarmDenyAllKind() {
	for _, k := range slices.Sorted(maps.Keys(denyAllBackends)) {
		warmWG.Add(1)
		go func() {
			defer warmWG.Done()
			headlessAgentAvailable(k)
		}()
	}
}

// denyAllProbeBudget bounds the probe: it starts a CLI process and talks to no model.
var denyAllProbeBudget = 45 * time.Second

// denyAllProbePrompt is a built-in slash command, answered locally by the CLI: measured on
// 2.1.302 as num_turns 0, zero tokens, zero cost, with the init event printed first. Were a
// CLI to send it to the model instead, the cost is one tiny prompt.
const denyAllProbePrompt = "/help"

// resolveDenyAllKind picks the backend for feature. A feature pin names a vendor the member
// chose: if that vendor cannot run deny-all, or is rate-limited (routehealth), the call fails
// rather than moving the transcript to another one. Without a usable pin the shared order is
// walked and kinds that cannot run deny-all or are rate-limited are skipped. Refusing while
// parked costs the caller nothing (no attempt is spent on ErrDenyAllUnavailable), where a call
// sent anyway would spend one on a near-certain failure.
func resolveDenyAllKind(feature string, tier OneShotTier) (string, error) {
	parked := func(k string) bool { return routeAvoid(k, denyAllModel(feature, tier, k)) }
	if pin := aiFeatureAgentPref(feature); oneShotRunnable(pin) {
		if !DenyAllKind(pin) {
			return "", fmt.Errorf("%w: the pinned backend %q cannot run without tools", ErrDenyAllUnavailable, pin)
		}
		if headlessAgentAvailable(pin) {
			if parked(pin) {
				return "", fmt.Errorf("%w: the pinned backend %q is rate-limited", ErrDenyAllUnavailable, pin)
			}
			return pin, nil
		}
	}
	for _, k := range oneShotOrder() {
		if DenyAllKind(k) && headlessAgentAvailable(k) && !parked(k) {
			return k, nil
		}
	}
	return "", ErrDenyAllUnavailable
}

// denyAllModel is the model OneShotDenyAll runs on kind.
func denyAllModel(feature string, tier OneShotTier, kind string) string {
	selected, configured := resolveOneShotModel(feature, tier, kind)
	if !configured || selected == AssistantRecommendedModel {
		selected = recommendedOneShotModel(kind, tier)
	}
	return strings.TrimSpace(selected)
}

// OneShotDenyAll runs one prompt on a backend that is verified to have no tool and no MCP
// server, and returns the reply. feature picks the pin as for OneShotHeadless; the ledger row is
// written from here under the caller's usagex tag. It makes ONE model call: retry policy
// belongs to the caller, which must count attempts.
//
// Errors wrap ErrDenyAllUnavailable when nothing was sent to a model.
func OneShotDenyAll(ctx context.Context, feature string, tier OneShotTier, persona, prompt string) (reply string, err error) {
	kind, err := resolveDenyAllKind(feature, tier)
	if err != nil {
		return "", err
	}
	selected := denyAllModel(feature, tier, kind)

	run := denyAllBackends[kind]
	if run == nil {
		return "", fmt.Errorf("%w: %q has no verified deny-all route", ErrDenyAllUnavailable, kind)
	}
	return run(ctx, persona, prompt, selected)
}

// runClaudeDenyAll is claude's runner: probe with the same argv and environment and a prompt that
// is not the transcript, then run, and read the init event of the real run.
func runClaudeDenyAll(ctx context.Context, persona, prompt, selected string) (string, error) {
	if err := verifyClaudeDenyAll(ctx, persona, selected); err != nil {
		return "", err
	}
	call := usagex.Call{Kind: session.KindClaude, ModelReq: selected}
	defer usagex.RecordCall(ctx, &call, time.Now())
	cmd := claudeOneShotCmd(ctx, claudeOneShotArgsFormat(persona, selected, true))
	cmd.Stdin = strings.NewReader(prompt)
	cmd.WaitDelay = time.Second // a child that outlives the killed CLI must not keep the call (and the caller's slot) open
	out, runErr := cmd.Output()
	ev := parseClaudeStream(out)
	r := ev.result
	call.Models, call.CostUSD = UsageModelRows(r.ModelUsage), r.TotalCostUSD
	call.FallbackTotals(r.Usage.LedgerTokens(), "")
	// The error result is classified here, where its text is at hand: the errors below
	// deliberately never quote what the run printed. Only an error result is reported; an exit
	// with no result says nothing about the backend's availability.
	if r.IsError && ctx.Err() == nil {
		routehealth.Default.Fail(session.KindClaude, selected, r.Result, routeNow())
	}
	if !ev.initSeen {
		// Without the init event nothing says what the run had, so the reply is not trusted.
		return "", errors.New("claude deny-all run: no init event, the reply was discarded")
	}
	if ev.bad != "" {
		return "", fmt.Errorf("claude deny-all run had %s: the reply was discarded", ev.bad)
	}
	if runErr != nil {
		// Not cliErr: stderr of a run that was given the transcript is not something to put in an
		// error that reaches logs and run records.
		return "", fmt.Errorf("claude execution failed (%s)", exitSummary(runErr))
	}
	// Success is evidence, not the absence of an error flag: a result that does not say so is
	// not a reply.
	if !ev.resultSeen || ev.facts.subtype != "success" || ev.facts.isError == nil || *ev.facts.isError || r.IsError {
		return "", errors.New("claude returned an invalid response/error")
	}
	call.OK = true
	routehealth.Default.Succeed(session.KindClaude, selected)
	return strings.TrimRight(r.Result, "\n"), nil
}

// verifyClaudeDenyAll runs the one-shot argv for model with a local slash command and checks the
// stream. Any doubt is a failure: the process must exit cleanly in time, print an init event that
// lists nothing, show no model or tool activity, and answer the way a local command does (zero
// turns, zero cost) - otherwise "/help" did not stay local and the probe proved nothing about a
// run that carries a transcript. Nothing the process printed is quoted in the error.
func verifyClaudeDenyAll(ctx context.Context, persona, model string) error {
	ctx, cancel := context.WithTimeout(ctx, denyAllProbeBudget)
	defer cancel()
	cmd := claudeOneShotCmd(ctx, claudeOneShotArgsFormat(persona, model, true))
	cmd.Stdin = strings.NewReader(denyAllProbePrompt)
	cmd.WaitDelay = time.Second // a child that outlives the killed CLI must not hold the probe open
	out, err := cmd.Output()
	ev := parseClaudeStream(out)
	fail := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrDenyAllUnavailable, fmt.Sprintf(format, args...))
	}
	switch {
	case ctx.Err() != nil:
		return fail("the probe did not finish in time")
	case err != nil:
		return fail("the probe failed (%s)", exitSummary(err))
	case !ev.initSeen:
		return fail("the probe printed no init event")
	case ev.bad != "":
		return fail("claude would run with %s", ev.bad)
	}
	if why := ev.notLocal(); why != "" {
		return fail("the probe is not proven to be a local command (%s)", why)
	}
	return nil
}

// claudeOneShotCmd is the claude command every one-shot starts: the chat command prefix and the
// one-shot environment.
func claudeOneShotCmd(ctx context.Context, args []string) *exec.Cmd {
	cmd := chatClaudeCmd(ctx, args...)
	cmd.Env = append(cmd.Env, claudeOneShotEnv...)
	return cmd
}

// claudeStream is what the deny-all route reads off a stream-json run. bad is the first reason the
// stream cannot be trusted ("" when none), decided as the lines go by, so a later line can never
// make up for an earlier one.
type claudeStream struct {
	initSeen    bool
	bad         string
	resultSeen  bool
	result      claudeResult
	results     int
	permDenials int
	facts       resultFacts
}

// resultFacts are the fields of the (first) result event the probe is judged by. Pointers keep
// "absent or null" apart from zero: a missing num_turns is unknown, not 0.
type resultFacts struct {
	subtype    string
	isError    *bool
	numTurns   *int
	cost       *float64
	usage      *ClaudeUsage
	modelUsage int
}

// notLocal says why the stream is not positive proof of a command the CLI answered by itself, ""
// when it is: exactly one result, subtype success, is_error false, zero turns, zero cost, zero
// tokens and no per-model usage - every one of them present, none assumed from a missing field.
func (s claudeStream) notLocal() string {
	f := s.facts
	switch {
	case s.results == 0:
		return "no result"
	case s.results > 1:
		return "more than one result"
	case f.subtype != "success":
		return "the result is not a success"
	case f.isError == nil || *f.isError:
		return "is_error is not false"
	case f.numTurns == nil:
		return "num_turns is not reported"
	case *f.numTurns != 0:
		return "it took a model turn"
	case f.cost == nil:
		return "total_cost_usd is not reported"
	case *f.cost != 0:
		return "it cost money"
	case f.usage == nil:
		return "usage is not reported"
	case f.usage.InputTokens != 0 || f.usage.OutputTokens != 0 || f.usage.CacheCreationInputTokens != 0 || f.usage.CacheReadInputTokens != 0:
		return "it used tokens"
	case f.modelUsage != 0:
		return "it reports per-model usage"
	}
	return ""
}

// streamHead is the part of an event every line is read for. The lists and the nested objects stay
// raw: they are decoded only for the event types that must carry them, so an unrelated event with
// a field of the same name cannot be mistaken for a violation or hide one.
type streamHead struct {
	Type              string            `json:"type"`
	Subtype           string            `json:"subtype"`
	Tools             json.RawMessage   `json:"tools"`
	MCPServers        json.RawMessage   `json:"mcp_servers"`
	Message           json.RawMessage   `json:"message"`
	Event             json.RawMessage   `json:"event"`
	PermissionDenials []json.RawMessage `json:"permission_denials"`
}

// parseClaudeStream reads claude's stream-json output, one JSON object per line. It fails closed:
//   - every init event is judged alone, and must carry BOTH lists as arrays, both empty;
//   - a line that is not a JSON object, or an event that cannot be read, is a reason to distrust
//     the stream, not something to skip;
//   - model or tool activity (an assistant or user message, a stream event, a result) before the
//     first init contradicts it, and so does any tool_use / tool_result / mcp block anywhere:
//     what the run says it has is checked against what it did;
//   - a result that records permission denials means a tool was attempted.
//
// Only counts and the result object are kept; the text of other events never is.
func parseClaudeStream(out []byte) claudeStream {
	var s claudeStream
	distrust := func(why string) {
		if s.bad == "" {
			s.bad = why
		}
	}
	for _, line := range bytes.Split(out, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var h streamHead
		if line[0] != '{' || json.Unmarshal(line, &h) != nil {
			distrust("an unreadable event")
			continue
		}
		if s.results > 0 {
			distrust("activity after the result") // a result is terminal
		}
		switch h.Type {
		case "system":
			if h.Subtype != "init" {
				continue
			}
			s.initSeen = true
			for _, l := range []struct {
				raw  json.RawMessage
				what string
			}{{h.Tools, "tool"}, {h.MCPServers, "MCP server"}} {
				var items []json.RawMessage
				switch {
				case len(l.raw) == 0 || bytes.Equal(bytes.TrimSpace(l.raw), []byte("null")) || json.Unmarshal(l.raw, &items) != nil:
					distrust("an init event without a readable " + l.what + " list")
				case len(items) > 0:
					distrust(fmt.Sprintf("%d %ss", len(items), l.what))
				}
			}
		case "assistant", "user", "stream_event":
			if !s.initSeen {
				distrust("activity before the init event")
			}
			if why := toolBlockIn(h); why != "" {
				distrust(why)
			}
		case "result":
			if !s.initSeen {
				distrust("a result before the init event")
			}
			s.results++
			if s.results > 1 {
				distrust("more than one result")
				continue
			}
			var r claudeResult
			var rs struct {
				Subtype    string                     `json:"subtype"`
				IsError    *bool                      `json:"is_error"`
				NumTurns   *int                       `json:"num_turns"`
				Cost       *float64                   `json:"total_cost_usd"`
				Usage      *ClaudeUsage               `json:"usage"`
				ModelUsage map[string]json.RawMessage `json:"modelUsage"`
			}
			if json.Unmarshal(line, &r) != nil || json.Unmarshal(line, &rs) != nil {
				distrust("an unreadable result")
				continue
			}
			s.result, s.resultSeen, s.permDenials = r, true, len(h.PermissionDenials)
			s.facts = resultFacts{rs.Subtype, rs.IsError, rs.NumTurns, rs.Cost, rs.Usage, len(rs.ModelUsage)}
			if s.permDenials > 0 {
				distrust("a tool attempt (permission denials)")
			}
		}
	}
	return s
}

// toolBlockIn reports a tool call or result inside an assistant, user or stream event ("" when
// none, or a reason to distrust when the event cannot be read).
func toolBlockIn(h streamHead) string {
	isTool := func(t string) bool {
		return strings.HasSuffix(t, "tool_use") || strings.HasSuffix(t, "tool_result") || strings.HasPrefix(t, "mcp_")
	}
	switch h.Type {
	case "stream_event":
		var ev struct {
			ContentBlock struct {
				Type string `json:"type"`
			} `json:"content_block"`
		}
		if len(h.Event) == 0 || json.Unmarshal(h.Event, &ev) != nil {
			return "an unreadable stream event"
		}
		if isTool(ev.ContentBlock.Type) {
			return "a tool call"
		}
	default:
		var m struct {
			Content json.RawMessage `json:"content"`
		}
		if len(h.Message) == 0 || json.Unmarshal(h.Message, &m) != nil {
			return "an unreadable message"
		}
		c := bytes.TrimSpace(m.Content)
		if len(c) == 0 || c[0] != '[' {
			return "" // a plain string content has no blocks
		}
		var blocks []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(c, &blocks) != nil {
			return "an unreadable message"
		}
		for _, b := range blocks {
			if isTool(b.Type) {
				return "a tool call"
			}
		}
	}
	return ""
}

// exitSummary says how a process ended without quoting anything it printed.
func exitSummary(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return fmt.Sprintf("exit status %d", ee.ExitCode())
	}
	return "could not run"
}
