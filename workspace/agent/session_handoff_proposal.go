package main

// Session-originated handoff proposals.  A coding session may describe the next
// session's first prompt, but it may never create that session itself: the Console
// presents this durable proposal and the user chooses the launch settings.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const handoffProposalMaxBytes = 64 << 10

type sessionHandoffProposal struct {
	Prompt    string `json:"prompt"`
	CreatedAt int64  `json:"created_at"`
}

func handoffProposalPath(name string) string {
	return filepath.Join(homeDir(), ".config", "agent-fleet", "session-handoffs", name+".json")
}

func readHandoffProposal(name string) (*sessionHandoffProposal, error) {
	b, err := os.ReadFile(handoffProposalPath(name))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p sessionHandoffProposal
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.Prompt) == "" {
		return nil, nil
	}
	return &p, nil
}

func writeHandoffProposal(name, prompt string) (*sessionHandoffProposal, error) {
	p := &sessionHandoffProposal{Prompt: prompt, CreatedAt: time.Now().UnixMilli()}
	if err := os.MkdirAll(filepath.Dir(handoffProposalPath(name)), 0o700); err != nil {
		return nil, err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(handoffProposalPath(name), append(b, '\n'), 0o600); err != nil {
		return nil, err
	}
	return p, nil
}

// handleSessionHandoffProposal lets the Console read, update, or discard the one
// outstanding proposal for a session.  POST is deliberately proposal-only; session
// creation remains the normal user-facing POST /sessions flow.
func handleSessionHandoffProposal(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	if _, ok := session.ReadMeta(name); !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	switch r.Method {
	case http.MethodGet:
		p, err := readHandoffProposal(name)
		if err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "handoff_read", err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"proposal": p})
	case http.MethodPost:
		var body struct {
			Prompt string `json:"prompt"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}
		prompt := strings.TrimSpace(body.Prompt)
		if prompt == "" {
			httpx.WriteErr(w, http.StatusBadRequest, "handoff_prompt_empty", "prompt is empty")
			return
		}
		if len(prompt) > handoffProposalMaxBytes {
			httpx.WriteErr(w, http.StatusBadRequest, "handoff_prompt_too_large", "prompt is too large")
			return
		}
		p, err := writeHandoffProposal(name, prompt)
		if err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "handoff_write", err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"proposal": p})
	case http.MethodDelete:
		if err := os.Remove(handoffProposalPath(name)); err != nil && !os.IsNotExist(err) {
			httpx.WriteErr(w, http.StatusInternalServerError, "handoff_delete", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		httpx.WriteErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}
