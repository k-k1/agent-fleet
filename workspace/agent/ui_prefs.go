package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Per-user UI preferences (theme / icon set / fonts / viewer options). The Console
// stores its display settings here so they follow the user across browsers/devices,
// instead of living only in each browser's localStorage. The payload is an opaque
// JSON object owned by the Console; the Agent just persists it verbatim. Stored under
// the denylisted .config/agent-fleet (hidden from the file browser) in the home
// volume, so it survives Stop→Start.

const maxUIPrefsBytes = 64 << 10 // 64 KiB cap on the prefs blob

func uiPrefsPath() string {
	return filepath.Join(homeDir(), ".config", "agent-fleet", "ui-prefs.json")
}

// readUIPrefs loads the raw prefs blob from disk — the same file handleGetUIPrefs
// serves over HTTP — for same-process feature gates that don't need (and shouldn't
// pay for) an HTTP round trip. Any read/parse failure returns an empty map, same
// fallback as the handler, so a corrupt/missing file just means "nothing set".
func readUIPrefs() map[string]any {
	b, err := os.ReadFile(uiPrefsPath())
	if err != nil || len(b) == 0 {
		return map[string]any{}
	}
	var obj map[string]any
	if json.Unmarshal(b, &obj) != nil {
		return map[string]any{}
	}
	return obj
}

// autoTitleSuggestEnabled is the ON/OFF for SESSION title suggestion — the automatic
// banner plus the manual title/branch suggest endpoints (Console AgentsTab セッション).
// The assistant-chat side gates separately (assistantTitleSuggestEnabled). Missing/
// invalid key ⇒ true, matching the frontend's DEFAULTS.autoTitleSuggest (settings.ts)
// so pre-feature clients get it without an explicit opt-in.
func autoTitleSuggestEnabled() bool {
	v, ok := readUIPrefs()["autoTitleSuggest"].(bool)
	return !ok || v
}

// opencodeCatalogPref is how the opencode launch-model list is shaped (設定 >
// エージェント > opencode, ui-prefs opencodeCatalog). One key serves both opencode.ai
// billing routes, so the same model can appear as opencode/… (Zen, metered) and
// opencode-go/… (the Go subscription); a Go subscriber rarely wants the metered twins
// in the list at all. Read live per request — the Console picker and the MCP
// list_models both go through handleAgentModels, so one preference shapes both.
func opencodeCatalogPref() string {
	v, _ := readUIPrefs()["opencodeCatalog"].(string)
	return opencode.CatalogPref(v)
}

// The opencode package needs the same preference to decide whether to inject
// OPENCODE_API_KEY at all（無料枠は注入しない）and what to report to /connections.
// It lives under internal/agents, which must not read main's config files itself.
func init() { opencode.UsagePref = opencodeCatalogPref }

// claudeCustomModelsPref is the user's durable extension to Claude's fixed tier
// aliases. Claude Code OAuth has no account-aware catalog endpoint, so only explicitly
// registered full ids are advertised by Console and MCP; malformed/duplicate values
// from older or corrupt prefs are ignored.
func claudeCustomModelsPref() []string {
	raw, ok := readUIPrefs()["claudeCustomModels"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, value := range raw {
		id, ok := value.(string)
		id = strings.TrimSpace(id)
		key := strings.ToLower(id)
		if !ok || !validClaudeCustomModel(id) || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, id)
	}
	return out
}

func validClaudeCustomModel(id string) bool {
	id = strings.TrimSpace(id)
	if !strings.HasPrefix(strings.ToLower(id), "claude-") || len(id) == len("claude-") {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

// assistantTitleSuggestEnabled is the ON/OFF for the assistant-chat title suggestion
// (the rename dialog's AI-suggest button, chat_title.go; Console AssistantTab). Split
// out of autoTitleSuggest so sessions and chats gate independently — prefs written
// before the split lack the key, so fall back to the combined flag to preserve an
// explicit OFF.
func assistantTitleSuggestEnabled() bool {
	if v, ok := readUIPrefs()["assistantTitleSuggest"].(bool); ok {
		return v
	}
	return autoTitleSuggestEnabled()
}

// assistantAgentOrderPref returns the user's assistant-chat backend priority (the
// AssistantTab 並べ替え UI, ui-prefs assistantAgentOrder), normalized into a TOTAL
// order: unknown kinds and dupes are dropped, and kinds missing from the stored
// list are appended in the built-in default order — so a partial or stale list
// (e.g. written before a new backend existed) still ranks every backend. When the
// key is absent, the legacy single-pin key (assistantAgent) degrades gracefully:
// a pin is simply "that backend first". Read live per call (like
// chatOutputLanguage) so a change applies from the next builtin-assistant
// conversation / one-shot call without a restart.
func assistantAgentOrderPref() []string {
	prefs := readUIPrefs()
	out := make([]string, 0, len(defaultHeadlessOrder))
	seen := map[string]bool{}
	add := func(k string) {
		if _, ok := chatProviders[k]; ok && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	if raw, ok := prefs["assistantAgentOrder"].([]any); ok {
		for _, v := range raw {
			if s, ok := v.(string); ok {
				add(s)
			}
		}
	} else if pin, _ := prefs["assistantAgent"].(string); pin != "" {
		add(pin) // legacy pin ("auto" is not a kind, so it falls through to the default)
	}
	for _, k := range defaultHeadlessOrder {
		add(k)
	}
	return out
}

// assistantModelPref returns a per-backend model selected in AssistantTab. The
// boolean distinguishes a missing (pre-feature) map from an explicit empty value:
// empty means "let this CLI choose its default", while missing keeps the historical
// backend-specific defaults.
func assistantModelPref(key, kind string) (string, bool) {
	raw, ok := readUIPrefs()[key].(map[string]any)
	if !ok {
		return "", false
	}
	v, ok := raw[kind].(string)
	if !ok {
		return "", false
	}
	// 「使わないモデル」で除外された値が設定に残っていても採用しない（model_deny.go）。
	// 未設定扱いに落とすことで、呼び出し側は推奨／CLI 既定へ退避する。
	// "recommended" は実モデル id ではない番兵なのでそのまま通す。
	if v != assistantRecommendedModel && modelHidden(kind, v) {
		return "", false
	}
	return v, true
}

func assistantChatModelPref(kind string) (string, bool) {
	return assistantModelPref("assistantModels", kind)
}

func assistantUtilityModelPref(kind string) (string, bool) {
	return assistantModelPref("assistantUtilityModels", kind)
}

// chatAutoTurnEnabled is the global ON/OFF for the operator's automatic turn on a
// session report (docs/30, 設定 > アシスタント「セッション報告への自動応答」). Missing/invalid
// key ⇒ true, matching the frontend default — the feature ships ON, with the
// per-conversation maxAutoTurns cap as the safety stop.
func chatAutoTurnEnabled() bool {
	v, ok := readUIPrefs()["assistantAutoTurn"].(bool)
	return !ok || v
}

// chatAutoTurnLimit is the per-conversation ceiling on unattended auto turns
// (docs/30, 設定 > アシスタント「自動応答の上限回数」). Missing/invalid ⇒
// defaultAutoTurns; always clamped to [1, maxAutoTurnLimit] — there is no
// unlimited mode, the clamp is the runaway stop.
func chatAutoTurnLimit() int {
	v, ok := readUIPrefs()["assistantAutoTurnLimit"].(float64)
	if !ok {
		return defaultAutoTurns
	}
	n := int(v)
	if n < 1 {
		return 1
	}
	if n > maxAutoTurnLimit {
		return maxAutoTurnLimit
	}
	return n
}

// chatAutoTurnModel is the dedicated model for the operator's automatic turns
// (設定 > アシスタント「自動応答のモデル」). 空 = 会話のモデルのまま。報告の確認・
// 要約は定型作業なので軽量モデル（haiku 等）へ逃がせる — 自動ターンはユーザー
// ターンより回数が多く(実測 121 vs 107/5日)、ここの単価がそのまま費用に効く。
// 適用は claude の会話のみ（codex/opencode は c.Model 直参照で上書き口が無い —
// runReportAutoTurn 側でゲート）。
func chatAutoTurnModel() string {
	v, _ := readUIPrefs()["assistantAutoTurnModel"].(string)
	// claude 専用の設定なので claude の除外リストで判定する。除外されていれば空＝
	// 会話のモデルのまま（model_deny.go）。
	return visibleModel(session.KindClaude, strings.TrimSpace(v))
}

// chatQuietCompletionEnabled is the global ON/OFF for 静かな完了報告 (設定 >
// アシスタント). ON のとき、正常な完了報告では自動ターンを回さず、報告カードと
// 通知センターへの配信だけにする（報告は未配信のまま残り、次のターンに相乗りする
// — injectPendingReports）。Missing/invalid key ⇒ FALSE: 完了の追撃・要約は既定の
// 体験として残し、費用を絞りたい利用者だけが明示的に静かにする。
func chatQuietCompletionEnabled() bool {
	v, ok := readUIPrefs()["assistantQuietCompletion"].(bool)
	return ok && v
}

// chatAutoPilotEnabled is the global ON/OFF for 自動走行 (docs/30, 設定 >
// アシスタント「自動走行」): the operator autonomously answers a session's
// AskUserQuestion with the session's own recommendation, and drives a presented plan
// through review-by-another-session → feedback → approval. Missing/invalid key ⇒
// FALSE — acting in the user's stead is consequential, so unlike auto-turn this mode
// is a deliberate opt-in. The mode gates only the INSTRUCTION text carried on the
// interim reports (reportHeadFor); the guardrails (share every decision, never
// auto-handle destructive or unclear cases) ride in that text and in the persona.
func chatAutoPilotEnabled() bool {
	v, ok := readUIPrefs()["assistantAutoPilot"].(bool)
	return ok && v
}

// chatAutoResumeEnabled is the global ON/OFF for 中断時の自動再開 (docs/47, 設定 >
// アシスタント): on an aborted turn (接続断・一時的なレート制限で切れた — 原因が
// 自然に解消する中断) the operator is told to nudge the session to continue instead of
// only relaying to the user. Missing/invalid key ⇒ TRUE, unlike 自動走行: the nudge
// carries no decision of the user's — it re-runs work the user already asked for, and
// its blast radius is bounded by the retryable/blocked split (a failure whose cause
// won't clear is never auto-resumed) and by maxAutoResumeAttempts.
func chatAutoResumeEnabled() bool {
	v, ok := readUIPrefs()["assistantAutoResume"].(bool)
	return !ok || v
}

// rateLimitAutoResumeEnabled is the ON/OFF for 利用上限リセット後の自動再開 (docs/47
// §4-4, 設定 > エージェント > Claude > 動作設定): when a claude session is cut off by
// its usage limit, book a one-shot schedule at the reset instant that tells the session
// to continue. Missing/invalid key ⇒ TRUE, like chatAutoResumeEnabled and for the same
// reason — the nudge re-runs work the user already asked for and carries no decision of theirs.
//
// このトグルが左右するのは**再開の予約だけ**。上限メニューの自動解除（キー入力待ちで
// 止まったペインを既定の「リセットまで待つ」で待機プロンプトへ戻す）は OFF でも行う:
// メニューが出ている間セッションは通知も報告も注入も受け付けられず、待つ側の選択肢は
// 課金判断を含まないため（tmuxx.DismissRateLimitModal）。
func rateLimitAutoResumeEnabled() bool {
	v, ok := readUIPrefs()["rateLimitAutoResume"].(bool)
	return !ok || v
}

// abortAutoResumeEnabled is the ON/OFF for 中断からの自動再開 (docs/47 §4-6, 設定 >
// エージェント > Claude > 動作設定): when a claude turn is cut off by something that
// clears on its own (接続断・一時的なレート制限・ストリームの番犬), the Agent itself
// re-sends「続けて」instead of routing the resume through the operator assistant.
// Missing/invalid key ⇒ TRUE, for the same reason as the two toggles above — 再開は
// 利用者が既に頼んだ作業を走らせ直すだけで、新しい判断を含まない。
//
// 置き場所が chatAutoResumeEnabled（設定 > アシスタント）と違うのは、効く範囲が違うから:
// こちらはアシスタント会話の有無に関わらず**すべての claude TUI セッション**に適用される
// （rateLimitAutoResume と同じ立場）。OFF にすると中断は従来どおり即座に報告され、会話を
// 持つセッションだけがオペレーター主導で再開される（docs/47 §3-4）。
func abortAutoResumeEnabled() bool {
	v, ok := readUIPrefs()["claudeAbortAutoResume"].(bool)
	return !ok || v
}

// chatAutoCompactEnabled is the global ON/OFF for the assistant chat's preventive
// auto-compaction at the context threshold (docs/33 第4段, 設定 > アシスタント
// 「コンテキストの自動圧縮」). Missing/invalid key ⇒ true, matching the frontend
// default — the 80% notice gives the user a manual window first, and the summary
// handoff keeps the stored thread intact, so ON is the safe default.
func chatAutoCompactEnabled() bool {
	v, ok := readUIPrefs()["assistantAutoCompact"].(bool)
	return !ok || v
}

// chatOutputLanguage returns the user's forced chat output language ("ja" | "en"),
// or "" when unset/"auto"/invalid — meaning "follow the input" (no language rule is
// injected, preserving the persona-driven default). Read live per turn from ui-prefs
// so a change takes effect on the next message of every conversation (not snapshotted).
func chatOutputLanguage() string {
	switch v, _ := readUIPrefs()["outputLanguage"].(string); v {
	case "ja", "en":
		return v
	default:
		return ""
	}
}

// uiLocale returns the Console display language the user picked (設定 > 表示言語,
// ADR 0016). The frontend keeps `locale` server-synced precisely because language is a
// per-person setting rather than a per-device one, so the Agent can read it for text it
// generates FOR that person — currently the title suggestion. Unknown/missing ⇒ "ja",
// matching the frontend's DEFAULT_LOCALE.
func uiLocale() string {
	if v, _ := readUIPrefs()["locale"].(string); v == "en" {
		return "en"
	}
	return "ja"
}

func handleGetUIPrefs(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, readUIPrefs())
}

func handlePutUIPrefs(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxUIPrefsBytes+1))
	if err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "read failed")
		return
	}
	if len(body) > maxUIPrefsBytes {
		httpx.WriteErr(w, http.StatusRequestEntityTooLarge, "too_large", "prefs too large")
		return
	}
	// Validate it's a JSON object before persisting (the Console owns the shape).
	var obj map[string]any
	if json.Unmarshal(body, &obj) != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_json", "body must be a JSON object")
		return
	}
	if err := os.MkdirAll(filepath.Dir(uiPrefsPath()), 0o700); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "mkdir_failed", err.Error())
		return
	}
	before := opencodeCatalogPref()
	if err := os.WriteFile(uiPrefsPath(), body, 0o600); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
		return
	}
	// 枠の切替は注入する env を変える（無料枠は OPENCODE_API_KEY を落とす）。鍵の
	// 変更と同じ扱いで、動いている serve を作り直さないと古い環境のまま残る。
	if after := opencodeCatalogPref(); after != before {
		opencode.ApplyUsageChange(before + " → " + after)
	}
	httpx.WriteJSON(w, http.StatusOK, obj)
}
