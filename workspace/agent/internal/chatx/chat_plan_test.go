package chatx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseCompactOutputSplitsBlocks(t *testing.T) {
	out := planMarker + "\n## 制約\n- gradle は2本まで\n## これからやること\n- Lane A を起こす\n\n" +
		summaryMarker + "\n請求バッチ改修の統括会話。"
	plan, summary := parseCompactOutput(out)
	if !strings.HasPrefix(plan, "## 制約") || !strings.Contains(plan, "Lane A を起こす") {
		t.Fatalf("plan = %q", plan)
	}
	if summary != "請求バッチ改修の統括会話。" {
		t.Fatalf("summary = %q", summary)
	}
}

// The heart of the degraded path: output that ignored the markers must never wipe the plan
// in use. parseCompactOutput returns plan="" and the caller (compactConversation) keeps the
// existing Plan.
func TestParseCompactOutputMalformedKeepsPlanEmpty(t *testing.T) {
	for name, out := range map[string]string{
		"markers missing": "ここまでの要約です。",
		"order swapped":   summaryMarker + "\n要約\n" + planMarker + "\n計画",
		"plan only":       planMarker + "\n## これからやること\n- A",
	} {
		plan, summary := parseCompactOutput(out)
		if plan != "" {
			t.Fatalf("%s: plan should stay empty, got %q", name, plan)
		}
		if summary == "" || strings.Contains(summary, planMarker) || strings.Contains(summary, summaryMarker) {
			t.Fatalf("%s: summary = %q", name, summary)
		}
	}
}

func TestParseCompactOutputStripsFenceAndBlankPlan(t *testing.T) {
	out := "```\n" + planMarker + "\nなし\n" + summaryMarker + "\n短い会話。\n```"
	plan, summary := parseCompactOutput(out)
	if plan != "" {
		t.Fatalf("placeholder plan should be treated as empty: %q", plan)
	}
	if summary != "短い会話。" {
		t.Fatalf("summary = %q", summary)
	}
}

// A malformation where only the summary is empty must not fail the compaction itself; the
// plan is reused as the summary.
func TestParseCompactOutputEmptySummaryFallsBackToPlan(t *testing.T) {
	plan, summary := parseCompactOutput(planMarker + "\n## これからやること\n- A\n" + summaryMarker + "\n   ")
	if plan == "" || summary != plan {
		t.Fatalf("plan=%q summary=%q", plan, summary)
	}
}

func TestSetPlanReportsChangeOnly(t *testing.T) {
	c := &ChatConversation{}
	if !setPlan(c, "## これからやること\n- A") {
		t.Fatal("first plan must count as a change")
	}
	first := c.PlanUpdatedAt
	if setPlan(c, "  ## これからやること\n- A  ") {
		t.Fatal("same plan (modulo trim) must not count as a change")
	}
	if c.PlanUpdatedAt != first {
		t.Fatal("unchanged plan must not bump PlanUpdatedAt")
	}
	if !setPlan(c, "") {
		t.Fatal("clearing is a change (a hand edit clearing the plan)")
	}
	if c.Plan != "" {
		t.Fatalf("plan not cleared: %q", c.Plan)
	}
}

func TestClampPlanCaps(t *testing.T) {
	long := strings.Repeat("あ", planMaxRunes+500)
	c := &ChatConversation{}
	setPlan(c, long)
	if len([]rune(c.Plan)) > planMaxRunes+40 {
		t.Fatalf("plan not clamped: %d runes", len([]rune(c.Plan)))
	}
}

// injectPlan rides only on a turn that starts a NEW native session. Riding on a turn whose
// resume is still live pays the same plan's input tokens twice, every time.
func TestInjectPlanOnlyOnFreshSession(t *testing.T) {
	c := &ChatConversation{Plan: "## これからやること\n- Lane A"}
	p, carried := InjectPlan(c, "claude", "続けて")
	if !carried || !strings.HasPrefix(p, PlanPreambleFor("ja")) || !strings.HasSuffix(p, "続けて") {
		t.Fatalf("fresh Session: (%q, %v)", p, carried)
	}
	c.ClaudeSessionID = "live"
	if p, carried = InjectPlan(c, "claude", "続けて"); carried || p != "続けて" {
		t.Fatalf("resumable Session: (%q, %v)", p, carried)
	}
	// A turn that switches to another backend is a new session as far as that backend goes.
	if _, carried = InjectPlan(c, "codex", "続けて"); !carried {
		t.Fatal("switching backend must carry the plan")
	}
	c.Plan = ""
	if _, carried = InjectPlan(c, "codex", "続けて"); carried {
		t.Fatal("no plan, nothing to carry")
	}
}

// The order is summary, then plan, then the message itself. The plan is the instruction to
// be followed right now, so it sits immediately before the message.
func TestInjectCarryoverOrder(t *testing.T) {
	c := &ChatConversation{Plan: "計画本文", PendingHandoff: "要約本文"}
	p, carried := InjectCarryover(c, "claude", "本題")
	if !carried {
		t.Fatal("handoff must be reported as carried")
	}
	iSummary := strings.Index(p, "要約本文")
	iPlan := strings.Index(p, "計画本文")
	iBody := strings.Index(p, "本題")
	if iSummary < 0 || iPlan < 0 || iBody < 0 || !(iSummary < iPlan && iPlan < iBody) {
		t.Fatalf("order wrong (summary=%d plan=%d body=%d): %q", iSummary, iPlan, iBody, p)
	}
	// The plan is not consumed: unlike the summary it keeps being carried into later sessions.
	if c.Plan == "" {
		t.Fatal("injectCarryover must not consume the plan")
	}
}

func TestCompactPromptCarriesExistingPlanForDiffUpdate(t *testing.T) {
	c := &ChatConversation{}
	if strings.Contains(CompactPrompt(c), PlanUpdateInstructionFor("ja")) {
		t.Fatal("no plan yet: must not ask for a diff update")
	}
	c.Plan = "## これからやること\n- Lane A"
	p := CompactPrompt(c)
	if !strings.Contains(p, PlanUpdateInstructionFor("ja")) || !strings.Contains(p, "Lane A") {
		t.Fatalf("existing plan not offered for diff update: %q", p)
	}
	if !strings.Contains(p, CompactSummaryPromptFor("ja")) {
		t.Fatal("summary instruction lost")
	}
}

func TestCompactConversationStoresPlanAndNotices(t *testing.T) {
	c := &ChatConversation{ID: RandUUID(), Agent: "claude", ClaudeSessionID: "old"}
	prov := &stubProvider{reply: planMarker + "\n## これからやること\n- Lane A を起こす\n" +
		summaryMarker + "\n統括会話の背景。"}
	if err := compactConversation(context.Background(), c, prov, CompactReasonAuto); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.Plan, "Lane A を起こす") {
		t.Fatalf("plan not stored: %q", c.Plan)
	}
	if c.PendingHandoff != "統括会話の背景。" {
		t.Fatalf("handoff = %q", c.PendingHandoff)
	}
	if c.ClaudeSessionID != "" {
		t.Fatal("resume handle not cleared")
	}
	keys := noticeKeys(c)
	if len(keys) != 2 || keys[0] != noticeKeyCompactAuto || keys[1] != noticeKeyPlanUpdated {
		t.Fatalf("notices = %v", keys)
	}
}

// When a second compaction leaves the plan unchanged, no plan card is appended: it would
// bury the one card that marks a plan that really did move.
func TestCompactConversationSilentWhenPlanUnchanged(t *testing.T) {
	c := &ChatConversation{ID: RandUUID(), Agent: "claude", ClaudeSessionID: "old",
		Plan: "## これからやること\n- Lane A を起こす"}
	prov := &stubProvider{reply: planMarker + "\n## これからやること\n- Lane A を起こす\n" +
		summaryMarker + "\n背景。"}
	if err := compactConversation(context.Background(), c, prov, CompactReasonAuto); err != nil {
		t.Fatal(err)
	}
	for _, k := range noticeKeys(c) {
		if k == noticeKeyPlanUpdated {
			t.Fatal("unchanged plan must not append a plan notice")
		}
	}
}

// Compacting on malformed output still leaves the plan in use alive; this is stage 5's most
// important degraded path.
func TestCompactConversationKeepsPlanOnMalformedOutput(t *testing.T) {
	c := &ChatConversation{ID: RandUUID(), Agent: "claude", ClaudeSessionID: "old",
		Plan: "## これからやること\n- Lane A を起こす"}
	prov := &stubProvider{reply: "区切りを無視した要約だけの出力。"}
	if err := compactConversation(context.Background(), c, prov, CompactReasonManual); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.Plan, "Lane A を起こす") {
		t.Fatalf("plan lost on malformed output: %q", c.Plan)
	}
	if c.PendingHandoff != "区切りを無視した要約だけの出力。" {
		t.Fatalf("handoff = %q", c.PendingHandoff)
	}
}

func TestCompactConversationFailureKeepsPlan(t *testing.T) {
	c := &ChatConversation{ID: RandUUID(), Agent: "claude", ClaudeSessionID: "old", Plan: "計画"}
	if err := compactConversation(context.Background(), c, &stubProvider{err: errors.New("boom")},
		CompactReasonManual); err == nil {
		t.Fatal("expected error")
	}
	if c.Plan != "計画" {
		t.Fatalf("plan mutated on failure: %q", c.Plan)
	}
}

func TestPlanRefreshPromptShape(t *testing.T) {
	c := &ChatConversation{Messages: []ChatMessage{
		{Role: "user", Content: "Wave 2 は Lane 2 を先に回して"},
		{Role: "notice", Content: "コンテキストを圧縮しました"},
		{Role: "report", Content: "セッション完了"},
		{Role: "assistant", Content: "了解。順序を入れ替える"},
	}}
	p := planRefreshPrompt(c, "ja")
	if !strings.Contains(p, "Wave 2 は Lane 2 を先に回して") || !strings.Contains(p, "順序を入れ替える") {
		t.Fatalf("recent turns missing: %q", p)
	}
	if strings.Contains(p, "コンテキストを圧縮しました") || strings.Contains(p, "セッション完了") {
		t.Fatal("notice/report must stay out of the plan context window")
	}
	if !strings.Contains(p, PlanShapeFor("ja")) {
		t.Fatal("plan shape missing")
	}
	c.Plan = "## これからやること\n- 旧順序"
	if !strings.Contains(planRefreshPrompt(c, "ja"), "旧順序") {
		t.Fatal("existing plan must be offered as the base for a diff update")
	}
}

func TestPlanContextTurnsWindow(t *testing.T) {
	var msgs []ChatMessage
	for i := 0; i < planTailTurns+5; i++ {
		msgs = append(msgs, ChatMessage{Role: "user", Content: "m"})
	}
	if got := len(planContextTurns(msgs)); got != planTailTurns {
		t.Fatalf("window = %d", got)
	}
	if len(planContextTurns(nil)) != 0 {
		t.Fatal("empty conversation must yield no turns")
	}
}

func noticeKeys(c *ChatConversation) []string {
	var out []string
	for _, m := range c.Messages {
		if m.Role == "notice" {
			out = append(out, m.NoticeKey)
		}
	}
	return out
}

// --- HTTP (hand edit / via MCP) -----------------------------------------------

func planMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /chat/conversations/{id}/plan", HandleChatPlanGet)
	mux.HandleFunc("PUT /chat/conversations/{id}/plan", HandleChatPlanSet)
	return mux
}

func TestHandleChatPlanGetSetRoundTrip(t *testing.T) {
	withTempHome(t)
	mux := planMux()
	c := &ChatConversation{ID: RandUUID(), Agent: "claude", Messages: []ChatMessage{}}
	if err := SaveConv(c); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("GET", "/chat/conversations/"+RandUUID()+"/plan", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown conv: code = %d", rr.Code)
	}

	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("PUT", "/chat/conversations/"+c.ID+"/plan",
		strings.NewReader(`{"plan":"## これからやること\n- Lane A"}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("put: code = %d body = %s", rr.Code, rr.Body.String())
	}

	// GET returns the plan alone: returning the whole conversation would hand the entire
	// conversation back to the model through MCP.
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("GET", "/chat/conversations/"+c.ID+"/plan", nil))
	var got struct {
		Plan      string `json:"plan"`
		UpdatedAt int64  `json:"plan_updated_at"`
		Messages  []any  `json:"messages"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Plan, "Lane A") || got.UpdatedAt == 0 || got.Messages != nil {
		t.Fatalf("get = %s", rr.Body.String())
	}
}

// A hand edit from the Console appends no notice; going through MCP (notice:true) always
// does. That is the only path on which the plan moves while the user is not watching, so it
// is the only one that leaves a trace.
func TestHandleChatPlanSetNoticeOnlyWhenAsked(t *testing.T) {
	withTempHome(t)
	mux := planMux()
	for name, tc := range map[string]struct {
		body   string
		notice bool
	}{
		"hand edit": {`{"plan":"## これからやること\n- A"}`, false},
		"via mcp":   {`{"plan":"## これからやること\n- A","notice":true}`, true},
		"unchanged": {`{"plan":"## これからやること\n- A"}`, false},
	} {
		c := &ChatConversation{ID: RandUUID(), Agent: "claude", Messages: []ChatMessage{}}
		if name == "unchanged" {
			c.Plan = "## これからやること\n- A"
		}
		if err := SaveConv(c); err != nil {
			t.Fatal(err)
		}
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest("PUT", "/chat/conversations/"+c.ID+"/plan", strings.NewReader(tc.body)))
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: code = %d", name, rr.Code)
		}
		saved, err := LoadConv(c.ID)
		if err != nil {
			t.Fatal(err)
		}
		has := false
		for _, k := range noticeKeys(saved) {
			has = has || k == noticeKeyPlanUpdated
		}
		if has != tc.notice {
			t.Fatalf("%s: plan notice = %v, want %v", name, has, tc.notice)
		}
	}
}
