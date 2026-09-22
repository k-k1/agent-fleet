package lcpp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/harness"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// scriptedClient is a harness.Client double that answers Send with a fixed sequence of
// canned turns, in order — enough to script a tool-call round trip (a ToolCalls-bearing Turn
// followed by a plain-text one) without a live engine. onSend, when set, is called with every
// request Send actually receives (used to assert what tools/messages the driver built).
type scriptedClient struct {
	mu          sync.Mutex
	turns       []harness.Turn
	idx         int
	inputTokens int
	// inputTokensSeq, when set, overrides inputTokens with a per-call sequence (index tokIdx)
	// — enough to script "under budget" then "over budget" so a compaction fires exactly on
	// cue (harness.NeedsCompaction reads InputTokens' own answer against the window).
	inputTokensSeq []int
	tokIdx         int
	onSend         func(messages []harness.Message, tools []harness.ToolDef)
	sendDelay      time.Duration // simulate a slow/cancellable engine round trip
}

func (c *scriptedClient) Send(ctx context.Context, messages []harness.Message, tools []harness.ToolDef) (harness.Turn, error) {
	if c.onSend != nil {
		c.onSend(messages, tools)
	}
	if c.sendDelay > 0 {
		select {
		case <-time.After(c.sendDelay):
		case <-ctx.Done():
			return harness.Turn{}, ctx.Err()
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.idx >= len(c.turns) {
		return harness.Turn{}, errors.New("scriptedClient: script exhausted")
	}
	t := c.turns[c.idx]
	c.idx++
	return t, nil
}

func (c *scriptedClient) InputTokens(context.Context, []harness.Message, []harness.ToolDef) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inputTokensSeq != nil {
		i := c.tokIdx
		if i >= len(c.inputTokensSeq) {
			i = len(c.inputTokensSeq) - 1
		}
		c.tokIdx++
		return c.inputTokensSeq[i], nil
	}
	return c.inputTokens, nil
}

// wireEngine points harness's func-var seams at client, restored on test cleanup — the same
// idiom internal/chatx's withLcppEngine test helper uses. Window 0 means NeedsCompaction never
// fires (compact.go's own <=0 rule); wireEngineWithWindow below opts into a real one.
func wireEngine(t *testing.T, client harness.Client) { wireEngineWithWindow(t, client, 0) }

func wireEngineWithWindow(t *testing.T, client harness.Client, window int) {
	t.Helper()
	oldToken, oldWindow, oldAvail := harness.EngineToken, harness.EngineWindow, harness.EngineAvailable
	oldNewClient := newHarnessClient
	harness.EngineToken = func(ctx context.Context, key, session string) (harness.EngineConn, bool) {
		return harness.EngineConn{BaseURL: "http://test.invalid", Token: "t"}, true
	}
	harness.EngineWindow = func(ctx context.Context, key string) int { return window }
	harness.EngineAvailable = func(ctx context.Context, key string) bool { return true }
	newHarnessClient = func(harness.EngineConn, string) harness.Client { return client }
	t.Cleanup(func() {
		harness.EngineToken, harness.EngineWindow, harness.EngineAvailable = oldToken, oldWindow, oldAvail
		newHarnessClient = oldNewClient
	})
}

func testMeta(t *testing.T, name string) session.Meta {
	t.Helper()
	return session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindLcpp, Model: "test-model"}
}

// waitState blocks until the handle reports one of `want`.
//
// It waits on the handle's EVENT channel rather than polling. The snapshot is read first
// because the transition can land before this call; after that every wake-up is a real
// transition instead of a 5ms tick. A poll against a short deadline makes the assertion a
// statement about how much CPU this process got, which is how codex's twin of this helper
// turned `go test ./... -p 2` into a red build on unrelated PRs. The 30s bound that remains is
// a HANG guard — nothing here should approach it.
func waitState(t *testing.T, h agents.ThreadHandle, want ...agents.TurnState) agents.ThreadSnapshot {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		snap, err := h.Snapshot()
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		for _, w := range want {
			if snap.TurnState == w {
				return snap
			}
		}
		select {
		case <-h.Events():
			// A transition happened; Snapshot above stays the authority (events are advisory
			// and dropped on overflow).
		case <-deadline:
			t.Fatalf("timed out waiting for state in %v, got %v", want, snap.TurnState)
		}
	}
}

func TestDriverSendPersistsTurnAndCompletes(t *testing.T) {
	testHome(t)
	wireEngine(t, &scriptedClient{turns: []harness.Turn{{Content: "hello there"}}})

	d := NewDriver()
	m := testMeta(t, "sess-basic")
	h, err := d.Resume(m)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := h.Send(agents.TurnInput{Prompt: "hi"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitState(t, h, agents.TurnCompleted)

	recs, _, err := Open(sidFor(m)).Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(recs) != 2 || recs[0].Kind != KindUser || recs[0].Content != "hi" {
		t.Fatalf("unexpected records: %+v", recs)
	}
	if recs[1].Kind != KindAssistant || recs[1].Content != "hello there" {
		t.Fatalf("unexpected assistant record: %+v", recs[1])
	}

	// The generic status route (§4.2) must show idle once the turn settles — this is the
	// route agent.go's WireLive and sessionx's DriveState both read.
	if st, ok := status.Read(sidFor(m)); !ok || st.State != "idle" {
		t.Fatalf("status = %+v, ok=%v, want idle", st, ok)
	}
}

// TestDriverToolLoopPersistsRoundTrip is the positive control for tool-call round trips: a
// scripted first turn asks for a non-mutating tool (ls, never gated), the second replies with
// no more calls, and BOTH the tool_result and the final assistant text must land in the store.
func TestDriverToolLoopPersistsRoundTrip(t *testing.T) {
	testHome(t)
	client := &scriptedClient{turns: []harness.Turn{
		{ToolCalls: []harness.ToolCall{{ID: "call-1", Name: "ls", Arguments: `{"path":"."}`}}, Finish: harness.FinishToolCalls},
		{Content: "done"},
	}}
	wireEngine(t, client)

	m := testMeta(t, "sess-tools")
	d := NewDriver()
	h, err := d.Resume(m)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := h.Send(agents.TurnInput{Prompt: "list files"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitState(t, h, agents.TurnCompleted, agents.TurnFailed)

	recs, _, err := Open(sidFor(m)).Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	var kinds []Kind
	for _, r := range recs {
		kinds = append(kinds, r.Kind)
	}
	want := []Kind{KindUser, KindAssistant, KindToolResult, KindAssistant}
	if len(kinds) != len(want) {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds = %v, want %v", kinds, want)
		}
	}
}

// TestDriverApprovalGateBlocksAndAnswers is the positive control for decision 5's
// "承認は本当にツールを止める": with SkipPermissions explicitly false, a Mutates tool call
// (bash) must NOT run until Respond answers the resulting Interaction, and denial must
// prevent the command from having run at all.
func TestDriverApprovalGateBlocksAndAnswers(t *testing.T) {
	testHome(t)
	client := &scriptedClient{turns: []harness.Turn{
		{ToolCalls: []harness.ToolCall{{ID: "call-1", Name: "bash", Arguments: `{"command":"echo gated > marker.txt"}`}}, Finish: harness.FinishToolCalls},
		{Content: "done"},
	}}
	wireEngine(t, client)

	m := testMeta(t, "sess-approve")
	skip := false
	m.SkipPermissions = &skip
	d := NewDriver()
	h, err := d.Resume(m)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := h.Send(agents.TurnInput{Prompt: "run it"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	snap := waitState(t, h, agents.TurnWaitingInteraction)
	if snap.Interaction == nil || snap.Interaction.Kind != "question" {
		t.Fatalf("Interaction = %+v, want a pending question", snap.Interaction)
	}
	id := snap.Interaction.ID

	if err := h.Respond(agents.InteractionReply{ID: id, Decision: agents.DecisionAllow}); err != nil {
		t.Fatalf("Respond: %v", err)
	}
	waitState(t, h, agents.TurnCompleted, agents.TurnFailed)

	recs, _, _ := Open(sidFor(m)).Records()
	found := false
	for _, r := range recs {
		if r.Kind == KindToolResult {
			found = true
		}
	}
	if !found {
		t.Fatalf("no tool_result record after Allow: %+v", recs)
	}
}

// TestDriverApprovalGateDeniedNeverRuns is the negative half of the same control: Deny must
// stop the command from running (the marker file the script would have created is absent).
func TestDriverApprovalGateDeniedNeverRuns(t *testing.T) {
	testHome(t)
	client := &scriptedClient{turns: []harness.Turn{
		{ToolCalls: []harness.ToolCall{{ID: "call-1", Name: "bash", Arguments: `{"command":"touch should-not-exist.txt"}`}}, Finish: harness.FinishToolCalls},
		{Content: "acknowledged"},
	}}
	wireEngine(t, client)

	m := testMeta(t, "sess-deny")
	skip := false
	m.SkipPermissions = &skip
	d := NewDriver()
	h, err := d.Resume(m)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := h.Send(agents.TurnInput{Prompt: "run it"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	snap := waitState(t, h, agents.TurnWaitingInteraction)
	if err := h.Respond(agents.InteractionReply{ID: snap.Interaction.ID, Decision: agents.DecisionDeny}); err != nil {
		t.Fatalf("Respond: %v", err)
	}
	waitState(t, h, agents.TurnCompleted, agents.TurnFailed)

	if _, err := os.Stat(filepath.Join(m.Dir, "should-not-exist.txt")); err == nil {
		t.Fatal("bash ran despite a deny decision")
	}
}

// TestDriverSkipPermissionsBypassesApproval is the negative control for the pair above: the
// fleet default (SkipPermissions unset) must never raise an Interaction at all.
func TestDriverSkipPermissionsBypassesApproval(t *testing.T) {
	testHome(t)
	client := &scriptedClient{turns: []harness.Turn{
		{ToolCalls: []harness.ToolCall{{ID: "call-1", Name: "bash", Arguments: `{"command":"true"}`}}, Finish: harness.FinishToolCalls},
		{Content: "done"},
	}}
	wireEngine(t, client)

	m := testMeta(t, "sess-bypass")
	d := NewDriver()
	h, err := d.Resume(m)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := h.Send(agents.TurnInput{Prompt: "run it"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	snap := waitState(t, h, agents.TurnCompleted, agents.TurnFailed, agents.TurnWaitingInteraction)
	if snap.TurnState == agents.TurnWaitingInteraction {
		t.Fatal("bypass session raised an Interaction — SkipPermissions default must skip approval")
	}
}

// TestDriverPlanModeDropsMutatesTools is the positive control for DynamicMode/launch-time
// plan mode: with Mode=plan, the tool defs the driver hands to Send must not include a
// Mutates tool (bash/write/edit).
func TestDriverPlanModeDropsMutatesTools(t *testing.T) {
	testHome(t)
	var gotTools []harness.ToolDef
	client := &scriptedClient{
		turns:  []harness.Turn{{Content: "ok"}},
		onSend: func(_ []harness.Message, tools []harness.ToolDef) { gotTools = tools },
	}
	wireEngine(t, client)

	m := testMeta(t, "sess-plan")
	m.Mode = "plan"
	d := NewDriver()
	h, err := d.Resume(m)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := h.Send(agents.TurnInput{Prompt: "plan it"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitState(t, h, agents.TurnCompleted, agents.TurnFailed)

	for _, td := range gotTools {
		if td.Name == "bash" || td.Name == "write" || td.Name == "edit" {
			t.Fatalf("plan mode still offered a mutating tool: %s", td.Name)
		}
	}
}

// TestDriverUpdateSettingsModel is the positive control for DynamicModel: a model change
// takes effect on the NEXT turn (the harness client is rebuilt with the new model string)
// and is recorded as a model-change system note.
// assertUserOnlyPlusVisibleError is the shared shape docs/log/109's three precondition
// failures (no model, no engine seam, engine unreachable) must all leave behind: the user's
// own prompt (AppendUser always runs first) PLUS a NoteTurnError record that actually renders
// — never just the former. A session that stops at record 1 is exactly the silent-failure bug:
// the user's message sits there forever with nothing explaining why no reply ever came.
func assertUserOnlyPlusVisibleError(t *testing.T, m session.Meta) {
	t.Helper()
	recs, _, err := Open(sidFor(m)).Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(recs) != 2 || recs[0].Kind != KindUser {
		t.Fatalf("records = %+v, want [user, turn-error note]", recs)
	}
	if recs[1].Kind != KindSystemNote || recs[1].Note != NoteTurnError || strings.TrimSpace(recs[1].Content) == "" {
		t.Fatalf("second record = %+v, want a non-empty NoteTurnError", recs[1])
	}
	turns, err := Open(sidFor(m)).Transcript()
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	if len(turns) != 2 || turns[1].Role != "assistant" || len(turns[1].Parts) != 1 || turns[1].Parts[0].Kind != "error" {
		t.Fatalf("transcript turns = %+v, want a rendered error turn (not silently dropped)", turns)
	}
}

// TestDriverSendWithNoModelRecordsVisibleError is the positive control for the missing-model
// precondition (the exact shape of svcnyrc's reproduction, docs/log/109): the server-side
// create-time guard (session_handlers.go) refuses this at launch now, but a session created
// before that guard existed, or one whose model was cleared via UpdateSettings(ClearModel)
// after launch, must not silently eat the user's next message either.
func TestDriverSendWithNoModelRecordsVisibleError(t *testing.T) {
	testHome(t)
	// Deliberately built by hand rather than via testMeta (which sets Model: "test-model"),
	// matching a legacy/direct-POST session that bypassed the create-time guard.
	m := session.Meta{Name: "sess-no-model", Dir: t.TempDir(), Kind: session.KindLcpp}

	d := NewDriver()
	h, err := d.Resume(m)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := h.Send(agents.TurnInput{Prompt: "hi"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitState(t, h, agents.TurnFailed)
	assertUserOnlyPlusVisibleError(t, m)
}

// TestDriverSendWithNoEngineSeamRecordsVisibleError covers the second precondition
// (harness.EngineToken == nil — a build with no self-hosted engines wired at all).
func TestDriverSendWithNoEngineSeamRecordsVisibleError(t *testing.T) {
	testHome(t)
	oldToken := harness.EngineToken
	harness.EngineToken = nil
	t.Cleanup(func() { harness.EngineToken = oldToken })

	m := testMeta(t, "sess-no-engine-seam")
	d := NewDriver()
	h, err := d.Resume(m)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := h.Send(agents.TurnInput{Prompt: "hi"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitState(t, h, agents.TurnFailed)
	assertUserOnlyPlusVisibleError(t, m)
}

// TestDriverSendWithUnreachableEngineRecordsVisibleError covers the third precondition
// (harness.EngineToken answers ok=false — no self-hosted chat engine currently reachable).
func TestDriverSendWithUnreachableEngineRecordsVisibleError(t *testing.T) {
	testHome(t)
	oldToken := harness.EngineToken
	harness.EngineToken = func(context.Context, string, string) (harness.EngineConn, bool) {
		return harness.EngineConn{}, false
	}
	t.Cleanup(func() { harness.EngineToken = oldToken })

	m := testMeta(t, "sess-engine-unreachable")
	d := NewDriver()
	h, err := d.Resume(m)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := h.Send(agents.TurnInput{Prompt: "hi"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitState(t, h, agents.TurnFailed)
	assertUserOnlyPlusVisibleError(t, m)
}

func TestDriverUpdateSettingsModel(t *testing.T) {
	testHome(t)
	var gotConnModel string
	oldNewClient := newHarnessClient
	t.Cleanup(func() { newHarnessClient = oldNewClient })
	newHarnessClient = func(_ harness.EngineConn, model string) harness.Client {
		gotConnModel = model
		return &scriptedClient{turns: []harness.Turn{{Content: "ok"}}}
	}
	oldToken, oldWindow, oldAvail := harness.EngineToken, harness.EngineWindow, harness.EngineAvailable
	t.Cleanup(func() {
		harness.EngineToken, harness.EngineWindow, harness.EngineAvailable = oldToken, oldWindow, oldAvail
	})
	harness.EngineToken = func(context.Context, string, string) (harness.EngineConn, bool) {
		return harness.EngineConn{BaseURL: "http://test.invalid"}, true
	}
	harness.EngineWindow = func(context.Context, string) int { return 0 }
	harness.EngineAvailable = func(context.Context, string) bool { return true }

	m := testMeta(t, "sess-settings")
	d := NewDriver()
	h, err := d.Resume(m)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := h.UpdateSettings(agents.ThreadSettings{Model: "model-2"}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if err := h.Send(agents.TurnInput{Prompt: "hi"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitState(t, h, agents.TurnCompleted, agents.TurnFailed)
	if gotConnModel != "model-2" {
		t.Fatalf("newHarnessClient model = %q, want model-2", gotConnModel)
	}
	recs, _, _ := Open(sidFor(m)).Records()
	foundNote := false
	for _, r := range recs {
		if r.Kind == KindSystemNote && r.Note == NoteModelChange && r.Model == "model-2" {
			foundNote = true
		}
	}
	if !foundNote {
		t.Fatalf("no model-change note recorded: %+v", recs)
	}

	if err := h.UpdateSettings(agents.ThreadSettings{Effort: "high"}); err == nil {
		t.Fatal("UpdateSettings(Effort) succeeded, want an error (DynamicEffort is unverified)")
	}
}

// TestDriverInterruptCancelsRunningTurn is the positive control for Interrupt: a slow Send is
// cancelled and lands as TurnCancelled, not TurnFailed/Completed.
func TestDriverInterruptCancelsRunningTurn(t *testing.T) {
	testHome(t)
	client := &scriptedClient{sendDelay: 2 * time.Second, turns: []harness.Turn{{Content: "too late"}}}
	wireEngine(t, client)

	m := testMeta(t, "sess-interrupt")
	d := NewDriver()
	h, err := d.Resume(m)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := h.Send(agents.TurnInput{Prompt: "hi"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitState(t, h, agents.TurnRunning)
	if err := h.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	waitState(t, h, agents.TurnCancelled)
}

// TestDriverRestartSettleAborted is the positive control for §4.4's restart recovery: a store
// whose tail is an assistant record still carrying ToolCalls (the process died mid round trip)
// must settle to TurnAborted on the FIRST Resume, and must notify (status shows idle+aborted).
func TestDriverRestartSettleAborted(t *testing.T) {
	testHome(t)
	m := testMeta(t, "sess-settle-aborted")
	st := Open(sidFor(m))
	if _, err := st.AppendUser("hi"); err != nil {
		t.Fatalf("AppendUser: %v", err)
	}
	if _, err := st.AppendMessage(harness.Message{
		Role: harness.RoleAssistant, Content: "",
		ToolCalls: []harness.ToolCall{{ID: "c1", Name: "bash", Arguments: `{"command":"true"}`}},
	}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	d := NewDriver()
	h, err := d.Resume(m)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	snap, _ := h.Snapshot()
	if snap.TurnState != agents.TurnAborted {
		t.Fatalf("TurnState = %v, want TurnAborted", snap.TurnState)
	}
	if s, ok := status.Read(sidFor(m)); !ok || s.State != "idle" || !s.TurnEnd || s.TurnEndReason != status.TurnEndReasonAborted {
		t.Fatalf("status = %+v, ok=%v, want idle/TurnEnd/aborted", s, ok)
	}
}

// TestDriverRestartSettleCompletedNoDuplicateNotify is the negative control: a clean tail
// (assistant record with no pending ToolCalls) settles to TurnCompleted, and — because it was
// already reported before the (simulated) restart — settle must NOT fire a second
// notification.
func TestDriverRestartSettleCompletedNoDuplicateNotify(t *testing.T) {
	testHome(t)
	var notifyCount int
	agents.SetStateNotifier(func(sid, previous, state, excerpt string) { notifyCount++ })
	t.Cleanup(func() { agents.SetStateNotifier(nil) })

	m := testMeta(t, "sess-settle-clean")
	st := Open(sidFor(m))
	if _, err := st.AppendUser("hi"); err != nil {
		t.Fatalf("AppendUser: %v", err)
	}
	if _, err := st.AppendMessage(harness.Message{Role: harness.RoleAssistant, Content: "all done"}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	d := NewDriver()
	h, err := d.Resume(m)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	snap, _ := h.Snapshot()
	if snap.TurnState != agents.TurnCompleted {
		t.Fatalf("TurnState = %v, want TurnCompleted", snap.TurnState)
	}
	time.Sleep(20 * time.Millisecond) // notify() is async (agents/notify.go) — give it a moment
	if notifyCount != 0 {
		t.Fatalf("notifyCount = %d, want 0 (a clean tail was already reported before restart)", notifyCount)
	}
}

// TestDriverToolLoopCompactionNoDuplicateRecords is the acceptance-criterion-6 test: a
// compaction firing MID-LOOP (inside one harness.Run call, between two Send round trips —
// loop.go's compactPreservingLastRoundTrip, the shape that inserts a synthetic
// continuationPrompt) must not duplicate the round trip that came before it, and the
// synthetic turn must land as KindContinuation, never KindUser.
func TestDriverToolLoopCompactionNoDuplicateRecords(t *testing.T) {
	testHome(t)
	client := &scriptedClient{
		turns: []harness.Turn{
			{ToolCalls: []harness.ToolCall{{ID: "t1", Name: "ls", Arguments: `{}`}}, Finish: harness.FinishToolCalls}, // iteration 1
			{Content: "the recap"},    // Compact's own summarization Send
			{Content: "final answer"}, // iteration 2's real continuation, post-compaction
		},
		inputTokensSeq: []int{10, 9999}, // under budget, then over budget (window*0.9=900)
	}
	wireEngineWithWindow(t, client, 1000)

	m := testMeta(t, "sess-compact")
	d := NewDriver()
	h, err := d.Resume(m)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := h.Send(agents.TurnInput{Prompt: "start"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitState(t, h, agents.TurnCompleted, agents.TurnFailed)

	recs, _, err := Open(sidFor(m)).Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	var kinds []Kind
	toolResults, assistants, continuations, notes := 0, 0, 0, 0
	for _, r := range recs {
		kinds = append(kinds, r.Kind)
		switch r.Kind {
		case KindToolResult:
			toolResults++
		case KindAssistant:
			assistants++
		case KindContinuation:
			continuations++
		case KindSystemNote:
			notes++
		}
	}
	// Exactly one of each — never two, which is what naively re-appending Result.Messages
	// wholesale would have produced (the pre-compaction round trip re-persisted alongside the
	// new compaction note).
	if toolResults != 1 || assistants != 2 || continuations != 1 || notes != 1 {
		t.Fatalf("kinds = %v (toolResults=%d assistants=%d continuations=%d notes=%d), want exactly 1/2/1/1 — a duplicate round trip",
			kinds, toolResults, assistants, continuations, notes)
	}
	for _, r := range recs {
		if r.Kind == KindContinuation && r.Content != "Continue with the task." {
			t.Fatalf("continuation record content = %q", r.Content)
		}
	}

	// The mirror must show the same shape: no doubled turn, the continuation invisible (its
	// own doc comment — never rendered), and the compaction note rendered once as a Compact
	// turn.
	turns, err := Open(sidFor(m)).Transcript()
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	compactTurns := 0
	for _, tu := range turns {
		if tu.Compact {
			compactTurns++
		}
	}
	if compactTurns != 1 {
		t.Fatalf("mirror compact turns = %d, want 1: %+v", compactTurns, turns)
	}
}

// TestDriverSystemPromptCarriesProjectInstructions is the positive control for "receives your
// agent instructions" (guide/ref/agents.md): lcpp drives no CLI to write AGENTS.md into
// (decision 5), so the driver has to fold the working copy's own AGENTS.md into the leading
// system message itself (harness.SystemPrompt/systemprompt.go) every turn.
func TestDriverSystemPromptCarriesProjectInstructions(t *testing.T) {
	testHome(t)
	m := testMeta(t, "sess-instructions")
	if err := os.WriteFile(filepath.Join(m.Dir, "AGENTS.md"), []byte("call every tool result FROBNITZ"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	var gotSystem string
	client := &scriptedClient{
		turns: []harness.Turn{{Content: "ok"}},
		onSend: func(messages []harness.Message, _ []harness.ToolDef) {
			if len(messages) > 0 && messages[0].Role == harness.RoleSystem {
				gotSystem = messages[0].Content
			}
		},
	}
	wireEngine(t, client)

	d := NewDriver()
	h, err := d.Resume(m)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := h.Send(agents.TurnInput{Prompt: "hi"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitState(t, h, agents.TurnCompleted, agents.TurnFailed)
	if !strings.Contains(gotSystem, "FROBNITZ") {
		t.Fatalf("system prompt = %q, want it to carry the project's AGENTS.md", gotSystem)
	}
}
