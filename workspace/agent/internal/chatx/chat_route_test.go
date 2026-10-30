package chatx

// Routing around a rate-limited backend (#1564): the send-time choice, the re-run of a failed
// turn, and the one-shot and deny-all resolvers reading the same cooldown table.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routehealth"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// routeT0 is 18:00 in Tokyo, so the measured banner "resets 7:50pm (Asia/Tokyo)" is a reset
// 1 h 50 min ahead, well past NearRecovery.
var routeT0 = time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)

const limitErr = "claude returned an error: You've hit your session limit · resets 7:50pm (Asia/Tokyo)"

// useRouteTable gives the test its own cooldown table and a fixed routing clock, and marks
// every kind of the default order logged in so no real CLI is asked.
func useRouteTable(t *testing.T) *routehealth.Table {
	t.Helper()
	prevTable, prevNow := routehealth.Default, routeNow
	tb := routehealth.NewTable()
	routehealth.Default, routeNow = tb, func() time.Time { return routeT0 }
	t.Cleanup(func() { routehealth.Default, routeNow = prevTable, prevNow })
	for _, k := range DefaultHeadlessOrder {
		forceHeadlessAvailable(t, k, true)
	}
	forceHeadlessAvailable(t, session.KindMuse, false)
	return tb
}

type scriptedProvider struct {
	reply   string
	err     error
	prompts *[]string
}

func (p scriptedProvider) Send(_ context.Context, _ *ChatConversation, prompt string) (string, error) {
	if p.prompts != nil {
		*p.prompts = append(*p.prompts, prompt)
	}
	return p.reply, p.err
}

// streamingScripted streams one delta and then fails, the shape of a turn cut off mid-reply.
type streamingScripted struct{ scriptedProvider }

func (p streamingScripted) SendStream(ctx context.Context, c *ChatConversation, prompt string, emit func(ChatStreamEvent)) (string, []chatStep, error) {
	emit(ChatStreamEvent{Delta: "partial"})
	r, err := p.Send(ctx, c, prompt)
	return r, nil, err
}

func TestChatRouteSkipsAParkedPin(t *testing.T) {
	writeUIPrefs(t, `{}`)
	tb := useRouteTable(t)
	c := &ChatConversation{Agent: session.KindClaude, Model: "model-a"}

	if _, k := chatRoute(c); k != session.KindClaude {
		t.Fatalf("unparked: route = %q, want the pin", k)
	}
	tb.Fail(session.KindClaude, "model-a", limitErr, routeT0)
	if _, k := chatRoute(c); k != session.KindCodex {
		t.Fatalf("pin on a usage window: route = %q, want codex (next in the order)", k)
	}
	// Every backend parked: the turn still goes to the pin rather than nowhere.
	for _, k := range DefaultHeadlessOrder {
		tb.Fail(k, "", "You've hit your usage limit.", routeT0)
	}
	if _, k := chatRoute(c); k != session.KindClaude {
		t.Fatalf("all parked: route = %q, want the pin", k)
	}
}

// TestChatRouteModelParkStaysOnThatModel: a 5xx parks one model, so a conversation on another
// model of the same backend keeps it.
func TestChatRouteModelParkStaysOnThatModel(t *testing.T) {
	writeUIPrefs(t, `{}`)
	tb := useRouteTable(t)
	tb.Park(session.KindClaude, "model-a",
		routehealth.Verdict{Class: routehealth.ClassServer, Scope: routehealth.ScopeModel, ResetAt: routeT0.Add(time.Hour)}, routeT0)

	if _, k := chatRoute(&ChatConversation{Agent: session.KindClaude, Model: "model-a"}); k != session.KindCodex {
		t.Errorf("parked model: route = %q, want codex", k)
	}
	if _, k := chatRoute(&ChatConversation{Agent: session.KindClaude, Model: "model-b"}); k != session.KindClaude {
		t.Errorf("other model: route = %q, want claude", k)
	}
}

func TestFailoverTurnOnlyOnACoolingVerdict(t *testing.T) {
	writeUIPrefs(t, `{}`)
	useRouteTable(t)
	c := &ChatConversation{Agent: session.KindClaude, Model: "model-a"}
	ctx := context.Background()

	v := NoteTurnResult(ctx, c, session.KindClaude, errors.New("error: model_not_found"))
	if _, _, _, ok := FailoverTurn(c, session.KindClaude, v); ok {
		t.Fatal("a config error moved the turn")
	}
	v = NoteTurnResult(ctx, c, session.KindClaude, errors.New(limitErr))
	_, kind, notice, ok := FailoverTurn(c, session.KindClaude, v)
	if !ok || kind != session.KindCodex {
		t.Fatalf("usage window: failover = (%q, %v), want codex", kind, ok)
	}
	if notice.Role != "notice" || notice.NoticeKey != noticeKeyAgentFailover ||
		notice.NoticeArgs["from"] != session.KindClaude || notice.NoticeArgs["to"] != session.KindCodex ||
		notice.NoticeArgs["agent"] != session.KindClaude {
		t.Fatalf("notice = %+v", notice)
	}
	// A stopped turn says nothing about the backend.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if v := NoteTurnResult(cancelled, c, session.KindCodex, errors.New("429 Too Many Requests")); v.Cools() {
		t.Fatal("a cancelled turn was classified")
	}
}

func sendTurn(t *testing.T, id, content string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/chat/conversations/"+id+"/send", strings.NewReader(`{"content":"`+content+`"}`))
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	HandleChatSend(rec, req)
	return rec
}

// TestHandleChatSendFailsOverOnALimit: the turn that hit claude's usage window is answered by
// codex, which receives the user's message, and the thread records the move.
func TestHandleChatSendFailsOverOnALimit(t *testing.T) {
	writeUIPrefs(t, `{}`)
	t.Cleanup(WaitInterimDeliveries)
	tb := useRouteTable(t)
	var codexPrompts []string
	stubChatProvider(t, session.KindClaude, scriptedProvider{err: errors.New(limitErr)})
	stubChatProvider(t, session.KindCodex, scriptedProvider{reply: "codex answer", prompts: &codexPrompts})
	conv := &ChatConversation{ID: RandUUID(), Slug: NewConvSlug(), Agent: session.KindClaude, Model: "model-a"}
	if err := SaveConv(conv); err != nil {
		t.Fatal(err)
	}

	rec := sendTurn(t, conv.ID, "hello there")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(codexPrompts) != 1 || !strings.Contains(codexPrompts[0], "hello there") {
		t.Fatalf("codex prompts = %q", codexPrompts)
	}
	c, err := LoadConv(conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	n := len(c.Messages)
	if n != 3 || c.Messages[1].NoticeKey != noticeKeyAgentFailover ||
		c.Messages[2].Role != "assistant" || c.Messages[2].Agent != session.KindCodex {
		t.Fatalf("messages = %+v", c.Messages)
	}
	if c.Agent != session.KindClaude {
		t.Fatalf("the conversation's own agent moved to %q", c.Agent)
	}
	if _, class, ok := tb.Until(session.KindClaude, "model-a", routeT0); !ok || class != routehealth.ClassWindow {
		t.Fatalf("claude not parked: (%s, %v)", class, ok)
	}
}

// TestHandleChatSendKeepsAConfigError: a request wrong as configured is the user's to see; it
// neither moves the turn nor parks the backend.
func TestHandleChatSendKeepsAConfigError(t *testing.T) {
	writeUIPrefs(t, `{}`)
	t.Cleanup(WaitInterimDeliveries)
	tb := useRouteTable(t)
	var codexPrompts []string
	stubChatProvider(t, session.KindClaude, scriptedProvider{err: errors.New("claude returned an error: 404 model not found")})
	stubChatProvider(t, session.KindCodex, scriptedProvider{reply: "codex answer", prompts: &codexPrompts})
	conv := &ChatConversation{ID: RandUUID(), Slug: NewConvSlug(), Agent: session.KindClaude, Model: "model-a"}
	if err := SaveConv(conv); err != nil {
		t.Fatal(err)
	}

	if rec := sendTurn(t, conv.ID, "hello"); rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if len(codexPrompts) != 0 {
		t.Fatal("a config error was re-run on codex")
	}
	if _, _, ok := tb.Until(session.KindClaude, "model-a", routeT0); ok {
		t.Fatal("a config error parked claude")
	}
}

// TestHandleChatStreamDoesNotRerunAfterOutput: once a delta reached the client, a re-run would
// show both attempts, so the error stands (the backend is still parked).
func TestHandleChatStreamDoesNotRerunAfterOutput(t *testing.T) {
	writeUIPrefs(t, `{}`)
	t.Cleanup(WaitInterimDeliveries)
	tb := useRouteTable(t)
	var codexPrompts []string
	stubChatProvider(t, session.KindClaude, streamingScripted{scriptedProvider{err: errors.New(limitErr)}})
	stubChatProvider(t, session.KindCodex, scriptedProvider{reply: "codex answer", prompts: &codexPrompts})
	conv := &ChatConversation{ID: RandUUID(), Slug: NewConvSlug(), Agent: session.KindClaude, Model: "model-a"}
	if err := SaveConv(conv); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/chat/conversations/"+conv.ID+"/stream", strings.NewReader(`{"content":"hello"}`))
	req.SetPathValue("id", conv.ID)
	rec := httptest.NewRecorder()
	HandleChatStream(rec, req)

	if len(codexPrompts) != 0 {
		t.Fatal("a turn that had already streamed was re-run on codex")
	}
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("no error frame: %s", rec.Body.String())
	}
	if _, _, ok := tb.Until(session.KindClaude, "model-a", routeT0); !ok {
		t.Fatal("the limit was not recorded")
	}
}

// TestHandleChatStreamFailsOverBeforeOutput: a limit before the first token moves the turn,
// and the stream announces the backend that now answers.
func TestHandleChatStreamFailsOverBeforeOutput(t *testing.T) {
	writeUIPrefs(t, `{}`)
	t.Cleanup(WaitInterimDeliveries)
	useRouteTable(t)
	stubChatProvider(t, session.KindClaude, scriptedProvider{err: errors.New(limitErr)})
	stubChatProvider(t, session.KindCodex, scriptedProvider{reply: "codex answer"})
	conv := &ChatConversation{ID: RandUUID(), Slug: NewConvSlug(), Agent: session.KindClaude, Model: "model-a"}
	if err := SaveConv(conv); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/chat/conversations/"+conv.ID+"/stream", strings.NewReader(`{"content":"hello"}`))
	req.SetPathValue("id", conv.ID)
	rec := httptest.NewRecorder()
	HandleChatStream(rec, req)

	var agents []string
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		var f struct {
			Agent string `json:"agent"`
			Done  bool   `json:"done"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &f) == nil && f.Agent != "" {
			agents = append(agents, f.Agent)
		}
	}
	if strings.Join(agents, ",") != "claude,codex" {
		t.Fatalf("agent frames = %v, want claude then codex", agents)
	}
	if !strings.Contains(rec.Body.String(), `"done":true`) {
		t.Fatalf("no done frame: %s", rec.Body.String())
	}
}

func TestOneShotRouteKindSkipsAParkedBackend(t *testing.T) {
	writeResolvePrefs(t, `{"aiFeatureAgents":{"title.session":"claude"}}`)
	tb := useRouteTable(t)
	model := oneShotRouteModel("title.session", OneShotShort, session.KindClaude, "model-a")

	if k := oneShotRouteKind("title.session", OneShotShort, "model-a"); k != session.KindClaude {
		t.Fatalf("unparked: kind = %q, want the pin", k)
	}
	tb.Fail(session.KindClaude, model, limitErr, routeT0)
	if k := oneShotRouteKind("title.session", OneShotShort, "model-a"); k != session.KindCodex {
		t.Fatalf("pin on a usage window: kind = %q, want codex", k)
	}
}

// TestDenyAllWaitsOutAParkedPin: the pin keeps the transcript on its vendor, so a parked pin
// answers "unavailable" (no attempt spent) instead of moving the review elsewhere.
func TestDenyAllWaitsOutAParkedPin(t *testing.T) {
	writeResolvePrefs(t, `{"aiFeatureAgents":{"memory.review":"claude"}}`)
	tb := useRouteTable(t)
	if k, err := resolveDenyAllKind("memory.review", OneShotProse); err != nil || k != session.KindClaude {
		t.Fatalf("unparked: (%q, %v), want claude", k, err)
	}
	tb.Fail(session.KindClaude, "", limitErr, routeT0)
	if _, err := resolveDenyAllKind("memory.review", OneShotProse); !errors.Is(err, ErrDenyAllUnavailable) {
		t.Fatalf("parked pin: err = %v, want ErrDenyAllUnavailable", err)
	}
}

// TestHandleChatSendFailoverMarksReportsDelivered: the pending reports point into c.Messages,
// so the failover notice must not be appended before they are marked. Run across history
// lengths so that some of them hit len == cap and the append moves the slice.
func TestHandleChatSendFailoverMarksReportsDelivered(t *testing.T) {
	writeUIPrefs(t, `{}`)
	t.Cleanup(WaitInterimDeliveries)
	useRouteTable(t)
	stubChatProvider(t, session.KindClaude, scriptedProvider{err: errors.New(limitErr)})
	stubChatProvider(t, session.KindCodex, scriptedProvider{reply: "codex answer"})
	for filler := 0; filler < 10; filler++ {
		routehealth.Default = routehealth.NewTable()
		msgs := []ChatMessage{{Role: "report", Content: "session report", TS: NowMs()}}
		for i := 0; i < filler; i++ {
			msgs = append(msgs, ChatMessage{Role: "notice", Content: "filler", TS: NowMs()})
		}
		conv := &ChatConversation{ID: RandUUID(), Slug: NewConvSlug(), Agent: session.KindClaude, Model: "model-a", Messages: msgs}
		if err := SaveConv(conv); err != nil {
			t.Fatal(err)
		}
		if rec := sendTurn(t, conv.ID, "hello"); rec.Code != http.StatusOK {
			t.Fatalf("filler %d: status = %d, body = %s", filler, rec.Code, rec.Body.String())
		}
		c, err := LoadConv(conv.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !c.Messages[0].Delivered {
			t.Fatalf("filler %d: the report the re-run delivered is still undelivered", filler)
		}
	}
}

// TestAutoTurnModelKeysTheRoute: the auto-turn model is what runs on claude, so a park on it
// moves the auto turn, while the conversation's own model stays unaffected; and it never leaks
// onto another kind.
func TestAutoTurnModelKeysTheRoute(t *testing.T) {
	writeUIPrefs(t, `{"assistantModels":{"codex":"gpt-custom"}}`)
	tb := useRouteTable(t)
	tb.Park(session.KindClaude, "model-auto",
		routehealth.Verdict{Class: routehealth.ClassServer, Scope: routehealth.ScopeModel, ResetAt: routeT0.Add(time.Hour)}, routeT0)
	c := &ChatConversation{Agent: session.KindClaude, Model: "model-conv"}

	if _, k := chatRoute(c); k != session.KindClaude {
		t.Fatalf("user turn: route = %q, want claude (its own model is healthy)", k)
	}
	c.modelOverride = "model-auto"
	if _, k := chatRoute(c); k != session.KindCodex {
		t.Fatalf("auto turn: route = %q, want codex (the auto-turn model is parked)", k)
	}
	if got := chatModelFor(c, session.KindCodex); got != "gpt-custom" {
		t.Fatalf("codex model under an auto-turn override = %q, want its own row", got)
	}
}

// TestAutoTurnRoutesOnItsOwnModel drives runReportAutoTurn: the pick that starts the auto turn
// keys on the auto-turn model, and a turn that fails over still marks its reports delivered.
func TestAutoTurnRoutesOnItsOwnModel(t *testing.T) {
	newConv := func(t *testing.T) *ChatConversation {
		t.Helper()
		conv := &ChatConversation{ID: RandUUID(), Slug: NewConvSlug(), Agent: session.KindClaude, Model: "model-conv",
			Messages: []ChatMessage{{Role: "report", Content: "session report", TS: NowMs()}}}
		if err := SaveConv(conv); err != nil {
			t.Fatal(err)
		}
		return conv
	}
	t.Run("parked auto-turn model", func(t *testing.T) {
		writeUIPrefs(t, `{"assistantAutoTurnModel":"model-auto"}`)
		t.Cleanup(WaitInterimDeliveries)
		tb := useRouteTable(t)
		tb.Park(session.KindClaude, "model-auto",
			routehealth.Verdict{Class: routehealth.ClassServer, Scope: routehealth.ScopeModel, ResetAt: routeT0.Add(time.Hour)}, routeT0)
		var claudePrompts, codexPrompts []string
		stubChatProvider(t, session.KindClaude, scriptedProvider{reply: "claude answer", prompts: &claudePrompts})
		stubChatProvider(t, session.KindCodex, scriptedProvider{reply: "codex answer", prompts: &codexPrompts})
		conv := newConv(t)

		runReportAutoTurn(conv.ID)
		if len(claudePrompts) != 0 || len(codexPrompts) != 1 {
			t.Fatalf("claude ran %d, codex ran %d; want the auto turn on codex", len(claudePrompts), len(codexPrompts))
		}
	})
	t.Run("failover marks the reports", func(t *testing.T) {
		writeUIPrefs(t, `{}`)
		t.Cleanup(WaitInterimDeliveries)
		useRouteTable(t)
		stubChatProvider(t, session.KindClaude, scriptedProvider{err: errors.New(limitErr)})
		stubChatProvider(t, session.KindCodex, scriptedProvider{reply: "codex answer"})
		conv := newConv(t)

		runReportAutoTurn(conv.ID)
		c, err := LoadConv(conv.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !c.Messages[0].Delivered {
			t.Fatal("the report the re-run delivered is still undelivered")
		}
		if n := len(c.Messages); n != 3 || c.Messages[1].NoticeKey != noticeKeyAgentFailover || c.Messages[2].Agent != session.KindCodex {
			t.Fatalf("messages = %+v", c.Messages)
		}
	})
}

// TestOneShotRouteKindSeesTheMuseSafeModel: a muse feature set to "" runs on the safe model,
// not on the CLI default, so a park on that model must move the call.
func TestOneShotRouteKindSeesTheMuseSafeModel(t *testing.T) {
	writeResolvePrefs(t, `{"aiFeatureAgents":{"title.session":"muse"},"aiFeatureModels":{"title.session":{"muse":""}}}`)
	tb := useRouteTable(t)
	forceHeadlessAvailable(t, session.KindMuse, true)
	stubMuseSafeModels(t, "muse-safe-a")
	if k := oneShotRouteKind("title.session", OneShotShort, ""); k != session.KindMuse {
		t.Fatalf("unparked: kind = %q, want the muse pin", k)
	}
	tb.Park(session.KindMuse, "muse-safe-a",
		routehealth.Verdict{Class: routehealth.ClassServer, Scope: routehealth.ScopeModel, ResetAt: routeT0.Add(time.Hour)}, routeT0)
	if k := oneShotRouteKind("title.session", OneShotShort, ""); k == session.KindMuse {
		t.Fatal("a park on the safe model muse runs did not move the call")
	}
}

// TestChatRouteSeesTheMuseSafeModel: a muse conversation with no model runs on the safe model,
// so the route is looked up, failed and cleared under that model, never under "" (which is the
// kind-wide key and would park every muse model).
func TestChatRouteSeesTheMuseSafeModel(t *testing.T) {
	writeUIPrefs(t, `{}`)
	tb := useRouteTable(t)
	forceHeadlessAvailable(t, session.KindMuse, true)
	stubMuseSafeModels(t, "muse-safe-a")
	c := &ChatConversation{Agent: session.KindMuse}

	if _, k := chatRoute(c); k != session.KindMuse {
		t.Fatalf("unparked: route = %q, want the muse pin", k)
	}
	tb.Park(session.KindMuse, "muse-safe-a",
		routehealth.Verdict{Class: routehealth.ClassServer, Scope: routehealth.ScopeModel, ResetAt: routeT0.Add(time.Hour)}, routeT0)
	if _, k := chatRoute(c); k == session.KindMuse {
		t.Fatal("a park on the safe model muse runs did not move the turn")
	}
	NoteTurnResult(context.Background(), c, session.KindMuse, nil)
	if _, _, ok := tb.Until(session.KindMuse, "muse-safe-a", routeT0); ok {
		t.Fatal("a success did not clear the safe model's park")
	}
	NoteTurnResult(context.Background(), c, session.KindMuse, errors.New("503 Service Unavailable"))
	if _, _, ok := tb.Until(session.KindMuse, "muse-other", routeT0); ok {
		t.Fatal("a model-scoped failure parked every muse model (recorded under the kind-wide key)")
	}
	if _, _, ok := tb.Until(session.KindMuse, "muse-safe-a", routeT0); !ok {
		t.Fatal("the failure was not recorded under the safe model")
	}
}

// TestNoteTurnResultKeepsAParkSetDuringTheCall: the call began before another request parked
// the backend, so its success does not lift that park; StartTurn stamps the start.
func TestNoteTurnResultKeepsAParkSetDuringTheCall(t *testing.T) {
	writeUIPrefs(t, `{}`)
	tb := useRouteTable(t)
	c := &ChatConversation{Agent: session.KindCodex, Model: "model-a"}
	c.StartTurn() // routeNow() = routeT0
	tb.Fail(session.KindCodex, "model-a", "You've hit your usage limit.", routeT0.Add(time.Second))
	NoteTurnResult(context.Background(), c, session.KindCodex, nil)
	if _, _, ok := tb.Until(session.KindCodex, "model-a", routeT0.Add(2*time.Second)); !ok {
		t.Fatal("a success that started before the park lifted it")
	}
}
