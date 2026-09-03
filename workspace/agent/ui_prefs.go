package main

// ui-prefs のうち、**main の機能側**（chatProviders / defaultAutoTurns / modelHidden /
// materializeMCPAll / agentOf）に依存するアクセサと HTTP ハンドラだけがここに残る。
// prefs そのものの読み書きと、何にも依存しないアクセサは internal/uiprefs を直接呼ぶ
// （ウェーブ B の別名 alias_uiprefs.go は RECLAIM-B で回収済み）。

import (
	"encoding/json"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

// skipPermissionsPref は per-kind の既定「権限確認をスキップする」（設定 >
// エージェント > 各カード、ui-prefs agentLaunchDefaults[<kind>].skipPermissions — docs/log/76）。
// ok=false は「その kind に設定が無い」で、既定値そのものは agents.SkipPermissions が
// 持つ（従来どおり true）。
//
// HTTP ではなくプロセス内で読むのは、Console 以外の起動経路 — MCP の create_session、
// 定時実行、停止セッションの再起動、fork/recreate — にも同じ既定を効かせるため。ここを
// Console 側だけの解決にすると「設定でオフにしたのに、定時実行で立ったセッションだけ
// bypass で走る」が起きる。
func skipPermissionsPref(kind string) (bool, bool) {
	k := sessionx.NormalizeKind(kind)
	// 承認待ちを Console から答えられない kind（codex / opencode）は選択の対象外。
	// 古い/壊れた prefs がその kind に false を書いていても、ここで落として従来どおり
	// bypass で起動する — 答えようのない承認ダイアログで固まるより確実に良い。
	if !sessionx.AgentOf(k).Caps().PermissionChoice {
		return false, false
	}
	defs, ok := uiprefs.Read()["agentLaunchDefaults"].(map[string]any)
	if !ok {
		return false, false
	}
	row, ok := defs[k].(map[string]any)
	if !ok {
		return false, false
	}
	v, ok := row["skipPermissions"].(bool)
	return v, ok
}

// internal/agents は main の設定ファイルを自分で読まない方針なので、opencode.UsagePref /
// mcpreg.PeerMessagingEnabled と同じくフックで渡す。
func init() { agents.SkipPermissionsPref = skipPermissionsPref }

// agentOrderPref normalizes a stored priority list into a TOTAL order: unknown kinds
// and dupes are dropped, and kinds missing from the stored list are appended in the
// built-in default order — so a partial or stale list (e.g. written before a new
// backend existed) still ranks every backend. Read live per call (like
// chatOutputLanguage) so a change applies to the next conversation / one-shot without
// a restart.
//
// keys は先勝ちのフォールバック鎖（新キー → 旧キー）。ここが 1 本のリストではなく鎖に
// なっているのは、チャットと AI 補助生成で優先順位を分けたとき（docs/log/84）に、旧
// 単一リストしか持たない prefs から補助生成側を引き継ぐため。
func agentOrderPref(keys ...string) []string {
	prefs := uiprefs.Read()
	out := make([]string, 0, len(chatx.DefaultHeadlessOrder))
	seen := map[string]bool{}
	add := func(k string) {
		if _, ok := chatx.ChatProviders[k]; ok && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	stored := false
	for _, key := range keys {
		raw, ok := prefs[key].([]any)
		if !ok {
			continue
		}
		for _, v := range raw {
			if s, ok := v.(string); ok {
				add(s)
			}
		}
		stored = true
		break
	}
	if !stored {
		// legacy pin ("auto" is not a kind, so it falls through to the default)
		if pin, _ := prefs["assistantAgent"].(string); pin != "" {
			add(pin)
		}
	}
	for _, k := range chatx.DefaultHeadlessOrder {
		add(k)
	}
	return out
}

// assistantAgentOrderPref is the priority for assistant-CHAT conversations
// (AssistantTab 並べ替え UI, ui-prefs assistantAgentOrder).
func assistantAgentOrderPref() []string { return agentOrderPref("assistantAgentOrder") }

// aiAssistOrderPref is the priority for the AI 補助生成 one-shots — titles, branch
// names, reply chips, edit suggestions, plan updates (設定 > AI補助, ui-prefs
// aiAssistOrder). Separate from the chat because the two want opposite things: the
// chat wants the strongest CLI, these run constantly and want the cheapest that
// works. Falls back to assistantAgentOrder, which is where both used to live.
func aiAssistOrderPref() []string { return agentOrderPref("aiAssistOrder", "assistantAgentOrder") }

// assistantModelPref returns a per-backend model selected in AssistantTab. The
// boolean distinguishes a missing (pre-feature) map from an explicit empty value:
// empty means "let this CLI choose its default", while missing keeps the historical
// backend-specific defaults.
func assistantModelPref(key, kind string) (string, bool) {
	raw, ok := uiprefs.Read()[key].(map[string]any)
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
	if v != chatx.AssistantRecommendedModel && sessionx.ModelHidden(kind, v) {
		return "", false
	}
	return v, true
}

func assistantChatModelPref(kind string) (string, bool) {
	return assistantModelPref("assistantModels", kind)
}

// aiShortModelPref / aiProseModelPref — AI 補助生成のモデルを用途で 2 系統に分けたもの
// （設定 > AI補助・docs/log/84）。short は短いラベル（タイトル・ブランチ名・返信候補）、
// prose は人が読んで採用する文章（ファイル編集の提案・計画の更新）。
//
// 分ける前は assistantUtilityModels 1 つが両方を兼ねており、「タイトル・サジェストの
// モデル」という名前のまま prose 側の既定（sonnet 級）まで置き換えていた。**両方**が
// その旧キーを引き継ぐ — 旧キーは実際に両方へ効いていたので、これが分割時点の挙動を
// そのまま持ち越す初期値になる。分けた意味は、この先それぞれ独立に変えられること。
func aiShortModelPref(kind string) (string, bool) {
	return aiModelPref("aiShortModels", kind)
}

func aiProseModelPref(kind string) (string, bool) {
	return aiModelPref("aiProseModels", kind)
}

func aiModelPref(key, kind string) (string, bool) {
	if v, ok := assistantModelPref(key, kind); ok {
		return v, true
	}
	return assistantModelPref("assistantUtilityModels", kind)
}

// chatAutoTurnLimit is the per-conversation ceiling on unattended auto turns
// (docs/log/30, 設定 > アシスタント「自動応答の上限回数」). Missing/invalid ⇒
// defaultAutoTurns; always clamped to [1, maxAutoTurnLimit] — there is no
// unlimited mode, the clamp is the runaway stop.
func chatAutoTurnLimit() int {
	v, ok := uiprefs.Read()["assistantAutoTurnLimit"].(float64)
	if !ok {
		return chatx.DefaultAutoTurns
	}
	n := int(v)
	if n < 1 {
		return 1
	}
	if n > chatx.MaxAutoTurnLimit {
		return chatx.MaxAutoTurnLimit
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
	v, _ := uiprefs.Read()["assistantAutoTurnModel"].(string)
	// claude 専用の設定なので claude の除外リストで判定する。除外されていれば空＝
	// 会話のモデルのまま（model_deny.go）。
	return sessionx.VisibleModel(session.KindClaude, strings.TrimSpace(v))
}

func handleGetUIPrefs(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, uiprefs.Read())
}

func handlePutUIPrefs(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, uiprefs.MaxBytes+1))
	if err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "read failed")
		return
	}
	if len(body) > uiprefs.MaxBytes {
		httpx.WriteErr(w, http.StatusRequestEntityTooLarge, "too_large", "prefs too large")
		return
	}
	// Validate it's a JSON object before persisting (the Console owns the shape).
	var obj map[string]any
	if json.Unmarshal(body, &obj) != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_json", "body must be a JSON object")
		return
	}
	if err := os.MkdirAll(filepath.Dir(uiprefs.Path()), 0o700); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "mkdir_failed", err.Error())
		return
	}
	// 累積データが痩せる書き込みだけ、直前の版を退避してから置き換える。毎回退避しないのは
	// 「復元したい版」が最後の 1 回で流れてしまうから — 事故の直前を残すことに意味がある。
	if lost := uiprefs.ShrunkKeys(uiprefs.Read(), obj); len(lost) > 0 {
		if old, err := os.ReadFile(uiprefs.Path()); err == nil && len(old) > 0 {
			if err := os.WriteFile(uiprefs.BackupPath(), old, 0o600); err != nil {
				log.Printf("ui-prefs: backup before shrinking write failed: %v", err)
			}
		}
		log.Printf("ui-prefs: incoming prefs drop accumulated keys %v (previous copy kept at %s)",
			lost, uiprefs.BackupPath())
	}
	before := uiprefs.OpencodeCatalog()
	peerBefore := uiprefs.PeerMessaging()
	if err := os.WriteFile(uiprefs.Path(), body, 0o600); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
		return
	}
	// セッション間メッセージ（docs/log/58）の ON/OFF は、セッション側 af サーバーの起動引数
	// （--peer-messaging）を変える。各 CLI のネイティブ MCP 設定は materialize 時に書かれる
	// ので、ここで書き直さないと「トグルしたのに何も起きない」になる。既に起動している
	// セッションは自分の設定を読み込み済みなので、効くのは次に起動するセッションから
	// （UI の説明文もそう書いてある）。
	if uiprefs.PeerMessaging() != peerBefore {
		mcpx.MaterializeAll()
	}
	// 枠の切替は注入する env を変える（無料枠は OPENCODE_API_KEY を落とす）。鍵の
	// 変更と同じ扱いで、動いている serve を作り直さないと古い環境のまま残る。
	if after := uiprefs.OpencodeCatalog(); after != before {
		opencode.ApplyUsageChange(before + " → " + after)
	}
	httpx.WriteJSON(w, http.StatusOK, obj)
}
