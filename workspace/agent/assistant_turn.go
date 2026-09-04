package main

// The entry point for a scheduled assistant firing (docs/log/38 session_mode=assistant).
//
// At fire time the CP scheduler calls POST /assistant-turns {conv, prompt}, which injects the
// prompt as a user turn into the named conversation (a UUID or an "a…" slug) and runs one turn.
// The turn machinery is not reinvented: it delegates to runOperatorTurn (the same path as the
// Discord @mention route, the non-HTTP twin of handleChatSend), so locking, auto-compaction,
// overflow self-repair, the AutoTurns reset and the unattended approval gate (a destructive tool
// needs bridge approval, so no bridge means fail-closed) all behave identically.
//
// Firing at a conversation with a turn in flight returns 409 turn_in_progress, which the CP
// records as skipped_overlap (the same unattended non-delivery surface as overlap=skip on a
// reused session). Nothing like the delivery confirmation of /input is needed: this turn runs
// synchronously, so "a reply came back" means it ran and "an error" means it did not - that is
// the call's semantics.

import (
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

func handleAssistantTurn(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Conv   string `json:"conv"` // conversation UUID or "a…" slug
		Prompt string `json:"prompt"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeChatPromptEmpty, "prompt is empty")
		return
	}
	id, ok := chatx.ResolveConvRef(req.Conv)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "conv_not_found", "no such conversation: "+req.Conv)
		return
	}
	// A turn already in flight would only queue behind the conversation lock and pile
	// up; surface it as a conflict so the scheduler records skipped_overlap instead.
	if chatx.TurnInFlight(id) {
		httpx.WriteErr(w, http.StatusConflict, "turn_in_progress", "an assistant turn is already running for this conversation")
		return
	}
	// Usage ledger (ADR 0029 §3): the machinery is the bridge's, but what is being consumed is one
	// chat turn run unattended by the scheduler, so count it as
	// feature=assistant.chat / trigger=schedule.
	reply, err := runOperatorTurnAs(id, req.Prompt, usagex.Tag{
		Feature: usagex.FeatureAssistantChat, Trigger: usagex.TriggerSchedule, Ref: id,
	})
	if err != nil {
		// reply carries the localized reason line (runOperatorTurn's contract).
		msg := strings.TrimSpace(reply)
		if msg == "" {
			msg = err.Error()
		}
		httpx.WriteErr(w, http.StatusBadGateway, "turn_failed", msg)
		return
	}
	slug := ""
	if c, cerr := chatx.LoadConv(id); cerr == nil {
		slug = c.Slug
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"conv": id, "slug": slug, "reply": reply})
}
