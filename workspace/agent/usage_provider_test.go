package main

// Regression cover for capturing usage at the provider layer (docs/log/46 §3-a, review P2-7 /
// P3-11).
//
// One rule is defended here: every call that was fired leaves a row in the ledger. Reporting 0
// tokens because no modelUsage arrived (stop, abnormal exit), or one call's worth because a
// retry happened, distorts the spend picture.

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// A stop, or an abnormal exit before result, delivers no modelUsage. The snapshot seen on the
// assistant events is kept as partial (never 0 tokens / measured=none).
func TestFallbackTotalsKeepsStoppedTurnTokens(t *testing.T) {
	// 1) No result arrived: take the snapshot as partial.
	stopped := usagex.Call{Kind: session.KindClaude}
	snap := chatx.ClaudeUsage{InputTokens: 12, OutputTokens: 34, CacheReadInputTokens: 5600, CacheCreationInputTokens: 78}
	stopped.FallbackTotals(snap.LedgerTokens(), usagex.MeasuredPartial)
	if stopped.Totals.In != 12 || stopped.Totals.Out != 34 ||
		stopped.Totals.CacheRead != 5600 || stopped.Totals.CacheCreate != 78 {
		t.Fatalf("tokens from the stopped turn were dropped: %+v", stopped.Totals)
	}
	if got := stopped.MeasuredOr(stopped.Totals); got != usagex.MeasuredPartial {
		t.Fatalf("measured = %q, want partial (a mid-turn snapshot is not exact)", got)
	}

	// 2) A call that did get modelUsage is left alone (the per-model breakdown is the truer one).
	full := usagex.Call{Kind: session.KindClaude, Models: chatx.UsageModelRows(map[string]chatx.ClaudeModelUsage{
		"claude-haiku-4-5-20251001": {InputTokens: 1, OutputTokens: 2, CanonicalModel: "claude-haiku-4-5"},
	})}
	full.FallbackTotals(snap.LedgerTokens(), usagex.MeasuredPartial)
	if full.Totals.Any() || full.Measured != "" {
		t.Fatalf("the fallback overwrote a call that has modelUsage: %+v", full)
	}

	// 3) A call with nothing captured stays none (never pass 0 off as "spent nothing").
	empty := usagex.Call{Kind: session.KindClaude}
	empty.FallbackTotals(chatx.ClaudeUsage{}.LedgerTokens(), usagex.MeasuredPartial)
	if empty.Measured != "" || empty.MeasuredOr(empty.Totals) != usagex.MeasuredNone {
		t.Fatalf("claimed partial with not a single token: %+v", empty)
	}
}

// Fire claude for real in the shape "dies before emitting result" and check the spend lands in
// the ledger. Only a stub CLI at the head of PATH, so no auth and no billing (the real CLI is
// exercised by the live tests).
func TestClaudeStreamRecordsTokensWhenKilledBeforeResult(t *testing.T) {
	useIsolatedUsageDir(t)
	bin := t.TempDir()
	// Emit one assistant event, then die without a result (the shape of a user stop or an OOM).
	stub := "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' '" +
		`{"type":"assistant","message":{"model":"claude-haiku-4-5","usage":` +
		`{"input_tokens":12,"output_tokens":34,"cache_read_input_tokens":5600,"cache_creation_input_tokens":78}}}` +
		"'\nexit 143\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	c := &chatx.ChatConversation{ID: "conv1", Model: "haiku"}
	ctx := usagex.WithTag(context.Background(), usagex.Tag{
		Feature: usagex.FeatureAssistantChat, Trigger: usagex.TriggerUser, Ref: c.ID,
	})
	if _, _, err := (chatx.ClaudeChat{}).SendStream(ctx, c, "hi", func(chatx.ChatStreamEvent) {}); err == nil {
		t.Fatal("died before result, yet the call was treated as a success")
	}

	rows := usagex.ReadRows()
	if len(rows) != 1 {
		t.Fatalf("ledger = %d rows, want 1 (the failure path still leaves one row)", len(rows))
	}
	r := rows[0]
	if r.Spend != 12+78+34 {
		t.Fatalf("spend = %d, want 124 (the snapshot at the stop was not captured): %+v", r.Spend, r)
	}
	if r.Measured != usagex.MeasuredPartial {
		t.Fatalf("measured = %q, want partial (no result yet, so not exact)", r.Measured)
	}
	if r.OK {
		t.Fatalf("a failed call recorded ok=true: %+v", r)
	}
}

// Every place claude usage is parsed must carry the "fall back to usage when modelUsage is
// empty" step. Forgetting it on a newly added path (another CLI version, another subcommand)
// fills that path's stops and abnormal exits entirely with measured=none, so the AST is watched.
func TestClaudeUsageSitesHaveTokenFallback(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "internal/chatx/chat_providers.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	sites := 0
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		uses, falls := false, false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				if fun.Name == "UsageModelRows" {
					uses = true
				}
			case *ast.SelectorExpr:
				if fun.Sel.Name == "FallbackTotals" {
					falls = true
				}
			}
			return true
		})
		if uses {
			sites++
		}
		if uses && !falls {
			t.Errorf("%s: uses chatx.UsageModelRows but has no fallbackTotals"+
				" (a stop or abnormal exit with no modelUsage records 0 spend)", fn.Name.Name)
		}
	}
	// If the scan matches nothing, this check is looking at nothing. Once usageModelRows stops
	// being a bare identifier (say it moves to usagex and becomes usagex.ModelRows), uses is
	// false forever and deleting every fallback still passes green. What is guarded is the
	// billing that goes missing when a stop or abnormal exit records 0 spend.
	if sites == 0 {
		t.Fatal("not one function calling chatx.UsageModelRows was found = this check has gone silent" +
			" (if the identifier changed or moved, fix this check's scan condition with it)")
	}
}

// A failure on a self-picked small model, then a re-fire with the model dropped: the two calls
// are summed. While the second overwrote the first, the most expensive path looked like one call.
func TestCodexOneShotRetryAccumulatesTokens(t *testing.T) {
	calls := 0
	run := func(_ context.Context, args []string, _ string) (string, chatx.CodexUsage, error) {
		calls++
		if calls == 1 {
			// First call: fired (and therefore billed), then failed.
			return "", chatx.CodexUsage{InputTokens: 100, OutputTokens: 10}, errors.New("model not available")
		}
		return "ok", chatx.CodexUsage{InputTokens: 300, OutputTokens: 30}, nil
	}
	reply, tok, modelReq, err := chatx.CodexOneShotWithRetry(context.Background(),
		[]string{"exec", "-m", "gpt-5.4-mini", "-"}, true, "p", run)
	if err != nil || reply != "ok" || calls != 2 {
		t.Fatalf("reply=%q calls=%d err=%v", reply, calls, err)
	}
	if tok.In != 400 || tok.Out != 40 {
		t.Fatalf("tokens = %+v, want in 400 / out 40 (first 100/10 + second 300/30)", tok)
	}
	// The re-fire is what produced the answer, so the requested model is recorded as none
	// (i.e. the CLI default).
	if modelReq != "" {
		t.Fatalf("model_req = %q, want \"\" (the re-fire dropped the model)", modelReq)
	}

	// A failure on a model the user named explicitly is not re-fired — only one call is left.
	calls = 0
	_, tok, modelReq, err = chatx.CodexOneShotWithRetry(context.Background(),
		[]string{"exec", "-m", "gpt-5.4-mini", "-"}, false, "p", run)
	if err == nil || calls != 1 || tok.In != 100 {
		t.Fatalf("re-fired after a failure on an explicitly named model: calls=%d tok=%+v err=%v", calls, tok, err)
	}
	if modelReq != "gpt-5.4-mini" {
		t.Fatalf("model_req = %q, want gpt-5.4-mini", modelReq)
	}
}

// ledgerTokens takes the amount billed for THIS call, which is a different quantity from the
// context occupancy (the snapshot at the tail of iterations).
func TestClaudeLedgerTokensUsesCallTotals(t *testing.T) {
	u := chatx.ClaudeUsage{
		InputTokens: 9, OutputTokens: 533, CacheCreationInputTokens: 10015, CacheReadInputTokens: 6002,
		Iterations: []chatx.ClaudeUsage{{InputTokens: 1}, {InputTokens: 2}},
	}
	got := u.LedgerTokens()
	if got.In != 9 || got.Out != 533 || got.CacheCreate != 10015 || got.CacheRead != 6002 {
		t.Fatalf("ledgerTokens = %+v", got)
	}
	if s := usagex.Spend(got.In, got.CacheCreate, got.Out); s != 10557 {
		t.Fatalf("spend = %d, want 10557 (cache_read must not be counted)", s)
	}
}

// Guard that no message body ever mixes into a ledger row (a non-negotiable rule): watch the
// field names of the ledger record for anything body-shaped.
func TestUsageRecordHasNoContentFields(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "internal/usagex/ledger.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	banned := []string{"text", "prompt", "reply", "body", "content", "message"}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "Record" {
			return true
		}
		found = true
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return false
		}
		for _, fld := range st.Fields.List {
			for _, nm := range fld.Names {
				for _, b := range banned {
					if strings.Contains(strings.ToLower(nm.Name), b) {
						t.Errorf("a body-shaped field has appeared in the ledger: %s", nm.Name)
					}
				}
			}
		}
		return false
	})
	// A wrong path is caught by ParseFile's t.Fatal, but a wrong TYPE NAME is not: it simply
	// matches nothing and goes quietly green. The last rename was noticed only because the check
	// went red, and the next one cannot count on the same luck.
	if !found {
		t.Fatal("the ledger row type was not found = this check has gone silent" +
			" (if the type was renamed or moved, fix this check's scan condition with it)")
	}
}
