package main

// AI edit suggestions for the editor (docs/log/44 Phase 4). The Console's file pane sends a
// selection and an instruction; this returns the replacement text and a summary as JSON.
// Generation goes through the same backend-agnostic one-shot headless channel as title and
// reply suggestions (oneShotHeadless), which is read-only by construction (docs/log/44 §1.3:
// claude --tools "" / codex with no tools / opencode OPENCODE_CONFIG deny / cursor
// --mode ask). This handler never reads disk: the text arrives in the request as a snapshot
// of the edit buffer, so a suggestion can be made against dirty content (docs/log/44 §1.3).
// Range, revision and identity checks all live at the Console's apply boundary
// (suggestion_stale); the server only generates the replacement.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

const (
	// editSuggestTimeout: a replacement can be a much longer output than a one-line title,
	// so this is a little wider than the existing 60-second pair (title/reply). The Console
	// timeout is wider still (editor/api.ts SUGGEST_EDIT_TIMEOUT_MS).
	editSuggestTimeout = 90 * time.Second
	// editSuggestMaxBody: wire body limit, comfortably above the selection and context
	// limits below and deliberately independent of PUT /fs/file's 16 MiB.
	editSuggestMaxBody = 2 * 1024 * 1024
	// editSuggestMaxSelection: limit on the selection handed to the prompt (UTF-8 bytes).
	// Rewriting a whole file through the LLM is out of scope for Phase 4, and this keeps
	// the context cost down.
	editSuggestMaxSelection = 256 * 1024
	// editSuggestMaxContext: limit on each side of the context around the selection (UTF-8
	// bytes). The Console slices with the same value (editor/suggest.ts).
	editSuggestMaxContext = 16 * 1024
	// editSuggestMaxInstruction: limit on the instruction (UTF-8 bytes).
	editSuggestMaxInstruction = 4 * 1024
	// editSuggestMaxSummary: limit on EditSuggestion.summary (UTF-8 bytes, docs/log/44 §4.2).
	editSuggestMaxSummary = 240
)

// editSuggestPersona constrains the output to a single JSON object. The replacement
// replaces the selection verbatim (docs/log/44 §4.2 — no automatic conversion, no trim, no
// whole-file regeneration), so the persona states that the surrounding context is reference
// material and must not appear in the output.
const editSuggestPersona = "あなたはコード/文書エディタの変更提案を作る専用ツールです。" +
	"ファイルの抜粋（選択範囲とその前後の文脈）と指示を受け取り、選択範囲を置き換える新しいテキストを作ります。" +
	"出力は必ず {\"summary\":\"…\",\"replacement\":\"…\"} の JSON オブジェクト1個だけ。" +
	"コードフェンス・説明・前置きは一切付けない。" +
	"replacement は選択範囲だけをそのまま差し替える本文で、前後の文脈を含めない。" +
	"元のインデント・字下げ・改行スタイルを維持し、改行は LF のみ（CR を含めない）。" +
	"選択範囲が空の場合はその位置に挿入する本文を作る。" +
	"summary は変更内容の短い要約（40文字以内）で、指示と同じ言語で書く。"

// editSuggestModel: generating a replacement is more quality-sensitive than the
// classification-shaped features (title, reply candidates), so the default is sonnet rather
// than haiku, overridable per deployment. It applies to the claude backend only; the others
// follow the prose-generation model in Settings > AI assistance (OneShotProse, docs/log/84).
func editSuggestModel() string { return envOr("AF_EDIT_SUGGEST_MODEL", "sonnet") }

// editSuggestRequest is the suggestion request the Console sends. before/selection/after are
// slices of the edit buffer (LF-only) and path is metadata for display and context. This
// handler never touches the fs, so path is neither resolved nor checked against the denylist.
type editSuggestRequest struct {
	Path        string `json:"path"`
	Instruction string `json:"instruction"`
	Before      string `json:"before"`
	Selection   string `json:"selection"`
	After       string `json:"after"`
}

// validate checks the shape of the input only, with no fs resolution. CR or NUL in a
// buffer-derived field is a client bug, so it collapses to bad_request.
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

// editSuggestResult is the suggestion body extracted from the LLM reply.
type editSuggestResult struct {
	Summary     string  `json:"summary"`
	Replacement *string `json:"replacement"`
}

// extractEditSuggestJSON pulls the suggestion JSON out of the LLM reply text. Despite the
// instruction, replies sometimes arrive wrapped in a code fence or behind a preamble (the
// same reality parseCursorResult deals with), so try in order: (1) the whole reply, (2) each
// fenced block, (3) the span from the first '{' to the last '}'.
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

// fencedBlocks returns the body of every ```-delimited block, ignoring the language tag.
func fencedBlocks(s string) []string {
	var out []string
	parts := strings.Split(s, "```")
	// parts[1], parts[3], … are the fenced bodies; strip the language tag on the first line.
	for i := 1; i < len(parts); i += 2 {
		body := parts[i]
		if nl := strings.IndexByte(body, '\n'); nl >= 0 {
			out = append(out, strings.TrimSpace(body[nl+1:]))
		}
	}
	return out
}

// clampUTF8Bytes truncates s to at most n bytes, cutting on a rune boundary so the result
// stays valid UTF-8.
func clampUTF8Bytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// cleanEditSuggestion brings the LLM output into the docs/log/44 §4.2 contract. replacement
// is never transformed: a CR in it is rejected as a failed generation rather than converted.
// summary is display metadata, so it only gets newlines collapsed, a 240-byte clamp, and a
// fallback to the instruction when empty.
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

// editSuggestLLM is the generation seam tests replace.
var editSuggestLLM = func(ctx context.Context, req *editSuggestRequest) (string, error) {
	return chatx.OneShotHeadless(ctx, chatx.OneShotProse, editSuggestPersona, editSuggestPrompt(req), editSuggestModel())
}

// handleFSSuggestEdit — POST /fs/suggest-edit (docs/log/44 Phase 4). The response carries
// only {"summary":…,"replacement":…}: the envelope (paneId/requestId/sourceRevision) stays
// off the wire because the Console holds it from request time and merges it into the reply.
func handleFSSuggestEdit(w http.ResponseWriter, r *http.Request) {
	// Settings > AI assistance > file edit suggestions: this feature can be turned off on
	// its own (docs/log/84).
	if !uiprefs.EditSuggest() {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeTitleFeatureDisabled, "edit suggestion is turned off")
		return
	}
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
