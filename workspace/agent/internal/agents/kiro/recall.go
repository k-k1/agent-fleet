package kiro

// Reading back the model and agent a session switched to in the TUI, so a resume does not
// revert them (#987).

import (
	"encoding/json"
	"os"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// RecallSettings reads the session's <sid>.json. kiro rewrites it the moment `/model` or
// `/plan` switches, reply or not, and resumes from it — except that `--model` on the resume
// command wins (measured on 2.16.0), so the launch flag was undoing every switch.
//
// The agent comes back by itself (`--agent` does not override it on resume); it is
// recalled so meta.Mode — which decides --trust-all-tools and the mirror's plan badge —
// follows the session. No effort is recorded anywhere.
func (agentImpl) RecallSettings(m session.Meta) agents.RecalledSettings {
	sid := sids.Read(session.UUID(m.Dir, m.Name))
	if sid == "" {
		return agents.RecalledSettings{}
	}
	b, err := os.ReadFile(sessionJSONPath(sid))
	if err != nil {
		return agents.RecalledSettings{}
	}
	return recallFrom(b)
}

func recallFrom(b []byte) agents.RecalledSettings {
	var sj struct {
		SessionState struct {
			AgentName     *string `json:"agent_name"`
			RTSModelState struct {
				ModelInfo struct {
					ModelID string `json:"model_id"`
				} `json:"model_info"`
			} `json:"rts_model_state"`
		} `json:"session_state"`
	}
	if json.Unmarshal(b, &sj) != nil {
		return agents.RecalledSettings{}
	}
	r := agents.RecalledSettings{Model: sj.SessionState.RTSModelState.ModelInfo.ModelID}
	// null on a session nobody switched; kiro_default otherwise.
	if a := sj.SessionState.AgentName; a != nil && *a != "" {
		r.Mode = "normal"
		if *a == "kiro_planner" {
			r.Mode = "plan"
		}
	}
	return r
}
