package codex

import (
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

func TestRecallFrom(t *testing.T) {
	// Shapes as codex 0.157.0 writes them (trimmed to the fields read here).
	turnCtx := func(model, effort, mode string) string {
		return `{"type":"turn_context","payload":{"turn_id":"t","model":"` + model + `","reasoning_effort":null,"collaboration_mode":{"mode":"` + mode + `","settings":{"model":"` + model + `","reasoning_effort":"` + effort + `"}}}}`
	}
	applied := func(model, effort, mode string) string {
		return `{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_id":"x","thread_settings":{"model":"` + model + `","reasoning_effort":"` + effort + `","collaboration_mode":{"mode":"` + mode + `","settings":{"model":"` + model + `","reasoning_effort":"` + effort + `"}}}}}`
	}
	cases := []struct {
		name  string
		lines []string
		want  agents.RecalledSettings
	}{
		{"nothing recorded", []string{`{"type":"session_meta","payload":{"cwd":"/w"}}`}, agents.RecalledSettings{}},
		{"last turn", []string{turnCtx("gpt-5.5", "low", "default")},
			agents.RecalledSettings{Model: "gpt-5.5", Effort: "low", Mode: "normal"}},
		{"a switch with no turn after it", []string{
			turnCtx("gpt-5.5", "low", "default"), applied("gpt-6-luna", "medium", "default"),
		}, agents.RecalledSettings{Model: "gpt-6-luna", Effort: "medium", Mode: "normal"}},
		{"plan toggled in the TUI", []string{
			applied("gpt-6-luna", "medium", "default"), applied("gpt-6-luna", "medium", "plan"),
		}, agents.RecalledSettings{Model: "gpt-6-luna", Effort: "medium", Mode: "plan"}},
		{"the model default effort clears the flag", []string{
			applied("gpt-6-luna", "high", "default"),
			`{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"gpt-5.5","reasoning_effort":null,"collaboration_mode":{"mode":"default","settings":{"model":"gpt-5.5","reasoning_effort":null}}}}}`,
		}, agents.RecalledSettings{Model: "gpt-5.5", ClearEffort: true, Mode: "normal"}},
		{"other event_msg types are ignored", []string{
			applied("gpt-5.5", "low", "default"),
			`{"type":"event_msg","payload":{"type":"agent_message","message":"thread_settings_applied"}}`,
		}, agents.RecalledSettings{Model: "gpt-5.5", Effort: "low", Mode: "normal"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := recallFrom(strings.NewReader(strings.Join(c.lines, "\n"))); got != c.want {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}
