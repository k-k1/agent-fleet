package memoafter

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// "Satisfied", one delivery state at a time: step 1 (a clean close with a good first turn) and
// step 2 (the PR snapshot). A first turn that is never recorded is satisfied by a merged PR alone,
// and a run without a PR only by a last answer that reads as done (issue #4876).
func TestDeliveryState(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	closed := func(ago time.Duration) string { return store.DeliveryTS(now.Add(-ago)) }
	good := store.MemoDelivery{State: store.DeliveryClosed, Outcome: store.DeliveryClean, ClosedAt: closed(time.Hour),
		TurnOutcome: "clean", TurnMs: 5_000, TurnEndedAt: "2026-11-19T10:00:00Z", PRState: store.PRStateNone,
		AnswerHead: "Done: x", AnswerClass: store.AnswerDone}
	with := func(f func(*store.MemoDelivery)) store.MemoDelivery { d := good; f(&d); return d }
	noTurn := func(d *store.MemoDelivery) { d.TurnEndedAt, d.TurnOutcome = "", "" }
	for _, c := range []struct {
		name          string
		d             store.MemoDelivery
		found         bool
		state, reason string
	}{
		{"gone", store.MemoDelivery{}, false, Failed, WhyUnknown},
		{"open", with(func(d *store.MemoDelivery) { d.State = store.DeliveryOpen }), true, Waiting, WhyRunning},
		{"sending", with(func(d *store.MemoDelivery) { d.State = store.DeliverySending }), true, Waiting, WhyRunning},
		{"aborted", with(func(d *store.MemoDelivery) { d.Outcome = store.DeliveryAborted }), true, Failed, WhyAborted},
		{"unknown", with(func(d *store.MemoDelivery) { d.Outcome = store.DeliveryUnknown }), true, Failed, WhyUnknown},
		{"ended by a person", with(func(d *store.MemoDelivery) { d.Outcome = store.DeliveryEnded }), true, Failed, WhyNotClean},
		{"overdue retire", with(func(d *store.MemoDelivery) { d.OverdueAt, d.RetireGen = closed(2*time.Hour), 4 }), true, Failed, WhyOverdue},
		{"overdue, no retire", with(func(d *store.MemoDelivery) { d.OverdueAt = closed(2 * time.Hour) }), true, Satisfied, ""},
		{"failed turn", with(func(d *store.MemoDelivery) { d.TurnOutcome = "turn-failed" }), true, Failed, WhyBadFirstTurn},
		{"quick turn of a quiet kind", with(func(d *store.MemoDelivery) { d.TurnOutcome = "" }), true, Failed, WhyBadFirstTurn},
		{"a bad turn with a merged PR", with(func(d *store.MemoDelivery) { d.TurnOutcome, d.PRState = "turn-failed", store.PRStateMerged }), true, Failed, WhyBadFirstTurn},
		// Step 2.
		{"good, no snapshot yet", with(func(d *store.MemoDelivery) { d.PRState = "" }), true, Waiting, WhyPR},
		{"good, a PR open", with(func(d *store.MemoDelivery) { d.PRState = store.PRStateWaiting }), true, Waiting, WhyPR},
		{"good, a PR closed", with(func(d *store.MemoDelivery) { d.PRState = store.PRStateClosed }), true, Failed, WhyPRClosed},
		{"good, session gone", with(func(d *store.MemoDelivery) { d.PRState = store.PRStateGone }), true, Failed, WhySessionGone},
		{"good, merged", with(func(d *store.MemoDelivery) { d.PRState = store.PRStateMerged }), true, Satisfied, ""},
		{"good, no PR", good, true, Satisfied, ""},
		{"good, no PR, answer not read", with(func(d *store.MemoDelivery) { d.AnswerHead, d.AnswerClass = "", "" }), true, Waiting, WhyAnswer},
		{"good, no PR, answer not read after the window", with(func(d *store.MemoDelivery) {
			d.AnswerHead, d.AnswerClass, d.ClosedAt = "", "", closed(25*time.Hour)
		}), true, Failed, WhyNotDone},
		{"good, no PR, partial", with(func(d *store.MemoDelivery) { d.AnswerClass = store.AnswerPartial }), true, Failed, WhyNotDone},
		{"good, no PR, other", with(func(d *store.MemoDelivery) { d.AnswerClass = store.AnswerOther }), true, Failed, WhyNotDone},
		{"good, no PR, unknown", with(func(d *store.MemoDelivery) { d.AnswerClass = store.AnswerUnknown }), true, Failed, WhyNotDone},
		{"good, merged, partial", with(func(d *store.MemoDelivery) { d.PRState, d.AnswerClass = store.PRStateMerged, store.AnswerPartial }), true, Satisfied, ""},
		{"no turn, no PR, done, in the window", with(noTurn), true, Waiting, WhyFirstTurn},
		// A first turn not recorded.
		{"no turn, no snapshot", with(func(d *store.MemoDelivery) { noTurn(d); d.PRState = "" }), true, Waiting, WhyPR},
		{"no turn, no PR yet in the window", with(noTurn), true, Waiting, WhyFirstTurn},
		{"no turn, no PR after the window", with(func(d *store.MemoDelivery) { noTurn(d); d.ClosedAt = closed(25 * time.Hour) }), true, Failed, WhyNoPR},
		{"no turn, merged, in the window", with(func(d *store.MemoDelivery) { noTurn(d); d.PRState = store.PRStateMerged }), true, Satisfied, ""},
		{"no turn, a PR open after the window", with(func(d *store.MemoDelivery) {
			noTurn(d)
			d.PRState, d.ClosedAt = store.PRStateWaiting, closed(30*time.Hour)
		}), true, Waiting, WhyPR},
	} {
		state, reason := DeliveryState(c.d, c.found, now)
		if state != c.state || reason != c.reason {
			t.Errorf("%s: %s/%s, want %s/%s", c.name, state, reason, c.state, c.reason)
		}
	}
}

// A snapshot sums up to none, closed, merged or waiting, and an unknown state never reads as merged.
func TestSnapshotState(t *testing.T) {
	for _, c := range []struct {
		prs  []PR
		want string
	}{
		{nil, store.PRStateNone},
		{[]PR{{State: "merged"}}, store.PRStateMerged},
		{[]PR{{State: "merged"}, {State: "open"}}, store.PRStateWaiting},
		{[]PR{{State: "merged"}, {State: "draft"}}, store.PRStateWaiting},
		{[]PR{{State: "merged"}, {State: ""}}, store.PRStateWaiting},
		{[]PR{{State: "open"}, {State: "closed"}}, store.PRStateClosed},
	} {
		if got := SnapshotState(c.prs); got != c.want {
			t.Errorf("%+v: %s, want %s", c.prs, got, c.want)
		}
	}
	if EncodeSnapshot(nil) != "[]" {
		t.Errorf("no PR encodes as %q", EncodeSnapshot(nil))
	}
}

// The head is the first line that holds anything, and its class reads past Markdown marks and case.
func TestClassifyAnswer(t *testing.T) {
	long := "Done: "
	for i := 0; i < 300; i++ {
		long += "x"
	}
	for _, c := range []struct{ in, head, class string }{
		{"Done: merged PR #3", "Done: merged PR #3", store.AnswerDone},
		{"\n\n  **Done:** nothing to change\nmore", "**Done:** nothing to change", store.AnswerDone},
		{"`Done:` x", "`Done:` x", store.AnswerDone},
		{"## done: x", "## done: x", store.AnswerDone},
		{"Done： 全角コロン", "Done： 全角コロン", store.AnswerDone},
		{"Partial: #1 part 1, next part queued", "Partial: #1 part 1, next part queued", store.AnswerPartial},
		{"Held: #1 part 2, PR #9 not merged", "Held: #1 part 2, PR #9 not merged", store.AnswerPartial},
		{"Deferred: waiting for #4", "Deferred: waiting for #4", store.AnswerPartial},
		{"Too large: #5", "Too large: #5", store.AnswerPartial},
		{"Stopped at part 3: #6", "Stopped at part 3: #6", store.AnswerPartial},
		{"- Partial: x", "- Partial: x", store.AnswerOther},
		{"> Done: the task it depends on\nThat was a quote: this one is not done.", "> Done: the task it depends on", store.AnswerOther},
		{"**Done**: x", "**Done**: x", store.AnswerDone},
		{"_Held:_ x", "_Held:_ x", store.AnswerPartial},
		{"**Stopped at part 3**: #6", "**Stopped at part 3**: #6", store.AnswerPartial},
		{"```Done:\nnot done yet\n```", "```Done:", store.AnswerOther},
		{"`Done: an unclosed mark", "`Done: an unclosed mark", store.AnswerOther},
		{"*Done: x", "*Done: x", store.AnswerOther},
		{"`Done`: x", "`Done`: x", store.AnswerDone},
		{"Done is what I would say: not yet", "Done is what I would say: not yet", store.AnswerOther},
		{"Done without a colon", "Done without a colon", store.AnswerOther},
		{"Not done: x", "Not done: x", store.AnswerOther},
		{"終わりました", "終わりました", store.AnswerOther},
		{"", "", store.AnswerUnknown},
		{" \n\t\n", "", store.AnswerUnknown},
	} {
		head, class := ClassifyAnswer(c.in)
		if head != c.head || class != c.class {
			t.Errorf("%q: %q/%s, want %q/%s", c.in, head, class, c.head, c.class)
		}
	}
	head, class := ClassifyAnswer(long)
	if n := len([]rune(head)); n != store.AnswerHeadMax || class != store.AnswerDone {
		t.Errorf("a long head: %d runes, %s", n, class)
	}
}

// The answer is what the session itself said after its last tool call, never a subagent's, and a
// response that ended on a tool call has none.
func TestLastAnswer(t *testing.T) {
	text := func(s string) TurnPart { return TurnPart{Kind: "text", Text: s} }
	tool := TurnPart{Kind: "tool"}
	for _, c := range []struct {
		name  string
		turns []Turn
		want  string
		found bool
	}{
		{"none", nil, "", false},
		{"user only", []Turn{{Role: "user", Text: "hi", Parts: []TurnPart{text("hi")}}}, "", false},
		{"after the last tool", []Turn{{Role: "assistant", Parts: []TurnPart{text("Looking."), tool, text("Done: a"), {Kind: "thinking"}, text("b")}}}, "Done: a\nb", true},
		{"one turn ending in a tool", []Turn{{Role: "assistant", Parts: []TurnPart{text("Done: wrote it"), tool}}}, "", true},
		{"a tool call in a turn of its own", []Turn{
			{Role: "assistant", Parts: []TurnPart{text("Done: wrote it")}},
			{Role: "assistant", Parts: []TurnPart{tool, text("  ")}},
		}, "", true},
		{"skips a subagent and a user turn", []Turn{
			{Role: "assistant", Parts: []TurnPart{text("Held: y")}},
			{Role: "assistant", Sidechain: true, Parts: []TurnPart{text("Done: sub")}},
			{Role: "user", Parts: []TurnPart{text("Done: user")}},
		}, "Held: y", true},
		{"skips a turn of thinking alone", []Turn{
			{Role: "assistant", Parts: []TurnPart{text("Done: z")}},
			{Role: "assistant", Parts: []TurnPart{{Kind: "thinking", Text: "hmm"}}},
		}, "Done: z", true},
		{"text without parts", []Turn{{Role: "assistant", Text: "Done: plain"}}, "Done: plain", true},
	} {
		if got, found := LastAnswer(c.turns); got != c.want || found != c.found {
			t.Errorf("%s: %q/%v, want %q/%v", c.name, got, found, c.want, c.found)
		}
	}
}
