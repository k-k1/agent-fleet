package muse

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp/msptest"
)

func textItem(id string, kind msp.ItemKind, text string, rev int64) msp.Item {
	return msp.Item{ItemID: id, Kind: kind, Text: &text, Status: msp.ItemStatusCompleted, Revision: rev}
}

// backfillHandle is a handle whose slot has a stored session to resume and a mirror holding
// the first turn as the Agent saw it: the user message, and the bash still at revision 1
// because the Agent died before its completion arrived.
func backfillHandle(t *testing.T) (*threadHandle, *msptest.Host) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	h := &threadHandle{slotSid: "00000000-0000-5000-8000-0000000000d1"}
	host := newTestHandle(t, h)
	registerHandle(t, "bf-"+t.Name(), h)
	writeSession(h.slotSid, museSession{ID: "01a0c1d6-0000-7000-8000-0000000000d2", Path: "/tmp/s.jsonl"})
	st := openStore(h.slotSid)
	for _, it := range []msp.Item{
		textItem("u1", msp.ItemKindUserMessage, "run the tests", 1),
		toolItem("bash-1", "bash", msp.ItemStatusInProgress, 1),
	} {
		if err := st.appendRecord(record{Item: it, Model: "muse-spark-1.3"}); err != nil {
			t.Fatal(err)
		}
	}
	return h, host
}

// hostHistory is the host's fold: the first turn finished, and a second one the Agent never saw.
func hostHistory() []msp.Item {
	return []msp.Item{
		textItem("u1", msp.ItemKindUserMessage, "run the tests", 1),
		toolItem("bash-1", "bash", msp.ItemStatusCompleted, 2),
		textItem("a1", msp.ItemKindAgentMessage, "all green", 1),
		textItem("u2", msp.ItemKindUserMessage, "now push", 1),
		textItem("a2", msp.ItemKindAgentMessage, "pushed", 1),
	}
}

func resumeWith(host *msptest.Host, history any) {
	sess := map[string]any{"sessionId": "01a0c1d6-0000-7000-8000-0000000000d2", "path": "/tmp/s.jsonl", "status": "idle", "createdAt": "", "updatedAt": "", "turnCount": 2}
	host.Handle(msp.MethodSessionResume, func(msptest.Message) (any, *msp.Error) {
		return map[string]any{"session": sess, "history": history, "pendingRequests": []any{}, "viewCursor": "c9"}, nil
	})
}

func mirrorState(t *testing.T, h *threadHandle) map[string]msp.Item {
	t.Helper()
	items, err := openStore(h.slotSid).Items()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]msp.Item{}
	for _, it := range items {
		out[it.ItemID] = it
	}
	return out
}

func wantMirrorMatchesHost(t *testing.T, h *threadHandle) {
	t.Helper()
	got := mirrorState(t, h)
	for _, want := range hostHistory() {
		it, ok := got[want.ItemID]
		if !ok {
			t.Errorf("mirror lacks %s after the resume", want.ItemID)
			continue
		}
		if it.Revision != want.Revision || it.Status != want.Status {
			t.Errorf("%s: mirror has rev %d %s, host rev %d %s", want.ItemID, it.Revision, it.Status, want.Revision, want.Status)
		}
	}
	items, _ := openStore(h.slotSid).Items()
	var order []string
	for _, it := range items {
		order = append(order, it.ItemID)
	}
	if len(order) != 5 || order[0] != "u1" || order[1] != "bash-1" || order[4] != "a2" {
		t.Errorf("mirror order = %v, want the conversation's order", order)
	}
}

// The case #1197 is about: a turn that ran while the Agent was down is in the host's history
// and not in the mirror, and so was the completion of a tool call the Agent did see start.
func TestResumeBackfillsWhatTheMirrorMissed(t *testing.T) {
	h, host := backfillHandle(t)
	resumeWith(host, map[string]any{"mode": "inline", "items": hostHistory()})
	if err := h.openSession(h.cl, agents.ThreadSettings{Model: "muse-spark-1.3"}); err != nil {
		t.Fatalf("openSession: %v", err)
	}
	wantMirrorMatchesHost(t, h)

	// What the member sees: the missed turn is in the transcript.
	items, err := openStore(h.slotSid).Items()
	if err != nil {
		t.Fatal(err)
	}
	turns := turnsFromItems(items)
	found := false
	for _, turn := range turns {
		if turn.Role == "assistant" && turn.Text == "pushed" {
			found = true
		}
	}
	if !found {
		t.Errorf("the missed turn's reply is not in the transcript: %+v", turns)
	}
	// The resume answered with the history itself; no second read.
	for _, m := range host.Received() {
		if m.Method == msp.MethodSessionRead {
			t.Error("session/read was called although session/resume carried the history")
		}
	}
	// And the rebuilt background set agrees: the bash is finished in the host's fold.
	if busy, _ := BackgroundWork(h.name); busy {
		t.Error("the backfilled, completed bash still reads as running")
	}
}

// A second resume over the same history writes nothing: the mirror already has it all.
func TestResumeBackfillIsIdempotent(t *testing.T) {
	h, host := backfillHandle(t)
	resumeWith(host, map[string]any{"mode": "inline", "items": hostHistory()})
	for i := 0; i < 2; i++ {
		if err := h.openSession(h.cl, agents.ThreadSettings{Model: "muse-spark-1.3"}); err != nil {
			t.Fatalf("openSession %d: %v", i, err)
		}
	}
	if n, err := openStore(h.slotSid).backfill(hostHistory()); err != nil || n != 0 {
		t.Errorf("a third backfill appended %d (err %v), want 0", n, err)
	}
}

// The mirror's own stamps survive: the first-seen model stays on the item the Agent recorded.
func TestResumeBackfillKeepsTheMirrorsStamps(t *testing.T) {
	h, host := backfillHandle(t)
	resumeWith(host, map[string]any{"mode": "inline", "items": hostHistory()})
	if err := h.openSession(h.cl, agents.ThreadSettings{Model: "muse-spark-1.3"}); err != nil {
		t.Fatal(err)
	}
	_, meta, err := openStore(h.slotSid).itemsWithMeta()
	if err != nil {
		t.Fatal(err)
	}
	if meta["bash-1"].model != "muse-spark-1.3" {
		t.Errorf("bash-1 model = %q after the backfill, want the stamp the Agent recorded", meta["bash-1"].model)
	}
}

// A resume that served no items (mode none) is not an empty history: the backfill asks
// session/read for the fold, with the items it carries only when asked.
func TestResumeWithoutItemsBackfillsFromSessionRead(t *testing.T) {
	h, host := backfillHandle(t)
	resumeWith(host, map[string]any{"mode": "none", "noneReason": "excludedByClient"})
	host.Handle(msp.MethodSessionRead, func(m msptest.Message) (any, *msp.Error) {
		var p msp.SessionReadParams
		_ = json.Unmarshal(m.Params, &p)
		if p.SessionID != "01a0c1d6-0000-7000-8000-0000000000d2" {
			t.Errorf("session/read sessionId = %q", p.SessionID)
		}
		if p.ExcludeItems == nil || *p.ExcludeItems {
			t.Error("session/read must ask for the items: excludeItems defaults to true there")
		}
		return map[string]any{
			"history":         map[string]any{"mode": "inline", "items": hostHistory()},
			"session":         map[string]any{"sessionId": p.SessionID, "path": "/tmp/s.jsonl", "status": "idle", "createdAt": "", "updatedAt": "", "turnCount": 2},
			"pendingRequests": []any{}, "viewCursor": "c9",
		}, nil
	})
	if err := h.openSession(h.cl, agents.ThreadSettings{Model: "muse-spark-1.3"}); err != nil {
		t.Fatalf("openSession: %v", err)
	}
	wantMirrorMatchesHost(t, h)
}

// A failed read leaves the mirror as it was and the session resumed: the host owns the
// conversation, and a gap in AF's copy is no reason to refuse it.
func TestResumeBackfillFailureIsNotFatal(t *testing.T) {
	h, host := backfillHandle(t)
	resumeWith(host, map[string]any{"mode": "none"})
	host.Handle(msp.MethodSessionRead, func(msptest.Message) (any, *msp.Error) {
		return nil, &msp.Error{Code: -32603, Message: "read failed"}
	})
	if err := h.openSession(h.cl, agents.ThreadSettings{Model: "muse-spark-1.3"}); err != nil {
		t.Fatalf("a failed backfill failed the resume: %v", err)
	}
	got := mirrorState(t, h)
	if len(got) != 2 || got["bash-1"].Revision != 1 {
		t.Errorf("the mirror changed on a failed read: %+v", got)
	}
}

// mirrorWith is a store holding exactly these items, in this order.
func mirrorWith(t *testing.T, sid string, items ...msp.Item) *store {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	st := openStore(sid)
	st.Remove()
	for _, it := range items {
		if err := st.appendRecord(record{Item: it}); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func storeOrder(t *testing.T, st *store) []string {
	t.Helper()
	items, err := st.Items()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, it := range items {
		ids = append(ids, it.ItemID)
	}
	return ids
}

func wantOrder(t *testing.T, st *store, want ...string) {
	t.Helper()
	if got := storeOrder(t, st); !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

var (
	u1 = textItem("u1", msp.ItemKindUserMessage, "first", 1)
	a1 = textItem("a1", msp.ItemKindAgentMessage, "reply one", 1)
	u2 = textItem("u2", msp.ItemKindUserMessage, "second", 1)
	a2 = textItem("a2", msp.ItemKindAgentMessage, "reply two", 1)
)

// A gap in the MIDDLE: a1's mirror write failed (non-fatal) and the later ones succeeded.
// Appended at the end, a1 would be folded into u2's turn as a second reply to the wrong
// prompt; the host's order has to win.
func TestBackfillPutsAMiddleGapInTheHostsOrder(t *testing.T) {
	st := mirrorWith(t, "00000000-0000-5000-8000-0000000000e1", u1, u2, a2)
	if _, err := st.backfill([]msp.Item{u1, a1, u2, a2}); err != nil {
		t.Fatal(err)
	}
	wantOrder(t, st, "u1", "a1", "u2", "a2")
	items, _ := st.Items()
	turns := turnsFromItems(items)
	var got []string
	for _, tr := range turns {
		got = append(got, tr.Role+":"+tr.Text)
	}
	want := []string{"user:first", "assistant:reply one", "user:second", "assistant:reply two"}
	if !slices.Equal(got, want) {
		t.Errorf("turns = %v, want %v", got, want)
	}
}

// A live item the host sent after its fold stays after it, whether it reached the store
// before the backfill ran or after.
func TestBackfillKeepsLiveItemsAfterTheFold(t *testing.T) {
	live := textItem("live-1", msp.ItemKindAgentMessage, "newer", 1)
	st := mirrorWith(t, "00000000-0000-5000-8000-0000000000e2", u1, u2, a2, live)
	if _, err := st.backfill([]msp.Item{u1, a1, u2, a2}); err != nil {
		t.Fatal(err)
	}
	wantOrder(t, st, "u1", "a1", "u2", "a2", "live-1")

	later := textItem("live-2", msp.ItemKindUserMessage, "after the backfill", 1)
	if err := st.appendRecord(record{Item: later}); err != nil {
		t.Fatal(err)
	}
	wantOrder(t, st, "u1", "a1", "u2", "a2", "live-1", "live-2")
}

// An anchored snapshot starts at a compaction anchor: what the mirror has from before it is not
// in the host's list, and stays in front.
func TestBackfillKeepsItemsBeforeTheAnchorInFront(t *testing.T) {
	old := textItem("p0", msp.ItemKindUserMessage, "before the anchor", 1)
	st := mirrorWith(t, "00000000-0000-5000-8000-0000000000e3", old, u1, u2, a2)
	if _, err := st.backfill([]msp.Item{u1, a1, u2, a2}); err != nil {
		t.Fatal(err)
	}
	wantOrder(t, st, "p0", "u1", "a1", "u2", "a2")
}

// A mirror that already matches the host gets no order line: a resume over an intact mirror
// writes nothing at all.
func TestBackfillWritesNothingOverAnIntactMirror(t *testing.T) {
	st := mirrorWith(t, "00000000-0000-5000-8000-0000000000e4", u1, a1, u2, a2)
	before, _ := os.ReadFile(st.Path())
	n, err := st.backfill([]msp.Item{u1, a1, u2, a2})
	if err != nil || n != 0 {
		t.Fatalf("backfill = %d, %v", n, err)
	}
	after, _ := os.ReadFile(st.Path())
	if !bytes.Equal(before, after) {
		t.Errorf("an intact mirror was written to:\n%s", after[len(before):])
	}
}
