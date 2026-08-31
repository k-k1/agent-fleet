package main

// プロバイダ層での使用量捕捉の回帰（docs/log/46 §3-a のレビュー P2-7 / P3-11）。
//
// ここで守るのは1つ: **撃った分は必ず台帳に残る**。modelUsage が来なかった（停止・異常
// 終了）から 0 トークン、リトライしたから1回分、では配分の絵が狂う。

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

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// 停止操作や result 前の異常終了では modelUsage が来ない。assistant イベントで見た
// スナップショットを partial として残す（0 トークン・measured=none にしない）。
func TestFallbackTotalsKeepsStoppedTurnTokens(t *testing.T) {
	// 1) result が来なかった: スナップショットを partial で採る。
	stopped := usageCall{Kind: session.KindClaude}
	snap := claudeUsage{InputTokens: 12, OutputTokens: 34, CacheReadInputTokens: 5600, CacheCreationInputTokens: 78}
	stopped.fallbackTotals(snap.ledgerTokens(), usageMeasuredPartial)
	if stopped.Totals.In != 12 || stopped.Totals.Out != 34 ||
		stopped.Totals.CacheRead != 5600 || stopped.Totals.CacheCreate != 78 {
		t.Fatalf("停止時のトークンを落とした: %+v", stopped.Totals)
	}
	if got := stopped.measuredOr(stopped.Totals); got != usageMeasuredPartial {
		t.Fatalf("measured = %q, want partial（途中のスナップショットは exact ではない）", got)
	}

	// 2) modelUsage が来た呼び出しは触らない（モデル別の内訳の方が正）。
	full := usageCall{Kind: session.KindClaude, Models: usageModelRows(map[string]claudeModelUsage{
		"claude-haiku-4-5-20251001": {InputTokens: 1, OutputTokens: 2, CanonicalModel: "claude-haiku-4-5"},
	})}
	full.fallbackTotals(snap.ledgerTokens(), usageMeasuredPartial)
	if full.Totals.any() || full.Measured != "" {
		t.Fatalf("modelUsage のある呼び出しを縮退で上書きした: %+v", full)
	}

	// 3) 何も取れていない呼び出しは none のまま（0 を「消費 0」と偽らない）。
	empty := usageCall{Kind: session.KindClaude}
	empty.fallbackTotals(claudeUsage{}.ledgerTokens(), usageMeasuredPartial)
	if empty.Measured != "" || empty.measuredOr(empty.Totals) != usageMeasuredNone {
		t.Fatalf("トークンが1つも無いのに partial を名乗った: %+v", empty)
	}
}

// 実際に claude を「result を出す前に落ちる」形で撃って、消費が台帳に残ることを見る。
// stub CLI を PATH の先頭に置くだけなので認証も課金も要らない（実 CLI は live テスト側）。
func TestClaudeStreamRecordsTokensWhenKilledBeforeResult(t *testing.T) {
	useIsolatedUsageDir(t)
	bin := t.TempDir()
	// assistant イベントを1つ吐いてから result を出さずに死ぬ（＝利用者の停止・OOM の形）。
	stub := "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' '" +
		`{"type":"assistant","message":{"model":"claude-haiku-4-5","usage":` +
		`{"input_tokens":12,"output_tokens":34,"cache_read_input_tokens":5600,"cache_creation_input_tokens":78}}}` +
		"'\nexit 143\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	c := &chatConversation{ID: "conv1", Model: "haiku"}
	ctx := withUsageTag(context.Background(), usageTag{
		Feature: usageFeatureAssistantChat, Trigger: usageTriggerUser, Ref: c.ID,
	})
	if _, _, err := (claudeChat{}).sendStream(ctx, c, "hi", func(chatStreamEvent) {}); err == nil {
		t.Fatal("result 前に死んだのに成功扱いになった")
	}

	rows := readUsageRows()
	if len(rows) != 1 {
		t.Fatalf("台帳 = %d 行, want 1（失敗経路でも1行残る）", len(rows))
	}
	r := rows[0]
	if r.Spend != 12+78+34 {
		t.Fatalf("spend = %d, want 124（停止時のスナップショットを採れていない）: %+v", r.Spend, r)
	}
	if r.Measured != usageMeasuredPartial {
		t.Fatalf("measured = %q, want partial（result 前なので exact ではない）", r.Measured)
	}
	if r.OK {
		t.Fatalf("失敗した呼び出しが ok=true: %+v", r)
	}
}

// claude の usage 解析地点はどれも「modelUsage が空なら usage から採る」を伴うこと。
// 新しい経路（別の CLI 版・別のサブコマンド）を足した時に縮退を書き忘れると、その経路の
// 停止・異常終了が丸ごと measured=none で埋まるので、AST で見張る。
func TestClaudeUsageSitesHaveTokenFallback(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "chat_providers.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
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
				if fun.Name == "usageModelRows" {
					uses = true
				}
			case *ast.SelectorExpr:
				if fun.Sel.Name == "fallbackTotals" {
					falls = true
				}
			}
			return true
		})
		if uses && !falls {
			t.Errorf("%s: usageModelRows を使っているのに fallbackTotals が無い"+
				"（modelUsage の来ない停止・異常終了で消費が 0 になる）", fn.Name.Name)
		}
	}
}

// 自前ピクの小型モデルで失敗 → モデルを外して撃ち直し、の2回分を合算する。
// 上書きしていた頃は「最も高くつく経路」が1回分に見えていた。
func TestCodexOneShotRetryAccumulatesTokens(t *testing.T) {
	calls := 0
	run := func(_ context.Context, args []string, _ string) (string, codexUsage, error) {
		calls++
		if calls == 1 {
			// 1回目: 撃って（＝課金されて）から失敗した。
			return "", codexUsage{InputTokens: 100, OutputTokens: 10}, errors.New("model not available")
		}
		return "ok", codexUsage{InputTokens: 300, OutputTokens: 30}, nil
	}
	reply, tok, modelReq, err := codexOneShotWithRetry(context.Background(),
		[]string{"exec", "-m", "gpt-5.4-mini", "-"}, true, "p", run)
	if err != nil || reply != "ok" || calls != 2 {
		t.Fatalf("reply=%q calls=%d err=%v", reply, calls, err)
	}
	if tok.In != 400 || tok.Out != 40 {
		t.Fatalf("トークン = %+v, want in 400 / out 40（1回目 100/10 + 2回目 300/30）", tok)
	}
	// 撃ち直した側が答えを出したので、要求値はモデル無し（＝CLI 既定）を載せる。
	if modelReq != "" {
		t.Fatalf("model_req = %q, want \"\"（モデルを外して撃ち直した）", modelReq)
	}

	// 自前ピクでない（利用者が明示した）失敗は撃ち直さない — 1回分だけ残る。
	calls = 0
	_, tok, modelReq, err = codexOneShotWithRetry(context.Background(),
		[]string{"exec", "-m", "gpt-5.4-mini", "-"}, false, "p", run)
	if err == nil || calls != 1 || tok.In != 100 {
		t.Fatalf("明示モデルの失敗で撃ち直した: calls=%d tok=%+v err=%v", calls, tok, err)
	}
	if modelReq != "gpt-5.4-mini" {
		t.Fatalf("model_req = %q, want gpt-5.4-mini", modelReq)
	}
}

// ledgerTokens は「この呼び出しで課金された量」を採る（コンテキスト占有＝iterations 末尾の
// スナップショットとは別の量）。
func TestClaudeLedgerTokensUsesCallTotals(t *testing.T) {
	u := claudeUsage{
		InputTokens: 9, OutputTokens: 533, CacheCreationInputTokens: 10015, CacheReadInputTokens: 6002,
		Iterations: []claudeUsage{{InputTokens: 1}, {InputTokens: 2}},
	}
	got := u.ledgerTokens()
	if got.In != 9 || got.Out != 533 || got.CacheCreate != 10015 || got.CacheRead != 6002 {
		t.Fatalf("ledgerTokens = %+v", got)
	}
	if s := usageSpend(got.In, got.CacheCreate, got.Out); s != 10557 {
		t.Fatalf("spend = %d, want 10557（cache_read を含めない）", s)
	}
}

// 台帳の行に本文が混ざらないことの番人（非交渉の原則）。usageRecord のフィールド名に
// 本文らしきものが増えていないかを見る。
func TestUsageRecordHasNoContentFields(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "usage_ledger.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	banned := []string{"text", "prompt", "reply", "body", "content", "message"}
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "usageRecord" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return false
		}
		for _, fld := range st.Fields.List {
			for _, nm := range fld.Names {
				for _, b := range banned {
					if strings.Contains(strings.ToLower(nm.Name), b) {
						t.Errorf("台帳に本文らしきフィールドが増えている: %s", nm.Name)
					}
				}
			}
		}
		return false
	})
}
