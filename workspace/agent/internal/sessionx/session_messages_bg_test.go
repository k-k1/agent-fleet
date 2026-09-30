package sessionx

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// fakeBgAgent is a non-claude kind that reports background work, so the generic /messages
// path can be checked against what the session list would say.
type fakeBgAgent struct {
	agents.Agent
	busy   bool
	reason string
}

func (fakeBgAgent) Kind() string { return session.KindMuse }
func (fakeBgAgent) Caps() agents.Caps {
	return agents.Caps{CanTranscript: true, ManagedOnly: true}
}
func (fakeBgAgent) BuildLaunch(session.Meta, agents.LaunchOpts) (agents.LaunchPlan, error) {
	return agents.LaunchPlan{}, errors.New("no terminal route")
}
func (a fakeBgAgent) WireLive(session.Meta, bool) agents.LiveInfo {
	return agents.LiveInfo{State: "idle", BackgroundBusy: a.busy, BackgroundBusyReason: a.reason}
}
func (fakeBgAgent) ClearResume(string) {}
func (fakeBgAgent) Transcript(session.Meta) (agents.TranscriptData, bool) {
	return agents.TranscriptData{Turns: []transcript.Turn{{Role: "user", Text: "run the tests", Idx: 0}}}, true
}
func (a fakeBgAgent) BackgroundWork(session.Meta) (bool, string) { return a.busy, a.reason }

// fakeNoBgAgent is the same kind without the capability.
type fakeNoBgAgent struct{ fakeBgAgent }

func (fakeNoBgAgent) BackgroundWork() {} // shadows the method, so the interface is not met

func genericMessages(t *testing.T, a agents.Agent, alive bool, state string) map[string]any {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	prev := agentRegistry[session.KindMuse]
	agentRegistry[session.KindMuse] = a
	t.Cleanup(func() { agentRegistry[session.KindMuse] = prev })

	m := session.Meta{Name: "slot-bg-wire", Dir: t.TempDir(), Kind: session.KindMuse, Driver: session.DriverManaged}
	session.WriteMeta(m)
	rec := httptest.NewRecorder()
	handleGenericMessages(rec, httptest.NewRequest("GET", "/api/sessions/"+m.Name+"/messages", nil), m, alive, state)
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	return resp
}

// The mirror header reads backgroundBusy from /messages while the session list reads it from
// WireLive. Only claude's path filled the first, so a muse or codex session with a command
// still running showed "background running" in the list and plain "ready" in its own header.
func TestGenericMessagesCarriesBackgroundBusy(t *testing.T) {
	resp := genericMessages(t, fakeBgAgent{busy: true, reason: "shell"}, true, "idle")
	if resp["backgroundBusy"] != true || resp["backgroundBusyReason"] != "shell" {
		t.Errorf("backgroundBusy = %v, reason = %v; want true, shell", resp["backgroundBusy"], resp["backgroundBusyReason"])
	}

	// An explicit false, as claude's path sends: the header clears a badge it showed before.
	resp = genericMessages(t, fakeBgAgent{}, true, "idle")
	if v, ok := resp["backgroundBusy"]; !ok || v != false {
		t.Errorf("idle with nothing running: backgroundBusy = %v (present %v), want false", v, ok)
	}
}

// Same gate as claude's path: a running turn shows "in progress", and a stopped session has
// nothing running.
func TestGenericMessagesAsksOnlyWhenIdleAndAlive(t *testing.T) {
	for _, tc := range []struct {
		alive bool
		state string
	}{{true, "working"}, {false, "stopped"}} {
		resp := genericMessages(t, fakeBgAgent{busy: true, reason: "shell"}, tc.alive, tc.state)
		if _, ok := resp["backgroundBusy"]; ok {
			t.Errorf("alive=%v state=%s: backgroundBusy sent", tc.alive, tc.state)
		}
	}
	if _, ok := genericMessages(t, fakeNoBgAgent{}, true, "idle")["backgroundBusy"]; ok {
		t.Error("a kind without BackgroundReporter got a backgroundBusy key")
	}
}
