package sessionx

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// fakeRecaller is a dummy kind that reports fixed settings from its "conversation".
type fakeRecaller struct {
	agents.Agent
	r agents.RecalledSettings
}

func (f fakeRecaller) RecallSettings(session.Meta) agents.RecalledSettings { return f.r }

// A switch the conversation recorded replaces the launch settings in both the meta the
// launch is built from and the stored meta, so a later fork/recreate inherits it too.
func TestRecallSettingsPersistsTheSwitch(t *testing.T) {
	withTempHome(t)
	withFakeAgent(t, session.KindCodex, fakeRecaller{Agent: AgentOf(session.KindCodex), r: agents.RecalledSettings{Model: "gpt-6-luna", Effort: "medium"}})
	m := session.Meta{Name: "s1", Dir: "/w", Kind: session.KindCodex, Model: "gpt-5.5", Effort: "low", Title: "keep"}
	session.WriteMeta(m)
	// Something else writes the meta between the launch's read and the recall.
	fresh := m
	fresh.Title = "renamed meanwhile"
	session.WriteMeta(fresh)

	recallSettings(&m)
	if m.Model != "gpt-6-luna" || m.Effort != "medium" {
		t.Errorf("launch meta = %q/%q, want gpt-6-luna/medium", m.Model, m.Effort)
	}
	got, _ := session.ReadMeta("s1")
	if got.Model != "gpt-6-luna" || got.Effort != "medium" {
		t.Errorf("stored meta = %q/%q, want gpt-6-luna/medium", got.Model, got.Effort)
	}
	if got.Title != "renamed meanwhile" {
		t.Errorf("stored title = %q: the recall overwrote a concurrent meta write", got.Title)
	}
}

// With nothing recorded the meta is left exactly as it was.
func TestRecallSettingsNothingRecorded(t *testing.T) {
	withTempHome(t)
	withFakeAgent(t, session.KindCodex, fakeRecaller{Agent: AgentOf(session.KindCodex)})
	m := session.Meta{Name: "s2", Dir: "/w", Kind: session.KindCodex, Model: "gpt-5.5"}
	recallSettings(&m)
	if m.Model != "gpt-5.5" {
		t.Errorf("model = %q, want gpt-5.5", m.Model)
	}
	if _, ok := session.ReadMeta("s2"); ok {
		t.Error("a meta was written although nothing changed")
	}
}
