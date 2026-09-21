package muse

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// newStore gives each test its own HOME so the stores do not collide, and returns a store
// with a unique sid.
func newStore(t *testing.T) *store {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	storesMu.Lock()
	stores = map[string]*store{}
	storesMu.Unlock()
	return openStore("sid-" + t.Name())
}

func sp(s string) *string { return &s }

func item(kind msp.ItemKind, id string, rev int64) msp.Item {
	return msp.Item{ItemID: id, Kind: kind, Revision: rev, Status: msp.ItemStatusCompleted}
}

func TestStoreFoldsRevisionsKeepingFirstSeenOrder(t *testing.T) {
	s := newStore(t)
	// A tool call is started, then completed; the text that follows it arrives in between as
	// far as the file is concerned. First-seen order is the order the conversation happened
	// in, so the tool call must stay before the text.
	tool := item(msp.ItemKindToolCall, "i-tool", 1)
	tool.Status = msp.ItemStatusInProgress
	tool.Tool = sp("shell")
	if err := s.Append(tool); err != nil {
		t.Fatal(err)
	}
	msg := item(msp.ItemKindAgentMessage, "i-msg", 1)
	msg.Text = sp("done")
	if err := s.Append(msg); err != nil {
		t.Fatal(err)
	}
	done := item(msp.ItemKindToolCall, "i-tool", 2)
	done.Tool = sp("shell")
	done.VisibleOutput = sp("ok")
	if err := s.Append(done); err != nil {
		t.Fatal(err)
	}

	items, err := s.Items()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("%d items, want 2 (the tool call folded)", len(items))
	}
	if items[0].ItemID != "i-tool" || items[1].ItemID != "i-msg" {
		t.Errorf("order = %s, %s — first-seen order was not kept", items[0].ItemID, items[1].ItemID)
	}
	if items[0].Revision != 2 || str(items[0].VisibleOutput) != "ok" {
		t.Errorf("the later revision did not win: %+v", items[0])
	}
}

// A revision that arrives out of order must not roll the item back to a stale body.
func TestStoreIgnoresAnOlderRevision(t *testing.T) {
	s := newStore(t)
	newer := item(msp.ItemKindAgentMessage, "i-1", 5)
	newer.Text = sp("final")
	older := item(msp.ItemKindAgentMessage, "i-1", 2)
	older.Text = sp("partial")
	s.Append(newer)
	s.Append(older)

	items, _ := s.Items()
	if len(items) != 1 || str(items[0].Text) != "final" {
		t.Errorf("an older revision overwrote a newer one: %+v", items)
	}
}

// A crash can leave a half-written last line. Losing that item is expected; losing the whole
// conversation is not.
func TestStoreSurvivesATornLastLine(t *testing.T) {
	s := newStore(t)
	good := item(msp.ItemKindAgentMessage, "i-1", 1)
	good.Text = sp("kept")
	if err := s.Append(good); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(s.Path(), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"seen":"2026-09-21T00:00:00Z","item":{"itemId":"i-2","ki`)
	f.Close()

	items, err := s.Items()
	if err != nil {
		t.Fatalf("a torn line made the whole store unreadable: %v", err)
	}
	if len(items) != 1 || str(items[0].Text) != "kept" {
		t.Errorf("items = %+v", items)
	}
}

func TestStoreOfAMissingFileIsEmptyNotAnError(t *testing.T) {
	s := newStore(t)
	items, err := s.Items()
	if err != nil {
		t.Fatalf("err = %v, want nil for a session with no history", err)
	}
	if len(items) != 0 {
		t.Errorf("%d items", len(items))
	}
}

// --- items to turns ----------------------------------------------------------

func TestTurnsGroupAssistantItemsUntilTheNextUserMessage(t *testing.T) {
	user := item(msp.ItemKindUserMessage, "u-1", 1)
	user.Text = sp("do the thing")
	think := item(msp.ItemKindReasoning, "r-1", 1)
	think.Text = sp("considering")
	tool := item(msp.ItemKindToolCall, "t-1", 1)
	tool.Tool = sp("shell")
	tool.CommandText = sp("ls -la")
	tool.VisibleOutput = sp("a\nb")
	msg := item(msp.ItemKindAgentMessage, "a-1", 1)
	msg.Text = sp("done")
	user2 := item(msp.ItemKindUserMessage, "u-2", 1)
	user2.Text = sp("again")

	turns := turnsFromItems([]msp.Item{user, think, tool, msg, user2})
	if len(turns) != 3 {
		t.Fatalf("%d turns, want user / assistant / user: %+v", len(turns), turns)
	}
	if turns[0].Role != "user" || turns[0].Text != "do the thing" {
		t.Errorf("turn 0 = %+v", turns[0])
	}
	a := turns[1]
	if a.Role != "assistant" {
		t.Fatalf("turn 1 role = %q", a.Role)
	}
	if len(a.Parts) != 3 {
		t.Fatalf("%d parts, want thinking + tool + text: %+v", len(a.Parts), a.Parts)
	}
	if a.Parts[0].Kind != "thinking" || a.Parts[1].Kind != "tool" || a.Parts[2].Kind != "text" {
		t.Errorf("part kinds = %s %s %s", a.Parts[0].Kind, a.Parts[1].Kind, a.Parts[2].Kind)
	}
	if a.Parts[1].Tool != "shell" || a.Parts[1].Info != "ls -la" || a.Parts[1].Output != "a\nb" {
		t.Errorf("tool part = %+v", a.Parts[1])
	}
	if a.Text != "done" {
		t.Errorf("assistant Text = %q", a.Text)
	}
	if turns[2].Role != "user" {
		t.Errorf("turn 2 role = %q", turns[2].Role)
	}
	// Idx is the render key and must be the position, or React reuses rows across polls.
	for i, tr := range turns {
		if tr.Idx != i {
			t.Errorf("turn %d has Idx %d", i, tr.Idx)
		}
	}
}

// Several agent messages in one turn concatenate rather than each opening a turn of their own.
func TestSeveralAgentMessagesStayInOneTurn(t *testing.T) {
	a := item(msp.ItemKindAgentMessage, "a-1", 1)
	a.Text = sp("first")
	b := item(msp.ItemKindAgentMessage, "a-2", 1)
	b.Text = sp("second")

	turns := turnsFromItems([]msp.Item{a, b})
	if len(turns) != 1 {
		t.Fatalf("%d turns, want 1", len(turns))
	}
	if turns[0].Text != "first\nsecond" {
		t.Errorf("Text = %q", turns[0].Text)
	}
}

// The member's own `!command` is their input, not the agent's work.
func TestUserShellOpensAUserTurn(t *testing.T) {
	sh := item(msp.ItemKindUserShell, "s-1", 1)
	sh.CommandText = sp("git status")
	sh.VisibleOutput = sp("clean")

	turns := turnsFromItems([]msp.Item{sh})
	if len(turns) != 1 || turns[0].Role != "user" {
		t.Fatalf("turns = %+v", turns)
	}
	if turns[0].Parts[0].Tool != "shell" || turns[0].Parts[0].Output != "clean" {
		t.Errorf("part = %+v", turns[0].Parts[0])
	}
}

// Subagent records are interleaved into the parent's own stream under their own stream id
// (there is no per-child directory), so the item's own fields are what tell them apart.
func TestSubagentItemsRenderAsDelegationAndMarkTheSidechain(t *testing.T) {
	sub := item(msp.ItemKindSubagent, "g-1", 1)
	sub.AgentPath = sp("explorer")
	sub.Objective = sp("find the caller")
	sub.SubagentID = sp("subagent-1")
	sub.Result = &msp.SubagentResult{Summary: "found it"}

	turns := turnsFromItems([]msp.Item{sub})
	if len(turns) != 1 {
		t.Fatalf("turns = %+v", turns)
	}
	p := turns[0].Parts[0]
	if p.Kind != "delegation" || p.AgentType != "explorer" || p.Prompt != "find the caller" {
		t.Errorf("part = %+v", p)
	}
	if p.Output != "found it" {
		t.Errorf("output = %q", p.Output)
	}
	if !turns[0].Sidechain {
		t.Error("a subagent turn must be marked as a sidechain")
	}
}

func TestSidechainDetection(t *testing.T) {
	depth := int64(2)
	zero := int64(0)
	cases := []struct {
		name string
		it   msp.Item
		want bool
	}{
		{"root", msp.Item{}, false},
		{"subagent id", msp.Item{SubagentID: sp("s-1")}, true},
		{"empty subagent id", msp.Item{SubagentID: sp("")}, false},
		{"child session", msp.Item{ChildSessionID: sp("c-1")}, true},
		{"depth", msp.Item{Depth: &depth}, true},
		{"depth zero", msp.Item{Depth: &zero}, false},
	}
	for _, c := range cases {
		if got := isSidechain(c.it); got != c.want {
			t.Errorf("%s: isSidechain = %v, want %v", c.name, got, c.want)
		}
	}
}

// A failed tool call with no output must not render as a blank result, which reads as "it
// worked and said nothing".
func TestFailedToolCallShowsWhyItFailed(t *testing.T) {
	it := item(msp.ItemKindToolCall, "t-1", 1)
	it.Tool = sp("shell")
	it.Status = msp.ItemStatusFailed
	it.FailureReason = sp("permission denied")

	p := toolPart(it)
	if !strings.Contains(p.Output, "permission denied") {
		t.Errorf("output = %q, want the failure reason", p.Output)
	}
}

func TestCompactionRendersAsACollapsedBlock(t *testing.T) {
	before, after := int64(100000), int64(20000)
	it := item(msp.ItemKindCompaction, "c-1", 1)
	it.TokensBefore, it.TokensAfter = &before, &after

	turns := turnsFromItems([]msp.Item{it})
	if len(turns) != 1 {
		t.Fatalf("turns = %+v", turns)
	}
	if !turns[0].Compact {
		t.Error("a compaction must be marked Compact, or it renders as an ordinary user prompt")
	}
	if !strings.Contains(turns[0].Text, "100000") {
		t.Errorf("text = %q", turns[0].Text)
	}
}

// A compaction closes the open assistant turn: the text after it belongs to a new one.
func TestCompactionClosesTheAssistantTurn(t *testing.T) {
	a := item(msp.ItemKindAgentMessage, "a-1", 1)
	a.Text = sp("before")
	c := item(msp.ItemKindCompaction, "c-1", 1)
	b := item(msp.ItemKindAgentMessage, "a-2", 1)
	b.Text = sp("after")

	turns := turnsFromItems([]msp.Item{a, c, b})
	if len(turns) != 3 {
		t.Fatalf("%d turns, want assistant / compaction / assistant", len(turns))
	}
	if turns[2].Text != "after" {
		t.Errorf("the turn after the compaction = %q", turns[2].Text)
	}
}

// Usage is per model completion and several items share one, so output is summed while the
// counted-once input and cache figures take the latest value.
func TestUsageIsSummedForOutputAndLatestForInput(t *testing.T) {
	a := item(msp.ItemKindAgentMessage, "a-1", 1)
	a.Text = sp("one")
	a.Usage = &msp.TokenUsage{InputTokens: 100, OutputTokens: 10, CachedTokens: 40}
	b := item(msp.ItemKindAgentMessage, "a-2", 1)
	b.Text = sp("two")
	b.Usage = &msp.TokenUsage{InputTokens: 160, OutputTokens: 7, CachedTokens: 90}

	turns := turnsFromItems([]msp.Item{a, b})
	if len(turns) != 1 {
		t.Fatalf("turns = %+v", turns)
	}
	if turns[0].OutTok != 17 {
		t.Errorf("OutTok = %d, want 17 (summed)", turns[0].OutTok)
	}
	if turns[0].InTok != 160 {
		t.Errorf("InTok = %d, want 160 (latest, counted once)", turns[0].InTok)
	}
	if turns[0].CacheRead != 90 {
		t.Errorf("CacheRead = %d, want 90", turns[0].CacheRead)
	}
}

// --- the live stream ---------------------------------------------------------

func TestItemNotificationsReachTheStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storesMu.Lock()
	stores = map[string]*store{}
	storesMu.Unlock()

	h := &threadHandle{slotSid: "sid-stream"}
	host := newTestHandle(t, h)

	msg := item(msp.ItemKindAgentMessage, "a-1", 1)
	msg.Text = sp("hello there")
	host.Notify(msp.NotificationItemCompleted, msp.ItemCompletedParams{Item: msg, SessionID: h.sid})

	deadline := time.After(5 * time.Second)
	for {
		items, _ := openStore("sid-stream").Items()
		if len(items) == 1 && str(items[0].Text) == "hello there" {
			return
		}
		select {
		case <-deadline:
			t.Fatal("the item never reached the store")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// Deltas are not persisted — the completed item carries the whole text — but they must show
// in the mirror while the turn runs, or an answer appears only once it is finished.
func TestDeltasStreamButAreNotPersisted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storesMu.Lock()
	stores = map[string]*store{}
	storesMu.Unlock()

	h := &threadHandle{slotSid: "sid-delta"}
	host := newTestHandle(t, h)
	host.Notify(msp.NotificationItemDelta, msp.ItemDeltaParams{ItemID: "a-1", Delta: "par", SessionID: h.sid})
	host.Notify(msp.NotificationItemDelta, msp.ItemDeltaParams{ItemID: "a-1", Delta: "tial", SessionID: h.sid})

	deadline := time.After(5 * time.Second)
	for {
		if f := h.streamingText(); f["a-1"] == "partial" {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("the deltas never accumulated: %v", h.streamingText())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if items, _ := openStore("sid-delta").Items(); len(items) != 0 {
		t.Errorf("%d items persisted; deltas belong in memory only", len(items))
	}

	// The overlay is what the mirror renders.
	turns := appendStreaming(nil, nil, h.streamingText())
	if len(turns) != 1 || turns[0].Text != "partial" {
		t.Errorf("overlay = %+v", turns)
	}
}

// Once the item lands, its fragments must go — leaving them would render the text twice.
func TestCompletedItemDropsItsFragments(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storesMu.Lock()
	stores = map[string]*store{}
	storesMu.Unlock()

	h := &threadHandle{slotSid: "sid-drop"}
	host := newTestHandle(t, h)
	host.Notify(msp.NotificationItemDelta, msp.ItemDeltaParams{ItemID: "a-1", Delta: "part", SessionID: h.sid})
	deadline := time.After(5 * time.Second)
	for h.streamingText()["a-1"] == "" {
		select {
		case <-deadline:
			t.Fatal("the delta never arrived")
		case <-time.After(10 * time.Millisecond):
		}
	}

	done := item(msp.ItemKindAgentMessage, "a-1", 2)
	done.Text = sp("partial answer")
	host.Notify(msp.NotificationItemCompleted, msp.ItemCompletedParams{Item: done, SessionID: h.sid})

	deadline = time.After(5 * time.Second)
	for len(h.streamingText()) > 0 {
		select {
		case <-deadline:
			t.Fatalf("fragments survived the completed item: %v", h.streamingText())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// A fragment for an item the store already holds is a delta that arrived after its own
// completion; re-appending it would duplicate text the turn already shows.
func TestOverlayDropsFragmentsOfKnownItems(t *testing.T) {
	known := item(msp.ItemKindAgentMessage, "a-1", 1)
	known.Text = sp("whole answer")
	turns := turnsFromItems([]msp.Item{known})

	out := appendStreaming(turns, []msp.Item{known}, map[string]string{"a-1": "whole"})
	if len(out) != 1 || len(out[0].Parts) != 1 {
		t.Errorf("a stale fragment was appended: %+v", out)
	}
}

// A delta naming a member other than the text is not something the mirror can splice, and
// appending it to the text would put a field's contents into the answer.
func TestDeltaForAnotherFieldIsDropped(t *testing.T) {
	h := &threadHandle{slotSid: "sid-field"}
	h.onDelta(msp.ItemDeltaParams{ItemID: "a-1", Delta: "x", Field: sp("visibleOutput")})
	if len(h.streamingText()) != 0 {
		t.Errorf("a non-text delta was buffered: %v", h.streamingText())
	}
	h.onDelta(msp.ItemDeltaParams{ItemID: "a-1", Delta: "y", Field: sp("text")})
	if h.streamingText()["a-1"] != "y" {
		t.Errorf("an explicit text delta was dropped: %v", h.streamingText())
	}
}

// --- the read layer ----------------------------------------------------------

func TestTranscriptOverlaysThePendingPromptAndTheQueue(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storesMu.Lock()
	stores = map[string]*store{}
	storesMu.Unlock()

	m := metaFor(t, "muse-td")
	h := &threadHandle{name: m.Name, slotSid: slotSid(m)}
	newTestHandle(t, h)
	handlesMu.Lock()
	handles[m.Name] = h
	handlesMu.Unlock()
	t.Cleanup(func() {
		handlesMu.Lock()
		delete(handles, m.Name)
		handlesMu.Unlock()
	})

	h.mu.Lock()
	h.inter = &agents.Interaction{ID: "q", Questions: []transcript.Question{{ID: "q", Question: "which?"}}}
	h.queue = []agents.TurnInput{{Prompt: "next please"}}
	h.mu.Unlock()

	td, ok := New().Transcript(m)
	if !ok {
		t.Fatal("Transcript reported no source")
	}
	if len(td.Pending) != 1 || td.Pending[0].Question != "which?" {
		t.Errorf("Pending = %+v", td.Pending)
	}
	if len(td.Queued) != 1 || td.Queued[0] != "next please" {
		t.Errorf("Queued = %+v", td.Queued)
	}
}

// metaFor builds a session meta in a fresh working copy.
func metaFor(t *testing.T, name string) session.Meta {
	t.Helper()
	return session.Meta{Kind: session.KindMuse, Name: name + "-" + t.Name(), Dir: t.TempDir(), Driver: session.DriverManaged}
}

// A user message must CLOSE the open assistant turn. Without that reset every assistant item
// in the whole conversation folds into the first assistant turn, and the mirror shows one
// enormous reply with the member's later prompts stranded after it.
func TestAUserMessageClosesTheOpenAssistantTurn(t *testing.T) {
	a1 := item(msp.ItemKindAgentMessage, "a-1", 1)
	a1.Text = sp("first answer")
	u := item(msp.ItemKindUserMessage, "u-1", 1)
	u.Text = sp("and now this")
	a2 := item(msp.ItemKindAgentMessage, "a-2", 1)
	a2.Text = sp("second answer")

	turns := turnsFromItems([]msp.Item{a1, u, a2})
	if len(turns) != 3 {
		t.Fatalf("%d turns, want assistant / user / assistant: %+v", len(turns), turns)
	}
	if turns[0].Text != "first answer" {
		t.Errorf("turn 0 = %q", turns[0].Text)
	}
	if turns[1].Role != "user" {
		t.Errorf("turn 1 role = %q", turns[1].Role)
	}
	if turns[2].Role != "assistant" || turns[2].Text != "second answer" {
		t.Errorf("turn 2 = %+v — the second answer folded into the first", turns[2])
	}
}

// The same for the member's own shell command, which is also their input.
func TestUserShellClosesTheOpenAssistantTurn(t *testing.T) {
	a1 := item(msp.ItemKindAgentMessage, "a-1", 1)
	a1.Text = sp("before")
	sh := item(msp.ItemKindUserShell, "s-1", 1)
	sh.CommandText = sp("git status")
	a2 := item(msp.ItemKindAgentMessage, "a-2", 1)
	a2.Text = sp("after")

	turns := turnsFromItems([]msp.Item{a1, sh, a2})
	if len(turns) != 3 {
		t.Fatalf("%d turns, want 3: %+v", len(turns), turns)
	}
	if turns[2].Text != "after" {
		t.Errorf("turn 2 = %q", turns[2].Text)
	}
}

// A failed delegation with no result must say why, for the same reason a failed tool call does.
func TestFailedDelegationShowsWhyItFailed(t *testing.T) {
	it := item(msp.ItemKindSubagent, "g-1", 1)
	it.Status = msp.ItemStatusFailed
	it.FailureReason = sp("child ran out of steps")

	p := delegationPart(it)
	if !strings.Contains(p.Output, "out of steps") {
		t.Errorf("output = %q, want the failure reason", p.Output)
	}
}

// 🔴 A muse assistant turn is ONE row, so without EndTS the mirror's footer shows the turn's
// START: footTime falls back to ts when endTs is absent (console turnTime.ts). Found by working
// through the Console-surface checklist, not by a failing test — nothing here asserted it.
//
// The user arm is the control: a user turn is a single moment and must NOT grow an end, or the
// mirror would draw a duration for a row that has none.
func TestAssistantTurnCarriesItsEndTime(t *testing.T) {
	const (
		t0 = "2026-09-21T10:00:00Z"
		t1 = "2026-09-21T10:00:20Z"
		t2 = "2026-09-21T10:01:30Z"
	)
	items := []msp.Item{
		{ItemID: "u1", Kind: msp.ItemKindUserMessage, Text: sp("go"), RecordedAt: sp(t0)},
		{ItemID: "a1", Kind: msp.ItemKindAgentMessage, Text: sp("working"), RecordedAt: sp(t1)},
		{ItemID: "a2", Kind: msp.ItemKindAgentMessage, Text: sp("done"), RecordedAt: sp(t2)},
	}
	turns := turnsFromItems(items)
	if len(turns) != 2 {
		t.Fatalf("got %d turns, want user + assistant", len(turns))
	}
	if turns[0].Role != "user" || turns[0].EndTS != "" {
		t.Errorf("the user turn grew an end time (%q); a single moment has no duration", turns[0].EndTS)
	}
	a := turns[1]
	if a.TS != t1 {
		t.Errorf("assistant TS = %q, want the FIRST folded item %q", a.TS, t1)
	}
	if a.EndTS != t2 {
		t.Errorf("assistant EndTS = %q, want the LAST folded item %q — the footer would show "+
			"the turn's start instead of when it finished", a.EndTS, t2)
	}
}
