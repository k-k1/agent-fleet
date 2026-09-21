package muse

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp/msptest"
)

func approvalParams() msp.ApprovalRequestParams {
	cmd := "rm -rf build"
	return msp.ApprovalRequestParams{
		ApprovalID:           "ap-1",
		SessionID:            "01a0c1d6-0000-7000-8000-000000000001",
		ToolName:             "shell",
		CurrentRequirementID: msp.ApprovalRequirementRef{ApprovalID: "ap-1", SourceIndex: 2},
		Subject:              msp.ApprovalSubject{Kind: "shell", Command: &cmd},
		AvailableChoices: []msp.ApprovalChoice{
			{ChoiceID: "allow_once", Decision: msp.ApprovalDecisionApproved, Scope: msp.ApprovalChoiceScopeOnce, Label: "Allow once"},
			{ChoiceID: "abort", Decision: msp.ApprovalDecisionAbort, Scope: msp.ApprovalChoiceScopeOnce, Label: "Abort"},
		},
	}
}

func waitInteraction(t *testing.T, h *threadHandle) *agents.Interaction {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-h.events:
			if ev.Interaction != nil {
				return ev.Interaction
			}
		case <-deadline:
			t.Fatal("no interaction event arrived")
		}
	}
}

// The delivery that matters: a real host sends approvals as a NOTIFICATION. A driver that
// only implements the server-request form leaves the turn parked on approvalPending forever.
func TestApprovalArrivesAsANotification(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	host.Notify(msp.NotificationApprovalRequested, approvalParams())

	inter := waitInteraction(t, h)
	if inter.Prompt != "rm -rf build" {
		t.Errorf("prompt = %q, want the command", inter.Prompt)
	}
	if len(inter.Questions) != 1 || len(inter.Questions[0].Options) != 2 {
		t.Fatalf("interaction = %+v", inter)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state != agents.TurnWaitingInteraction {
		t.Errorf("state = %s, want waiting_interaction", h.state)
	}
	if h.pending == nil || !h.pending.isApproval() {
		t.Fatal("no approval recorded")
	}
	if h.pending.allowChoice != "allow_once" || h.pending.abortChoice != "abort" {
		t.Errorf("choices = %+v", h.pending)
	}
}

// The server-initiated form must still be ANSWERED with a receipt, or the host waits on a
// request nobody acknowledged — and it must build the same interaction.
func TestApprovalAsAServerRequestGetsAReceipt(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	host.Request(`"srv-1"`, msp.ServerRequestApprovalRequest, approvalParams())

	waitInteraction(t, h)
	reply := host.WaitFor(func(m msptest.Message) bool { return len(m.Result) > 0 })
	if string(reply.ID) != `"srv-1"` {
		t.Errorf("receipt id = %s, want \"srv-1\" verbatim", reply.ID)
	}
	if string(reply.Result) != "{}" {
		t.Errorf("receipt = %s, want {} (the presentation receipt is an empty object)", reply.Result)
	}
}

func TestRespondAllowDecidesTheApproval(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	host.Handle(msp.MethodApprovalDecide, func(m msptest.Message) (any, *msp.Error) {
		return msp.CommandAcceptedResult{}, nil
	})
	host.Notify(msp.NotificationApprovalRequested, approvalParams())
	inter := waitInteraction(t, h)

	if err := h.Respond(agents.InteractionReply{ID: inter.ID, Decision: agents.DecisionAllow}); err != nil {
		t.Fatalf("respond: %v", err)
	}
	m := host.WaitForMethod(msp.MethodApprovalDecide)
	var p msp.ApprovalDecideParams
	if err := json.Unmarshal(m.Params, &p); err != nil {
		t.Fatal(err)
	}
	if p.ChoiceID != "allow_once" {
		t.Errorf("choiceId = %q, want allow_once", p.ChoiceID)
	}
	// The requirement ref is the multi-stage race guard and has to go back verbatim.
	if p.RequirementID.ApprovalID != "ap-1" || p.RequirementID.SourceIndex != 2 {
		t.Errorf("requirementId = %+v, want the one the host sent", p.RequirementID)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.inter != nil || h.pending != nil {
		t.Error("the interaction is still pending after it was answered")
	}
}

func TestRespondDenyPicksTheAbortChoice(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	host.Handle(msp.MethodApprovalDecide, func(m msptest.Message) (any, *msp.Error) {
		return msp.CommandAcceptedResult{}, nil
	})
	host.Notify(msp.NotificationApprovalRequested, approvalParams())
	inter := waitInteraction(t, h)

	if err := h.Respond(agents.InteractionReply{ID: inter.ID, Decision: agents.DecisionDeny}); err != nil {
		t.Fatal(err)
	}
	m := host.WaitForMethod(msp.MethodApprovalDecide)
	var p msp.ApprovalDecideParams
	json.Unmarshal(m.Params, &p)
	if p.ChoiceID != "abort" {
		t.Errorf("choiceId = %q, want abort", p.ChoiceID)
	}
}

// The interaction is built as a question, so the Console may answer it by option index too.
func TestRespondByOptionIndexMapsOntoAllowAndDeny(t *testing.T) {
	for idx, want := range map[int]string{0: "allow_once", 1: "abort"} {
		h := &threadHandle{}
		host := newTestHandle(t, h)
		host.Handle(msp.MethodApprovalDecide, func(m msptest.Message) (any, *msp.Error) {
			return msp.CommandAcceptedResult{}, nil
		})
		host.Notify(msp.NotificationApprovalRequested, approvalParams())
		inter := waitInteraction(t, h)

		err := h.Respond(agents.InteractionReply{
			ID:       inter.ID,
			Decision: agents.DecisionAnswer,
			Answers:  []agents.InteractionAnswer{{Options: []int{idx}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		m := host.WaitForMethod(msp.MethodApprovalDecide)
		var p msp.ApprovalDecideParams
		json.Unmarshal(m.Params, &p)
		if p.ChoiceID != want {
			t.Errorf("option %d chose %q, want %q", idx, p.ChoiceID, want)
		}
	}
}

// Both prompt channels re-deliver, so answering twice is normal traffic. The host's refusal is
// a success: the member's click landed, and an error would send them back to a prompt nobody
// is waiting on.
func TestAnsweringAnAlreadySettledPromptIsNotAnError(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	host.Handle(msp.MethodApprovalDecide, func(m msptest.Message) (any, *msp.Error) {
		return nil, &msp.Error{Code: msp.ErrCodeApprovalAlreadyResolved, Message: "already resolved"}
	})
	host.Notify(msp.NotificationApprovalRequested, approvalParams())
	inter := waitInteraction(t, h)

	if err := h.Respond(agents.InteractionReply{ID: inter.ID, Decision: agents.DecisionAllow}); err != nil {
		t.Errorf("an already-settled approval should read as success, got %v", err)
	}
}

// ...but a real refusal must still surface, or a stale requirement looks like a landed click.
func TestARealDecideFailureSurfaces(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	host.Handle(msp.MethodApprovalDecide, func(m msptest.Message) (any, *msp.Error) {
		return nil, &msp.Error{Code: msp.ErrCodeApprovalRequirementStale, Message: "stale"}
	})
	host.Notify(msp.NotificationApprovalRequested, approvalParams())
	inter := waitInteraction(t, h)

	err := h.Respond(agents.InteractionReply{ID: inter.ID, Decision: agents.DecisionAllow})
	if err == nil {
		t.Fatal("a stale requirement must not read as success")
	}
	if !msp.HasCode(err, msp.ErrCodeApprovalRequirementStale) {
		t.Errorf("err = %v", err)
	}
}

func TestUserInputRoundTripSendsLabelsNotIndexes(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	host.Handle(msp.MethodUserInputAnswer, func(m msptest.Message) (any, *msp.Error) {
		return msp.CommandAcceptedResult{}, nil
	})
	host.Notify(msp.NotificationUserInputRequested, msp.UserInputRequestParams{
		UserInputID: "ui-1",
		SessionID:   h.sid,
		Questions: []msp.UserInputQuestion{{
			ID: "q1", Header: "Deploy", Question: "Which environment?",
			Options: []msp.UserInputOption{{Label: "staging"}, {Label: "production"}},
		}},
	})

	inter := waitInteraction(t, h)
	if len(inter.Questions) != 1 || inter.Questions[0].Header != "Deploy" {
		t.Fatalf("interaction = %+v", inter)
	}
	if len(inter.Questions[0].Options) != 2 || inter.Questions[0].Options[1].Label != "production" {
		t.Errorf("options = %+v", inter.Questions[0].Options)
	}

	err := h.Respond(agents.InteractionReply{
		ID:       inter.ID,
		Decision: agents.DecisionAnswer,
		Answers:  []agents.InteractionAnswer{{Options: []int{1}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := host.WaitForMethod(msp.MethodUserInputAnswer)
	var p msp.UserInputAnswerParams
	json.Unmarshal(m.Params, &p)
	if p.UserInputID != "ui-1" || len(p.Answers) != 1 {
		t.Fatalf("params = %+v", p)
	}
	if p.Answers[0].SelectedLabel == nil || *p.Answers[0].SelectedLabel != "production" {
		t.Errorf("selectedLabel = %v, want production — the wire takes labels, not indexes", p.Answers[0].SelectedLabel)
	}
	if p.Answers[0].QuestionID != "q1" {
		t.Errorf("questionId = %q", p.Answers[0].QuestionID)
	}
}

// An index the host never offered must be dropped rather than sent as a label it would refuse.
func TestOutOfRangeOptionIndexIsDropped(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	host.Handle(msp.MethodUserInputAnswer, func(m msptest.Message) (any, *msp.Error) {
		return msp.CommandAcceptedResult{}, nil
	})
	host.Notify(msp.NotificationUserInputRequested, msp.UserInputRequestParams{
		UserInputID: "ui-2", SessionID: h.sid,
		Questions: []msp.UserInputQuestion{{ID: "q1", Options: []msp.UserInputOption{{Label: "only"}}}},
	})
	inter := waitInteraction(t, h)

	err := h.Respond(agents.InteractionReply{
		ID: inter.ID, Decision: agents.DecisionAnswer,
		Answers: []agents.InteractionAnswer{{Options: []int{7}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := host.WaitForMethod(msp.MethodUserInputAnswer)
	var p msp.UserInputAnswerParams
	json.Unmarshal(m.Params, &p)
	if len(p.Answers[0].SelectedLabels) != 0 || p.Answers[0].SelectedLabel != nil {
		t.Errorf("an out-of-range index reached the wire: %+v", p.Answers[0])
	}
}

// The host settling a prompt on its own (a timeout, another client) has to clear it here too,
// or the Console keeps showing a dialog nobody is waiting on.
func TestSettledNotificationClearsThePrompt(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	host.Notify(msp.NotificationUserInputRequested, msp.UserInputRequestParams{
		UserInputID: "ui-3", SessionID: h.sid,
		Questions: []msp.UserInputQuestion{{ID: "q1"}},
	})
	waitInteraction(t, h)

	host.Notify(msp.NotificationUserInputSettled, map[string]any{"userInputId": "ui-3", "sessionId": h.sid})
	deadline := time.After(5 * time.Second)
	for {
		h.mu.Lock()
		cleared := h.inter == nil
		h.mu.Unlock()
		if cleared {
			return
		}
		select {
		case <-deadline:
			t.Fatal("the prompt was never cleared")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// An approval being resolved must not clear a pending user-input prompt, and vice versa: they
// are two channels, and clearing the wrong one loses a question the member still owes.
func TestApprovalResolvedDoesNotClearAUserInputPrompt(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	host.Notify(msp.NotificationUserInputRequested, msp.UserInputRequestParams{
		UserInputID: "ui-4", SessionID: h.sid,
		Questions: []msp.UserInputQuestion{{ID: "q1"}},
	})
	waitInteraction(t, h)

	host.Notify(msp.NotificationApprovalResolved, map[string]any{"approvalId": "ap-9", "sessionId": h.sid})
	time.Sleep(100 * time.Millisecond)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.inter == nil {
		t.Error("an approval/resolved cleared the pending user-input prompt")
	}
}

func TestRespondWithNothingPendingFails(t *testing.T) {
	h := &threadHandle{}
	newTestHandle(t, h)
	if err := h.Respond(agents.InteractionReply{Decision: agents.DecisionAllow}); err == nil {
		t.Error("responding with nothing pending should fail")
	}
}

// --- status mapping ----------------------------------------------------------

func TestStatusChangedMapsOntoTheTurnStateMachine(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)

	host.Notify(msp.NotificationSessionStatusChanged, msp.SessionStatusChangedParams{
		SessionID: h.sid, Status: msp.SessionStatusRunning,
	})
	waitEvent(t, h, agents.TurnRunning)

	// An attention flag outranks the status: the host is waiting on a human, and showing
	// "running" would hide the dialog the member has to answer.
	host.Notify(msp.NotificationSessionStatusChanged, msp.SessionStatusChangedParams{
		SessionID: h.sid, Status: msp.SessionStatusRunning,
		Attention: []msp.AttentionFlag{msp.AttentionFlagApprovalPending},
	})
	waitEvent(t, h, agents.TurnWaitingInteraction)

	host.Notify(msp.NotificationSessionStatusChanged, msp.SessionStatusChangedParams{
		SessionID: h.sid, Status: msp.SessionStatusIdle,
	})
	waitEvent(t, h, agents.TurnCompleted)

	host.Notify(msp.NotificationSessionStatusChanged, msp.SessionStatusChangedParams{
		SessionID: h.sid, Status: msp.SessionStatusNotLoaded,
	})
	waitEvent(t, h, agents.TurnUnknown)
}

// An idle status on a session that never ran a turn must not fabricate a completed one.
func TestIdleWithNoRunningTurnDoesNotEndATurn(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	h.mu.Lock()
	h.state = agents.TurnUnknown
	h.mu.Unlock()

	host.Notify(msp.NotificationSessionStatusChanged, msp.SessionStatusChangedParams{
		SessionID: h.sid, Status: msp.SessionStatusIdle,
	})
	time.Sleep(100 * time.Millisecond)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state == agents.TurnCompleted {
		t.Error("an idle status invented a completed turn")
	}
}

// An undeclared notification must be dropped, not treated as a protocol error: the host emits
// session/started, which the stable surface does not declare at all.
func TestUndeclaredNotificationIsIgnored(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	host.Notify("session/started", map[string]any{"session": map[string]any{"sessionId": h.sid}})
	host.Notify(msp.NotificationSessionStatusChanged, msp.SessionStatusChangedParams{
		SessionID: h.sid, Status: msp.SessionStatusRunning,
	})
	// The connection still works afterwards, which is the whole assertion.
	waitEvent(t, h, agents.TurnRunning)
}

// --- settings ----------------------------------------------------------------

func TestUpdateSettingsSendsModelAndEffort(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	host.Handle(msp.MethodSessionSetModel, func(m msptest.Message) (any, *msp.Error) {
		return msp.CommandAcceptedResult{}, nil
	})
	host.Handle(msp.MethodSessionSetReasoningEffort, func(m msptest.Message) (any, *msp.Error) {
		return msp.CommandAcceptedResult{}, nil
	})

	err := h.UpdateSettings(agents.ThreadSettings{Model: "muse-spark-1.3", Effort: "high"})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	var mp msp.SessionSetModelParams
	json.Unmarshal(host.WaitForMethod(msp.MethodSessionSetModel).Params, &mp)
	if mp.Model.ModelID != "muse-spark-1.3" {
		t.Errorf("modelId = %q", mp.Model.ModelID)
	}
	var ep msp.SessionSetReasoningEffortParams
	json.Unmarshal(host.WaitForMethod(msp.MethodSessionSetReasoningEffort).Params, &ep)
	if ep.ReasoningEffort != msp.ReasoningEffortHigh {
		t.Errorf("reasoningEffort = %q", ep.ReasoningEffort)
	}
}

// An effort string MSP does not know is refused rather than silently replaced with a default:
// sending a guessed effort is a behaviour change the member did not ask for.
func TestUnknownEffortIsRefused(t *testing.T) {
	h := &threadHandle{}
	newTestHandle(t, h)
	if err := h.UpdateSettings(agents.ThreadSettings{Effort: "turbo"}); err == nil {
		t.Error("an unknown effort should be refused")
	}
	for _, ok := range []string{"none", "MEDIUM", " ultra "} {
		if reasoningEffort(ok) == nil {
			t.Errorf("%q should map onto the wire enum", ok)
		}
	}
}

// --- approval mode -----------------------------------------------------------

// The sandbox is off, so the approval gate is the only thing between the agent and the
// container. Both modes are selected explicitly: the server default is onRequest, and
// inheriting it silently is not a choice a member made.
func TestApprovalModeFollowsThePermissionChoice(t *testing.T) {
	if got := *approvalModeFor(false); got != msp.ApprovalModeOnRequest {
		t.Errorf("approvals on = %q, want onRequest", got)
	}
	if got := *approvalModeFor(true); got != msp.ApprovalModeAllowAll {
		t.Errorf("skip permissions = %q, want allowAll", got)
	}
}

func TestSnapshotReportsTheThreadState(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	host.Notify(msp.NotificationApprovalRequested, approvalParams())
	waitInteraction(t, h)

	snap, err := h.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snap.TurnState != agents.TurnWaitingInteraction {
		t.Errorf("state = %s", snap.TurnState)
	}
	if snap.Interaction == nil {
		t.Error("the pending interaction is missing from the snapshot")
	}
}
