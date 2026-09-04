package usagex

// One recording point in the provider layer (docs/log/46 §3-a, ADR0029 §3).
//
// usage is parsed inside the provider implementations (claudeChat.send /
// parseCodexExecEvents / parseOpencodeRunEvents / cursorChat.send / oneShotHeadless), which
// already hold both the model and the tokens. The only thing missing is what the call was
// FOR, so that alone rides on the context.Context (Tag / WithTag / TagOf). Changing where a
// consumption comes from is one line in one place, and no new recording point appears.
//
// The methods on Tokens / Call are exported because main has to call them and Go cannot alias
// a method.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// TagOrUnknown pulls the tag out. An untagged call is still recorded, as feature=unknown:
// consumption disappearing because someone forgot a tag is worse than unknown rows mixed in.
func TagOrUnknown(ctx context.Context) Tag {
	if t, ok := TagOf(ctx); ok {
		return t
	}
	return Tag{Feature: FeatureUnknown}
}

// Tokens is one model's token breakdown.
type Tokens struct {
	In          int
	Out         int
	CacheRead   int
	CacheCreate int
}

// ModelRow is one model actually billed within a single call. claude reports a per-model
// breakdown in modelUsage, so there can be several (bundled under the same Call and counted
// as one call).
type ModelRow struct {
	Model    string // canonical name (claude's canonicalModel)
	ModelRaw string // raw id (version included)
	Tokens   Tokens
	CostUSD  float64
}

// Call is one LLM call as the provider layer sees it. Each provider function makes a zero
// value at the top, defers the recording and fills in whatever it learns — the point of the
// shape is that success, failure and every early return all record exactly once.
type Call struct {
	Kind     string     // the agent kind that ran (the outcome, not the request)
	ModelReq string     // the requested value ("" = left to the CLI's default)
	Models   []ModelRow // per-model breakdown; if empty, one row is raised from Totals
	Totals   Tokens     // the tokens when Models is empty
	CostUSD  float64    // measured cost (claude only)
	OK       bool
	Measured string // empty = infer it from what Totals/Models hold
}

// SetTotals is the recording entry for providers with no per-model breakdown
// (codex/opencode/cursor).
func (c *Call) SetTotals(in, out, cread, ccreate int) {
	c.Totals = Tokens{In: in, Out: out, CacheRead: cread, CacheCreate: ccreate}
}

// Add sums what was fired twice within the same call (the codex one-shot retry that drops the
// model). Two separate processes were started, so the input side is a sum of real consumption
// too, not a snapshot.
func (t Tokens) Add(o Tokens) Tokens {
	return Tokens{
		In: t.In + o.In, Out: t.Out + o.Out,
		CacheRead: t.CacheRead + o.CacheRead, CacheCreate: t.CacheCreate + o.CacheCreate,
	}
}

func (t Tokens) Any() bool { return t.In+t.Out+t.CacheRead+t.CacheCreate > 0 }

// FallbackTotals is the degraded recording entry that only applies when no per-model breakdown
// could be obtained. claude normally splits per model via the result's modelUsage, but a user's
// stop and an abnormal exit before the result both arrive with no modelUsage. So whatever usage
// snapshot is left gets taken: take nothing here and the context actually consumed vanishes as
// "0 tokens / measured=none", making consumption invisible for exactly the runs that were
// stopped (and the heavier the turn, the more often it is stopped).
//
// measured is declared by the caller: empty when it comes from a completed result (i.e. leave
// the exact/none verdict to MeasuredOr), partial when it comes from a mid-turn snapshot.
func (c *Call) FallbackTotals(t Tokens, measured string) {
	if len(c.Models) > 0 || c.Totals.Any() || !t.Any() {
		return
	}
	c.Totals = t
	if measured != "" {
		c.Measured = measured
	}
}

// RecordCall writes one call (one row or more) to the ledger. ctx is used only to read the tag,
// so an already-cancelled ctx (a user's stop, a timeout) still leaves the record behind.
func RecordCall(ctx context.Context, c *Call, started time.Time) {
	if !Enabled() {
		return
	}
	tag := TagOrUnknown(ctx)
	origin, originConv := originOf(tag.Ref)
	base := Record{
		TS:         time.Now().UTC().Format(time.RFC3339),
		Call:       newCallID(),
		Feature:    tag.Feature,
		Trigger:    tag.Trigger,
		Origin:     origin,
		OriginConv: originConv,
		Kind:       c.Kind,
		ModelReq:   c.ModelReq,
		Ref:        tag.Ref,
		Verb:       tag.Verb,
		MS:         int(time.Since(started).Milliseconds()),
		OK:         c.OK,
	}
	rows := make([]Record, 0, max(1, len(c.Models)))
	if len(c.Models) == 0 {
		r := base
		r.Model, r.ModelSrc = ModelFallback(c.ModelReq)
		r.In, r.Out = c.Totals.In, c.Totals.Out
		r.CacheRead, r.CacheCreate = c.Totals.CacheRead, c.Totals.CacheCreate
		r.CostUSD = c.CostUSD
		r.Spend = Spend(r.In, r.CacheCreate, r.Out)
		r.Measured = c.MeasuredOr(c.Totals)
		rows = append(rows, r)
	}
	for _, m := range c.Models {
		r := base
		r.Model, r.ModelRaw, r.ModelSrc = m.Model, m.ModelRaw, ModelReported
		if r.Model == "" {
			r.Model = m.ModelRaw // no canonicalModel: the raw id becomes the series key
		}
		r.In, r.Out = m.Tokens.In, m.Tokens.Out
		r.CacheRead, r.CacheCreate = m.Tokens.CacheRead, m.Tokens.CacheCreate
		r.CostUSD = m.CostUSD
		r.Spend = Spend(r.In, r.CacheCreate, r.Out)
		r.Measured = c.MeasuredOr(m.Tokens)
		rows = append(rows, r)
	}
	AppendRows(rows)
}

// MeasuredOr is the self-declared measurement accuracy. It follows the provider when the
// provider is explicit, and otherwise decides exact / none on whether a single token was
// obtained at all (a failed turn is none).
func (c *Call) MeasuredOr(t Tokens) string {
	if c.Measured != "" {
		return c.Measured
	}
	if t.In+t.Out+t.CacheRead+t.CacheCreate > 0 {
		return MeasuredExact
	}
	return MeasuredNone
}

// ModelFallback is the degraded path for CLIs that do not report a model (codex/cursor/agy).
// requested when there is a requested value, default_unknown when there is not — the aim is
// that "it is running on the CLI's default (usually the flagship)" is visible in one column
// (docs/log/46 §2-b).
func ModelFallback(req string) (model, src string) {
	if req == "" {
		return "", ModelUnknown
	}
	return req, ModelRequest
}

// originOf resolves a session's origin from its ref (ADR0029 §6). It is burned into the row, so
// deleting the session does not break the aggregation. A conversation-scoped ref (an assistant
// conversation id) has no origin axis and returns empty — origin is an axis of sessions.
func originOf(ref string) (origin, conv string) {
	if ref == "" || !session.ValidName(ref) {
		return "", ""
	}
	m, ok := session.ReadMeta(ref)
	if !ok {
		return "", ""
	}
	return session.OriginOf(m), m.OriginConv
}

// newCallID produces the ledger's Call column, the id that bundles one call whose rows split
// across several models. RFC-4122 v4.
//
// A copied pure function: identical to randUUID in internal/browserx/uuid.go, and due to be
// unified into one place in the collection wave. UUID v4 is a standard, so the copies cannot
// diverge in behaviour — only in how many places hold one.
func newCallID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}
