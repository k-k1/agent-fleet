package muse

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp/msptest"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// seedConversation writes a three-exchange conversation into a slot's store and returns the
// anchors of the three user messages, in order.
func seedConversation(t *testing.T, sid string) []string {
	t.Helper()
	st := openStore(sid)
	var anchors []string
	for _, turn := range []string{"t1", "t2", "t3"} {
		user := "u-" + turn
		tid := turn
		if err := st.Append(msp.Item{ItemID: user, Kind: msp.ItemKindUserMessage, TurnID: &tid,
			Text: strPtr("ask " + turn)}); err != nil {
			t.Fatal(err)
		}
		if err := st.Append(msp.Item{ItemID: "a-" + turn, Kind: msp.ItemKindAgentMessage, TurnID: &tid,
			Text: strPtr("answer " + turn)}); err != nil {
			t.Fatal(err)
		}
		anchors = append(anchors, user)
	}
	return anchors
}

func museMeta(t *testing.T, name string) session.Meta {
	t.Helper()
	return session.Meta{Kind: session.KindMuse, Name: name, Dir: t.TempDir(), Driver: session.DriverManaged}
}

// 🔴 The item→turn bridge, and the off-by-one that would be invisible: an item's turnId is
// "the owning turn (== the submitting commandId for fresh turns)", so a user message belongs
// to the turn it STARTED. Redoing that message therefore cuts at the PREVIOUS turn, and
// continuing from it cuts at that turn itself. Getting this backwards forks one exchange off
// in either direction, and the result still looks like a plausible conversation.
func TestResolveForkAtMapsTheAnchorOntoTheRightTurn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := museMeta(t, "muse-fork-src")
	anchors := seedConversation(t, slotSid(m))

	// "Redo this message" on the second exchange keeps everything through the first.
	got, err := New().(agents.ForkAtResolver).ResolveForkAt(m, agents.ForkPoint{Anchor: anchors[1]})
	if err != nil {
		t.Fatalf("ResolveForkAt: %v", err)
	}
	if got != "t1" {
		t.Errorf("exclusive cut = %q, want t1", got)
	}
	// "Continue from this message" on the second keeps that exchange too.
	got, err = New().(agents.ForkAtResolver).ResolveForkAt(m, agents.ForkPoint{Anchor: anchors[1], Include: true})
	if err != nil {
		t.Fatalf("ResolveForkAt(include): %v", err)
	}
	if got != "t2" {
		t.Errorf("inclusive cut = %q, want t2", got)
	}
}

// Continuing from the LAST exchange is the whole conversation, and "" is how that is said —
// the wire omits `cutPoint` for "all completed turns". A value here would name the final turn
// and mean the same thing, but "" is the one the driver already treats as "send no cut".
func TestResolveForkAtOnTheLastExchangeIsTheWholeConversation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := museMeta(t, "muse-fork-last")
	anchors := seedConversation(t, slotSid(m))

	got, err := New().(agents.ForkAtResolver).ResolveForkAt(m, agents.ForkPoint{Anchor: anchors[2], Include: true})
	if err != nil {
		t.Fatalf("ResolveForkAt: %v", err)
	}
	if got != "" {
		t.Errorf("cut = %q, want the whole conversation", got)
	}
}

// Two refusals that must NOT fall back to a whole-conversation fork: forking before the first
// turn, and an anchor this conversation does not have. A silent fallback arrives with
// plausible history, so the member cannot see that the point they picked was ignored.
func TestResolveForkAtRefusesRatherThanFallingBack(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := museMeta(t, "muse-fork-edge")
	anchors := seedConversation(t, slotSid(m))

	if _, err := New().(agents.ForkAtResolver).ResolveForkAt(m, agents.ForkPoint{Anchor: anchors[0]}); err == nil {
		t.Error("forking before the first turn was accepted")
	}
	if _, err := New().(agents.ForkAtResolver).ResolveForkAt(m, agents.ForkPoint{Anchor: "nosuchitem"}); err == nil {
		t.Error("an unknown anchor was accepted")
	}
}

// ForkSource names the SLOT, not the muse session: the fork copies the host's conversation and
// AF's own store, and only the slot sid keys both.
func TestForkSourceNamesTheSlotAndRefusesAnEmptyConversation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := museMeta(t, "muse-fork-source")
	if _, err := New().(agents.Forker).ForkSource(m); err == nil {
		t.Fatal("a session that never opened one was reported as forkable")
	}
	writeSession(slotSid(m), museSession{ID: "01a0c1d6-0000-7000-8000-00000000f001", Path: "/tmp/x.jsonl"})
	got, err := New().(agents.Forker).ForkSource(m)
	if err != nil {
		t.Fatalf("ForkSource: %v", err)
	}
	if got != slotSid(m) {
		t.Errorf("ForkSource = %q, want the slot sid %q", got, slotSid(m))
	}
}

// The store copy is the half nobody sees until it is missing: `session/fork` copies the HOST's
// history, and AF's own store is what Transcript reads. Without this the forked session opens
// with an empty conversation — the opposite of what forking is for.
func TestForkCopiesAFsOwnTranscriptUpToTheCut(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	src := museMeta(t, "muse-fork-copy-src")
	seedConversation(t, slotSid(src))

	dstSID := "00000000-0000-5000-8000-00000000d001"
	if err := openStore(slotSid(src)).ForkAt(dstSID, "t2"); err != nil {
		t.Fatalf("ForkAt: %v", err)
	}
	items, err := openStore(dstSID).Items()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Fatalf("copied %d items, want the four of t1 and t2", len(items))
	}
	if items[3].ItemID != "a-t2" {
		t.Errorf("last copied item = %q, want a-t2", items[3].ItemID)
	}
	// The whole conversation, for the no-cut case.
	whole := "00000000-0000-5000-8000-00000000d002"
	if err := openStore(slotSid(src)).ForkAt(whole, ""); err != nil {
		t.Fatalf("ForkAt(whole): %v", err)
	}
	if items, _ := openStore(whole).Items(); len(items) != 6 {
		t.Errorf("whole-conversation copy has %d items, want 6", len(items))
	}
}

// The wire half: a forked slot opens with `session/fork` carrying the source's muse session id
// and the cut, never with `session/start` — which would silently give the member an empty
// conversation that looks like a fresh session.
func TestForkedSlotOpensWithSessionFork(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srcSlot := "00000000-0000-5000-8000-00000000e001"
	writeSession(srcSlot, museSession{ID: "01a0c1d6-0000-7000-8000-00000000e009", Path: "/tmp/src.jsonl"})

	h := &threadHandle{slotSid: "00000000-0000-5000-8000-00000000e002", forkFrom: srcSlot, forkAt: "t2"}
	host := newTestHandle(t, h)
	host.Handle(msp.MethodSessionStart, func(msptest.Message) (any, *msp.Error) {
		t.Error("a forked slot started a fresh session instead of forking")
		return map[string]any{}, nil
	})
	forked := make(chan msp.SessionForkParams, 1)
	host.Handle(msp.MethodSessionFork, func(m msptest.Message) (any, *msp.Error) {
		var p msp.SessionForkParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			t.Errorf("session/fork params: %v", err)
		}
		forked <- p
		return map[string]any{
			"session": map[string]any{
				"sessionId": "01a0c1d6-0000-7000-8000-00000000e00f", "path": "/tmp/fork.jsonl",
				"status": "idle", "createdAt": "", "updatedAt": "", "turnCount": 0,
			},
			"history": map[string]any{}, "pendingRequests": []any{}, "viewCursor": "c1",
		}, nil
	})

	if err := h.openSession(h.cl, agents.ThreadSettings{}); err != nil {
		t.Fatalf("openSession: %v", err)
	}
	p := awaitFork(t, forked)
	if p.SessionID != "01a0c1d6-0000-7000-8000-00000000e009" {
		t.Errorf("session/fork named source %q", p.SessionID)
	}
	if p.CutPoint == nil || p.CutPoint.LastTurnID != "t2" {
		t.Errorf("cut point = %+v", p.CutPoint)
	}
	// The host mints the new id, AF does not — so it has to be read out of the result and
	// stored, or the next Resume starts a second conversation for the same slot.
	stored, ok := readSession(h.slotSid)
	if !ok || stored.ID != "01a0c1d6-0000-7000-8000-00000000e00f" {
		t.Errorf("stored session = %+v (ok=%v)", stored, ok)
	}
}

// A whole-conversation fork sends NO cut point. Sending one that names the final turn would
// mean the same thing today and diverge the moment a turn lands between the request and the
// fork; the wire's own "omitted means all completed turns" is the value to use.
func TestWholeConversationForkSendsNoCutPoint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srcSlot := "00000000-0000-5000-8000-00000000e011"
	writeSession(srcSlot, museSession{ID: "01a0c1d6-0000-7000-8000-00000000e019", Path: "/tmp/src.jsonl"})

	h := &threadHandle{slotSid: "00000000-0000-5000-8000-00000000e012", forkFrom: srcSlot}
	host := newTestHandle(t, h)
	forked := make(chan msp.SessionForkParams, 1)
	host.Handle(msp.MethodSessionFork, func(m msptest.Message) (any, *msp.Error) {
		var p msp.SessionForkParams
		_ = json.Unmarshal(m.Params, &p)
		forked <- p
		return map[string]any{
			"session": map[string]any{
				"sessionId": "01a0c1d6-0000-7000-8000-00000000e01f", "path": "/tmp/fork.jsonl",
				"status": "idle", "createdAt": "", "updatedAt": "", "turnCount": 0,
			},
			"history": map[string]any{}, "pendingRequests": []any{}, "viewCursor": "c1",
		}, nil
	})

	if err := h.openSession(h.cl, agents.ThreadSettings{}); err != nil {
		t.Fatalf("openSession: %v", err)
	}
	if p := awaitFork(t, forked); p.CutPoint != nil {
		t.Errorf("a whole-conversation fork carried a cut point: %+v", p.CutPoint)
	}
}

// awaitFork reads the captured params with a deadline. A bare channel receive turns "the
// fork was never sent" — the exact defect these tests exist for — into a test that HANGS
// rather than fails, which in CI is a ten-minute timeout and no name to look at.
func awaitFork(t *testing.T, ch <-chan msp.SessionForkParams) msp.SessionForkParams {
	t.Helper()
	select {
	case p := <-ch:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("session/fork was never sent")
		return msp.SessionForkParams{}
	}
}

// forkedHost wires a fake host that forks to a fixed id and records every session/resume it is
// sent. A nil handler result means "accept".
func forkedHost(t *testing.T, h *threadHandle, onResume func(msp.SessionResumeParams) *msp.Error) (sent <-chan msp.SessionResumeParams, left chan string) {
	t.Helper()
	left = make(chan string, 4)
	host := newTestHandle(t, h)
	host.Handle(msp.MethodSessionFork, func(msptest.Message) (any, *msp.Error) {
		return map[string]any{
			"session": map[string]any{
				"sessionId": forkedSID, "path": "/tmp/fork.jsonl",
				"status": "idle", "createdAt": "", "updatedAt": "", "turnCount": 0,
			},
			"history": map[string]any{}, "pendingRequests": []any{}, "viewCursor": "c1",
		}, nil
	})
	host.Handle(msp.MethodSessionLeave, func(m msptest.Message) (any, *msp.Error) {
		var p msp.SessionLeaveParams
		_ = json.Unmarshal(m.Params, &p)
		left <- p.SessionID
		return map[string]any{}, nil
	})
	resumes := make(chan msp.SessionResumeParams, 8)
	host.Handle(msp.MethodSessionResume, func(m msptest.Message) (any, *msp.Error) {
		var p msp.SessionResumeParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			t.Errorf("session/resume params: %v", err)
		}
		resumes <- p
		if onResume != nil {
			if e := onResume(p); e != nil {
				return nil, e
			}
		}
		return resumedSession(), nil
	})
	refuseSessionRead(t, host)
	return resumes, left
}

const forkedSID = "01a0c1d6-0000-7000-8000-00000000e10f"

func registerWikiServer(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	if _, err := mcpreg.Create(mcpreg.ServerDef{
		Name: "wiki", Transport: mcpreg.TransportStdio, Command: "/bin/true",
		Enabled: true, Targets: mcpreg.Targets{Session: true}, Kinds: []string{session.KindMuse},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
}

// session/fork has no config member, so the servers ride a session/resume of the NEW id.
func TestForkedSlotGetsTheServersByResumingTheNewSession(t *testing.T) {
	registerWikiServer(t)
	srcSlot := "00000000-0000-5000-8000-00000000e101"
	writeSession(srcSlot, museSession{ID: "01a0c1d6-0000-7000-8000-00000000e109", Path: "/tmp/src.jsonl"})
	h := &threadHandle{slotSid: "00000000-0000-5000-8000-00000000e102", forkFrom: srcSlot, sessionMCP: true}
	sent, left := forkedHost(t, h, nil)

	if err := h.openSession(h.cl, agents.ThreadSettings{}); err != nil {
		t.Fatalf("openSession: %v", err)
	}
	select {
	case sid := <-left:
		if sid != forkedSID {
			t.Errorf("left %q, want the forked session %q", sid, forkedSID)
		}
	default:
		t.Fatal("the fork was resumed without being unloaded first: the host refuses that with a configuration conflict")
	}
	select {
	case p := <-sent:
		if p.SessionID != forkedSID {
			t.Errorf("resumed %q, want the forked session %q", p.SessionID, forkedSID)
		}
		if p.Config == nil {
			t.Fatal("the post-fork resume carried no MCP servers")
		}
		if _, ok := p.Config.MCPServers["wiki"]; !ok {
			t.Errorf("servers = %+v", p.Config.MCPServers)
		}
		af, ok := p.Config.MCPServers[mcpreg.AFServerName()]
		if !ok {
			t.Fatalf("the af server is not on the post-fork resume: %+v", p.Config.MCPServers)
		}
		if af.Env[agents.SessionNameEnvVar] != h.name {
			t.Errorf("af server serves %q, want %q", af.Env[agents.SessionNameEnvVar], h.name)
		}
	default:
		t.Fatal("no session/resume followed the fork: the forked session has none of af's tools")
	}
	if stored, _ := readSession(h.slotSid); stored.ID != forkedSID {
		t.Errorf("stored session = %q, want %q", stored.ID, forkedSID)
	}
}

// Without the sessionMcp grant nothing may be put on the wire, so there is no resume at all.
func TestForkedSlotWithoutTheGrantSendsNoResume(t *testing.T) {
	registerWikiServer(t)
	srcSlot := "00000000-0000-5000-8000-00000000e111"
	writeSession(srcSlot, museSession{ID: "01a0c1d6-0000-7000-8000-00000000e119", Path: "/tmp/src.jsonl"})
	h := &threadHandle{slotSid: "00000000-0000-5000-8000-00000000e112", forkFrom: srcSlot}
	sent, left := forkedHost(t, h, nil)

	if err := h.openSession(h.cl, agents.ThreadSettings{}); err != nil {
		t.Fatalf("openSession: %v", err)
	}
	select {
	case p := <-sent:
		t.Errorf("a session/resume was sent without the grant: %+v", p)
	case sid := <-left:
		t.Errorf("the fork %q was unloaded without the grant", sid)
	default:
	}
}

// The fork exists once the host has answered it; a host that will not take the servers costs the
// tools, not the session.
func TestForkedSlotSurvivesARefusedPostForkResume(t *testing.T) {
	for name, code := range map[string]int{"invalid params": msp.ErrCodeInvalidParams, "internal": msp.ErrCodeInternal} {
		t.Run(name, func(t *testing.T) {
			// Each case needs a slot with no stored session, or openSession resumes instead of forking.
			registerWikiServer(t)
			srcSlot := "00000000-0000-5000-8000-00000000e121"
			writeSession(srcSlot, museSession{ID: "01a0c1d6-0000-7000-8000-00000000e129", Path: "/tmp/src.jsonl"})
			h := &threadHandle{slotSid: "00000000-0000-5000-8000-00000000e122", forkFrom: srcSlot, sessionMCP: true}
			sent, _ := forkedHost(t, h, func(msp.SessionResumeParams) *msp.Error {
				return &msp.Error{Code: code, Message: "no"}
			})
			if err := h.openSession(h.cl, agents.ThreadSettings{}); err != nil {
				t.Fatalf("a refused post-fork resume failed the open: %v", err)
			}
			if n := len(sent); n != 1 {
				t.Errorf("session/resume sent %d times, want exactly one (no bare retry)", n)
			}
			if stored, _ := readSession(h.slotSid); stored.ID != forkedSID {
				t.Errorf("stored session = %q, want the fork %q", stored.ID, forkedSID)
			}
		})
	}
}

// The host unloads asynchronously: until it has, the resume answers a retryable runtime_busy.
// Giving up on the first one would leave the fork without servers on every fork.
func TestForkedSlotRetriesTheResumeWhileTheHostIsUnloading(t *testing.T) {
	registerWikiServer(t)
	forkLeavePoll = time.Millisecond
	t.Cleanup(func() { forkLeavePoll = 100 * time.Millisecond })
	srcSlot := "00000000-0000-5000-8000-00000000e131"
	writeSession(srcSlot, museSession{ID: "01a0c1d6-0000-7000-8000-00000000e139", Path: "/tmp/src.jsonl"})
	h := &threadHandle{slotSid: "00000000-0000-5000-8000-00000000e132", forkFrom: srcSlot, sessionMCP: true}
	calls := 0
	sent, _ := forkedHost(t, h, func(msp.SessionResumeParams) *msp.Error {
		calls++
		if calls < 3 {
			return &msp.Error{Code: msp.ErrCodeCommandRejected, Message: "a session/leave is in progress",
				Data: json.RawMessage(`{"reason":"runtime_busy","retryable":true}`)}
		}
		return nil
	})
	if err := h.openSession(h.cl, agents.ThreadSettings{}); err != nil {
		t.Fatalf("openSession: %v", err)
	}
	if n := len(sent); n != 3 {
		t.Errorf("session/resume sent %d times, want 3 (two busy, one accepted)", n)
	}
}
