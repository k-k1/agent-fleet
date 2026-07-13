package main

import (
	"encoding/json"
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func handleNotifications(w http.ResponseWriter, _ *http.Request) {
	// Codex has no question hook: detect its pending request_user_input from the
	// rollout tail at sync time and latch by call id.
	for _, m := range session.ListMetas() {
		if m.Kind != session.KindCodex || m.Archived {
			continue
		}
		if callID := codex.PendingQuestionID(m); callID != "" {
			_ = notice.PutOnce("codex-question:"+m.Name+":"+callID,
				notice.New("question", m.Name, m.Kind, session.Display(m)))
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"notifications": notice.List()})
}

func handleNotificationsAck(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs []string `json:"ids"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	notice.Ack(in.IDs)
	w.WriteHeader(http.StatusNoContent)
}
