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
const handoffProposalTitleMaxBytes = 512

type sessionHandoffProposal struct {
	Prompt string `json:"prompt"`
	Title  string `json:"title,omitempty"`
	// CreatedAt is when the SESSION proposed the handoff, and it is stable across edits
	// on purpose: the mirror places the card at this point in the conversation. Re-stamping
	// it on every edit would slide the card back to the bottom of the transcript — the
	// layout that hid every later message (2026-08-04 実障害).
	CreatedAt int64 `json:"created_at"`
	// LaunchedAt marks that a session was actually created from this proposal. The
	// proposal is NOT deleted then — a handoff is worth re-reading, and discarding is
	// the user's call — so the card only gets a 起動済み badge.
	LaunchedAt int64 `json:"launched_at,omitempty"`
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

// writeHandoffProposal creates or edits the proposal. An edit keeps the ORIGINAL
// CreatedAt (and LaunchedAt) — see the field comments: those two are what the mirror
// positions and badges the card by, and an edit changes neither fact.
func writeHandoffProposal(name, prompt, title string) (*sessionHandoffProposal, error) {
	p := &sessionHandoffProposal{Prompt: prompt, Title: title, CreatedAt: time.Now().UnixMilli()}
	if prev, err := readHandoffProposal(name); err == nil && prev != nil {
		p.CreatedAt, p.LaunchedAt = prev.CreatedAt, prev.LaunchedAt
	}
	if err := storeHandoffProposal(name, p); err != nil {
		return nil, err
	}
	return p, nil
}

// storeHandoffProposal persists p as-is (the write half of writeHandoffProposal, also
// used by the launched-marking path, which must not touch prompt/title/created_at).
func storeHandoffProposal(name string, p *sessionHandoffProposal) error {
	if err := os.MkdirAll(filepath.Dir(handoffProposalPath(name)), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return os.WriteFile(handoffProposalPath(name), append(b, '\n'), 0o600)
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
			Title  string `json:"title"`
			// Launched marks that a session was really created from this proposal.
			// Sent ALONE (no prompt/title) by the Console once the launch dialog
			// reports success — a cancelled dialog must not badge the card.
			Launched bool `json:"launched"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}
		if body.Launched && strings.TrimSpace(body.Prompt) == "" && strings.TrimSpace(body.Title) == "" {
			p, err := readHandoffProposal(name)
			if err != nil {
				httpx.WriteErr(w, http.StatusInternalServerError, "handoff_read", err.Error())
				return
			}
			if p == nil {
				httpx.WriteErr(w, http.StatusNotFound, "handoff_missing", "no outstanding proposal to mark launched")
				return
			}
			if p.LaunchedAt == 0 {
				p.LaunchedAt = time.Now().UnixMilli()
				if err := storeHandoffProposal(name, p); err != nil {
					httpx.WriteErr(w, http.StatusInternalServerError, "handoff_write", err.Error())
					return
				}
			}
			httpx.WriteJSON(w, http.StatusOK, map[string]any{"proposal": p})
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
		title := strings.TrimSpace(body.Title)
		if title == "" {
			httpx.WriteErr(w, http.StatusBadRequest, "handoff_title_empty", "title is empty")
			return
		}
		if len(title) > handoffProposalTitleMaxBytes {
			httpx.WriteErr(w, http.StatusBadRequest, "handoff_title_too_large", "title is too large")
			return
		}
		p, err := writeHandoffProposal(name, prompt, title)
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
