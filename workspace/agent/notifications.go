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
			ev := notice.New("question", m.Name, m.Kind, session.Display(m))
			// P2b managed (docs/log/37): carry the pending question payload so an
			// interact-capable provider renders option buttons. Read from the live
			// driver handle (no resume); absent → the notice still fires, just
			// button-less (Console fallback).
			if q, ok := codex.PendingInteraction(m.Name); ok {
				ev.Payload["questions"] = q
			}
			_ = notice.PutOnce("codex-question:"+m.Name+":"+callID, ev)
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
