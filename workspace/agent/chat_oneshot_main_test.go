package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

func TestUsageLedgerLive(t *testing.T) {
	if os.Getenv("AF_TITLE_LIVE") != "1" {
		t.Skip("AF_TITLE_LIVE=1 で有効化")
	}
	useTempUsageDir(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ctx = usagex.WithTag(ctx, usagex.Tag{
		Feature: usagex.FeatureTitleSession, Trigger: usagex.TriggerManual, Ref: "slot99",
	})
	if _, err := oneShotHeadless(ctx, titleSuggestPersona("ja"),
		"以下の会話に件名を付けてください。\nuser: 使用量のグラフを作りたい\nassistant: 台帳を設計します",
		titleModel()); err != nil {
		t.Fatalf("oneShotHeadless: %v", err)
	}
	rows := usagex.ReadRows()
	if len(rows) == 0 {
		t.Fatal("台帳に1行も落ちていない")
	}
	r := rows[0]
	t.Logf("row = %+v", r)
	if r.Feature != usagex.FeatureTitleSession || r.Trigger != usagex.TriggerManual || r.Ref != "slot99" {
		t.Fatalf("タグが行に乗っていない: %+v", r)
	}
	if !r.OK || r.Kind == "" {
		t.Fatalf("実行結果の kind と成否が入っていない: %+v", r)
	}
	if r.Kind != session.KindClaude {
		t.Skipf("claude 以外（%s）で実行された — 以降はコスト実測の検証なのでスキップ", r.Kind)
	}
	// claude だけはモデル・トークン・コストが全部実測で返る（docs/log/46 §0）。
	if r.ModelSrc != usagex.ModelReported || r.Model == "" || r.ModelRaw == "" {
		t.Fatalf("モデルが報告値として記録されていない: %+v", r)
	}
	if r.Model == r.ModelRaw {
		t.Errorf("canonicalModel と生 id が同じ — 版を畳めていない可能性: %q", r.Model)
	}
	if r.Spend <= 0 || r.Measured != usagex.MeasuredExact {
		t.Fatalf("トークンが実測で入っていない: %+v", r)
	}
	if r.CostUSD <= 0 {
		t.Fatalf("コスト実測が入っていない: %+v", r)
	}
}

func TestBranchSuggestLive(t *testing.T) {
	if os.Getenv("AF_TITLE_LIVE") != "1" {
		t.Skip("AF_TITLE_LIVE=1 で有効化")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	reply, err := oneShotHeadless(ctx, branchSuggestPersona, branchSuggestPrompt(oneShotLiveTurns()), titleModel())
	if err != nil {
		t.Fatalf("oneShotHeadless: %v", err)
	}
	name := cleanBranchName(reply)
	if name == "" {
		t.Fatalf("ブランチ名が空: reply=%q", reply)
	}
	t.Logf("branch=%q (raw=%q)", name, reply)
}

func TestReplySuggestLive(t *testing.T) {
	if os.Getenv("AF_TITLE_LIVE") != "1" {
		t.Skip("AF_TITLE_LIVE=1 で有効化")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	reply, err := oneShotHeadless(ctx, replySuggestPersona("ja"), replySuggestPrompt(oneShotLiveTurns(), "ja"), replySuggestModel())
	if err != nil {
		t.Fatalf("oneShotHeadless: %v", err)
	}
	list := cleanSuggestedReplies(reply)
	if len(list) == 0 {
		t.Fatalf("返信候補が空: reply=%q", reply)
	}
	t.Logf("suggestions=%q (raw=%q)", list, reply)
}
