package main

import (
	"context"
	"fmt"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// useTempUsageDir points the ledger at a temp directory and clears the prune throttle state
// (it is process-global, so carrying it across tests makes prune either not run or run too often).
//
// Collect the running bulk fold before swapping the ledger. The fold runs in a goroutine and
// reads its destination (`AF_USAGE_DIR`) from the env at the moment it writes; the env is
// process-wide, so a fold an earlier test started and left alive writes into this test's ledger
// — a red like "ledger = 39 rows, want 4" whose cause is nowhere inside that test. On a real
// machine folding 200 sessions takes hundreds of ms to tens of seconds, so a single test that
// does not wait is enough to leak (CI's HOME is empty and has no sessions, so it is green only
// there).
//
// Clearing `usageFoldRunning` instead of waiting does not fix the leak, it erases its traces —
// the goroutine keeps running, and the double-start guard is dropped so a second one may run.
// Waiting is the correct fix.
func useTempUsageDir(t *testing.T) string {
	t.Helper()
	waitUsageFoldIdle(t) // collect a fold an earlier test started, before swapping the ledger
	dir := t.TempDir()
	t.Setenv("AF_USAGE_DIR", dir)
	// Push this Cleanup after t.TempDir(). Cleanup is LIFO, so this one runs before the temp
	// directory is removed — the other order leaves the writer writing into a directory that
	// disappeared while we were waiting (memory: tempdir-cleanup-lifo-detached-writer).
	t.Cleanup(func() { waitUsageFoldIdle(t) })
	usagex.ResetPruneClock() // usageMu / usagePrunedAt are unexported state inside usagex
	usageFoldGate.Lock()
	usageFoldedAt = time.Time{}
	usageFoldGate.Unlock()
	return dir
}

// useIsolatedUsageDir swaps HOME in addition to the ledger. Aggregation API tests go through the
// handler and hit fold-on-read, so without a separate HOME they fold the real workspace's
// sessions and the expected values break against real data (hit for real — the total came to
// millions of tokens). Live tests that fire the real CLI cannot use it (auth lives under HOME),
// so those stay on useTempUsageDir. HOME alone is not enough (memory: ci-runner-xdg-not-home):
// the price catalog also reads the opencode cache under XDG_CACHE_HOME, so unless that is swapped
// too the expected values move with the real catalog on the dev machine (i.e. the test ends up
// inspecting the environment).
func useIsolatedUsageDir(t *testing.T) string {
	t.Helper()
	dir := useTempUsageDir(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("AF_USAGE_CATALOG", "")
	resetUsageCatalogCache(t)
	return dir
}

// resetUsageCatalogCache drops the in-process cache so an earlier test's catalog does not leak
// into the next one. Also registered as a Cleanup so it takes effect on both sides.
func resetUsageCatalogCache(t *testing.T) {
	t.Helper()
	clear := func() {
		usageCatalogMu.Lock()
		usageCatalogCache, usageCatalogSrc = nil, ""
		usageCatalogAt, usageCatalogChecked = time.Time{}, time.Time{}
		usageCatalogMu.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

func TestRecordUsageCallSplitsClaudeModelRows(t *testing.T) {
	useTempUsageDir(t)
	ctx := usagex.WithTag(context.Background(), usagex.Tag{
		Feature: usagex.FeatureTitleSession, Trigger: usagex.TriggerAuto, Ref: "slot01",
	})
	call := usagex.Call{
		Kind: session.KindClaude, ModelReq: "haiku", OK: true,
		CostUSD: 0.0084,
		Models: chatx.UsageModelRows(map[string]chatx.ClaudeModelUsage{
			"claude-haiku-4-5-20251001": {
				InputTokens: 2, OutputTokens: 5, CacheCreationInputTokens: 4186,
				CostUSD: 0.0084, CanonicalModel: "claude-haiku-4-5",
			},
			"claude-sonnet-4-6-20260101": {
				InputTokens: 10, OutputTokens: 20, CacheReadInputTokens: 100,
				CostUSD: 0.02, CanonicalModel: "claude-sonnet-4-6",
			},
		}),
	}
	usagex.RecordCall(ctx, &call, time.Now())

	rows := usagex.ReadRows()
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (one row per model)", len(rows))
	}
	// One call split across several models is still one call — the call id ties the rows together
	// (the aggregate's calls counts distinct calls).
	if rows[0].Call != rows[1].Call || rows[0].Call == "" {
		t.Fatalf("call ids = %q / %q, want the same non-empty id", rows[0].Call, rows[1].Call)
	}
	h := rows[0]
	if h.Model != "claude-haiku-4-5" || h.ModelRaw != "claude-haiku-4-5-20251001" {
		t.Fatalf("model = %q / raw %q", h.Model, h.ModelRaw)
	}
	if h.ModelSrc != usagex.ModelReported || h.ModelReq != "haiku" {
		t.Fatalf("model_src = %q, model_req = %q", h.ModelSrc, h.ModelReq)
	}
	// spend = in + ccreate + out (cache_read excluded)
	if want := 2 + 4186 + 5; h.Spend != want {
		t.Fatalf("spend = %d, want %d", h.Spend, want)
	}
	if h.CostUSD != 0.0084 || h.Measured != usagex.MeasuredExact || !h.OK {
		t.Fatalf("row = %+v", h)
	}
	if h.Feature != usagex.FeatureTitleSession || h.Trigger != usagex.TriggerAuto || h.Ref != "slot01" {
		t.Fatalf("tag not carried: %+v", h)
	}
}

// A CLI that does not report its model degrades to requested / default_unknown. The point is to
// make it visible in a single column when helper calls run on the default model (usually the
// flagship).
func TestRecordUsageCallModelFallback(t *testing.T) {
	useTempUsageDir(t)
	for _, tc := range []struct {
		name       string
		req        string
		wantModel  string
		wantSrc    string
		wantMeasrd string
	}{
		{"requested value given", "gpt-5.4-mini", "gpt-5.4-mini", usagex.ModelRequest, usagex.MeasuredExact},
		{"left to the CLI default", "", "", usagex.ModelUnknown, usagex.MeasuredExact},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := useTempUsageDir(t)
			call := usagex.Call{Kind: session.KindCodex, ModelReq: tc.req, OK: true}
			call.SetTotals(100, 20, 30, 40)
			usagex.RecordCall(context.Background(), &call, time.Now())
			rows := usagex.ReadRows()
			if len(rows) != 1 {
				t.Fatalf("rows = %d in %s", len(rows), dir)
			}
			r := rows[0]
			if r.Model != tc.wantModel || r.ModelSrc != tc.wantSrc || r.Measured != tc.wantMeasrd {
				t.Fatalf("row = %+v", r)
			}
			// A call with no tag still leaves one row (nothing unrecorded, i.e. no invisible spend).
			if r.Feature != usagex.FeatureUnknown {
				t.Fatalf("feature = %q, want %q", r.Feature, usagex.FeatureUnknown)
			}
			if want := 100 + 40 + 20; r.Spend != want {
				t.Fatalf("spend = %d, want %d", r.Spend, want)
			}
		})
	}
}

// A zero from a CLI that does not report tokens is not "spent 0" — measured=none counts only the
// number of calls.
func TestRecordUsageCallUnmeasuredCountsTheCall(t *testing.T) {
	useTempUsageDir(t)
	call := usagex.Call{Kind: session.KindAgy, Measured: usagex.MeasuredNone, OK: true}
	usagex.RecordCall(context.Background(), &call, time.Now())
	rows := usagex.ReadRows()
	if len(rows) != 1 || rows[0].Measured != usagex.MeasuredNone || rows[0].Spend != 0 {
		t.Fatalf("rows = %+v", rows)
	}
}

// Failed turns are recorded too (ok=false). Dropping them on error creates spend that was fired
// but is invisible.
func TestRecordUsageCallRecordsFailures(t *testing.T) {
	useTempUsageDir(t)
	call := usagex.Call{Kind: session.KindClaude, ModelReq: "haiku"} // OK stays false
	usagex.RecordCall(context.Background(), &call, time.Now())
	rows := usagex.ReadRows()
	if len(rows) != 1 || rows[0].OK {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestUsageRecordingCanBeDisabled(t *testing.T) {
	useTempUsageDir(t)
	t.Setenv("AF_USAGE_RECORD", "0")
	call := usagex.Call{Kind: session.KindClaude, OK: true}
	call.SetTotals(1, 2, 3, 4)
	usagex.RecordCall(context.Background(), &call, time.Now())
	if rows := usagex.ReadRows(); len(rows) != 0 {
		t.Fatalf("rows = %d, want 0 when recording is off", len(rows))
	}
}

func TestPruneUsageRawDropsExpiredDays(t *testing.T) {
	dir := useTempUsageDir(t)
	t.Setenv("AF_USAGE_RETENTION_DAYS", "7")
	raw := filepath.Join(dir, "raw")
	if err := os.MkdirAll(raw, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().AddDate(0, 0, -30).Format("2006-01-02") + ".jsonl"
	recent := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02") + ".jsonl"
	for _, n := range []string{old, recent, "notes.txt"} {
		if err := os.WriteFile(filepath.Join(raw, n), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	usagex.PruneRawNow()
	if _, err := os.Stat(filepath.Join(raw, old)); !os.IsNotExist(err) {
		t.Fatalf("%s is past the retention window but is still there", old)
	}
	for _, keep := range []string{recent, "notes.txt"} {
		if _, err := os.Stat(filepath.Join(raw, keep)); err != nil {
			t.Fatalf("%s was deleted: %v", keep, err)
		}
	}
}

// ---- folding (foldTurnRows) ----

func asst(model string, in, out, read, create int, sidechain bool) transcript.Turn {
	return transcript.Turn{
		Role: "assistant", Model: model, InTok: in, OutTok: out,
		CacheRead: read, CacheCreate: create, Sidechain: sidechain, TS: "2026-07-26T00:00:00Z",
	}
}

func TestFoldTurnRowsMatchesAggregateUsage(t *testing.T) {
	// Run the same event stream through both the ledger and get_session_usage and check the spend
	// totals agree (regression against the two views showing different numbers).
	turns := []transcript.Turn{
		{Role: "user", Text: "1"},
		asst("claude-haiku-4-5", 100, 10, 5, 20, false),
		asst("claude-haiku-4-5", 150, 30, 5, 25, false), // second event of the same logical turn
		{Role: "user", Text: "2", Source: sessionx.TurnSourceOperator},
		asst("claude-haiku-4-5", 200, 40, 10, 0, false),
		asst("claude-haiku-4-5", 300, 60, 0, 0, true), // subagent (separate group)
		{Role: "user", Text: "3"},
		asst("claude-haiku-4-5", 400, 80, 0, 0, false), // trailing turn, still open
	}
	rows := foldTurnRows(turns, false)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3 (the open trailing turn is not included)", len(rows))
	}
	// Input replaces, output accumulates (the same rule as aggregateUsage).
	if rows[0].Tokens.In != 150 || rows[0].Tokens.Out != 40 || rows[0].Tokens.CacheCreate != 25 {
		t.Fatalf("row0 = %+v", rows[0])
	}
	if !rows[2].Sidechain {
		t.Fatalf("the subagent turn was not split off as sidechain: %+v", rows[2])
	}
	if rows[1].Trigger != usagex.TriggerOperator {
		t.Fatalf("trigger = %q, want %q", rows[1].Trigger, usagex.TriggerOperator)
	}
	if rows[0].Idx != 1 || rows[1].Idx != 2 || rows[2].Idx != 3 {
		t.Fatalf("idx is not a running number: %+v", rows)
	}

	// Including the trailing turn, the total matches cumulative (get_session_usage) spend.
	all := foldTurnRows(turns, true)
	sum := 0
	for _, r := range all {
		sum += usagex.Spend(r.Tokens.In, r.Tokens.CacheCreate, r.Tokens.Out)
	}
	want := sessionx.AggregateUsage(turns).Cumulative.Spend
	if sum != want {
		t.Fatalf("ledger spend total = %d, get_session_usage = %d", sum, want)
	}
	if len(all) != sessionx.AggregateUsage(turns).Cumulative.Turns {
		t.Fatalf("logical turns = %d, get_session_usage = %d", len(all), sessionx.AggregateUsage(turns).Cumulative.Turns)
	}
}

// Folding is idempotent on (session, idx) — re-reading the same transcript adds no rows.
func TestFoldSessionUsageIsIdempotent(t *testing.T) {
	useTempUsageDir(t)
	turns := []transcript.Turn{
		{Role: "user", Text: "1"},
		asst("claude-haiku-4-5", 100, 10, 0, 20, false),
		{Role: "user", Text: "2"},
		asst("claude-haiku-4-5", 200, 20, 0, 0, false),
		{Role: "user", Text: "3"},
		asst("claude-haiku-4-5", 300, 30, 0, 0, false), // still open
	}
	m := session.Meta{Name: "slot01", Kind: session.KindClaude, Origin: session.OriginOperator, OriginConv: "a1b2c3d"}
	fold := func(includeTrailing bool) int {
		t.Helper()
		usageFoldMu.Lock()
		defer usageFoldMu.Unlock()
		st := readUsageFoldState()
		n, err := foldSessionUsageWithTurns(m, &st, turns, includeTrailing)
		if err != nil {
			t.Fatalf("fold failed: %v", err)
		}
		if err := writeUsageFoldState(st); err != nil {
			t.Fatalf("writing the watermark failed: %v", err)
		}
		return n
	}
	if n := fold(false); n != 2 {
		t.Fatalf("first pass = %d rows, want 2 (the trailing turn is open)", n)
	}
	if n := fold(false); n != 0 {
		t.Fatalf("re-reading added %d rows (not idempotent)", n)
	}
	// The trailing turn closed (a new user turn arrived).
	turns = append(turns, transcript.Turn{Role: "user", Text: "4"})
	if n := fold(false); n != 1 {
		t.Fatalf("fold of the closed trailing turn = %d rows, want 1", n)
	}
	rows := usagex.ReadRows()
	if len(rows) != 3 {
		t.Fatalf("ledger = %d rows, want 3", len(rows))
	}
	for i, r := range rows {
		if r.Feature != usagex.FeatureSession || r.Ref != "slot01" || r.Idx != i+1 {
			t.Fatalf("row%d = %+v", i, r)
		}
		// Origin is baked into the row so aggregation survives the session disappearing.
		if r.Origin != session.OriginOperator || r.OriginConv != "a1b2c3d" {
			t.Fatalf("row%d origin = %q/%q", i, r.Origin, r.OriginConv)
		}
		if r.ModelSrc != usagex.ModelReported || r.Model != "claude-haiku-4-5" {
			t.Fatalf("row%d model = %q (%q)", i, r.Model, r.ModelSrc)
		}
	}
}

// TestFoldMatchesSessionUsageLive checks against the real transcripts of a real workspace that
// the total folded into the ledger matches get_session_usage's cumulative (opt-in; docs/log/46
// P2 completion criterion). Unit tests over synthetic turns cannot cover the shapes each kind's
// real parser emits (how sidechain is attached, how events are chunked).
// Example run: AF_USAGE_FOLD_LIVE=1 go test -run TestFoldMatchesSessionUsageLive -v .
func TestFoldMatchesSessionUsageLive(t *testing.T) {
	if os.Getenv("AF_USAGE_FOLD_LIVE") != "1" {
		t.Skip("set AF_USAGE_FOLD_LIVE=1 to enable the check against real transcripts")
	}
	useTempUsageDir(t)
	checked := 0
	for _, m := range session.ListMetas() {
		if !sessionx.AgentOf(m.Kind).Caps().CanTranscript {
			continue
		}
		turns := sessionx.UsageTurns(m)
		if len(turns) == 0 {
			continue
		}
		// Folding with the trailing turn included covers the whole transcript, the same range as
		// cumulative.
		rows := foldTurnRows(turns, true)
		sum := 0
		for _, r := range rows {
			sum += usagex.Spend(r.Tokens.In, r.Tokens.CacheCreate, r.Tokens.Out)
		}
		cum := sessionx.AggregateUsage(turns).Cumulative
		if sum != cum.Spend || len(rows) != cum.Turns {
			t.Errorf("%s(%s): ledger spend=%d turns=%d / get_session_usage spend=%d turns=%d",
				m.Name, m.Kind, sum, len(rows), cum.Spend, cum.Turns)
			continue
		}
		checked++
		t.Logf("%s(%s): turns=%d spend=%d match", m.Name, m.Kind, len(rows), cum.Spend)
	}
	if checked == 0 {
		t.Skip("no session in this workspace has a transcript")
	}
	t.Logf("matched on %d sessions", checked)

	// Backfill and idempotence on real data: the first pass takes the whole history in, the
	// second adds nothing.
	n1 := foldAllSessionUsage()
	rows := usagex.ReadRows()
	if n1 == 0 || len(rows) != n1 {
		t.Fatalf("first backfill = %d rows / ledger %d rows", n1, len(rows))
	}
	if n2 := foldAllSessionUsage(); n2 != 0 {
		t.Fatalf("the second pass added %d rows (the watermark is not working)", n2)
	}
	t.Logf("backfill %d rows, second pass 0 rows (idempotent)", n1)
}

func TestUsageMeasuredForKind(t *testing.T) {
	for kind, want := range map[string]string{
		session.KindClaude:  usagex.MeasuredExact,
		session.KindCodex:   usagex.MeasuredExact,
		session.KindCopilot: usagex.MeasuredPartial, // only outTok in the transcript
		session.KindCursor:  usagex.MeasuredNone,    // no tokens in the transcript
		session.KindKiro:    usagex.MeasuredNone,
		session.KindAgy:     usagex.MeasuredNone,
	} {
		if got := usageMeasuredForKind(kind); got != want {
			t.Errorf("%s: measured = %q, want %q", kind, got, want)
		}
	}
}

// ---- origin ----

func TestCreateOriginResolution(t *testing.T) {
	for _, tc := range []struct {
		name     string
		req      sessionx.CreateReq
		want     string
		wantConv string
	}{
		{"Console (unmarked)", sessionx.CreateReq{}, session.OriginUser, ""},
		{"MCP create_session", sessionx.CreateReq{Origin: "operator", OriginConv: "conv-1"}, session.OriginOperator, "conv-1"},
		{"scheduled run", sessionx.CreateReq{Source: sessionx.TurnSourceSchedule}, session.OriginSchedule, ""},
		{"fired by hand", sessionx.CreateReq{Source: sessionx.TurnSourceScheduleManual}, session.OriginSchedule, ""},
		{"unknown value degrades to user", sessionx.CreateReq{Origin: "hacked"}, session.OriginUser, ""},
		// Outside origin=operator the conversation slug carries no meaning, so it is dropped.
		{"conv only when operator", sessionx.CreateReq{Origin: "user", OriginConv: "conv-1"}, session.OriginUser, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, conv := sessionx.CreateOrigin(&tc.req)
			if got != tc.want || conv != tc.wantConv {
				t.Fatalf("origin = %q/%q, want %q/%q", got, conv, tc.want, tc.wantConv)
			}
		})
	}
}

// Existing sessions (no such field) are unknown — neither 0 nor user.
func TestOriginOfDefaultsToUnknown(t *testing.T) {
	if got := session.OriginOf(session.Meta{Name: "old"}); got != session.OriginUnknown {
		t.Fatalf("origin = %q, want %q", got, session.OriginUnknown)
	}
	if got := session.OriginOf(session.Meta{Name: "new", Origin: session.OriginSchedule}); got != session.OriginSchedule {
		t.Fatalf("origin = %q", got)
	}
}

func TestUsageTriggerFromTurnSource(t *testing.T) {
	for src, want := range map[string]string{
		"":                                usagex.TriggerUser,
		sessionx.TurnSourceOperator:       usagex.TriggerOperator,
		sessionx.TurnSourceDiscord:        usagex.TriggerBridge,
		sessionx.TurnSourceSlack:          usagex.TriggerBridge,
		sessionx.TurnSourceSchedule:       usagex.TriggerSchedule,
		sessionx.TurnSourceScheduleManual: usagex.TriggerSchedule,
	} {
		if got := usageTriggerFromTurnSource(src); got != want {
			t.Errorf("%q -> %q, want %q", src, got, want)
		}
	}
}

func TestCompactTriggerMapping(t *testing.T) {
	for reason, want := range map[string]string{
		chatx.CompactReasonManual:   usagex.TriggerManual,
		chatx.CompactReasonAuto:     usagex.TriggerAuto,
		chatx.CompactReasonRecovery: usagex.TriggerRecovery,
	} {
		if got := chatx.CompactTrigger(reason); got != want {
			t.Errorf("%q -> %q, want %q", reason, got, want)
		}
	}
}

// --- fault tolerance of folding (P1 review regressions) ---------------------------

// If a row could not be written, the watermark is not advanced. Advancing it keeps that spend out
// of the diff on every later pass, so it never enters the ledger again (the ledger side has no
// way to pick up what it missed).
func TestFoldDoesNotAdvanceWatermarkWhenAppendFails(t *testing.T) {
	dir := useTempUsageDir(t)
	turns := []transcript.Turn{
		{Role: "user", Text: "1"},
		asst("claude-haiku-4-5", 100, 10, 0, 20, false),
		{Role: "user", Text: "2"},
		asst("claude-haiku-4-5", 200, 20, 0, 0, false),
		{Role: "user", Text: "3"}, // the trailing turn is closed, so both turns can be folded
	}
	m := session.Meta{Name: "slot01", Kind: session.KindClaude}

	// Put a file where raw/ belongs so the append fails (MkdirAll dies with ENOTDIR).
	raw := filepath.Join(dir, "raw")
	if err := os.WriteFile(raw, []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := readUsageFoldState()
	n, err := foldSessionUsageWithTurns(m, &st, turns, false)
	if err == nil {
		t.Fatal("the append failed but no error was returned")
	}
	if n != 0 {
		t.Fatalf("a failed fold reported %d rows, want 0", n)
	}
	if mark, ok := st.Sessions[m.Name]; ok {
		t.Fatalf("the append failed but the watermark advanced: %+v", mark)
	}

	// Once writing works again, everything goes in with nothing lost.
	if err := os.Remove(raw); err != nil {
		t.Fatal(err)
	}
	if n, err = foldSessionUsageWithTurns(m, &st, turns, false); err != nil || n != 2 {
		t.Fatalf("fold after recovery = %d rows / err=%v, want 2 / nil", n, err)
	}
	if rows := usagex.ReadRows(); len(rows) != 2 {
		t.Fatalf("ledger = %d rows, want 2", len(rows))
	}
	if st.Sessions[m.Name].Groups != 2 {
		t.Fatalf("watermark = %+v, want groups=2", st.Sessions[m.Name])
	}
}

// commitSessionUsageFold writes through to the watermark once per session. Writing it once at the
// end of the pass instead duplicates every session folded so far on the next pass whenever the
// pass dies partway.
func TestCommitSessionUsageFoldPersistsWatermarkPerSession(t *testing.T) {
	useIsolatedUsageDir(t)
	m := session.Meta{Name: "slot01", Kind: session.KindShell} // a kind with no transcript
	session.WriteMeta(m)
	if _, err := commitSessionUsageFold(m, false); err != nil {
		t.Fatalf("commit failed: %v", err)
	}
	// No transcript, so neither rows nor watermarks grow (no empty state scattered around).
	if rows := usagex.ReadRows(); len(rows) != 0 {
		t.Fatalf("ledger = %d rows, want 0", len(rows))
	}

	// A session that has a watermark has it on disk the moment that one session is folded.
	usageFoldMu.Lock()
	st := readUsageFoldState()
	st.Sessions["slot02"] = usageFoldMark{Groups: 3}
	if err := writeUsageFoldState(st); err != nil {
		t.Fatalf("writing the watermark failed: %v", err)
	}
	usageFoldMu.Unlock()
	if got := readUsageFoldState().Sessions["slot02"].Groups; got != 3 {
		t.Fatalf("watermark after re-reading = %d, want 3", got)
	}
}

// fold-on-read may only ask "is a pass running?"; it must not wait on the fold's own lock.
// Waiting blocks the second and later of the three /usage/series requests the Console's usage
// view fires on one screen, for as long as the bulk fold (measured ~20 s) takes.
func TestFoldOnReadDoesNotBlockOnRunningPass(t *testing.T) {
	useIsolatedUsageDir(t)
	usageFoldMu.Lock() // stand in for the lock a running fold holds
	done := make(chan struct{})
	go func() {
		maybeFoldSessionUsage()
		close(done)
	}()
	select {
	case <-done:
		usageFoldMu.Unlock()
	case <-time.After(2 * time.Second):
		usageFoldMu.Unlock()
		t.Fatal("fold-on-read blocked waiting for the fold's own lock")
	}
	// Collect the async pass we started before leaving (no global state carried into the next
	// test). Waiting only 2 seconds and leaving silently is not enough: the fold reads its
	// destination from the env at the moment it writes, so once the next test swaps
	// `AF_USAGE_DIR` it writes into that test's ledger. If the limit cannot be waited out, fail
	// rather than move on quietly.
	waitUsageFoldIdle(t)
}

// waitUsageFoldIdle collects the running bulk fold. It shares globals (the running flag, the
// throttle timestamp, the destination env), so leaking one across tests makes the next test fail
// or skip for no reason of its own. If the wait cannot be completed, fail instead of moving on
// quietly — tolerating it lets the leaked writer write into another test's ledger and turn that
// test red with no visible cause.
//
// The limit is chosen to cover a bulk fold on a real machine (a dev box with hundreds of sessions
// in HOME). Measured 2026-09-02: 0.58 s for 200 synthetic sessions of one transcript each. Real
// transcripts are an order of magnitude larger (measured on the code side: 158 sessions, ~20 s).
func waitUsageFoldIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for usageFoldRunning.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if usageFoldRunning.Load() {
		t.Fatal("the bulk fold did not finish in 60s (cannot move on leaving a running writer behind)")
	}
}

// resetUsageFold returns the fold-on-read throttle to its unused state, both before and after.
func resetUsageFold(t *testing.T) {
	t.Helper()
	clear := func() {
		usageFoldGate.Lock()
		usageFoldedAt = time.Time{}
		usageFoldGate.Unlock()
	}
	waitUsageFoldIdle(t)
	clear()
	t.Cleanup(func() {
		waitUsageFoldIdle(t)
		clear()
	})
}

// seedClaudeSessions plants n claude sessions that have a transcript (1 session = 1 logical turn),
// so the fold has real work to do. On CI, where HOME is empty, the fold finishes instantly and a
// "does not wait" defect never surfaces.
func seedClaudeSessions(t *testing.T, n int) {
	t.Helper()
	home := os.Getenv("HOME")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	dir := filepath.Join(home, ".claude", "projects", "-proj")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		m := session.Meta{Name: fmt.Sprintf("slot%03d", i), Dir: t.TempDir(), Kind: session.KindClaude}
		session.WriteMeta(m)
		// The trailing user turn closes the logical turn (an open one is not picked up).
		body := `{"type":"user","timestamp":"2026-07-26T01:00:00Z","message":{"content":"go"}}` + "\n" +
			`{"type":"assistant","timestamp":"2026-07-26T01:00:01Z","message":{"model":"claude-haiku-4-5",` +
			`"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":100,"output_tokens":10}}}` + "\n" +
			`{"type":"user","timestamp":"2026-07-26T02:00:00Z","message":{"content":"next"}}` + "\n"
		p := filepath.Join(dir, session.UUID(m.Dir, m.Name)+".jsonl")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// A bulk fold started by an earlier test must not write into the next test's ledger.
//
// The fold runs in a goroutine and reads its destination (`AF_USAGE_DIR`) from the env at the
// moment it writes. The env is process-wide, so moving on to the next test while it runs grows
// extra rows in that test with no cause inside it (the symptom hit for real was "ledger = 39
// rows, want 4", one extra row per session that happened to be on the dev machine). CI's HOME has
// no sessions at all and the fold finishes instantly, so it is green only there and red for no
// apparent reason only on a dev machine.
//
// To keep this off the clock, a lock stands in for the real machine's "hundreds of ms to tens of
// seconds for 200 sessions".
func TestFoldDoesNotWriteIntoTheNextTestsLedger(t *testing.T) {
	const sessions = 5
	prev := useIsolatedUsageDir(t)
	seedClaudeSessions(t, sessions)

	// --- "the earlier test": start fold-on-read and try to finish while it is still running.
	usageFoldMu.Lock() // the fold itself stops here
	if !startFoldSessionUsage(true) {
		usageFoldMu.Unlock()
		t.Fatal("fold-on-read did not start")
	}
	go func() {
		time.Sleep(100 * time.Millisecond) // gets going about when the next test starts
		usageFoldMu.Unlock()
	}()

	// --- "the next test": swap in its own ledger. Unless the running writer was collected here,
	// the fold writes into this new directory.
	next := useTempUsageDir(t)

	// Do not use `usageFoldRunning` to decide it is done: a version that fixed this defect the
	// wrong way clears the flag, so reading it means mistaking "already finished" and the check
	// passes before any write happens (mutation testing did slip through that way). Wait until a
	// row appears in either ledger, then look at which one it landed in.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && countUsageRowsIn(t, prev)+countUsageRowsIn(t, next) < sessions {
		time.Sleep(10 * time.Millisecond)
	}
	if n := countUsageRowsIn(t, next); n != 0 {
		t.Fatalf("the next test's ledger = %d rows, want 0 (the earlier test's fold wrote into %s)", n, next)
	}
	// Guard against a vacuous pass: if the fold wrote nothing at all, the 0 rows above prove
	// nothing.
	if n := countUsageRowsIn(t, prev); n != sessions {
		t.Fatalf("the earlier test's ledger = %d rows, want %d (the fold did not run, so this check is vacuous)", n, sessions)
	}
}

// countUsageRowsIn counts the rows in a ledger directory without going through the env (to see
// which of the two ledgers was written to).
func countUsageRowsIn(t *testing.T, dir string) int {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "raw", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, ln := range strings.Split(string(b), "\n") {
			if strings.TrimSpace(ln) != "" {
				n++
			}
		}
	}
	return n
}

// "Refresh" has to be able to skip the throttle. Without that, turns that had already finished
// when the button was pressed stay out for up to a minute and the user keeps pressing until it
// catches up (exactly the "takes a few presses to be up to date" seen on this screen).
func TestStartFoldSessionUsageForceSkipsThrottle(t *testing.T) {
	useIsolatedUsageDir(t)
	resetUsageFold(t)

	if !startFoldSessionUsage(false) {
		t.Fatal("the first fold-on-read did not start")
	}
	waitUsageFoldIdle(t)

	// A normal read right after is skipped by the throttle. Nothing is running, so folding is not
	// reported (reporting it makes the Console re-fetch forever).
	if startFoldSessionUsage(false) {
		t.Fatal("a read during the throttle claimed a fold was in progress")
	}
	// force skips that throttle only (the double-start guard stays).
	if !startFoldSessionUsage(true) {
		t.Fatal("force hit the throttle and did not start")
	}
	waitUsageFoldIdle(t)
}

// finalizeSessionUsage forgets the watermark of a session that is going away. Keeping it makes
// state.json grow monotonically by every session that no longer exists.
func TestFinalizeSessionUsageForgetsWatermark(t *testing.T) {
	useIsolatedUsageDir(t)
	m := session.Meta{Name: "slot01", Kind: session.KindShell}
	usageFoldMu.Lock()
	st := readUsageFoldState()
	st.Sessions[m.Name] = usageFoldMark{Groups: 5}
	if err := writeUsageFoldState(st); err != nil {
		t.Fatalf("writing the watermark failed: %v", err)
	}
	usageFoldMu.Unlock()

	finalizeSessionUsage(m)
	if mark, ok := readUsageFoldState().Sessions[m.Name]; ok {
		t.Fatalf("the watermark is still there after removal: %+v", mark)
	}
}

// Every path that forgets a meta must settle usage before forgetting it. The moment it is
// forgotten the session drops out of ListMetas and is never folded again, so an open trailing turn
// not settled here never enters the ledger. handleStopSession (the Console's Delete) did miss it.
func TestMetaRemovalPathsFinalizeUsage(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, d := range af.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			removes, finalizes := false, false
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fn := call.Fun.(type) {
				case *ast.SelectorExpr:
					if x, ok := fn.X.(*ast.Ident); ok && x.Name == "session" && fn.Sel.Name == "RemoveMeta" {
						removes = true
					}
				case *ast.Ident:
					if fn.Name == "finalizeSessionUsage" {
						finalizes = true
					}
				}
				return true
			})
			if !removes {
				continue
			}
			checked++
			if !finalizes {
				t.Errorf("%s: %s calls session.RemoveMeta but not finalizeSessionUsage"+
					" (docs/log/46 §3-b: settle the trailing turn before forgetting the meta)", name, fd.Name.Name)
			}
		}
	}
	if checked == 0 {
		t.Fatal("found no call to session.RemoveMeta at all (the scan is broken)")
	}
	t.Logf("checked %d meta-removal paths", checked)
}

// asstAt is an assistant event at a given timestamp (for assembling a transcript switch).
func asstAt(ts string, in, out int) transcript.Turn {
	t := asst("claude-haiku-4-5", in, out, 0, 0, false)
	t.TS = ts
	return t
}

// P2-8: claude can have sibling jsonl files under one sid, and the one that is read is chosen by
// mtime (internal/agents/claude/transcript.go). Taking the diff by count alone means that the
// moment the selection switches, folding either stops forever (switch to the shorter file) or
// drops the leading turns of another conversation as already folded (switch to the longer one).
// Decide on both count and timestamp.
func TestFoldSurvivesTranscriptFileSwitch(t *testing.T) {
	useTempUsageDir(t)
	m := session.Meta{Name: "slot01", Kind: session.KindClaude}
	fold := func(turns []transcript.Turn, st *usageFoldState) int {
		t.Helper()
		n, err := foldSessionUsageWithTurns(m, st, turns, false)
		if err != nil {
			t.Fatalf("fold failed: %v", err)
		}
		return n
	}
	st := readUsageFoldState()

	// The main transcript: 3 logical turns (closed by the trailing user turn).
	main3 := []transcript.Turn{
		{Role: "user"}, asstAt("2026-07-26T01:00:00Z", 100, 10),
		{Role: "user"}, asstAt("2026-07-26T02:00:00Z", 200, 20),
		{Role: "user"}, asstAt("2026-07-26T03:00:00Z", 300, 30),
		{Role: "user"},
	}
	if n := fold(main3, &st); n != 3 {
		t.Fatalf("first pass = %d rows, want 3", n)
	}

	// mtime swung to the sibling file (shorter, but newer). A count-based diff stops here forever.
	sibling := []transcript.Turn{
		{Role: "user"}, asstAt("2026-07-26T04:00:00Z", 400, 40),
		{Role: "user"}, asstAt("2026-07-26T05:00:00Z", 500, 50),
		{Role: "user"},
	}
	if n := fold(sibling, &st); n != 2 {
		t.Fatalf("new turns of the switched transcript = %d rows, want 2 (count alone gives 0)", n)
	}
	if got := st.Sessions[m.Name]; got.Groups != 3 || got.LastTS != "2026-07-26T05:00:00Z" {
		t.Fatalf("watermark = %+v, want groups 3 (never lowered) / lastTS 05:00", got)
	}

	// A swing to an older sibling (a stub) picks up nothing — a finished conversation is not
	// recounted.
	oldStub := []transcript.Turn{
		{Role: "user"}, asstAt("2026-07-25T01:00:00Z", 900, 90),
		{Role: "user"},
	}
	if n := fold(oldStub, &st); n != 0 {
		t.Fatalf("picked up %d rows from the older transcript, want 0", n)
	}

	// When the main file grows, only the continuation is picked up (ordinary incremental folding
	// is unchanged).
	grown := append(append([]transcript.Turn{}, main3...),
		asstAt("2026-07-26T06:00:00Z", 600, 60), transcript.Turn{Role: "user"})
	if n := fold(grown, &st); n != 1 {
		t.Fatalf("continuation of the grown main file = %d rows, want 1", n)
	}
	rows := usagex.ReadRows()
	if len(rows) != 6 {
		t.Fatalf("ledger = %d rows, want 6", len(rows))
	}
	spend := 0
	for _, r := range rows {
		spend += r.Spend
	}
	if want := 110 + 220 + 330 + 440 + 550 + 660; spend != want {
		t.Fatalf("spend total = %d, want %d", spend, want)
	}
}

// P3-10: even if the process dies with an append only partly written, recovery takes everything
// in with nothing missing and nothing extra. The watermark is not advanced, so the next pass
// re-appends the same turns and the aggregation side's (ref, idx) dedup drops the overlap — a
// regression on the writer and the reader meshing as a two-stage defence.
func TestFoldRecoversAfterPartialAppend(t *testing.T) {
	useIsolatedUsageDir(t)
	m := session.Meta{Name: "slot01", Kind: session.KindClaude}
	turns := []transcript.Turn{
		{Role: "user"}, asstAt("2026-07-26T01:00:00Z", 100, 10),
		{Role: "user"}, asstAt("2026-07-26T02:00:00Z", 200, 20),
		{Role: "user"},
	}
	// Build the state where only the first turn made it to disk before the crash (no watermark
	// written).
	st := readUsageFoldState()
	partial := usageFoldState{Sessions: map[string]usageFoldMark{}}
	if _, err := foldSessionUsageWithTurns(m, &partial, turns[:2], true); err != nil {
		t.Fatal(err)
	}
	if n := len(usagex.ReadRows()); n != 1 {
		t.Fatalf("the premise is broken: ledger = %d rows, want 1", n)
	}

	// The pass after recovery runs from watermark 0, so it re-appends the first turn.
	if n, err := foldSessionUsageWithTurns(m, &st, turns, false); err != nil || n != 2 {
		t.Fatalf("fold after recovery = %d rows / err=%v, want 2 / nil", n, err)
	}
	if n := len(usagex.ReadRows()); n != 3 {
		t.Fatalf("ledger = %d rows, want 3 (one stays as a duplicate)", n)
	}
	got := getSeries(t, "from=2026-07-26&to=2026-07-26")
	if want := 110 + 220; got.Totals.Spend != want || got.Totals.Calls != 2 {
		t.Fatalf("aggregate = %+v, want spend %d / calls 2 (is the duplicate being counted?)", got.Totals, want)
	}
}
