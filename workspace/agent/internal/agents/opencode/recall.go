package opencode

// Reading back the agent (build/plan) and model a conversation switched to in the TUI, so
// a resume does not revert it (#987).

import (
	"database/sql"
	"encoding/json"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// RecallSettings reads the session's last user message. On resume opencode honours
// `--agent`, so a plan/build switch made in the TUI is undone by the launch flag; `--model`
// it ignores in favour of the session's own last model (both measured on 1.18.32). The
// model is recalled anyway so the meta, and a fork or recreate built from it, follow the
// conversation. A switch with no prompt after it is not in the store at all — opencode
// itself loses it too.
func (agentImpl) RecallSettings(m session.Meta) agents.RecalledSettings {
	db, ok := openRO()
	if !ok {
		return agents.RecalledSettings{}
	}
	defer db.Close()
	ses := activeSession(db, m)
	if ses == "" {
		return agents.RecalledSettings{}
	}
	return agents.RecalledSettings{Model: lastUserModel(db, ses), Mode: mode(db, ses)}
}

// lastUserModel is the provider/model the session's newest user message was sent with,
// in the form `--model` takes. "" when none is recorded.
func lastUserModel(db *sql.DB, ses string) string {
	var data []byte
	if db.QueryRow(`SELECT data FROM message WHERE session_id = ? AND json_extract(data, '$.role') = 'user' ORDER BY time_created DESC LIMIT 1`, ses).Scan(&data) != nil {
		return ""
	}
	var md struct {
		Model struct {
			ProviderID string `json:"providerID"`
			ModelID    string `json:"modelID"`
		} `json:"model"`
	}
	if json.Unmarshal(data, &md) != nil || md.Model.ProviderID == "" || md.Model.ModelID == "" {
		return ""
	}
	return md.Model.ProviderID + "/" + md.Model.ModelID
}
