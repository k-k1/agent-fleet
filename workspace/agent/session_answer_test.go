package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

func TestValidateAnswerChoices(t *testing.T) {
	qs := []transcript.Question{
		{Question: "a", Options: []transcript.Option{{Label: "a1"}, {Label: "a2"}}},
		{Question: "b", Options: []transcript.Option{{Label: "b1"}, {Label: "b2"}, {Label: "b3"}}},
	}
	labels, err := validateAnswerChoices(qs, []int{2, 3})
	if err != nil || len(labels) != 2 || labels[0] != "a2" || labels[1] != "b3" {
		t.Fatalf("labels=%v err=%v", labels, err)
	}
	if _, err := validateAnswerChoices(qs, []int{1}); err == nil {
		t.Fatal("count mismatch must fail")
	}
	if _, err := validateAnswerChoices(qs, []int{0, 1}); err == nil {
		t.Fatal("0 is out of range (choices are 1-based)")
	}
	if _, err := validateAnswerChoices(qs, []int{1, 4}); err == nil {
		t.Fatal("out-of-range option must fail")
	}
	multi := []transcript.Question{{Question: "m", MultiSelect: true,
		Options: []transcript.Option{{Label: "x"}, {Label: "y"}}}}
	if _, err := validateAnswerChoices(multi, []int{1}); err == nil {
		t.Fatal("multi-select must be rejected (Console-only)")
	}
	if _, err := validateAnswerChoices(nil, []int{1}); err == nil {
		t.Fatal("no questions must fail")
	}
}

// TestPlanRespondGuards pins the /plan-respond preconditions: unknown session,
// non-claude kind, and no pending plan all fail cleanly before any key is sent.
func TestPlanRespondGuards(t *testing.T) {
	withTempHome(t)
	do := func(name, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/plan-respond", strings.NewReader(body))
		req.SetPathValue("name", name)
		rec := httptest.NewRecorder()
		handleSessionPlanRespond(rec, req)
		return rec
	}
	if rec := do("slot61", `{"decision":"approve"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown session: %d %s", rec.Code, rec.Body.String())
	}
	session.WriteMeta(session.Meta{Name: "slot62", Dir: t.TempDir(), Kind: session.KindCodex})
	if rec := do("slot62", `{"decision":"approve"}`); rec.Code != http.StatusNotImplemented {
		t.Fatalf("non-claude kind: %d %s", rec.Code, rec.Body.String())
	}
	session.WriteMeta(session.Meta{Name: "slot63", Dir: t.TempDir(), Kind: session.KindClaude})
	if rec := do("slot63", `{"decision":"approve"}`); rec.Code != http.StatusConflict {
		t.Fatalf("no pending plan: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do("slot63", `{"decision":"maybe"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad decision: %d %s", rec.Code, rec.Body.String())
	}
	// …but the pending plan is the captured PAYLOAD, not the state. ExitPlanMode's own
	// permission_prompt overwrites "plan" with "permission" while its approval dialog is
	// still up, and refusing there broke the plan card's コメント送信 outright: this 409
	// no_plan made the Console fall back to /input, which the {prompt} guard then refused
	// as permission_pending. Nothing could deliver the feedback.
	dir64 := t.TempDir()
	session.WriteMeta(session.Meta{Name: "slot64", Dir: dir64, Kind: session.KindClaude})
	sid64 := session.UUID(dir64, "slot64")
	status.Persist(sid64, "permission")
	status.WritePendingPlan(sid64, "# 作業計画")
	if rec := do("slot64", `{"decision":"reject","feedback":"ここを直して"}`); strings.Contains(rec.Body.String(), "no_plan") {
		t.Fatalf("captured plan must be decidable under the permission state: %d %s", rec.Code, rec.Body.String())
	}
}

// TestApplyManagedAnswerAll pins the operator's full-form managed answer: the live
// interaction is re-read, the 1-based choices land as 0-based option picks in
// question order, and the arm-side effects (working mark) fire.
func TestApplyManagedAnswerAll(t *testing.T) {
	withTempHome(t)
	m := session.Meta{Name: "slot51", Dir: t.TempDir(), Kind: session.KindCodex}
	session.WriteMeta(m)

	h := &fakeHandle{snap: agents.ThreadSnapshot{Interaction: questionInteraction("int-1")}}
	labels, err := applyManagedAnswerAll(h, m.Name, []int{2, 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(labels) != 2 || labels[0] != "a1" || labels[1] != "b2" {
		t.Fatalf("labels = %v", labels)
	}
	if h.responded == nil || h.responded.ID != "int-1" || h.responded.Decision != agents.DecisionAnswer {
		t.Fatalf("responded = %+v", h.responded)
	}
	if len(h.responded.Answers) != 2 ||
		len(h.responded.Answers[0].Options) != 1 || h.responded.Answers[0].Options[0] != 1 ||
		len(h.responded.Answers[1].Options) != 1 || h.responded.Answers[1].Options[0] != 2 {
		t.Fatalf("answers = %+v", h.responded.Answers)
	}

	// No pending interaction → a distinct sentinel (the handler maps it to 409).
	empty := &fakeHandle{snap: agents.ThreadSnapshot{}}
	if _, err := applyManagedAnswerAll(empty, m.Name, []int{1}); err != errNoPendingQuestion {
		t.Fatalf("err = %v, want errNoPendingQuestion", err)
	}
	// Bad choices never Respond.
	bad := &fakeHandle{snap: agents.ThreadSnapshot{Interaction: questionInteraction("int-2")}}
	if _, err := applyManagedAnswerAll(bad, m.Name, []int{9, 9}); err == nil || bad.responded != nil {
		t.Fatalf("out-of-range must fail without Respond (err=%v responded=%+v)", err, bad.responded)
	}
}
