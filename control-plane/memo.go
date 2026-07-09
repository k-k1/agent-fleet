package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
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

// memoAPI is the memo-queue feature handler set（docs/23 残③ の機能 struct 実例）:
// 解決は埋め込みの memberAuth（登録側で withMembership / withResolved に包む）、
// store は MemoStore の narrow view だけを持つ。flush だけは実ランタイムへ送る
// ため withResolved で登録する。
type memoAPI struct {
	memberAuth
	store MemoStore
}

func newMemoAPI(m *manager) memoAPI { return memoAPI{memberAuth{m}, m.store} }

func (a memoAPI) list(w http.ResponseWriter, r *http.Request, _ Identity, mv MembershipView) {
	cutoff := memoRetainBefore()
	// Lazy sweep: drop expired sent memos before listing (best-effort; a failed
	// sweep still lets the list run, the cutoff filter hides expired rows anyway).
	_ = a.store.SweepSentMemos(r.Context(), cutoff)
	rows, err := a.store.ListMemos(r.Context(), mv.MembershipID, cutoff)
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

func (a memoAPI) create(w http.ResponseWriter, r *http.Request, _ Identity, mv MembershipView) {
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
	if err := a.store.CreateMemo(r.Context(), m); err != nil {
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

func (a memoAPI) update(w http.ResponseWriter, r *http.Request, _ Identity, mv MembershipView) {
	cur, found, err := a.store.GetMemo(r.Context(), r.PathValue("id"))
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
	if err := a.store.UpdateMemo(r.Context(), cur); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, memoToDTO(cur))
}

// buildFlushMessage concatenates the selected memos into one message, grouping by
// category (empty category -> 未分類) and preserving the ORDER of the resolved memos
// (already sorted by category/position on the way in). File memos surface their
// ~/repos ref path plus any comment, text memos surface their body.
func buildFlushMessage(memos []Memo) string {
	var b strings.Builder
	b.WriteString("以下のメモをまとめて処理して。\n")
	lastCat := "\x00" // sentinel so the first real category (incl. "") emits a heading
	n := 0
	for _, m := range memos {
		if m.Category != lastCat {
			lastCat = m.Category
			cat := m.Category
			if cat == "" {
				cat = "未分類"
			}
			b.WriteString("\n## " + cat + "\n")
			n = 0
		}
		n++
		if m.Kind == "file" {
			fmt.Fprintf(&b, "%d. 対象ファイル: %s\n", n, m.RefPath)
			if m.Body != "" {
				b.WriteString("   " + m.Body + "\n")
			}
		} else {
			fmt.Fprintf(&b, "%d. %s\n", n, m.Body)
		}
	}
	return b.String()
}

// handleMemoFlush concatenates the selected memos into one message, sends it once to
// the session's input, and stamps sent_at on those memos. The ids list unifies the
// three send granularities (whole repo / category / individual) — the client just
// builds the right ids. Sending happens through the running workspace agent, so this
// resolves the full runtime (not just membership).
func (a memoAPI) flush(w http.ResponseWriter, r *http.Request, res *resolved) {
	var in struct {
		SessionName string   `json:"sessionName"`
		IDs         []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
		return
	}
	in.SessionName = strings.TrimSpace(in.SessionName)
	if in.SessionName == "" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_session", "sessionName is required"})
		return
	}
	if len(in.IDs) == 0 {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "no_ids", "ids is required"})
		return
	}
	// Resolve the requested ids to owned memos (foreign/unknown ids are dropped).
	memos := make([]Memo, 0, len(in.IDs))
	for _, id := range in.IDs {
		m, found, err := a.store.GetMemo(r.Context(), id)
		if err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
		if found && m.MembershipID == res.mv.MembershipID {
			memos = append(memos, m)
		}
	}
	if len(memos) == 0 {
		writeAPIErr(w, &apiError{http.StatusNotFound, "no_memos", "no owned memos for the given ids"})
		return
	}
	// Group by category (stable within-category order preserved from ListMemos).
	sort.SliceStable(memos, func(i, j int) bool { return memos[i].Category < memos[j].Category })

	payload, _ := json.Marshal(map[string]string{"prompt": buildFlushMessage(memos)})
	if _, err := agentText(r.Context(), res.rt,
		"POST", "/sessions/"+url.PathEscape(in.SessionName)+"/input", payload); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadGateway, "flush_failed", err.Error()})
		return
	}

	sentIDs := make([]string, len(memos))
	for i, m := range memos {
		sentIDs[i] = m.ID
	}
	sentAt := nowTS()
	if err := a.store.MarkMemosSent(r.Context(), res.mv.MembershipID, sentIDs, sentAt); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent": len(sentIDs), "sentAt": sentAt, "ids": sentIDs})
}

func (a memoAPI) delete(w http.ResponseWriter, r *http.Request, _ Identity, mv MembershipView) {
	if err := a.store.DeleteMemo(r.Context(), r.PathValue("id"), mv.MembershipID); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
