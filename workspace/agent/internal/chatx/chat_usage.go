package chatx

// Context-usage capture and pressure notices for the assistant chat (docs/log/33).
//
// Each turn picks up the usage events every provider's headless run returns and persists
// a "latest context occupancy" snapshot on the conversation (contextUsage — the same
// shape as get_session_usage and the mirror's ContextBar). A resume-driven chat piles
// context up without bound in the provider-side transcript, so the first stage of the
// answer is to make the occupancy visible, and to append a notice exactly once past the
// threshold nudging the user to hand over to a new thread. Handing over a summary
// (compaction of our own) is later work.
//
// The event shapes are measured on all three providers (2026-07: claude-code 2.1.x /
// codex-cli 0.144 / opencode 1.18). A turn that yielded no usage keeps the previous value.

import (
	"sort"
	"strconv"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// chatCtxWarnPct is the context-usage percentage at or above which a notice nudges the
// user to hand over. It matches the ContextBar's "near" band (80%) so the words arrive at
// the same moment the bar turns its warning colour, and so the user hears about it before
// the provider's own auto-compaction or a window-exceeded error does.
const chatCtxWarnPct = 80.0

// ClaudeUsage is the usage block of claude -p (the shape shared by the result event and
// an assistant event's message.usage). iterations holds one snapshot per API call within
// the turn; measured, the last element is the final context occupancy, and using that
// last element rather than the top-level sum stays accurate even for a multi-tool turn.
type ClaudeUsage struct {
	InputTokens              int `json:"input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	// OutputTokens does not affect context occupancy (output comes back as the next
	// turn's input) but the usage ledger's spend needs it (docs/log/46 §2). Its
	// presence is measured.
	OutputTokens int           `json:"output_tokens"`
	Iterations   []ClaudeUsage `json:"iterations,omitempty"`
}

// contextTokens is the sum of the input-side snapshot = the context occupancy.
func (u ClaudeUsage) contextTokens() int {
	return u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
}

// LedgerTokens is the usage ledger's token breakdown (docs/log/46). It is the DEGRADED
// path for when modelUsage could not be read: the per-model breakdown is lost but the
// totals survive. The top-level values are used because what the ledger wants is "how
// much this call was actually billed for" — a different quantity from context occupancy
// (the snapshot at the tail of iterations).
func (u ClaudeUsage) LedgerTokens() usagex.Tokens {
	return usagex.Tokens{
		In: u.InputTokens, Out: u.OutputTokens,
		CacheRead: u.CacheReadInputTokens, CacheCreate: u.CacheCreationInputTokens,
	}
}

// ClaudeModelUsage is the part of a result event's modelUsage entry we need. The map key
// is the raw model id including the version (claude-haiku-4-5-20251001) and CanonicalModel
// is the series key with the version folded away (claude-haiku-4-5); both are kept so a
// version bump does not split the ledger's series. contextWindow is the model's real
// window (usable as recorded). The four token counts and CostUSD are the ledger's
// per-model row (ADR 0029 §1). One claude call can be split across several models, so this
// is the only path that carries a measured per-model breakdown. All fields are measured.
type ClaudeModelUsage struct {
	ContextWindow            int     `json:"contextWindow"`
	InputTokens              int     `json:"inputTokens"`
	OutputTokens             int     `json:"outputTokens"`
	CacheReadInputTokens     int     `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int     `json:"cacheCreationInputTokens"`
	CostUSD                  float64 `json:"costUSD"`
	CanonicalModel           string  `json:"canonicalModel"`
}

// UsageModelRows converts modelUsage into the ledger's per-model rows: the key (the raw
// id) becomes model_raw and canonicalModel the series key. The order is pinned to
// ascending key — left at map iteration order, the same call would produce a different
// row order each time, making both the tests and the ledger's read side unstable.
func UsageModelRows(mu map[string]ClaudeModelUsage) []usagex.ModelRow {
	if len(mu) == 0 {
		return nil
	}
	raws := make([]string, 0, len(mu))
	for raw := range mu {
		raws = append(raws, raw)
	}
	sort.Strings(raws)
	rows := make([]usagex.ModelRow, 0, len(raws))
	for _, raw := range raws {
		m := mu[raw]
		rows = append(rows, usagex.ModelRow{
			Model: m.CanonicalModel, ModelRaw: raw, CostUSD: m.CostUSD,
			Tokens: usagex.Tokens{
				In: m.InputTokens, Out: m.OutputTokens,
				CacheRead: m.CacheReadInputTokens, CacheCreate: m.CacheCreationInputTokens,
			},
		})
	}
	return rows
}

// claudeCtx tracks context occupancy across the event stream of one claude run. In stream
// mode it takes each assistant event's message.usage; otherwise the tail of the result's
// usage.iterations is the final snapshot.
type claudeCtx struct {
	snap   ClaudeUsage
	window int
	model  string
}

// observeAssistant applies a stream assistant event (message.usage). Events for the same
// message carry the same usage repeatedly, so plain last-write-wins is enough.
func (t *claudeCtx) observeAssistant(model string, u ClaudeUsage) {
	if u.contextTokens() <= 0 {
		return
	}
	t.snap = u
	if model != "" {
		t.model = model // prefer the real model name the event carries
	}
}

// observeResult applies a result event's usage / modelUsage. When iterations is present
// its tail is the final snapshot (and agrees with the assistant events' values).
func (t *claudeCtx) observeResult(u ClaudeUsage, modelUsage map[string]ClaudeModelUsage) {
	if n := len(u.Iterations); n > 0 {
		t.snap = u.Iterations[n-1]
	} else if t.snap.contextTokens() == 0 {
		t.snap = u
	}
	// Pick up the measured window from modelUsage. A chat that forbids subagents
	// normally has one entry; should there be several, only trust the one matching
	// the already-resolved model name.
	if len(modelUsage) == 1 {
		for k, mu := range modelUsage {
			if t.model == "" {
				t.model = k
			}
			t.window = mu.ContextWindow
		}
		return
	}
	if mu, ok := modelUsage[t.model]; ok {
		t.window = mu.ContextWindow
	}
}

// apply stores the tracking result on the conversation (call it before a successful
// turn's saveConv). claude is one of the few providers that puts the real model on its
// events, so this is also where the turn's model is recorded — the version-bearing id the
// API named itself, not the alias passed to --model.
func (t *claudeCtx) apply(c *ChatConversation) {
	setChatContext(c, t.snap.InputTokens, t.snap.CacheReadInputTokens,
		t.snap.CacheCreationInputTokens, t.window, t.model)
	c.NoteTurnModel(t.model)
}

// CodexUsage is the usage carried by turn.completed of codex exec --json. input_tokens
// includes cached_input_tokens (the same convention as the rollout's token_count). Being
// a per-turn total it over-approximates context occupancy on a multi-tool turn — accurate
// for the bulk of chats (one tool-less call) and on the safe side for a warning.
type CodexUsage struct {
	InputTokens       int `json:"input_tokens"`
	CachedInputTokens int `json:"cached_input_tokens"`
	// For the usage ledger (docs/log/46 §2). Measured on codex-cli 0.144.x:
	// turn.completed also carries cache_write_input_tokens / output_tokens /
	// reasoning_output_tokens. reasoning is a breakdown already contained in output,
	// so it is not added to spend.
	CacheWriteInputTokens int `json:"cache_write_input_tokens"`
	OutputTokens          int `json:"output_tokens"`
}

// LedgerTokens maps codex's per-turn total usage onto the ledger's breakdown.
// input_tokens includes cached (the rollout token_count convention), so
// fresh = input - cached.
func (u CodexUsage) LedgerTokens() usagex.Tokens {
	fresh := u.InputTokens - u.CachedInputTokens
	if fresh < 0 {
		fresh = 0
	}
	return usagex.Tokens{
		In: fresh, Out: u.OutputTokens,
		CacheRead: u.CachedInputTokens, CacheCreate: u.CacheWriteInputTokens,
	}
}

// opencodeUsage is the part.tokens carried by step_finish of opencode run --format json.
// input excludes the cached share (the same shape as message.data.tokens in the SQLite
// store).
type opencodeUsage struct {
	Input int `json:"input"`
	// Output is for the usage ledger (docs/log/46 §2). This workspace is not logged
	// into opencode, so it has no live verification; when it cannot be read it stays
	// 0 rather than being filled in by guesswork (ADR 0029 §4).
	Output int `json:"output"`
	Cache  struct {
		Read  int `json:"read"`
		Write int `json:"write"`
	} `json:"cache"`
}

// LedgerTokens maps opencode's breakdown onto the ledger's shape (input excludes cache).
func (u opencodeUsage) LedgerTokens() usagex.Tokens {
	return usagex.Tokens{In: u.Input, Out: u.Output, CacheRead: u.Cache.Read, CacheCreate: u.Cache.Write}
}

// setChatContext is the shared store point. On a turn with an empty snapshot (a provider
// path that yields no usage, an empty answer) it does nothing and leaves the previous
// value in place. When the window cannot be read it is estimated from the model name
// (contextWindowGuess — the same as get_session_usage).
func setChatContext(c *ChatConversation, fresh, read, create, window int, model string) {
	if fresh < 0 {
		fresh = 0
	}
	tokens := fresh + read + create
	if tokens <= 0 {
		return
	}
	source := "recorded"
	if window <= 0 {
		window, source = usagex.WindowGuess(model, tokens), "estimated"
	}
	u := &usagex.ContextUsage{
		Tokens: tokens, Read: read, Create: create, Fresh: fresh,
		Window: window, WindowSource: source, Model: model,
	}
	if window > 0 {
		u.Pct = float64(tokens) / float64(window) * 100
	}
	c.Context = u
}

// chatCtxModelFor is the model name used to estimate the window: the value resolved
// against the backend that actually ran the turn, if there is one (chatModelFor — with an
// auth fallback or a mid-conversation switch, a CLI other than the pinned one runs, so
// without passing kind the window would be estimated from another CLI's model name), and
// otherwise the per-backend default.
func chatCtxModelFor(c *ChatConversation, kind string) string {
	if m := chatModelFor(c, kind); m != "" {
		return m
	}
	switch kind {
	case session.KindCodex:
		return defaultCodexChatModel
	case session.KindOpencode:
		return defaultOpencodeChatModel
	}
	return envOr("AF_CHAT_MODEL", defaultChatModel)
}

// NoteContextPressure appends the notice exactly once past the threshold and also pushes
// it to the notification center, so it is noticed during an auto turn as well (when the
// conversation is not open). Dropping back below the threshold — occupancy shrinking
// after a provider-side compaction, say — clears the flag, and the next crossing notifies
// once again. The caller must hold the conversation lock and saveConv right afterwards.
func NoteContextPressure(c *ChatConversation) {
	u := c.Context
	if u == nil || u.Pct < chatCtxWarnPct {
		c.CtxWarned = false
		return
	}
	if c.CtxWarned {
		return
	}
	c.CtxWarned = true
	c.Messages = append(c.Messages, newNotice(noticeKeyCtxPressure, map[string]string{
		"pct":    strconv.Itoa(int(u.Pct)),
		"tokens": fmtKTokens(u.Tokens),
		"window": fmtKTokens(u.Window),
	}, ctxPressureContent(u)))
	ev := notice.New("chat-context-pressure", "", "", c.Title)
	ev.Payload["conversation_id"] = c.ID
	ev.Payload["conversationTitle"] = c.Title
	_ = notice.Put(ev)
}

// ctxPressureContent is the pressure notice's fallback body in the canonical language
// (ja). Display goes through noticeKeyCtxPressure's catalogue translation
// (chat_notice.go / ADR 0033).
func ctxPressureContent(u *usagex.ContextUsage) string {
	return "この会話のコンテキスト使用量が上限の約" + strconv.Itoa(int(u.Pct)) + "%" +
		"（" + fmtKTokens(u.Tokens) + " / " + fmtKTokens(u.Window) + " トークン）に達しました。" +
		"このまま続けると、応答の品質低下・ターンの失敗・トークン消費の増大につながります。" +
		"ヘッダのコンテキストバー右にある「圧縮」で要約だけを新しいセッションへ引き継いで続行するか、" +
		"区切りの良いところで新しいチャットを開くことを検討してください。"
}

// fmtKTokens is a display helper rounding 1000 and above to "123k".
func fmtKTokens(n int) string {
	if n >= 1000 {
		return strconv.Itoa(n/1000) + "k"
	}
	return strconv.Itoa(n)
}
