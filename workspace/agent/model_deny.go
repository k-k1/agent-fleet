package main

// 使わないモデル（ui-prefs hiddenModels、設定 > エージェント > 各カード > 動作設定）。
//
// 動機は課金事故の予防: Claude の Team プランでは Fable が API クレジット扱いになる —
// ピッカーに並んでいる限り「うっかり選ぶ」も「アシスタントが list_models から選ぶ」も
// 起こり得る。そこで kind ごとに除外リストを持ち、
//
//   ①一覧から消す — handleAgentModels（Console のピッカーと MCP list_models の合流点）
//   ②起動を断る   — create_session / セッション設定変更 / アシスタントのモデル解決
//
// の二段で止める。①だけだと明示的に id を書く経路（定時実行のモデル欄、ユーザー定義
// アシスタント、MCP の直指定）が素通りするので、②が本体で①は導線。
//
// これは「選択の事故防止」であって課金ガードではない: TUI 内で /model を打つ・CLI 自身の
// 設定でモデルを変える経路までは塞がない（エージェントの内部状態は触らない方針）。組織
// 単位で本当に禁じるならプラン提供元側の設定が本丸。
//
// 除外は kind スコープ。モデル id の名前空間が kind ごとに別（fable は claude、
// gpt-5.6-… は codex）で、同じ文字列が別の kind で別物を指し得るため。

import (
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

// normModelToken は id/別名を突き合わせ用の正規形にする。区切り（/ . _ 空白）を
// ハイフンへ寄せて小文字化するので、"claude-fable-5" と "fable" のような
// 「別名 ⊂ 完全 id」の関係をトークン境界つきで判定できる。
func normModelToken(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer("/", "-", "_", "-", ".", "-", " ", "-", ":", "-").Replace(s)
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-")
}

// modelMatchesHidden は要求モデルが除外エントリに当たるかを判定する。
//
// 基本は完全一致。加えて「別名を隠したら、その別名を含む完全 id も隠す」を見る必要が
// ある — claude は --model に別名（fable）でも完全 id（claude-fable-5）でも渡せるので、
// 別名だけ除外しても完全 id で抜けられては意味がない。
//
// ただしこの含意一致は「除外エントリが単一トークンのとき」に限る。具体的なカタログ id
// （gpt-5-4 のように複数トークン）にまで広げると、それを接頭辞に持つ別モデル
// （gpt-5-4-mini）を巻き添えにする — 実際に GPT-5.4 を隠すと mini まで消えた。
// 単一トークンは「族の名前」（fable / opus / sonnet / haiku）、複数トークンは
// 「1つの具体的なモデル」という区別。
func modelMatchesHidden(requested, hidden string) bool {
	r, h := normModelToken(requested), normModelToken(hidden)
	if r == "" || h == "" {
		return false
	}
	if r == h {
		return true
	}
	if strings.Contains(h, "-") {
		return false // 具体 id を隠しただけ — 前方一致する別モデルは別物
	}
	return strings.Contains("-"+r+"-", "-"+h+"-")
}

// hiddenModelsFor は kind に対する実効の除外リストを返す。ui-prefs は Console が所有する
// 不透明 JSON なので、型が違う／壊れている場合は「除外なし」に落とす。
//
// claude だけフェイルセーフを持つ: 固定4ティアを全部除外すると起動できるモデルが
// 無くなる（claude のピッカーには「既定」の選択肢が無い — settings.ts の意図的な設計）。
// 全部隠すような壊れた設定は無視して素の一覧に戻す。ライブカタログの kind は
// 「空カタログ＝既定で起動」が元から正常状態なので、この保護は要らない。
func hiddenModelsFor(kind string) []string {
	raw, ok := uiprefs.Read()["hiddenModels"].(map[string]any)
	if !ok {
		return nil
	}
	list, ok := raw[kind].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	if kind == "claude" {
		all := true
		for _, c := range claude.Models() {
			if !modelHiddenIn(out, c.ID) {
				all = false
				break
			}
		}
		if all {
			return nil
		}
	}
	return out
}

// modelHiddenIn は既に解決済みの除外リストに対する判定（hiddenModelsFor を
// 何度も読み直さずに済むよう分けてある）。
func modelHiddenIn(hidden []string, requested string) bool {
	for _, h := range hidden {
		if modelMatchesHidden(requested, h) {
			return true
		}
	}
	return false
}

// modelHidden は kind の設定に照らして requested が除外されているか。空文字（＝CLI の
// 既定に委ねる）は常に許可 — 何も選んでいないのだから塞ぐ対象がない。
func modelHidden(kind, requested string) bool {
	if strings.TrimSpace(requested) == "" {
		return false
	}
	return modelHiddenIn(hiddenModelsFor(kind), requested)
}

// hiddenModelError は起動ガードが返す文言。Console の利用者とアシスタント（LLM）の
// 両方が読むので、原因（設定で除外）と回復手段（設定を解除するか別モデル）を書く。
func hiddenModelError(requested string) string {
	return "モデル " + requested + " は設定「使わないモデル」で除外されています。" +
		"別のモデルを指定するか、設定 > エージェント > 動作設定 で除外を解除してください。"
}

// filterVisibleModels はカタログから除外モデルを落とす。handleAgentModels 経由なので
// Console のピッカーと MCP list_models の両方に同じ結果が出る。
func filterVisibleModels(kind string, list []agents.ModelChoice) []agents.ModelChoice {
	hidden := hiddenModelsFor(kind)
	if len(hidden) == 0 {
		return list
	}
	out := make([]agents.ModelChoice, 0, len(list))
	for _, c := range list {
		if modelHiddenIn(hidden, c.ID) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// visibleModel は固定の推奨値（アシスタントの "sonnet" / "haiku" / agy の既定名）を
// 除外設定に通す。除外されていれば "" を返し、呼び出し側は CLI 自身の既定へ委ねる。
func visibleModel(kind, model string) string {
	if modelHidden(kind, model) {
		return ""
	}
	return model
}

// visibleModelIDs は id だけのカタログ（opencode.Models() など）用。アシスタントの
// 「推奨モデル」自動選択がここを通るので、除外したモデルが自動で選ばれることはない。
func visibleModelIDs(kind string, ids []string) []string {
	hidden := hiddenModelsFor(kind)
	if len(hidden) == 0 {
		return ids
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if modelHiddenIn(hidden, id) {
			continue
		}
		out = append(out, id)
	}
	return out
}
