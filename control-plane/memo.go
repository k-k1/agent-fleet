package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Memo queue (docs/21). Per-membership notes accumulated across devices, then flushed
// to a coding session as one concatenated message. All routes resolve scope with
// withMembership (no workspace build); mutations are ownership-guarded in the store by
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

// memoListFor lists a membership's memos (unsent + retention-window sent), sweeping
// expired sent rows first. The membership-scoped core shared by the three faces: the
// session HTTP handler, the internal operator-token bridge, and the CP MCP tools.
func memoListFor(ctx context.Context, store MemoStore, membershipID string) ([]memoDTO, error) {
	cutoff := memoRetainBefore()
	// Lazy sweep: drop expired sent memos before listing (best-effort; a failed
	// sweep still lets the list run, the cutoff filter hides expired rows anyway).
	_ = store.SweepSentMemos(ctx, cutoff)
	rows, err := store.ListMemos(ctx, membershipID, cutoff)
	if err != nil {
		return nil, err
	}
	out := make([]memoDTO, 0, len(rows))
	for _, m := range rows {
		out = append(out, memoToDTO(m))
	}
	return out, nil
}

func (a memoAPI) list(w http.ResponseWriter, r *http.Request, _ Identity, mv MembershipView) {
	out, err := memoListFor(r.Context(), a.store, mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
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

// memoCreateFor validates + inserts one memo for a membership. Shared core.
func memoCreateFor(ctx context.Context, store MemoStore, mv MembershipView, in memoDTO) (memoDTO, *apiError) {
	m, aerr := validateMemo(mv, in)
	if aerr != nil {
		return memoDTO{}, aerr
	}
	m.ID = newID()
	m.CreatedAt = nowTS()
	if err := store.CreateMemo(ctx, m); err != nil {
		return memoDTO{}, internalErr(err)
	}
	return memoToDTO(m), nil
}

func (a memoAPI) create(w http.ResponseWriter, r *http.Request, _ Identity, mv MembershipView) {
	var in memoDTO
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
		return
	}
	dto, aerr := memoCreateFor(r.Context(), a.store, mv, in)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusCreated, dto)
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

// memoUpdateFor applies a partial edit to an owned memo (ownership by membership).
// Nil patch fields are left unchanged. Shared core.
func memoUpdateFor(ctx context.Context, store MemoStore, membershipID, id string, in memoPatch) (memoDTO, *apiError) {
	cur, found, err := store.GetMemo(ctx, id)
	if err != nil {
		return memoDTO{}, internalErr(err)
	}
	if !found || cur.MembershipID != membershipID {
		return memoDTO{}, &apiError{http.StatusNotFound, "not_found", "memo not found"}
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
		return memoDTO{}, &apiError{http.StatusBadRequest, "bad_ref", "refPath is required for kind=file"}
	}
	if cur.Kind == "text" && cur.Body == "" {
		return memoDTO{}, &apiError{http.StatusBadRequest, "bad_body", "body is required for kind=text"}
	}
	if err := store.UpdateMemo(ctx, cur); err != nil {
		return memoDTO{}, internalErr(err)
	}
	return memoToDTO(cur), nil
}

func (a memoAPI) update(w http.ResponseWriter, r *http.Request, _ Identity, mv MembershipView) {
	var in memoPatch
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
		return
	}
	dto, aerr := memoUpdateFor(r.Context(), a.store, mv.MembershipID, r.PathValue("id"), in)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, dto)
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
	out, aerr := memoFlushFor(r.Context(), a.store, res.rt, res.mv.MembershipID, in.SessionName, in.IDs)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a memoAPI) delete(w http.ResponseWriter, r *http.Request, _ Identity, mv MembershipView) {
	if err := a.store.DeleteMemo(r.Context(), r.PathValue("id"), mv.MembershipID); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// memoFlushFor resolves the requested ids to OWNED memos, concatenates them into one
// message (category-grouped), sends it once to the session's input via the workspace
// runtime, and stamps sent_at on those memos. Returns the flush summary. Shared core
// between the session HTTP handler, the internal operator-token bridge, and the CP MCP
// tool — every face funnels through this so the "send once + mark sent" stays atomic.
func memoFlushFor(ctx context.Context, store MemoStore, rt Runtime, membershipID, sessionName string, ids []string) (map[string]any, *apiError) {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return nil, &apiError{http.StatusBadRequest, "bad_session", "sessionName is required"}
	}
	if len(ids) == 0 {
		return nil, &apiError{http.StatusBadRequest, "no_ids", "ids is required"}
	}
	// Resolve the requested ids to owned memos (foreign/unknown ids are dropped).
	memos := make([]Memo, 0, len(ids))
	for _, id := range ids {
		m, found, err := store.GetMemo(ctx, id)
		if err != nil {
			return nil, internalErr(err)
		}
		if found && m.MembershipID == membershipID {
			memos = append(memos, m)
		}
	}
	if len(memos) == 0 {
		return nil, &apiError{http.StatusNotFound, "no_memos", "no owned memos for the given ids"}
	}
	// Group by category (stable within-category order preserved from ListMemos).
	sort.SliceStable(memos, func(i, j int) bool { return memos[i].Category < memos[j].Category })

	if aerr := sendMemoPrompt(ctx, rt, sessionName, buildFlushMessage(memos)); aerr != nil {
		return nil, aerr
	}

	sentIDs := make([]string, len(memos))
	for i, m := range memos {
		sentIDs[i] = m.ID
	}
	sentAt := nowTS()
	if err := store.MarkMemosSent(ctx, membershipID, sentIDs, sentAt); err != nil {
		return nil, internalErr(err)
	}
	return map[string]any{"sent": len(sentIDs), "sentAt": sentAt, "ids": sentIDs}, nil
}

// sendMemoPrompt uses the same semantic turn endpoint as Console chat. /input is
// deliberately TUI-only now; managed sessions have no tmux process, so sending a
// memo there incorrectly reports not_running. Old workspace images can briefly lag
// the Control Plane during a rollout, hence the narrow fallback when /turn itself is
// absent (a plain 404/405 rather than a structured Agent error).
func sendMemoPrompt(ctx context.Context, rt Runtime, sessionName, prompt string) *apiError {
	path := "/sessions/" + url.PathEscape(sessionName)
	payload, _ := json.Marshal(map[string]string{"op": "start", "prompt": prompt})
	if _, err := agentText(ctx, rt, http.MethodPost, path+"/turn", payload); err == nil {
		return nil
	} else if !agentEndpointMissing(err) {
		return memoAgentError(err)
	}

	legacy, _ := json.Marshal(map[string]string{"prompt": prompt})
	if _, err := agentText(ctx, rt, http.MethodPost, path+"/input", legacy); err != nil {
		return memoAgentError(err)
	}
	return nil
}

// agentEndpointMissing distinguishes an old Agent with no /turn route from a real
// structured 404/405 (for example, an unknown session), which must reach the user.
func agentEndpointMissing(err error) bool {
	var he *agentHTTPError
	if !errors.As(err, &he) || (he.status != http.StatusNotFound && he.status != http.StatusMethodNotAllowed) {
		return false
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	return json.Unmarshal([]byte(he.body), &envelope) != nil || envelope.Error.Code == ""
}

// memoAgentError preserves the Agent's stable error code so the Console can show
// its localized message (question_pending, not_running, runtime_failed, ...).
// Transport and non-JSON failures retain the memo-specific gateway fallback.
func memoAgentError(err error) *apiError {
	var he *agentHTTPError
	if errors.As(err, &he) {
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(he.body), &envelope) == nil && envelope.Error.Code != "" {
			return &apiError{he.status, envelope.Error.Code, envelope.Error.Message}
		}
	}
	return &apiError{http.StatusBadGateway, "flush_failed", err.Error()}
}
