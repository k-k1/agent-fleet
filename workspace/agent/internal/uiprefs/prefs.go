package uiprefs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// Per-user UI preferences (theme / icon set / fonts / viewer options). The Console
// stores its display settings here so they follow the user across browsers/devices,
// instead of living only in each browser's localStorage. The payload is an opaque
// JSON object owned by the Console; the Agent just persists it verbatim. Stored under
// the denylisted .config/agent-fleet (hidden from the file browser) in the home
// volume, so it survives Stop→Start.
//
// このパッケージには「prefs の読み書き」と「main の何にも依存しないアクセサ」だけを置く。
// 機能側の定数・関数（chatProviders / defaultAutoTurns / modelHidden / materializeMCPAll 等）を
// 参照するアクセサと HTTP ハンドラは ui_prefs.go（package main）に残っている。

const MaxBytes = 64 << 10 // 64 KiB cap on the prefs blob

func Path() string {
	return filepath.Join(paths.HomeDir(), ".config", "agent-fleet", "ui-prefs.json")
}

// 直前の版の退避先。PUT は本文をそのまま置き換えるので、事故で痩せた blob が来ると
// それ以前の内容はどこにも残らない（実際に返信サジェストの学習が全端末で消えた）。
func BackupPath() string {
	return Path() + ".prev"
}

// 累積データのキー — 溜まっていく設定で、失っても作り直せない。Console 側の
// ACCUMULATED（console/src/lib/settings.ts）と同じ顔ぶれにしてある。
var accumulatedPrefKeys = []string{
	"quickReplies",
	"quickRepliesPinned",
	"quickRepliesHidden",
	"ssmHostUsage",
	"ssmHostColors",
	"keybindings",
	"hiddenModels",
	"expandThinking",
	"claudeCustomModels",
	"workingSets",
	"ttsVoicePool",
	"ttsUserDict",
}

// emptyPref は「中身が無い」— 欠落・null・空文字・空配列・空オブジェクト。
// 数値/真偽値は対象外（0 や false は消えた印ではなく選ばれた値）。
func emptyPref(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	default:
		return false
	}
}

// ShrunkKeys は「前は中身があったのに、この PUT で空（または欠落）になる」累積キーを返す。
// 利用者が明示的に消すこともある（設定 > キー の「学習済みの候補を全消去」）ので拒否はしない —
// 拒否すると正当な操作が効かなくなる。代わりに直前の版を .prev へ退避し、何が痩せたかを
// ログに残す（この形跡が無かったせいで、原因の特定に転写と mtime の突き合わせが要った）。
func ShrunkKeys(before, after map[string]any) []string {
	var out []string
	for _, k := range accumulatedPrefKeys {
		old, had := before[k]
		if !had || emptyPref(old) {
			continue
		}
		if now, ok := after[k]; !ok || emptyPref(now) {
			out = append(out, k)
		}
	}
	return out
}

// Read loads the raw prefs blob from disk — the same file handleGetUIPrefs
// serves over HTTP — for same-process feature gates that don't need (and shouldn't
// pay for) an HTTP round trip. Any read/parse failure returns an empty map, same
// fallback as the handler, so a corrupt/missing file just means "nothing set".
func Read() map[string]any {
	b, err := os.ReadFile(Path())
	if err != nil || len(b) == 0 {
		return map[string]any{}
	}
	var obj map[string]any
	if json.Unmarshal(b, &obj) != nil {
		return map[string]any{}
	}
	return obj
}

// AutoTitleSuggest is the ON/OFF for SESSION title suggestion — the automatic
// banner plus the manual title/branch suggest endpoints (Console AgentsTab セッション).
// The assistant-chat side gates separately (AssistantTitleSuggest). Missing/
// invalid key ⇒ true, matching the frontend's DEFAULTS.autoTitleSuggest (settings.ts)
// so pre-feature clients get it without an explicit opt-in.
func AutoTitleSuggest() bool {
	v, ok := Read()["autoTitleSuggest"].(bool)
	return !ok || v
}

// OpencodeCatalog is how the opencode launch-model list is shaped (設定 >
// エージェント > opencode, ui-prefs opencodeCatalog). One key serves both opencode.ai
// billing routes, so the same model can appear as opencode/… (Zen, metered) and
// opencode-go/… (the Go subscription); a Go subscriber rarely wants the metered twins
// in the list at all. Read live per request — the Console picker and the MCP
// list_models both go through handleAgentModels, so one preference shapes both.
func OpencodeCatalog() string {
	v, _ := Read()["opencodeCatalog"].(string)
	return opencode.CatalogPref(v)
}

// PeerMessaging is the ON/OFF for session-to-session messaging (docs/log/58 / ADR 0041,
// ui-prefs peerMessaging). Missing/invalid ⇒ **false**: this one is opt-in, unlike
// AutoTitleSuggest. Letting sessions type into each other widens the injection surface
// (a session that read a poisoned repo can now reach every other session), so it has to
// be a deliberate choice rather than something a fleet inherits by upgrading.
func PeerMessaging() bool {
	v, _ := Read()["peerMessaging"].(bool)
	return v
}

// mcpreg builds the session-side af server's launch args and must not read main's
// config files itself, so it takes the answer as a hook (same shape as opencode.UsagePref).
func init() { mcpreg.PeerMessagingEnabled = PeerMessaging }

// The opencode package needs the same preference to decide whether to inject
// OPENCODE_API_KEY at all（無料枠は注入しない）and what to report to /connections.
// It lives under internal/agents, which must not read main's config files itself.
func init() { opencode.UsagePref = OpencodeCatalog }

// ClaudeCustomModels is the user's durable extension to Claude's fixed tier
// aliases. Claude Code OAuth has no account-aware catalog endpoint, so only explicitly
// registered full ids are advertised by Console and MCP; malformed/duplicate values
// from older or corrupt prefs are ignored.
func ClaudeCustomModels() []string {
	raw, ok := Read()["claudeCustomModels"].([]any)
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

// AssistantTitleSuggest is the ON/OFF for the assistant-chat title suggestion
// (the rename dialog's AI-suggest button, chat_title.go; Console AssistantTab). Split
// out of autoTitleSuggest so sessions and chats gate independently — prefs written
// before the split lack the key, so fall back to the combined flag to preserve an
// explicit OFF.
func AssistantTitleSuggest() bool {
	if v, ok := Read()["assistantTitleSuggest"].(bool); ok {
		return v
	}
	return AutoTitleSuggest()
}

// ChatAutoTurn is the global ON/OFF for the operator's automatic turn on a
// session report (docs/log/30, 設定 > アシスタント「セッション報告への自動応答」). Missing/invalid
// key ⇒ true, matching the frontend default — the feature ships ON, with the
// per-conversation maxAutoTurns cap as the safety stop.
func ChatAutoTurn() bool {
	v, ok := Read()["assistantAutoTurn"].(bool)
	return !ok || v
}

// ChatQuietCompletion is the global ON/OFF for 静かな完了報告 (設定 >
// アシスタント). ON のとき、正常な完了報告では自動ターンを回さず、報告カードと
// 通知センターへの配信だけにする（報告は未配信のまま残り、次のターンに相乗りする
// — injectPendingReports）。Missing/invalid key ⇒ FALSE: 完了の追撃・要約は既定の
// 体験として残し、費用を絞りたい利用者だけが明示的に静かにする。
func ChatQuietCompletion() bool {
	v, ok := Read()["assistantQuietCompletion"].(bool)
	return ok && v
}

// ChatAutoPilot is the global ON/OFF for 自動走行 (docs/log/30, 設定 >
// アシスタント「自動走行」): the operator autonomously answers a session's
// AskUserQuestion with the session's own recommendation, and drives a presented plan
// through review-by-another-session → feedback → approval. Missing/invalid key ⇒
// FALSE — acting in the user's stead is consequential, so unlike auto-turn this mode
// is a deliberate opt-in. The mode gates only the INSTRUCTION text carried on the
// interim reports (reportHeadFor); the guardrails (share every decision, never
// auto-handle destructive or unclear cases) ride in that text and in the persona.
func ChatAutoPilot() bool {
	v, ok := Read()["assistantAutoPilot"].(bool)
	return ok && v
}

// ChatAutoResume is the global ON/OFF for 中断時の自動再開 (docs/log/47, 設定 >
// アシスタント): on an aborted turn (接続断・一時的なレート制限で切れた — 原因が
// 自然に解消する中断) the operator is told to nudge the session to continue instead of
// only relaying to the user. Missing/invalid key ⇒ TRUE, unlike 自動走行: the nudge
// carries no decision of the user's — it re-runs work the user already asked for, and
// its blast radius is bounded by the retryable/blocked split (a failure whose cause
// won't clear is never auto-resumed) and by maxAutoResumeAttempts.
func ChatAutoResume() bool {
	v, ok := Read()["assistantAutoResume"].(bool)
	return !ok || v
}

// RateLimitAutoResume is the ON/OFF for 利用上限リセット後の自動再開 (docs/log/47
// §4-4, 設定 > エージェント > Claude > 動作設定): when a claude session is cut off by
// its usage limit, book a one-shot schedule at the reset instant that tells the session
// to continue. Missing/invalid key ⇒ TRUE, like ChatAutoResume and for the same
// reason — the nudge re-runs work the user already asked for and carries no decision of theirs.
//
// このトグルが左右するのは**再開の予約だけ**。上限メニューの自動解除（キー入力待ちで
// 止まったペインを既定の「リセットまで待つ」で待機プロンプトへ戻す）は OFF でも行う:
// メニューが出ている間セッションは通知も報告も注入も受け付けられず、待つ側の選択肢は
// 課金判断を含まないため（tmuxx.DismissRateLimitModal）。
func RateLimitAutoResume() bool {
	v, ok := Read()["rateLimitAutoResume"].(bool)
	return !ok || v
}

// AbortAutoResume is the ON/OFF for 中断からの自動再開 (docs/log/47 §4-6, 設定 >
// エージェント > Claude > 動作設定): when a claude turn is cut off by something that
// clears on its own (接続断・一時的なレート制限・ストリームの番犬), the Agent itself
// re-sends「続けて」instead of routing the resume through the operator assistant.
// Missing/invalid key ⇒ TRUE, for the same reason as the two toggles above — 再開は
// 利用者が既に頼んだ作業を走らせ直すだけで、新しい判断を含まない。
//
// 置き場所が ChatAutoResume（設定 > アシスタント）と違うのは、効く範囲が違うから:
// こちらはアシスタント会話の有無に関わらず**すべての claude TUI セッション**に適用される
// （rateLimitAutoResume と同じ立場）。OFF にすると中断は従来どおり即座に報告され、会話を
// 持つセッションだけがオペレーター主導で再開される（docs/log/47 §3-4）。
func AbortAutoResume() bool {
	v, ok := Read()["claudeAbortAutoResume"].(bool)
	return !ok || v
}

// ChatAutoCompact is the global ON/OFF for the assistant chat's preventive
// auto-compaction at the context threshold (docs/log/33 第4段, 設定 > アシスタント
// 「コンテキストの自動圧縮」). Missing/invalid key ⇒ true, matching the frontend
// default — the 80% notice gives the user a manual window first, and the summary
// handoff keeps the stored thread intact, so ON is the safe default.
func ChatAutoCompact() bool {
	v, ok := Read()["assistantAutoCompact"].(bool)
	return !ok || v
}

// ChatOutputLanguage returns the user's forced chat output language ("ja" | "en"),
// or "" when unset/"auto"/invalid — meaning "follow the input" (no language rule is
// injected, preserving the persona-driven default). Read live per turn from ui-prefs
// so a change takes effect on the next message of every conversation (not snapshotted).
func ChatOutputLanguage() string {
	switch v, _ := Read()["outputLanguage"].(string); v {
	case "ja", "en":
		return v
	default:
		return ""
	}
}

// Locale returns the Console display language the user picked (設定 > 表示言語,
// ADR 0016). The frontend keeps `locale` server-synced precisely because language is a
// per-person setting rather than a per-device one, so the Agent can read it for text it
// generates FOR that person — currently the title suggestion. Unknown/missing ⇒ "ja",
// matching the frontend's DEFAULT_LOCALE.
func Locale() string {
	if v, _ := Read()["locale"].(string); v == "en" {
		return "en"
	}
	return "ja"
}
