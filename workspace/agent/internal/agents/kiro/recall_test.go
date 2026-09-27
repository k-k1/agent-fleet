package kiro

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

func TestRecallFrom(t *testing.T) {
	cases := []struct {
		name string
		json string
		want agents.RecalledSettings
	}{
		{"fresh session", `{"session_id":"s","session_state":{"agent_name":null,"rts_model_state":{"model_info":{"model_name":"qwen3-coder-next","model_id":"qwen3-coder-next"}}}}`,
			agents.RecalledSettings{Model: "qwen3-coder-next"}},
		{"switched model", `{"session_state":{"agent_name":"kiro_default","rts_model_state":{"model_info":{"model_id":"minimax-m2.1"}}}}`,
			agents.RecalledSettings{Model: "minimax-m2.1", Mode: "normal"}},
		{"plan agent", `{"session_state":{"agent_name":"kiro_planner","rts_model_state":{"model_info":{"model_id":"auto"}}}}`,
			agents.RecalledSettings{Model: "auto", Mode: "plan"}},
		{"no state", `{"session_id":"s","cwd":"/w"}`, agents.RecalledSettings{}},
		{"not json", `{`, agents.RecalledSettings{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := recallFrom([]byte(c.json)); got != c.want {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}
