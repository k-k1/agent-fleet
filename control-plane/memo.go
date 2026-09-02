package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"
)

// Memo queue (docs/log/21). Per-membership notes accumulated across devices, then flushed
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

// memoAttachment is one image attached to a memo (docs/log/21 画像添付). Path is the
// absolute in-container path returned by POST /api/memos/paste-image (under
// ~/.cache/agent-fleet/memo-images); Name is its basename for display.
type memoAttachment struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

// memoDTO is the JSON wire shape. sentAt is "" for an unsent memo.
type memoDTO struct {
	ID          string           `json:"id"`
	Repo        string           `json:"repo"`
	Category    string           `json:"category"`
	Kind        string           `json:"kind"`
	Body        string           `json:"body"`
	RefPath     string           `json:"refPath"`
	Attachments []memoAttachment `json:"attachments,omitempty"`
	Position    int              `json:"position"`
	CreatedAt   string           `json:"createdAt"`
	SentAt      string           `json:"sentAt"`
}

func memoToDTO(m Memo) memoDTO {
	return memoDTO{ID: m.ID, Repo: m.Repo, Category: m.Category, Kind: m.Kind,
		Body: m.Body, RefPath: m.RefPath, Attachments: parseMemoAttachments(m.Attachments),
		Position: m.Position, CreatedAt: m.CreatedAt, SentAt: m.SentAt}
}

// parseMemoAttachments decodes the stored JSON attachments column into a slice
// (nil for an empty/invalid column, so a corrupt row degrades to "no images"
// rather than failing the whole list).
func parseMemoAttachments(raw string) []memoAttachment {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []memoAttachment
	if json.Unmarshal([]byte(raw), &out) != nil {
		return nil
	}
	return out
}

// normalizeAttachments trims + validates the incoming attachments and marshals them
// back to the JSON string stored in the column ("" for none). Each attachment needs a
// non-empty path; a missing name is derived from the path's basename.
func normalizeAttachments(in []memoAttachment) (string, *apiError) {
	out := make([]memoAttachment, 0, len(in))
	for _, a := range in {
		p := strings.TrimSpace(a.Path)
		if p == "" {
			return "", &apiError{http.StatusBadRequest, "bad_attachment", "attachment path is required"}
		}
		name := strings.TrimSpace(a.Name)
		if name == "" {
			name = path.Base(p)
		}
		out = append(out, memoAttachment{Path: p, Name: name})
	}
	if len(out) == 0 {
		return "", nil
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", internalErr(err)
	}
	return string(b), nil
}

// memoAPI is the memo-queue feature handler set（docs/log/23 残③ の機能 struct 実例）:
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
func memoListFor(ctx context.Context, st MemoStore, membershipID string) ([]memoDTO, error) {
	cutoff := memoRetainBefore()
	// Lazy sweep: drop expired sent memos before listing (best-effort; a failed
	// sweep still lets the list run, the cutoff filter hides expired rows anyway).
	_ = st.SweepSentMemos(ctx, cutoff)
	rows, err := st.ListMemos(ctx, membershipID, cutoff)
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
// unset). kind must be "file" (ref_path required) or "text" (body OR image attachments
// required — an image-only memo shared from a phone carries no body).
func validateMemo(mv MembershipView, in memoDTO) (Memo, *apiError) {
	atts, aerr := normalizeAttachments(in.Attachments)
	if aerr != nil {
		return Memo{}, aerr
	}
	m := Memo{
		MembershipID: mv.MembershipID,
		Repo:         strings.TrimSpace(in.Repo),
		Category:     strings.TrimSpace(in.Category),
		Kind:         strings.TrimSpace(in.Kind),
		Body:         strings.TrimSpace(in.Body),
		RefPath:      strings.TrimSpace(in.RefPath),
		Attachments:  atts,
		Position:     in.Position,
	}
	switch m.Kind {
	case "file":
		if m.RefPath == "" {
			return Memo{}, &apiError{http.StatusBadRequest, "bad_ref", "refPath is required for kind=file"}
		}
	case "text":
		if m.Body == "" && atts == "" {
			return Memo{}, &apiError{http.StatusBadRequest, "bad_body", "body or attachments is required for kind=text"}
		}
	default:
		return Memo{}, &apiError{http.StatusBadRequest, "bad_kind", "kind must be file or text"}
	}
	return m, nil
}

// memoCreateFor validates + inserts one memo for a membership. Shared core.
func memoCreateFor(ctx context.Context, st MemoStore, mv MembershipView, in memoDTO) (memoDTO, *apiError) {
	m, aerr := validateMemo(mv, in)
	if aerr != nil {
		return memoDTO{}, aerr
	}
	// New memos always join the end of their own repo/category group.  Clients do
	// not need to coordinate a position (and an omitted JSON position otherwise
	// becomes zero, which used to put every new memo at the top of the group).
	rows, err := st.ListMemos(ctx, m.MembershipID, "")
	if err != nil {
		return memoDTO{}, internalErr(err)
	}
	for _, row := range rows {
		if row.Repo == m.Repo && row.Category == m.Category && row.Position >= m.Position {
			m.Position = row.Position + 1
		}
	}
	m.ID = newID()
	m.CreatedAt = nowTS()
	if err := st.CreateMemo(ctx, m); err != nil {
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
	Repo        *string           `json:"repo"`
	Category    *string           `json:"category"`
	Body        *string           `json:"body"`
	RefPath     *string           `json:"refPath"`
	Attachments *[]memoAttachment `json:"attachments"`
	Position    *int              `json:"position"`
}

// memoUpdateFor applies a partial edit to an owned memo (ownership by membership).
// Nil patch fields are left unchanged. Shared core.
func memoUpdateFor(ctx context.Context, st MemoStore, membershipID, id string, in memoPatch) (memoDTO, *apiError) {
	cur, found, err := st.GetMemo(ctx, id)
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
	if in.Attachments != nil {
		atts, aerr := normalizeAttachments(*in.Attachments)
		if aerr != nil {
			return memoDTO{}, aerr
		}
		cur.Attachments = atts
	}
	if in.Position != nil {
		cur.Position = *in.Position
	}
	if cur.Kind == "file" && cur.RefPath == "" {
		return memoDTO{}, &apiError{http.StatusBadRequest, "bad_ref", "refPath is required for kind=file"}
	}
	if cur.Kind == "text" && cur.Body == "" && cur.Attachments == "" {
		return memoDTO{}, &apiError{http.StatusBadRequest, "bad_body", "body or attachments is required for kind=text"}
	}
	if err := st.UpdateMemo(ctx, cur); err != nil {
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

// buildFlushMessage concatenates the selected memos directly, grouping by category
// (empty category -> 未分類) and preserving the ORDER of the resolved memos
// (already sorted by category/position on the way in). File memos surface their
// ~/repos ref path plus any comment, text memos surface their body.
func buildFlushMessage(memos []Memo) string {
	var b strings.Builder
	lastCat := "\x00" // sentinel so the first real category (incl. "") emits a heading
	n := 0
	var imgPaths []string // absolute in-container image paths, appended once at the end
	for _, m := range memos {
		if m.Category != lastCat {
			lastCat = m.Category
			cat := m.Category
			if cat == "" {
				cat = "未分類"
			}
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString("## " + cat + "\n")
			n = 0
		}
		n++
		atts := parseMemoAttachments(m.Attachments)
		switch {
		case m.Kind == "file":
			fmt.Fprintf(&b, "%d. 対象ファイル: %s\n", n, m.RefPath)
			if m.Body != "" {
				b.WriteString("   " + m.Body + "\n")
			}
		case m.Body != "":
			fmt.Fprintf(&b, "%d. %s\n", n, m.Body)
		default:
			fmt.Fprintf(&b, "%d. （画像）\n", n)
		}
		if len(atts) > 0 {
			names := make([]string, len(atts))
			for i, a := range atts {
				names[i] = a.Name
				imgPaths = append(imgPaths, a.Path)
			}
			b.WriteString("   添付画像: " + strings.Join(names, ", ") + "\n")
		}
	}
	// Machine-facing line (mirrors the paste composer's FILE_PROMPT) so the target
	// agent opens the attached images with its Read tool. Kept on ONE line and last
	// so send-keys can't submit early and the human-readable memo body stays clean.
	if len(imgPaths) > 0 {
		b.WriteString("\n" + memoImageOpenPrompt + " " + strings.Join(imgPaths, " ") + "\n")
	}
	return b.String()
}

// memoImageOpenPrompt is the agent-facing instruction appended to a flush when memos
// carry image attachments. English + "Read tool" wording matches the paste-image flow
// (console/src/lib/pastedImages.ts FILE_PROMPT) so both paths read identically.
const memoImageOpenPrompt = "Open the following file(s) with the Read tool:"

// handleMemoFlush concatenates the selected memos into one message, sends it once to
// the session's input, and stamps sent_at on those memos. The ids list unifies the
// three send granularities (whole repo / category / individual) — the client just
// builds the right ids. Sending happens through the running workspace agent, so this
// resolves the full runtime (not just membership).
func (a memoAPI) flush(w http.ResponseWriter, r *http.Request, res *resolved) {
	var in struct {
		SessionName string   `json:"sessionName"`
		IDs         []string `json:"ids"`
		// Text, when non-empty, is sent verbatim instead of the server-composed
		// message — the send modal lets the user edit the concatenated text before
		// sending (docs/log/21 UI刷新). The ids still drive which memos get stamped sent.
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
		return
	}
	out, aerr := memoFlushFor(r.Context(), a.store, res.rt, res.mv.MembershipID, in.SessionName, in.IDs, in.Text)
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

// --- Memo categories (docs/log/21 UI刷新) -------------------------------------------
// First-class categories: created ahead of any memo, reordered by drag-and-drop, kept
// while empty. A category's NAME stays the grouping key (Memo.Category), so a rename
// cascades onto the memos and a rename onto an existing name merges the two.

type memoCategoryDTO struct {
	ID        string `json:"id"`
	Repo      string `json:"repo"`
	Name      string `json:"name"`
	Position  int    `json:"position"`
	CreatedAt string `json:"createdAt"`
}

func memoCategoryToDTO(c MemoCategory) memoCategoryDTO {
	return memoCategoryDTO{ID: c.ID, Repo: c.Repo, Name: c.Name, Position: c.Position, CreatedAt: c.CreatedAt}
}

func (a memoAPI) listCategories(w http.ResponseWriter, r *http.Request, _ Identity, mv MembershipView) {
	rows, err := a.store.ListCategories(r.Context(), mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := make([]memoCategoryDTO, 0, len(rows))
	for _, c := range rows {
		out = append(out, memoCategoryToDTO(c))
	}
	writeJSON(w, http.StatusOK, out)
}

// createCategory adds a category to a repo bucket, appended after the existing ones. A
// duplicate (membership, repo, name) is a no-op that returns the existing row, so the
// "＋カテゴリ" button is idempotent and never 409s.
func (a memoAPI) createCategory(w http.ResponseWriter, r *http.Request, _ Identity, mv MembershipView) {
	var in memoCategoryDTO
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
		return
	}
	repo := strings.TrimSpace(in.Repo)
	name := strings.TrimSpace(in.Name)
	if name == "" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_name", "name is required"})
		return
	}
	existing, err := a.store.ListCategories(r.Context(), mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	maxPos := -1
	for _, c := range existing {
		if c.Repo == repo {
			if c.Name == name {
				writeJSON(w, http.StatusOK, memoCategoryToDTO(c))
				return
			}
			if c.Position > maxPos {
				maxPos = c.Position
			}
		}
	}
	c := MemoCategory{ID: newID(), MembershipID: mv.MembershipID, Repo: repo, Name: name, Position: maxPos + 1, CreatedAt: nowTS()}
	if err := a.store.CreateCategory(r.Context(), c); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusCreated, memoCategoryToDTO(c))
}

// memoCategoryPatch carries a rename and/or reorder. Nil fields are unchanged, so a
// drag reorder sends only position and a rename sends only name.
type memoCategoryPatch struct {
	Name     *string `json:"name"`
	Position *int    `json:"position"`
}

// updateCategory renames and/or reorders an owned category. A rename cascades onto the
// memos (Memo.Category is the name); renaming onto a name that already exists in the same
// repo MERGES — the memos move to the survivor and the renamed row is deleted — because
// the unique (membership, repo, name) index forbids two categories sharing a name.
func (a memoAPI) updateCategory(w http.ResponseWriter, r *http.Request, _ Identity, mv MembershipView) {
	var in memoCategoryPatch
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
		return
	}
	id := r.PathValue("id")
	cur, found, err := a.store.GetCategory(r.Context(), id)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !found || cur.MembershipID != mv.MembershipID {
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "category not found"})
		return
	}
	if in.Position != nil {
		cur.Position = *in.Position
	}
	if in.Name != nil {
		newName := strings.TrimSpace(*in.Name)
		if newName == "" {
			writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_name", "name is required"})
			return
		}
		if newName != cur.Name {
			// Move the memos onto the new name, then either merge into an existing
			// same-name category or rename this row.
			if err := a.store.ReassignMemoCategory(r.Context(), mv.MembershipID, cur.Repo, cur.Name, newName); err != nil {
				writeAPIErr(w, internalErr(err))
				return
			}
			if dup := a.findCategory(r.Context(), mv.MembershipID, cur.Repo, newName); dup != nil {
				if err := a.store.DeleteCategory(r.Context(), cur.ID, mv.MembershipID); err != nil {
					writeAPIErr(w, internalErr(err))
					return
				}
				writeJSON(w, http.StatusOK, memoCategoryToDTO(*dup))
				return
			}
			cur.Name = newName
		}
	}
	if err := a.store.UpdateCategory(r.Context(), cur); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, memoCategoryToDTO(cur))
}

// findCategory returns the caller's category with (repo, name), or nil. Small helper for
// the merge-on-rename path.
func (a memoAPI) findCategory(ctx context.Context, membershipID, repo, name string) *MemoCategory {
	rows, err := a.store.ListCategories(ctx, membershipID)
	if err != nil {
		return nil
	}
	for i := range rows {
		if rows[i].Repo == repo && rows[i].Name == name {
			return &rows[i]
		}
	}
	return nil
}

// deleteCategory removes a category and empties its memos (moves them to 未分類 rather
// than deleting them — a category delete must never lose notes).
func (a memoAPI) deleteCategory(w http.ResponseWriter, r *http.Request, _ Identity, mv MembershipView) {
	id := r.PathValue("id")
	cur, found, err := a.store.GetCategory(r.Context(), id)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !found || cur.MembershipID != mv.MembershipID {
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "category not found"})
		return
	}
	if err := a.store.ReassignMemoCategory(r.Context(), mv.MembershipID, cur.Repo, cur.Name, ""); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if err := a.store.DeleteCategory(r.Context(), id, mv.MembershipID); err != nil {
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
func memoFlushFor(ctx context.Context, st MemoStore, rt Runtime, membershipID, sessionName string, ids []string, textOverride string) (map[string]any, *apiError) {
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
		m, found, err := st.GetMemo(ctx, id)
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

	// Use the caller's edited text when provided; otherwise compose from the memos.
	message := strings.TrimSpace(textOverride)
	if message == "" {
		message = buildFlushMessage(memos)
	}
	if aerr := sendMemoPrompt(ctx, rt, sessionName, message); aerr != nil {
		return nil, aerr
	}

	sentIDs := make([]string, len(memos))
	for i, m := range memos {
		sentIDs[i] = m.ID
	}
	sentAt := nowTS()
	if err := st.MarkMemosSent(ctx, membershipID, sentIDs, sentAt); err != nil {
		// 送信自体は完了している。ここで 500 を返すとクライアントの再試行が同内容の
		// 二重送信を誘発するので、ログに残して成功として返す(メモは未送信のまま残る)。
		log.Printf("memo flush: mark sent failed (message already delivered) session=%s ids=%v: %v",
			sessionName, sentIDs, err)
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
