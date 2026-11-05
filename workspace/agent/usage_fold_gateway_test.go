package main

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// One model call is counted once (ADR 0128 decision 8): the route gateway writes the row of every
// bedrock call itself, so the claude transcript's turns on bedrock reach no ledger row, while the
// watermark still moves past them; a turn of another route is written as before.
func TestBedrockTranscriptTurnsAreLeftToTheGatewayRows(t *testing.T) {
	useTempUsageDir(t)
	stamp := func(route, billing string, tt transcript.Turn) transcript.Turn {
		tt.Route, tt.Billing = route, billing
		return tt
	}
	fold := func(m session.Meta, turns []transcript.Turn) int {
		t.Helper()
		usageFoldMu.Lock()
		defer usageFoldMu.Unlock()
		st := readUsageFoldState()
		n, err := foldSessionUsageWithTurns(m, &st, turns, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeUsageFoldState(st); err != nil {
			t.Fatal(err)
		}
		return n
	}
	bed := session.Meta{Name: "bed01", Kind: session.KindClaude, Route: &routes.Selection{Route: usagex.RouteBedrock, Connection: "p1@eu-west-1"}}
	turns := []transcript.Turn{
		{Role: "user", Text: "1"},
		stamp(usagex.RouteBedrock, usagex.BillingMetered, asst("us.anthropic.claude-opus-5-5", 11, 5, 0, 0, false)),
		{Role: "user", Text: "2"},
		stamp(usagex.RouteBedrock, usagex.BillingMetered, asst("us.anthropic.claude-opus-5-5", 20, 7, 0, 0, false)),
	}
	if n := fold(bed, turns); n != 0 {
		t.Fatalf("bedrock turns wrote %d rows", n)
	}
	usageFoldMu.Lock()
	mark := readUsageFoldState().Sessions[bed.Name]
	usageFoldMu.Unlock()
	if mark.Groups != 2 {
		t.Fatalf("watermark = %+v, want it past both turns", mark)
	}
	// The gateway's rows for the same calls are the ones that count.
	if err := usagex.AppendRows([]usagex.Record{{TS: "2026-11-06T09:00:00Z", Feature: usagex.FeatureRouteGateway, Kind: session.KindClaude,
		Ref: bed.Name, Route: usagex.RouteBedrock, Billing: usagex.BillingMetered, In: 11, Out: 5, OK: true}}); err != nil {
		t.Fatal(err)
	}
	api := session.Meta{Name: "api01", Kind: session.KindClaude, Route: &routes.Selection{Route: "anthropic-api"}}
	apiTurns := []transcript.Turn{{Role: "user", Text: "1"}, stamp("anthropic-api", usagex.BillingMetered, asst("claude-opus-5-5", 11, 5, 0, 0, false))}
	if n := fold(api, apiTurns); n != 1 {
		t.Fatalf("an anthropic-api turn wrote %d rows, want 1", n)
	}
	rows := usagex.ReadRows()
	var gw, sessionRows int
	for _, r := range rows {
		switch {
		case r.Feature == usagex.FeatureRouteGateway:
			gw++
		case r.Ref == bed.Name:
			t.Errorf("a transcript row of the bedrock session reached the ledger: %+v", r)
		default:
			sessionRows++
		}
	}
	if gw != 1 || sessionRows != 1 {
		t.Fatalf("ledger: %d gateway rows, %d session rows; want 1 and 1", gw, sessionRows)
	}
}

// A subagent transcript is not stamped: its rows take the session's selection at their time.
func TestDropGatewayCountedUsesTheSelectionForUnstampedRows(t *testing.T) {
	bed := session.Meta{Name: "bed02", Kind: session.KindClaude, Route: &routes.Selection{Route: usagex.RouteBedrock}}
	recs := []usagex.Record{{TS: "2026-11-06T09:00:00Z", Ref: "bed02"}, {TS: "2026-11-06T09:01:00Z", Ref: "bed02", Route: "anthropic-api"}}
	if got := dropGatewayCounted(bed, recs); len(got) != 1 || got[0].Route != "anthropic-api" {
		t.Fatalf("bedrock session: kept %+v", got)
	}
	plain := session.Meta{Name: "p02", Kind: session.KindClaude}
	if got := dropGatewayCounted(plain, recs); len(got) != 2 {
		t.Fatalf("no selection: kept %d of 2", len(got))
	}
}
