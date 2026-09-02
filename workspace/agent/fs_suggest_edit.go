package main

// エディタの AI 変更提案（docs/log/44 Phase 4）。Console のファイルペインが選択範囲と
// 指示文を送り、置換文と要約を JSON で返す。生成はタイトル/返信サジェストと同じ
// backend-agnostic な一発ヘッドレス（oneShotHeadless）＝ read-only 提案生成チャネル
// （docs/log/44 §1.3: claude --tools "" / codex tool 無付与 / opencode OPENCODE_CONFIG deny /
// cursor --mode ask）。このハンドラはディスクを一切読まない — 本文は編集バッファの
// スナップショットとしてリクエストに載って来る（dirty な本文への提案を可能にするため。
// docs/log/44 §1.3「dirtyな本文から提案を作る場合」）。range・revision・identity の検証は
// すべて Console 側の適用境界（suggestion_stale）が持ち、サーバーは置換文の生成だけを行う。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

const (
	// editSuggestTimeout: 置換文の生成はタイトル1行より長い出力になり得るので、
	// 既存の 60 秒勢（title/reply）よりだけ広く取る。Console 側のタイムアウトは
	// これより広い（editor/api.ts SUGGEST_EDIT_TIMEOUT_MS）。
	editSuggestTimeout = 90 * time.Second
	// editSuggestMaxBody: wire body の上限。選択・前後文脈の各上限より十分広く、
	// PUT /fs/file の 16 MiB とは独立した提案専用の上限。
	editSuggestMaxBody = 2 * 1024 * 1024
	// editSuggestMaxSelection: 置換対象としてプロンプトへ渡す選択の上限（UTF-8 bytes）。
	// LLM に全文リライトさせる用途は Phase 4 の範囲外なので、コンテキスト費用を抑える。
	editSuggestMaxSelection = 256 * 1024
	// editSuggestMaxContext: 選択の前後に付ける文脈の各上限（UTF-8 bytes）。
	// Console 側も同じ値で切り出す（editor/suggest.ts）。
	editSuggestMaxContext = 16 * 1024
	// editSuggestMaxInstruction: 指示文の上限（UTF-8 bytes）。
	editSuggestMaxInstruction = 4 * 1024
	// editSuggestMaxSummary: EditSuggestion.summary の上限（UTF-8 bytes、docs/log/44 §4.2）。
	editSuggestMaxSummary = 240
)

// editSuggestPersona: 出力は JSON 1 オブジェクトのみ。置換文は「選択範囲をそのまま
// 差し替える」契約（docs/log/44 §4.2 — 自動変換・trim・全体再生成をしない）なので、
// 前後文脈は参考であり出力に含めないことを明示する。
const editSuggestPersona = "あなたはコード/文書エディタの変更提案を作る専用ツールです。" +
	"ファイルの抜粋（選択範囲とその前後の文脈）と指示を受け取り、選択範囲を置き換える新しいテキストを作ります。" +
	"出力は必ず {\"summary\":\"…\",\"replacement\":\"…\"} の JSON オブジェクト1個だけ。" +
	"コードフェンス・説明・前置きは一切付けない。" +
	"replacement は選択範囲だけをそのまま差し替える本文で、前後の文脈を含めない。" +
	"元のインデント・字下げ・改行スタイルを維持し、改行は LF のみ（CR を含めない）。" +
	"選択範囲が空の場合はその位置に挿入する本文を作る。" +
	"summary は変更内容の短い要約（40文字以内）で、指示と同じ言語で書く。"

// editSuggestModel: 置換文の生成は分類系（タイトル/返信候補）より品質感度が高いので、
// 既定は haiku ではなく sonnet。deployment 単位で上書き可。claude backend のみに効き、
// 他 backend は AssistantTab のユーティリティモデル設定に従う（oneShotHeadless）。
func editSuggestModel() string { return envOr("AF_EDIT_SUGGEST_MODEL", "sonnet") }

// editSuggestRequest は Console が送る提案リクエスト。before/selection/after は
// 編集バッファ（LF-only）からの切り出しで、path は表示・文脈用のメタデータ。
// このハンドラは fs を触らないため path の解決・denylist 判定は行わない。
type editSuggestRequest struct {
	Path        string `json:"path"`
	Instruction string `json:"instruction"`
	Before      string `json:"before"`
	Selection   string `json:"selection"`
	After       string `json:"after"`
}

// validate は入力の形だけを確認する（fs 解決なし）。バッファ由来のフィールドに
// CR/NUL が混じるのはクライアント実装バグなので bad_request に丸める。
func (req *editSuggestRequest) validate() error {
	if strings.TrimSpace(req.Path) == "" || len(req.Path) > 4096 {
		return errors.New("path is required")
	}
	if strings.TrimSpace(req.Instruction) == "" {
		return errors.New("instruction is required")
	}
	if len(req.Instruction) > editSuggestMaxInstruction {
		return errors.New("instruction is too long")
	}
	if len(req.Selection) > editSuggestMaxSelection {
		return errors.New("selection is too long")
	}
	if len(req.Before) > editSuggestMaxContext || len(req.After) > editSuggestMaxContext {
		return errors.New("context window is too long")
	}
	for _, f := range []string{req.Path, req.Instruction, req.Before, req.Selection, req.After} {
		if strings.ContainsAny(f, "\x00\r") {
			return errors.New("fields must be LF-only text without NUL")
		}
	}
	return nil
}

func editSuggestPrompt(req *editSuggestRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "対象ファイル: %s\n\n", req.Path)
	if req.Before != "" {
		b.WriteString("--- 選択範囲の前の文脈（参考。出力に含めない） ---\n")
		b.WriteString(req.Before)
		b.WriteString("\n")
	}
	if req.Selection == "" {
		b.WriteString("--- 選択範囲（空 = この位置への挿入） ---\n")
	} else {
		b.WriteString("--- 選択範囲（これを置き換える） ---\n")
		b.WriteString(req.Selection)
		b.WriteString("\n")
	}
	if req.After != "" {
		b.WriteString("--- 選択範囲の後の文脈（参考。出力に含めない） ---\n")
		b.WriteString(req.After)
		b.WriteString("\n")
	}
	b.WriteString("\n--- 指示 ---\n")
	b.WriteString(req.Instruction)
	b.WriteString("\n\n{\"summary\":\"…\",\"replacement\":\"…\"} の JSON 1個だけを出力してください。")
	return b.String()
}

// editSuggestResult は LLM 応答から取り出す提案本体。
type editSuggestResult struct {
	Summary     string  `json:"summary"`
	Replacement *string `json:"replacement"`
}

// extractEditSuggestJSON は LLM の応答テキストから提案 JSON を取り出す。指示に反して
// コードフェンスや前置きが付くことがある（parseCursorResult と同じ現実）ので、
// (1) 全体、(2) フェンス内、(3) 最初の '{' から最後の '}' まで、の順に試す。
func extractEditSuggestJSON(reply string) (editSuggestResult, error) {
	try := func(s string) (editSuggestResult, bool) {
		var r editSuggestResult
		if json.Unmarshal([]byte(s), &r) == nil && r.Replacement != nil {
			return r, true
		}
		return editSuggestResult{}, false
	}
	trimmed := strings.TrimSpace(reply)
	if r, ok := try(trimmed); ok {
		return r, nil
	}
	for _, block := range fencedBlocks(trimmed) {
		if r, ok := try(block); ok {
			return r, nil
		}
	}
	if i, j := strings.Index(trimmed, "{"), strings.LastIndex(trimmed, "}"); i >= 0 && j > i {
		if r, ok := try(trimmed[i : j+1]); ok {
			return r, nil
		}
	}
	return editSuggestResult{}, errors.New("no suggestion JSON in reply")
}

// fencedBlocks は ``` で囲まれた各ブロックの中身を返す（言語タグは無視）。
func fencedBlocks(s string) []string {
	var out []string
	parts := strings.Split(s, "```")
	// parts[1], parts[3], … がフェンス内。先頭行の言語タグを剥がす。
	for i := 1; i < len(parts); i += 2 {
		body := parts[i]
		if nl := strings.IndexByte(body, '\n'); nl >= 0 {
			out = append(out, strings.TrimSpace(body[nl+1:]))
		}
	}
	return out
}

// clampUTF8Bytes は s を UTF-8 のまま最大 n bytes に切り詰める（rune 境界で切る）。
func clampUTF8Bytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// cleanEditSuggestion は LLM 出力を docs/log/44 §4.2 の契約へ整える。replacement は
// 一切変換しない（CR 混入は変換ではなく生成失敗として拒否）。summary は表示用
// メタデータなので、改行の除去と 240 bytes への切り詰め、空時の指示文フォールバック
// だけを行う。
func cleanEditSuggestion(r editSuggestResult, instruction string) (summary, replacement string, err error) {
	replacement = *r.Replacement
	if strings.ContainsAny(replacement, "\x00\r") {
		return "", "", errors.New("replacement contains CR or NUL")
	}
	summary = strings.Join(strings.Fields(r.Summary), " ")
	if summary == "" {
		summary = strings.Join(strings.Fields(instruction), " ")
	}
	summary = strings.TrimSpace(clampUTF8Bytes(summary, editSuggestMaxSummary))
	if summary == "" {
		return "", "", errors.New("empty summary")
	}
	return summary, replacement, nil
}

// editSuggestLLM はテストで差し替える生成シーム。
var editSuggestLLM = func(ctx context.Context, req *editSuggestRequest) (string, error) {
	return oneShotHeadless(ctx, editSuggestPersona, editSuggestPrompt(req), editSuggestModel())
}

// handleFSSuggestEdit — POST /fs/suggest-edit（docs/log/44 Phase 4）。
// 応答は {"summary":…,"replacement":…} のみ。envelope（paneId/requestId/sourceRevision）
// は Console がリクエスト時に控えて応答へ合成するため、wire には載せない。
func handleFSSuggestEdit(w http.ResponseWriter, r *http.Request) {
	var req editSuggestRequest
	if serr := httpx.DecodeStrictJSON(r, &req, editSuggestMaxBody); serr != nil {
		httpx.WriteErr(w, serr.Status, serr.Code, serr.Message)
		return
	}
	if err := req.validate(); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeFSBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), editSuggestTimeout)
	defer cancel()
	ctx = usagex.WithTag(ctx, usagex.Tag{Feature: usagex.FeatureSuggestEdit, Trigger: usagex.TriggerManual, Ref: req.Path})
	reply, err := editSuggestLLM(ctx, &req)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "generation_failed", "edit suggestion failed")
		return
	}
	parsed, err := extractEditSuggestJSON(reply)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "generation_failed", "edit suggestion returned no usable JSON")
		return
	}
	summary, replacement, err := cleanEditSuggestion(parsed, req.Instruction)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "generation_failed", "edit suggestion output rejected")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"summary": summary, "replacement": replacement})
}
