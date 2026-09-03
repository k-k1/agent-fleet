package sessionx

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/bridge"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// fakeHandle is a minimal agents.ThreadHandle for the managed-answer tests: it serves
// a canned Snapshot and records the Respond it receives. Every other method is an
// inert stub (applyManagedQuestion only touches Snapshot + Respond).
type fakeHandle struct {
	snap      agents.ThreadSnapshot
	responded *agents.InteractionReply
	respErr   error
}

func (f *fakeHandle) Snapshot() (agents.ThreadSnapshot, error) { return f.snap, nil }
func (f *fakeHandle) Respond(r agents.InteractionReply) error {
	f.responded = &r
	return f.respErr
}
func (f *fakeHandle) Send(agents.TurnInput) error                { return nil }
func (f *fakeHandle) Steer(agents.TurnInput) error               { return nil }
func (f *fakeHandle) Interrupt() error                           { return nil }
func (f *fakeHandle) UpdateSettings(agents.ThreadSettings) error { return nil }
func (f *fakeHandle) Events() <-chan agents.Event                { return nil }

func questionInteraction(id string) *agents.Interaction {
	return &agents.Interaction{ID: id, Kind: "question", Questions: []transcript.Question{
		{Header: "A", Question: "pick a", Options: []transcript.Option{{Label: "a0"}, {Label: "a1"}}},
		{Header: "B", Question: "pick b", Options: []transcript.Option{{Label: "b0"}, {Label: "b1"}, {Label: "b2"}}},
	}}
}

// fpOf reproduces the send-side fingerprint: the marshaled live questions, exactly
// what codex.PendingInteraction attaches and questionMessages hashes.
func fpOf(t *testing.T, inter *agents.Interaction) string {
	t.Helper()
	raw, err := json.Marshal(inter.Questions)
	if err != nil {
		t.Fatal(err)
	}
	return bridge.QuestionFingerprint(raw)
}

// TestBuildClaudeSingleSelectKeys pins the Go reproduction of console
// questionKeys.ts buildClaudeSeq (single-select, no free-text): per question
// Down×index + Enter, then a trailing Enter for the review page.
func TestBuildClaudeSingleSelectKeys(t *testing.T) {
	cases := []struct {
		name  string
		n     int
		picks map[int]int
		want  []string
	}{
		{"single q, first option", 1, map[int]int{0: 0}, []string{"Enter", "Enter"}},
		{"single q, third option", 1, map[int]int{0: 2}, []string{"Down", "Down", "Enter", "Enter"}},
		{"two q", 2, map[int]int{0: 1, 1: 0}, []string{"Down", "Enter", "Enter", "Enter"}},
		{"three q", 3, map[int]int{0: 0, 1: 2, 2: 1},
			[]string{"Enter", "Down", "Down", "Enter", "Down", "Enter", "Enter"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildClaudeSingleSelectKeys(tc.n, tc.picks); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("keys=%v, want %v", got, tc.want)
			}
		})
	}
}

// TestDecisionKeys pins the permission/plan sequences to exactly what MirrorView
// drives (option order is claude-version-verified there).
func TestDecisionKeys(t *testing.T) {
	if got := permKeys("allow"); !reflect.DeepEqual(got, []string{"Enter"}) {
		t.Errorf("perm allow=%v", got)
	}
	if got := permKeys("deny"); !reflect.DeepEqual(got, []string{"Down", "Down", "Enter"}) {
		t.Errorf("perm deny=%v", got)
	}
	if got := planKeys("approve"); !reflect.DeepEqual(got, []string{"Enter"}) {
		t.Errorf("plan approve=%v", got)
	}
	if got := planKeys("reject"); !reflect.DeepEqual(got, []string{"Down", "Down", "Down", "Enter"}) {
		t.Errorf("plan reject=%v", got)
	}
}

// TestBuildInteractionAnswers maps accumulated picks to one option-index answer per
// question, in order.
func TestBuildInteractionAnswers(t *testing.T) {
	got := buildInteractionAnswers(map[int]int{0: 1, 1: 2}, 2)
	want := []agents.InteractionAnswer{{Options: []int{1}}, {Options: []int{2}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("answers=%v, want %v", got, want)
	}
}

// TestApplyManagedQuestionAccumulatesThenResponds: a two-question form takes one pick
// per question and only Responds once both are in, with the LIVE interaction id and the
// per-question option indices.
func TestApplyManagedQuestionAccumulatesThenResponds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	inter := questionInteraction("live-id-9")
	fp := fpOf(t, inter)
	h := &fakeHandle{snap: agents.ThreadSnapshot{Interaction: inter}}

	// First question answered → waits, no Respond yet.
	out, err := applyManagedQuestion(h, "sess", bridge.ParsedInteraction{Kind: "q", Session: "sess", QI: 0, OI: 1, Fp: fp}, false)
	if err != nil {
		t.Fatal(err)
	}
	if h.responded != nil {
		t.Fatalf("must not respond before all questions answered; out=%q", out)
	}

	// Second question answered → Respond fires.
	out, err = applyManagedQuestion(h, "sess", bridge.ParsedInteraction{Kind: "q", Session: "sess", QI: 1, OI: 2, Fp: fp}, false)
	if err != nil {
		t.Fatal(err)
	}
	if h.responded == nil {
		t.Fatalf("expected Respond after both questions; out=%q", out)
	}
	if h.responded.ID != "live-id-9" || h.responded.Decision != agents.DecisionAnswer {
		t.Fatalf("reply id/decision = %q/%q", h.responded.ID, h.responded.Decision)
	}
	want := []agents.InteractionAnswer{{Options: []int{1}}, {Options: []int{2}}}
	if !reflect.DeepEqual(h.responded.Answers, want) {
		t.Fatalf("answers=%v, want %v", h.responded.Answers, want)
	}
}

// TestApplyManagedQuestionStaleFingerprint rejects a click whose questions no longer
// match the live interaction (the form changed) — without responding.
func TestApplyManagedQuestionStaleFingerprint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := &fakeHandle{snap: agents.ThreadSnapshot{Interaction: questionInteraction("id1")}}
	out, err := applyManagedQuestion(h, "sess", bridge.ParsedInteraction{Kind: "q", Session: "sess", QI: 0, OI: 0, Fp: "stale0"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if h.responded != nil {
		t.Fatal("stale fingerprint must not respond")
	}
	if out == "" {
		t.Fatal("stale click should return a feedback line")
	}
}

// TestApplyManagedQuestionNoInteraction: the interaction is already gone (answered
// elsewhere) → feedback, no Respond.
func TestApplyManagedQuestionNoInteraction(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := &fakeHandle{snap: agents.ThreadSnapshot{Interaction: nil}}
	out, err := applyManagedQuestion(h, "sess", bridge.ParsedInteraction{Kind: "q", Session: "sess", Fp: "x"}, false)
	if err != nil || h.responded != nil || out == "" {
		t.Fatalf("want feedback and no respond; out=%q err=%v responded=%v", out, err, h.responded)
	}
}
