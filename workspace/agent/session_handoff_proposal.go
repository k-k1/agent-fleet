package main

// Session-originated handoff proposals.  A coding session may describe one or more
// successors' first prompts (e.g. splitting a task into parallel follow-ups in the
// same turn), but it may never create those sessions itself: the Console presents
// each durable proposal and the user chooses the launch settings.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

// handoffProposalMaxCount guards against a runaway loop growing the file unboundedly.
// It is far above any real fan-out (a several-way split is the motivating case).
const handoffProposalMaxCount = 64

type sessionHandoffProposal struct {
	ID     string `json:"id"`
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

func newHandoffProposalID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return "hp_" + hex.EncodeToString(b)
}

// readHandoffProposals loads every outstanding proposal for name, oldest first. It also
// reads the pre-fan-out format — a single JSON object, one proposal per session, written
// before this file supported more than one outstanding proposal at a time — minting a
// stable ID from its CreatedAt so it round-trips into the array shape on the next write.
func readHandoffProposals(name string) ([]*sessionHandoffProposal, error) {
	b, err := os.ReadFile(handoffProposalPath(name))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []*sessionHandoffProposal
	if err := json.Unmarshal(b, &list); err != nil {
		var legacy sessionHandoffProposal
		if err2 := json.Unmarshal(b, &legacy); err2 != nil {
			return nil, err
		}
		if strings.TrimSpace(legacy.Prompt) == "" {
			return nil, nil
		}
		legacy.ID = fmt.Sprintf("legacy-%d", legacy.CreatedAt)
		return []*sessionHandoffProposal{&legacy}, nil
	}
	out := list[:0]
	for _, p := range list {
		if p != nil && strings.TrimSpace(p.Prompt) != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

// writeHandoffProposals persists list as-is, oldest first. An empty list removes the
// file rather than leaving an empty array behind.
func writeHandoffProposals(name string, list []*sessionHandoffProposal) error {
	if len(list) == 0 {
		if err := os.Remove(handoffProposalPath(name)); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(handoffProposalPath(name)), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return os.WriteFile(handoffProposalPath(name), append(b, '\n'), 0o600)
}

// addHandoffProposal always creates a NEW outstanding proposal, so a session can fan a
// single turn out into several successor prompts — three propose_session_handoff calls
// used to collapse into one silently, because the pre-fan-out format kept a single slot
// per session and each call overwrote the last (the sbm2uo3 incident).
func addHandoffProposal(name, prompt, title string) (*sessionHandoffProposal, error) {
	list, err := readHandoffProposals(name)
	if err != nil {
		return nil, err
	}
	if len(list) >= handoffProposalMaxCount {
		return nil, fmt.Errorf("too many outstanding handoff proposals (%d)", handoffProposalMaxCount)
	}
	p := &sessionHandoffProposal{ID: newHandoffProposalID(), Prompt: prompt, Title: title, CreatedAt: time.Now().UnixMilli()}
	list = append(list, p)
	if err := writeHandoffProposals(name, list); err != nil {
		return nil, err
	}
	return p, nil
}

// editHandoffProposal updates the proposal identified by id in place, keeping its
// ORIGINAL CreatedAt (and LaunchedAt) — see the field comments: an edit changes neither.
func editHandoffProposal(name, id, prompt, title string) (*sessionHandoffProposal, error) {
	list, err := readHandoffProposals(name)
	if err != nil {
		return nil, err
	}
	for _, p := range list {
		if p.ID == id {
			p.Prompt, p.Title = prompt, title
			if err := writeHandoffProposals(name, list); err != nil {
				return nil, err
			}
			return p, nil
		}
	}
	return nil, os.ErrNotExist
}

// markHandoffProposalLaunched badges the proposal identified by id once, idempotently.
func markHandoffProposalLaunched(name, id string) (*sessionHandoffProposal, error) {
	list, err := readHandoffProposals(name)
	if err != nil {
		return nil, err
	}
	for _, p := range list {
		if p.ID == id {
			if p.LaunchedAt == 0 {
				p.LaunchedAt = time.Now().UnixMilli()
				if err := writeHandoffProposals(name, list); err != nil {
					return nil, err
				}
			}
			return p, nil
		}
	}
	return nil, os.ErrNotExist
}

// removeHandoffProposals はセッションが消えた（＝スロット名が再利用され得る）ときの後片付け。
// 残すと、次にそのスロットへ入った別のセッションの会話に前のセッションの提案カードが出る。
func removeHandoffProposals(name string) {
	_ = os.Remove(handoffProposalPath(name))
}

// discardHandoffProposal removes the single proposal identified by id, leaving any
// other outstanding proposals for the session untouched.
func discardHandoffProposal(name, id string) error {
	list, err := readHandoffProposals(name)
	if err != nil {
		return err
	}
	out := list[:0]
	found := false
	for _, p := range list {
		if p.ID == id {
			found = true
			continue
		}
		out = append(out, p)
	}
	if !found {
		return os.ErrNotExist
	}
	return writeHandoffProposals(name, out)
}

// handleSessionHandoffProposal lets the Console read, create, edit, badge, or discard a
// session's outstanding handoff proposals. POST is deliberately proposal-only; session
// creation remains the normal user-facing POST /sessions flow.
//
// POST semantics turn on whether the body carries an id: no id creates a new proposal
// (the shape propose_session_handoff always sends — see mcp_stdio.go), an id edits that
// one proposal in place. {"id":..., "launched":true} alone (no prompt/title) badges it.
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
		list, err := readHandoffProposals(name)
		if err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "handoff_read", err.Error())
			return
		}
		if list == nil {
			list = []*sessionHandoffProposal{}
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"proposals": list})
	case http.MethodPost:
		var body struct {
			ID     string `json:"id"`
			Prompt string `json:"prompt"`
			Title  string `json:"title"`
			// Launched marks that a session was really created from this proposal.
			// Sent ALONE (with id, no prompt/title) by the Console once the launch
			// dialog reports success — a cancelled dialog must not badge the card.
			Launched bool `json:"launched"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}
		id := strings.TrimSpace(body.ID)
		if body.Launched && strings.TrimSpace(body.Prompt) == "" && strings.TrimSpace(body.Title) == "" {
			if id == "" {
				httpx.WriteErr(w, http.StatusBadRequest, "handoff_id_missing", "id is required to mark a proposal launched")
				return
			}
			p, err := markHandoffProposalLaunched(name, id)
			if errors.Is(err, os.ErrNotExist) {
				httpx.WriteErr(w, http.StatusNotFound, "handoff_missing", "no such proposal")
				return
			}
			if err != nil {
				httpx.WriteErr(w, http.StatusInternalServerError, "handoff_write", err.Error())
				return
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
		var p *sessionHandoffProposal
		var err error
		if id == "" {
			p, err = addHandoffProposal(name, prompt, title)
		} else {
			p, err = editHandoffProposal(name, id, prompt, title)
			if errors.Is(err, os.ErrNotExist) {
				httpx.WriteErr(w, http.StatusNotFound, "handoff_missing", "no such proposal")
				return
			}
		}
		if err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "handoff_write", err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"proposal": p})
	case http.MethodDelete:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			httpx.WriteErr(w, http.StatusBadRequest, "handoff_id_missing", "id is required")
			return
		}
		if err := discardHandoffProposal(name, id); err != nil && !errors.Is(err, os.ErrNotExist) {
			httpx.WriteErr(w, http.StatusInternalServerError, "handoff_delete", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		httpx.WriteErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}
