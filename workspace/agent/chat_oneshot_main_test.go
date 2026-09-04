package main

import (
	"context"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"os"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

func TestUsageLedgerLive(t *testing.T) {
	if os.Getenv("AF_TITLE_LIVE") != "1" {
		t.Skip("set AF_TITLE_LIVE=1 to enable")
	}
	useTempUsageDir(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ctx = usagex.WithTag(ctx, usagex.Tag{
		Feature: usagex.FeatureTitleSession, Trigger: usagex.TriggerManual, Ref: "slot99",
	})
	if _, err := chatx.OneShotHeadless(ctx, chatx.OneShotShort, sessionx.TitleSuggestPersona("ja"),
		"以下の会話に件名を付けてください。\nuser: 使用量のグラフを作りたい\nassistant: 台帳を設計します",
		sessionx.TitleModel()); err != nil {
		t.Fatalf("chatx.OneShotHeadless: %v", err)
	}
	rows := usagex.ReadRows()
	if len(rows) == 0 {
		t.Fatal("not a single row landed in the ledger")
	}
	r := rows[0]
	t.Logf("row = %+v", r)
	if r.Feature != usagex.FeatureTitleSession || r.Trigger != usagex.TriggerManual || r.Ref != "slot99" {
		t.Fatalf("the tag did not make it onto the row: %+v", r)
	}
	if !r.OK || r.Kind == "" {
		t.Fatalf("the run's kind and success flag are missing: %+v", r)
	}
	if r.Kind != session.KindClaude {
		t.Skipf("ran on something other than claude (%s); the rest checks measured cost, so skip", r.Kind)
	}
	// Only claude reports model, tokens and cost all as measured values (docs/log/46 §0).
	if r.ModelSrc != usagex.ModelReported || r.Model == "" || r.ModelRaw == "" {
		t.Fatalf("the model was not recorded as a reported value: %+v", r)
	}
	if r.Model == r.ModelRaw {
		t.Errorf("canonicalModel equals the raw id; the version may not have been folded: %q", r.Model)
	}
	if r.Spend <= 0 || r.Measured != usagex.MeasuredExact {
		t.Fatalf("tokens are not present as measured values: %+v", r)
	}
	if r.CostUSD <= 0 {
		t.Fatalf("no measured cost is present: %+v", r)
	}
}

func TestBranchSuggestLive(t *testing.T) {
	if os.Getenv("AF_TITLE_LIVE") != "1" {
		t.Skip("set AF_TITLE_LIVE=1 to enable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	reply, err := chatx.OneShotHeadless(ctx, chatx.OneShotShort, sessionx.BranchSuggestPersona, sessionx.BranchSuggestPrompt(oneShotLiveTurns()), sessionx.TitleModel())
	if err != nil {
		t.Fatalf("chatx.OneShotHeadless: %v", err)
	}
	name := sessionx.CleanBranchName(reply)
	if name == "" {
		t.Fatalf("branch name is empty: reply=%q", reply)
	}
	t.Logf("branch=%q (raw=%q)", name, reply)
}

func TestReplySuggestLive(t *testing.T) {
	if os.Getenv("AF_TITLE_LIVE") != "1" {
		t.Skip("set AF_TITLE_LIVE=1 to enable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	reply, err := chatx.OneShotHeadless(ctx, chatx.OneShotShort, sessionx.ReplySuggestPersona("ja"), sessionx.ReplySuggestPrompt(oneShotLiveTurns(), "ja"), sessionx.ReplySuggestModel())
	if err != nil {
		t.Fatalf("chatx.OneShotHeadless: %v", err)
	}
	list := sessionx.CleanSuggestedReplies(reply)
	if len(list) == 0 {
		t.Fatalf("reply candidates are empty: reply=%q", reply)
	}
	t.Logf("suggestions=%q (raw=%q)", list, reply)
}
