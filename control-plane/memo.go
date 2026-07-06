package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// Memo queue (docs/21). Per-membership notes accumulated across devices, then flushed
// to a coding session as one concatenated message. All routes resolve scope with
// membershipFor (no workspace build); mutations are ownership-guarded in the store by
// membership_id. Persistence mirrors the SSM profile CRUD (ssm.go) 1:1.

// memoRetentionDays is how long a flushed (sent) memo is kept for history/re-send
// before the sweep on list removes it.
const memoRetentionDays = 7

// memoRetainBefore is the RFC3339 cutoff: sent memos stamped before it are swept.
func memoRetainBefore() string {
	return time.Now().UTC().Add(-memoRetentionDays * 24 * time.Hour).Format(time.RFC3339)
}

// memoDTO is the JSON wire shape. sentAt is "" for an unsent memo.
type memoDTO struct {
	ID        string `json:"id"`
	Repo      string `json:"repo"`
	Category  string `json:"category"`
	Kind      string `json:"kind"`
	Body      string `json:"body"`
	RefPath   string `json:"refPath"`
	Position  int    `json:"position"`
	CreatedAt string `json:"createdAt"`
	SentAt    string `json:"sentAt"`
}

func memoToDTO(m Memo) memoDTO {
	return memoDTO{ID: m.ID, Repo: m.Repo, Category: m.Category, Kind: m.Kind,
		Body: m.Body, RefPath: m.RefPath, Position: m.Position,
		CreatedAt: m.CreatedAt, SentAt: m.SentAt}
}

func (c config) handleMemosList(w http.ResponseWriter, r *http.Request) {
	_, mv, ok := c.membershipFor(w, r)
	if !ok {
		return
	}
	cutoff := memoRetainBefore()
	// Lazy sweep: drop expired sent memos before listing (best-effort; a failed
	// sweep still lets the list run, the cutoff filter hides expired rows anyway).
	_ = c.mgr.store.SweepSentMemos(r.Context(), cutoff)
	rows, err := c.mgr.store.ListMemos(r.Context(), mv.MembershipID, cutoff)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := make([]memoDTO, 0, len(rows))
	for _, m := range rows {
		out = append(out, memoToDTO(m))
	}
	writeJSON(w, http.StatusOK, out)
}

// validateMemo trims + checks a memo DTO into a normalized Memo (id/created_at/sent_at
// unset). kind must be "file" (ref_path required) or "text" (body required).
func validateMemo(mv MembershipView, in memoDTO) (Memo, *apiError) {
	m := Memo{
		MembershipID: mv.MembershipID,
		Repo:         strings.TrimSpace(in.Repo),
		Category:     strings.TrimSpace(in.Category),
		Kind:         strings.TrimSpace(in.Kind),
		Body:         strings.TrimSpace(in.Body),
		RefPath:      strings.TrimSpace(in.RefPath),
		Position:     in.Position,
	}
	switch m.Kind {
	case "file":
		if m.RefPath == "" {
			return Memo{}, &apiError{http.StatusBadRequest, "bad_ref", "refPath is required for kind=file"}
		}
	case "text":
		if m.Body == "" {
			return Memo{}, &apiError{http.StatusBadRequest, "bad_body", "body is required for kind=text"}
		}
	default:
		return Memo{}, &apiError{http.StatusBadRequest, "bad_kind", "kind must be file or text"}
	}
	return m, nil
}

func (c config) handleMemoCreate(w http.ResponseWriter, r *http.Request) {
	_, mv, ok := c.membershipFor(w, r)
	if !ok {
		return
	}
	var in memoDTO
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
		return
	}
	m, aerr := validateMemo(mv, in)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	m.ID = newID()
	m.CreatedAt = nowTS()
	if err := c.mgr.store.CreateMemo(r.Context(), m); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusCreated, memoToDTO(m))
}

// memoPatch carries partial edits. Nil fields are left unchanged so a reorder can send
// only position and an assistant tidy-up can send only body/category.
type memoPatch struct {
	Repo     *string `json:"repo"`
	Category *string `json:"category"`
	Body     *string `json:"body"`
	RefPath  *string `json:"refPath"`
	Position *int    `json:"position"`
}

func (c config) handleMemoUpdate(w http.ResponseWriter, r *http.Request) {
	_, mv, ok := c.membershipFor(w, r)
	if !ok {
		return
	}
	cur, found, err := c.mgr.store.GetMemo(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !found || cur.MembershipID != mv.MembershipID {
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "memo not found"})
		return
	}
	var in memoPatch
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
		return
	}
	if in.Repo != nil {
		cur.Repo = strings.TrimSpace(*in.Repo)
	}
	if in.Category != nil {
		cur.Category = strings.TrimSpace(*in.Category)
	}
	if in.Body != nil {
		cur.Body = strings.TrimSpace(*in.Body)
	}
	if in.RefPath != nil {
		cur.RefPath = strings.TrimSpace(*in.RefPath)
	}
	if in.Position != nil {
		cur.Position = *in.Position
	}
	if cur.Kind == "file" && cur.RefPath == "" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_ref", "refPath is required for kind=file"})
		return
	}
	if cur.Kind == "text" && cur.Body == "" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_body", "body is required for kind=text"})
		return
	}
	if err := c.mgr.store.UpdateMemo(r.Context(), cur); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, memoToDTO(cur))
}

func (c config) handleMemoDelete(w http.ResponseWriter, r *http.Request) {
	_, mv, ok := c.membershipFor(w, r)
	if !ok {
		return
	}
	if err := c.mgr.store.DeleteMemo(r.Context(), r.PathValue("id"), mv.MembershipID); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
