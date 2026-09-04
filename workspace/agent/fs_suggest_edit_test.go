package main

// Unit tests for the editor's AI edit suggestion (docs/log/44 Phase 4). The LLM is swapped out at
// the editSuggestLLM seam, pinning input validation, JSON extraction, cleanup (summary clamp, CR
// rejection) and the handler's error classification without a real process.

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func validSuggestReq() *editSuggestRequest {
	return &editSuggestRequest{
		Path:        "repos/example/README.md",
		Instruction: "見出しを具体化して",
		Before:      "# Example\n",
		Selection:   "## overview\n",
		After:       "body\n",
	}
}

func TestEditSuggestValidate(t *testing.T) {
	if err := validSuggestReq().validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*editSuggestRequest)
	}{
		{"empty path", func(r *editSuggestRequest) { r.Path = " " }},
		{"long path", func(r *editSuggestRequest) { r.Path = strings.Repeat("a", 4097) }},
		{"empty instruction", func(r *editSuggestRequest) { r.Instruction = "\n " }},
		{"long instruction", func(r *editSuggestRequest) { r.Instruction = strings.Repeat("x", editSuggestMaxInstruction+1) }},
		{"long selection", func(r *editSuggestRequest) { r.Selection = strings.Repeat("x", editSuggestMaxSelection+1) }},
		{"long before", func(r *editSuggestRequest) { r.Before = strings.Repeat("x", editSuggestMaxContext+1) }},
		{"long after", func(r *editSuggestRequest) { r.After = strings.Repeat("x", editSuggestMaxContext+1) }},
		{"CR in selection", func(r *editSuggestRequest) { r.Selection = "a\r\nb" }},
		{"NUL in before", func(r *editSuggestRequest) { r.Before = "a\x00b" }},
	}
	for _, tc := range cases {
		req := validSuggestReq()
		tc.mutate(req)
		if err := req.validate(); err == nil {
			t.Errorf("%s: want error, got nil", tc.name)
		}
	}
	// An empty selection (an insertion) and empty context are allowed.
	req := validSuggestReq()
	req.Selection, req.Before, req.After = "", "", ""
	if err := req.validate(); err != nil {
		t.Fatalf("insertion request rejected: %v", err)
	}
}

func TestExtractEditSuggestJSON(t *testing.T) {
	want := editSuggestResult{Summary: "s", Replacement: strPtr("r\n")}
	cases := []string{
		`{"summary":"s","replacement":"r\n"}`,
		"前置きの説明。\n```json\n{\"summary\":\"s\",\"replacement\":\"r\\n\"}\n```\n後書き。",
		"```\n{\"summary\":\"s\",\"replacement\":\"r\\n\"}\n```",
		"answer: {\"summary\":\"s\",\"replacement\":\"r\\n\"} done",
	}
	for i, in := range cases {
		got, err := extractEditSuggestJSON(in)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if got.Summary != want.Summary || *got.Replacement != *want.Replacement {
			t.Fatalf("case %d: got %+v", i, got)
		}
	}
	for i, in := range []string{"", "ただのテキスト", `{"summary":"s"}`, "{broken"} {
		if _, err := extractEditSuggestJSON(in); err == nil {
			t.Fatalf("bad case %d: want error", i)
		}
	}
}

func strPtr(s string) *string { return &s }

func TestCleanEditSuggestion(t *testing.T) {
	// An empty replacement (a deletion suggestion) passes through unchanged, as does anything
	// without CR/NUL.
	sum, rep, err := cleanEditSuggestion(editSuggestResult{Summary: " 見出し\nを直す ", Replacement: strPtr("")}, "instr")
	if err != nil || rep != "" || sum != "見出し を直す" {
		t.Fatalf("got %q %q %v", sum, rep, err)
	}
	// An empty summary falls back to the instruction text.
	sum, _, err = cleanEditSuggestion(editSuggestResult{Replacement: strPtr("x")}, "  直して  ")
	if err != nil || sum != "直して" {
		t.Fatalf("fallback summary: %q %v", sum, err)
	}
	// Anything over 240 bytes is truncated on a rune boundary.
	long := strings.Repeat("あ", 100) // 300 bytes
	sum, _, err = cleanEditSuggestion(editSuggestResult{Summary: long, Replacement: strPtr("x")}, "i")
	if err != nil || len(sum) > editSuggestMaxSummary || !strings.HasPrefix(long, sum) {
		t.Fatalf("clamp: len=%d err=%v", len(sum), err)
	}
	// A replacement containing CR or NUL is rejected, not converted (docs/log/44 §4.2: never
	// convert silently).
	for _, bad := range []string{"a\r\nb", "a\x00b"} {
		if _, _, err := cleanEditSuggestion(editSuggestResult{Summary: "s", Replacement: strPtr(bad)}, "i"); err == nil {
			t.Fatalf("replacement %q: want error", bad)
		}
	}
}

func suggestEditDo(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/fs/suggest-edit", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handleFSSuggestEdit(w, r)
	return w
}

func TestHandleFSSuggestEdit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	orig := editSuggestLLM
	t.Cleanup(func() { editSuggestLLM = orig })

	// Success: summary/replacement come back from the LLM response. Also pins that the prompt
	// carries the selection and the instruction.
	var gotPrompt string
	editSuggestLLM = func(_ context.Context, req *editSuggestRequest) (string, error) {
		gotPrompt = editSuggestPrompt(req)
		return "```json\n{\"summary\":\"改善\",\"replacement\":\"## 保存競合の扱い\\n\"}\n```", nil
	}
	w := suggestEditDo(t, `{"path":"repos/a.md","instruction":"直して","before":"","selection":"## old\n","after":""}`)
	if w.Code != http.StatusOK {
		t.Fatalf("ok case: %d %s", w.Code, w.Body.String())
	}
	var res struct{ Summary, Replacement string }
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || res.Summary != "改善" || res.Replacement != "## 保存競合の扱い\n" {
		t.Fatalf("response: %s (%v)", w.Body.String(), err)
	}
	if !strings.Contains(gotPrompt, "## old") || !strings.Contains(gotPrompt, "直して") {
		t.Fatalf("prompt missing pieces: %q", gotPrompt)
	}

	// Invalid input never reaches the LLM and is a 400 bad_request.
	editSuggestLLM = func(context.Context, *editSuggestRequest) (string, error) {
		t.Fatal("LLM must not run for invalid input")
		return "", nil
	}
	for _, body := range []string{
		`{"path":"","instruction":"x","before":"","selection":"","after":""}`,
		`{"path":"a","instruction":"","before":"","selection":"","after":""}`,
		`{"path":"a","instruction":"x","before":"","selection":"a\r\n","after":""}`,
		`{"path":"a","instruction":"x","unknown":true}`,
		`not json`,
	} {
		if w := suggestEditDo(t, body); w.Code != http.StatusBadRequest {
			t.Fatalf("body %q: want 400 got %d %s", body, w.Code, w.Body.String())
		}
	}

	// A generation failure or a malformed response is a 500 generation_failed.
	for _, llm := range []func(context.Context, *editSuggestRequest) (string, error){
		func(context.Context, *editSuggestRequest) (string, error) { return "", errors.New("boom") },
		func(context.Context, *editSuggestRequest) (string, error) { return "no json here", nil },
		func(context.Context, *editSuggestRequest) (string, error) {
			return `{"summary":"s","replacement":"a\r\nb"}`, nil
		},
	} {
		editSuggestLLM = llm
		w := suggestEditDo(t, `{"path":"a","instruction":"x","before":"","selection":"","after":""}`)
		if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "generation_failed") {
			t.Fatalf("failure case: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestFSSuggestEditRouteRegistered(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/fs/suggest-edit", nil)
	_, pattern := buildMux().Handler(req)
	if pattern != "POST /fs/suggest-edit" {
		t.Fatalf("route pattern=%q", pattern)
	}
}

// The read-only stance of the opencode one-shot (docs/log/44 §1.3): pins that the generated
// OPENCODE_CONFIG carries the same deny policy as chat.
func TestOpencodeOneShotConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := chatx.OpencodeOneShotConfig()
	if p == "" {
		t.Fatal("config path empty")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg struct {
		Permission map[string]string `json:"permission"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if cfg.Permission["edit"] != "deny" || cfg.Permission["bash"] != "deny" {
		t.Fatalf("policy not carried: %s", b)
	}
}
