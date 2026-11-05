package usagex

// Usage ledger (docs/log/46 / ADR0029 P1). One row = one LLM call, or one logical turn of
// a session folded in.
//
// Non-negotiable: neither prompt nor response text is ever recorded — token counts and
// metadata only.
//
// Stored in ~/.local/share/agent-fleet/usage/raw/YYYY-MM-DD.jsonl (append-only, rotated
// daily, kept 90 days by default). ~/.local survives a Workspace recreate (Workspace
// Guide). A row is ~200B, so even 100 auxiliary calls/day plus 2,000 session turns/day
// comes to ~420KB/day.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// feature — the enumeration of consumers, frozen in ADR0029 §2. The Console does the i18n.
const (
	FeatureAssistantChat    = "assistant.chat"     // one turn of the user's chat
	FeatureAssistantAsk     = "assistant.ask"      // one-shot advisory (not persisted)
	FeatureAssistantAutoTur = "assistant.autoturn" // automatic turn on a session completion report
	FeatureAssistantBridge  = "assistant.bridge"   // operator reply from Discord/Slack
	FeatureCompact          = "compact"            // summary carry-forward (docs/log/33)
	FeaturePlanUpdate       = "plan.update"        // explicit work-plan refresh (docs/log/33 stage 5)
	FeatureTitleSession     = "title.session"      // session title suggestion
	FeatureTitleChat        = "title.chat"         // conversation title suggestion
	FeatureBranchSuggest    = "branch.suggest"     // branch-name suggestion
	FeatureSuggestSession   = "suggest.session"    // the mirror's ✨ reply suggestions
	FeatureSuggestChat      = "suggest.chat"       // the chat's ✨ reply suggestions
	FeatureSuggestEdit      = "suggest.edit"       // the editor's ✨ AI edit suggestion (docs/log/44 Phase 4)
	FeatureTranslate        = "translate.mirror"   // the mirror's per-answer translation (docs/log/97)
	FeatureMemoryReview     = "memory.review"      // the background memory review's helper call (ADR 0117)
	FeatureSession          = "session"            // the interactive session itself (folded in from the transcript)
	FeatureToolImagegen     = "tool.imagegen"      // the image generation tool (ADR 0069)
	// FeatureEngineLLM is one call to the fleet's OWN llama.cpp engine, routed through the
	// CP's /engine/llm gateway (ADR 0071 decision 9). The tokens are real and measured, but
	// there is no price to attach: the cost of that answer is a share of a GPU box's hour,
	// which is a component cost shown as such and never apportioned (ADR 0048).
	FeatureEngineLLM = "engine.llm"
	// FeatureRouteGateway is one model call the AF route gateway forwarded for a session's
	// route (ADR 0128 P2a). It is written by the gateway, which sees every call, and it is a
	// feature of its own so that a harness whose transcript is also folded into feature=session
	// is never counted twice by one writer reading the other's rows.
	FeatureRouteGateway = "route.gateway"
	// FeatureUnknown is a call that carried no tag. A row is written even when a new
	// auxiliary feature forgets to tag itself: not recording it would make the consumption
	// invisible, which matters more than the tag being right.
	FeatureUnknown = "unknown"
)

// trigger — where the turn was injected from.
const (
	TriggerUser     = "user"
	TriggerAuto     = "auto"
	TriggerManual   = "manual"
	TriggerSchedule = "schedule"
	TriggerOperator = "operator"
	TriggerBridge   = "bridge"
	TriggerRecovery = "recovery"
)

// model_src — self-reported provenance of the model dimension (ADR0029 §4).
const (
	ModelReported = "reported"        // reported by the executor (claude / the transcript's Turn.Model)
	ModelRequest  = "requested"       // only the value we requested is known
	ModelUnknown  = "default_unknown" // left to the CLI's default, so the resolved model is unknown
)

// measured — self-reported, so that "0" and "not measured" can never be confused.
const (
	MeasuredExact   = "exact"   // in/out/cache all obtained
	MeasuredPartial = "partial" // only some of them (copilot's outTok alone, say)
	MeasuredNone    = "none"    // a CLI that reports no tokens — only the calls are counted
)

// Route ids: where a call's model traffic went and with which credential (ADR 0128
// decision 1). A route is recorded only from per-call evidence in the transcript or log the
// row comes from, never from the session's current settings at fold time. Empty means the
// call's route is not recorded (a row written before the field existed, or a kind whose route
// is not provable yet) and reads like unknown, never like a default route.
const (
	RouteOpencodeZen = "opencode-zen" // opencode's provider "opencode" (pay-per-request Zen)
	RouteOpencodeGo  = "opencode-go"  // opencode's provider "opencode-go" (the Go subscription)
	RouteAFEngineLLM = "af-engine-llm"
	RouteMemberLcpp  = "member-llamacpp"
	// RouteBedrock is a call the AF route gateway forwarded to Amazon Bedrock (ADR 0128 P2a). The
	// gateway sees every call, so its rows are metered.
	RouteBedrock = "bedrock"
)

// Billing values: how a call was charged (ADR 0128 decision 8). Empty means "not recorded"
// and must be read as BillingUnknown — never as included in a subscription.
const (
	BillingSubscriptionIncluded = "subscription-included"
	BillingSubscriptionExtra    = "subscription-extra" // usage credits past a window, still on the login
	BillingMetered              = "metered"
	BillingSelfHosted           = "self-hosted"
	BillingFree                 = "free"
	BillingUnknown              = "unknown"
)

// BillingOrUnknown is the reading rule for Record.Billing: a row with none recorded is unknown.
func BillingOrUnknown(b string) string {
	if b == "" {
		return BillingUnknown
	}
	return b
}

// RouteOfOpencodeProvider is the route of one opencode call, from the provider id opencode
// itself wrote on that call's message. Only the two opencode.ai routes are provable from it;
// a direct provider (anthropic, openrouter, …) could be a key or a login, so it stays empty.
func RouteOfOpencodeProvider(providerID string) string {
	return opencodeProviderRoutes[providerID]
}

// BillingOfOpencodeCall is how one opencode call was charged, from its route and the cost
// opencode computed for that call. Only a priced Zen call is provable: Zen bills its balance per
// request, so a Zen call that carries a cost was charged. A zero cost is a free model or an empty
// step and proves nothing, and the Go route is left open because the Go subscription can spill
// onto the balance past its limits without the message saying so.
func BillingOfOpencodeCall(route string, costUSD float64) string {
	if route == RouteOpencodeZen && costUSD > 0 {
		return BillingMetered
	}
	return ""
}

// A table, not a switch: "opencode" is also a session kind, and a case on it reads as a
// branch on the kind (the kind-literal ceiling counts those); here it is a provider id.
var opencodeProviderRoutes = map[string]string{
	"opencode":    RouteOpencodeZen,
	"opencode-go": RouteOpencodeGo,
}

// Record is one ledger row. The JSON tags are the frozen wire it shares with the
// Console/API (ADR0029 §1).
type Record struct {
	TS   string `json:"ts"`   // when the call finished (UTC, RFC3339)
	Call string `json:"call"` // call id: binds the rows one call splits into across models
	// Feature/Trigger/Ref/Verb come from the ctx tag (WithTag).
	Feature string `json:"feature"`
	Trigger string `json:"trigger,omitempty"`
	// Origin/OriginConv are the session's provenance (ADR0029 §6). Resolved from ref and
	// burned into the row, so deleting the session does not break the aggregation.
	Origin     string `json:"origin,omitempty"`
	OriginConv string `json:"origin_conv,omitempty"`
	// Kind is the agent kind that actually ran — the outcome, not the request.
	Kind     string `json:"kind"`
	Model    string `json:"model,omitempty"`     // canonical model name (family key, versions folded)
	ModelRaw string `json:"model_raw,omitempty"` // the raw reported id, version included
	ModelReq string `json:"model_req,omitempty"` // the value requested; a mismatch detects a fallback
	ModelSrc string `json:"model_src,omitempty"`
	// Route and Billing are what the call ran on and how it was charged. Both are dimensions
	// (usageKey), so rows that differ in either are never merged; both are empty on rows
	// written before they existed.
	Route   string `json:"route,omitempty"`
	Billing string `json:"billing,omitempty"`
	Ref     string `json:"ref,omitempty"`  // session name or conversation id
	Verb    string `json:"verb,omitempty"` // sub-dimension of assistant.chat (translate|summarize)
	// Sidechain is a sub-dimension of feature=session (subagent / Workflow consumption).
	Sidechain bool `json:"sidechain,omitempty"`
	// Idx is the running number of a feature=session logical turn (1-based). The writer's
	// idempotency rests on the watermark in usage/state.json, but the append and the
	// watermark live in separate files and cannot be written atomically together — a crash
	// in between re-appends — so the aggregation reads (ref, Idx) to drop duplicates
	// (usage_dedup.go). It is not a dimension, so it stays out of usageKey.
	Idx int `json:"idx,omitempty"`
	// Agent names the subagent / Workflow transcript a Sidechain row came from (a hash of its
	// path below the subagents directory). Idx numbers each agent's turns on their own, so
	// (Ref, Agent, Idx) is the row's identity and the dedup keeps one entry per agent. Empty
	// for the session's own transcript.
	Agent string `json:"agent,omitempty"`
	// Key is the stable accounting identity of a row outside the (ref, Idx) sequence — a
	// cost-only session row, keyed by the agent's own message id. The aggregation counts one row
	// per Key, so a re-append after a crash before the watermark write is dropped like a
	// repeated (ref, Idx).
	Key         string  `json:"key,omitempty"`
	In          int     `json:"in"`
	Out         int     `json:"out"`
	CacheRead   int     `json:"cread"`
	CacheCreate int     `json:"ccreate"`
	Spend       int     `json:"spend"`              // = in + ccreate + out (cache_read excluded)
	CostUSD     float64 `json:"cost_usd,omitempty"` // only when the agent reports it (claude aux calls, opencode sessions)
	MS          int     `json:"ms,omitempty"`
	OK          bool    `json:"ok"`
	Measured    string  `json:"measured"`
	// Images and Pixels are the non-token dimension of feature=tool.imagegen (ADR 0069
	// decision 9): the plan quota an image consumes is not expressible in tokens, so what CAN
	// be counted is counted here and the rest is left unmeasured rather than zero-filled.
	// Absent on every other feature — never write a 0 to mean "no images".
	Images int `json:"images,omitempty"`
	Pixels int `json:"pixels,omitempty"`
	// TokensOnly marks a row that adds tokens to a turn another row already counts as a call
	// (cursor's stop hook beside the transcript fold's row for the same turn). The aggregation
	// sums its tokens and never counts it as a call, so the two writers cannot double the call
	// count.
	TokensOnly bool `json:"tokens_only,omitempty"`
}

// Spend is the headline metric. cache_read is excluded to match the definition
// get_session_usage and the mirror's ContextBar already use: two screens agreeing weighs
// more than theoretical correctness.
func Spend(in, create, out int) int { return in + create + out }

// Enabled is the master switch for recording. AF_USAGE_RECORD=0 stops it entirely (the
// hook the P5 settings UI writes through). On by default.
func Enabled() bool { return os.Getenv("AF_USAGE_RECORD") != "0" }

// Dir is the ledger root. AF_USAGE_DIR is the substitution hook for tests.
func Dir() string {
	if v := os.Getenv("AF_USAGE_DIR"); v != "" {
		return v
	}
	return filepath.Join(paths.AgentDataDir(), "usage")
}

func RawDir() string { return filepath.Join(Dir(), "raw") }

// RetentionDays is how many days of raw rows to keep; rollups are kept forever
// (ADR0029 §7-3).
func RetentionDays() int {
	if v := os.Getenv("AF_USAGE_RETENTION_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 90
}

var (
	// Mu serialises appends and prunes (concurrent turns inside one process). It is exported
	// so that readUsageDayForRollup in usage_rollup.go can take the same lock and read a
	// day's rows without racing an append.
	//
	// Never bind it to an alias variable: `var usageMu = usagex.Mu` copies the mutex itself,
	// so the appending and the reading side take different locks and the serialisation
	// silently disappears. go vet's copylocks catches a direct value copy, but not a lock
	// carried around inside a struct returned from a function. Callers write
	// `usagex.Mu.Lock()` directly.
	Mu sync.Mutex
	// prunedAt is when the retention prune last ran. It throttles the directory scan away
	// from every append — the ledger sees orders of magnitude more appends than prunes.
	prunedAt time.Time
)

// AppendRows appends rows to today's file. Rows a single call split across several models
// arrive sharing one Call, so the call is not counted twice.
//
// A failed write comes back as an error, and callers handle it in one of two ways:
//   - Recording an auxiliary call (recordUsageCall) is best-effort and swallows it: an
//     unwritable ledger must not make a chat or a title suggestion fail.
//   - Session folding (usage_fold.go) propagates the failure: advancing the watermark over
//     rows that were never written loses that consumption for good.
func AppendRows(rows []Record) error {
	if len(rows) == 0 || !Enabled() {
		return nil
	}
	Mu.Lock()
	defer Mu.Unlock()
	dir := RawDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	day := time.Now().UTC().Format("2006-01-02")
	f, err := os.OpenFile(filepath.Join(dir, day+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			f.Close()
			return err
		}
	}
	// Check the error from Close too: an append can defer the actual write, so Close is the
	// last gate.
	if err := f.Close(); err != nil {
		return err
	}
	pruneRawLocked()
	return nil
}

// pruneRawLocked deletes daily files past the retention window. Mu must be held. It runs at
// most once an hour, to keep a ReadDir off the append hot path.
func pruneRawLocked() {
	now := time.Now()
	if !prunedAt.IsZero() && now.Sub(prunedAt) < time.Hour {
		return
	}
	prunedAt = now
	dir := RawDir()
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := now.UTC().AddDate(0, 0, -RetentionDays())
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".jsonl" {
			continue
		}
		// Judge by the date in the file name alone: mtime drifts across copy/restore.
		day, err := time.Parse("2006-01-02", name[:len(name)-len(".jsonl")])
		if err != nil {
			continue // leave names we do not recognise alone
		}
		if day.Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

// RawDays returns the days present in the ledger (UTC YYYY-MM-DD) in ascending order.
func RawDays() []string {
	ents, err := os.ReadDir(RawDir())
	if err != nil {
		return nil
	}
	days := make([]string, 0, len(ents))
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || filepath.Ext(n) != ".jsonl" {
			continue
		}
		day := n[:len(n)-len(".jsonl")]
		if _, err := time.Parse("2006-01-02", day); err == nil {
			days = append(days, day)
		}
	}
	sort.Strings(days) // date names, so lexical order is chronological
	return days
}

// ReadDay reads one day's rows in append order. The order matters: when a call splits into
// several model rows, the first row of a call is the one that counts the call
// (usage_rollup.go).
func ReadDay(day string) []Record {
	b, err := os.ReadFile(filepath.Join(RawDir(), day+".jsonl"))
	if err != nil {
		return nil
	}
	var out []Record
	for _, ln := range bytes.Split(b, []byte("\n")) {
		if len(bytes.TrimSpace(ln)) == 0 {
			continue
		}
		var r Record
		if json.Unmarshal(ln, &r) == nil && r.Feature != "" {
			out = append(out, r)
		}
	}
	return out
}

// ReadRows reads every row in the ledger in chronological order, for tests and small scans.
func ReadRows() []Record {
	var out []Record
	for _, day := range RawDays() {
		out = append(out, ReadDay(day)...)
	}
	return out
}

// PruneRawNow runs the retention prune immediately (a test-only hook). The throttle still
// applies, so call ResetPruneClock first. It exists because pruneRawLocked is unexported
// and a test outside this package has no other way in.
func PruneRawNow() {
	Mu.Lock()
	pruneRawLocked()
	Mu.Unlock()
}

// ResetPruneClock rewinds the retention prune's throttle clock (a test-only hook). It is
// the one opening a test helper needs, since prunedAt is unexported.
func ResetPruneClock() {
	Mu.Lock()
	prunedAt = time.Time{}
	Mu.Unlock()
}
