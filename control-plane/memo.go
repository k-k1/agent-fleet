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

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
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

// memoAttachment is one image attached to a memo (docs/log/21 image attachments). Path is the
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
	// Origin is set only on a memo taken in from the inbox, by the CP (ADR 0111 decision 3).
	// A client may send one: validateMemo and memoPatch never read it.
	Origin *memoOriginWire `json:"origin,omitempty"`
	// The message fields of ADR 0137 (memo_queue.go), all written by the CP. A client may
	// send them: validateMemo and memoPatch never read them. State is "" for a plain note.
	// Provenance is the face the memo was created by, "workspace" for a row from before it was
	// recorded. ContentHash is what a release names (POST /api/memos/release).
	State       string `json:"state"`
	Provenance  string `json:"provenance"`
	ReleasedAt  string `json:"releasedAt,omitempty"`
	ContentHash string `json:"contentHash"`
	// Producer and ProducerOrigin are attribution only: on the token face the session name and
	// origin the Agent relay supplies on create (the one place a request field is read for
	// them), on /mcp the connection id. Never read by a rule.
	Producer       string `json:"producer,omitempty"`
	ProducerOrigin string `json:"producerOrigin,omitempty"`
}

// memoOriginWire is where a taken-in memo came from: the inbox item and its sender.
type memoOriginWire struct {
	ItemID string `json:"itemId"`
	Sender string `json:"sender"`
}

func memoToDTO(m store.Memo) memoDTO {
	d := memoDTO{ID: m.ID, Repo: m.Repo, Category: m.Category, Kind: m.Kind,
		Body: m.Body, RefPath: m.RefPath, Attachments: parseMemoAttachments(m.Attachments),
		Position: m.Position, CreatedAt: m.CreatedAt, SentAt: m.SentAt,
		State: m.State, Provenance: memoEffectiveProvenance(m), ReleasedAt: m.ReleasedAt,
		ContentHash: memoContentHash(m), Producer: m.Producer, ProducerOrigin: m.ProducerOrigin}
	if m.OriginItemID != "" {
		d.Origin = &memoOriginWire{ItemID: m.OriginItemID, Sender: m.OriginSenderKey}
	}
	return d
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

// memoAPI is the memo-queue feature handler set (docs/log/23, remaining item 3, as the
// worked example of a feature struct): resolution comes from the embedded memberAuth
// (registration wraps it in withMembership / withResolved) and the store is a narrow
// MemoStore view. flush alone registers through withResolved because it has to reach
// the live runtime.
type memoAPI struct {
	memberAuth
	store store.MemoStore
}

func newMemoAPI(m *manager) memoAPI { return memoAPI{memberAuth{m}, m.store} }

// memoListFor lists a membership's memos (unsent + retention-window sent), sweeping
// expired sent rows first. The membership-scoped core shared by the three faces: the
// session HTTP handler, the internal operator-token bridge, and the CP MCP tools.
func memoListFor(ctx context.Context, st store.MemoStore, membershipID string) ([]memoDTO, error) {
	cutoff := memoRetainBefore()
	// Lazy sweep: drop expired sent memos before listing (best-effort; a failed
	// sweep still lets the list run, the cutoff filter hides expired rows anyway).
	_ = st.SweepSentMemos(ctx, cutoff)
	rows, err := st.ListMemos(ctx, membershipID, cutoff)
	if err != nil {
		return nil, err
	}
	// A send reservation left by a CP that died is taken back by MemoTx. Lists are polled,
	// so the transaction is opened only when the list shows a stale one.
	stale := time.Now().UTC().Add(-store.MemoSendStaleAfter).Format(time.RFC3339)
	for _, m := range rows {
		if m.SendToken != "" && m.SendStartedAt < stale {
			if err := st.MemoTx(ctx, membershipID, func(store.MemoTxOps) error { return nil }); err != nil {
				return nil, err
			}
			if rows, err = st.ListMemos(ctx, membershipID, cutoff); err != nil {
				return nil, err
			}
			break
		}
	}
	out := make([]memoDTO, 0, len(rows))
	for _, m := range rows {
		out = append(out, memoToDTO(m))
	}
	return out, nil
}

func (a memoAPI) list(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
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
func validateMemo(mv store.MembershipView, in memoDTO) (store.Memo, *apiError) {
	atts, aerr := normalizeAttachments(in.Attachments)
	if aerr != nil {
		return store.Memo{}, aerr
	}
	m := store.Memo{
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
			return store.Memo{}, &apiError{http.StatusBadRequest, "bad_ref", "refPath is required for kind=file"}
		}
	case "text":
		if m.Body == "" && atts == "" {
			return store.Memo{}, &apiError{http.StatusBadRequest, "bad_body", "body or attachments is required for kind=text"}
		}
	default:
		return store.Memo{}, &apiError{http.StatusBadRequest, "bad_kind", "kind must be file or text"}
	}
	return m, nil
}

// memoCreateFor is memoCreateVia on the Console face.
func memoCreateFor(ctx context.Context, st store.MemoStore, mv store.MembershipView, in memoDTO) (memoDTO, *apiError) {
	dto, _, aerr := memoCreateVia(ctx, st, mv, in, memoViaConsole)
	return dto, aerr
}

// memoCreateVia validates + inserts one memo for a membership. Shared core. The provenance,
// the producer and, in a queue, the entering state come from via, never from in. created is
// false when a create into a queue from inside the workspace or /mcp matched a message already
// waiting there, which is answered instead.
func memoCreateVia(ctx context.Context, st store.MemoStore, mv store.MembershipView, in memoDTO, via memoVia) (memoDTO, bool, *apiError) {
	m, aerr := validateMemo(mv, in)
	if aerr != nil {
		return memoDTO{}, false, aerr
	}
	m.Provenance, m.Producer, m.ProducerOrigin = via.provenance(), clipProducer(via.producer), clipProducer(via.producerOrigin)
	created := true
	err := st.MemoTx(ctx, mv.MembershipID, func(tx store.MemoTxOps) error {
		inQ, err := memoInQueue(tx, m.Repo, m.Category)
		if err != nil {
			return err
		}
		if inQ && via.fromWorkspace() {
			dup, aerr, err := memoCreateBounds(tx, m)
			if err != nil {
				return err
			}
			if aerr != nil {
				return memoTxFail(aerr)
			}
			if dup != nil {
				m, created = *dup, false
				return nil
			}
		}
		// New memos always join the end of their own repo/category group.  Clients do
		// not need to coordinate a position (and an omitted JSON position otherwise
		// becomes zero, which used to put every new memo at the top of the group).
		rows, err := tx.ListMemosIn(m.Repo, m.Category)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.Position >= m.Position {
				m.Position = row.Position + 1
			}
		}
		m.ID = store.NewID()
		m.CreatedAt = store.NowTS()
		if m, aerr = memoQueueMove(m, m, false, inQ, via); aerr != nil {
			return memoTxFail(aerr)
		}
		if inQ && via.fromWorkspace() {
			if err := tx.RecordQueueIntake(m.CreatedAt); err != nil {
				return err
			}
		}
		return tx.InsertMemo(m)
	})
	if aerr := memoTxResult(err); aerr != nil {
		return memoDTO{}, false, aerr
	}
	return memoToDTO(m), created, nil
}

func (a memoAPI) create(via memoVia) func(http.ResponseWriter, *http.Request, store.Identity, store.MembershipView) {
	return func(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
		var in memoDTO
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
			return
		}
		v := via
		if v.face == memoFaceToken {
			v.producer, v.producerOrigin = in.Producer, in.ProducerOrigin
		}
		dto, created, aerr := memoCreateVia(r.Context(), a.store, mv, in, v)
		if aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
		status := http.StatusCreated
		if !created {
			status = http.StatusOK
		}
		writeJSON(w, status, dto)
	}
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
// Nil patch fields are left unchanged. Shared core. In a queue the edit also moves the
// message's state by the rules of the face it arrived by (memoQueueMove).
func memoUpdateFor(ctx context.Context, st store.MemoStore, membershipID, id string, in memoPatch, via memoVia) (memoDTO, *apiError) {
	var out store.Memo
	err := st.MemoTx(ctx, membershipID, func(tx store.MemoTxOps) error {
		cur, found, err := tx.GetMemo(id)
		if err != nil {
			return err
		}
		if !found {
			return memoTxFail(&apiError{http.StatusNotFound, "not_found", "memo not found"})
		}
		if memoStateLocked(cur.State) {
			return memoTxFail(&apiError{http.StatusConflict, errCodeMemoLocked, "this message has been delivered and cannot be changed"})
		}
		next, aerr := applyMemoPatch(cur, in)
		if aerr != nil {
			return memoTxFail(aerr)
		}
		inPrev, err := memoInQueue(tx, cur.Repo, cur.Category)
		if err != nil {
			return err
		}
		inNext, err := memoInQueue(tx, next.Repo, next.Category)
		if err != nil {
			return err
		}
		if next, aerr = memoQueueMove(cur, next, inPrev, inNext, via); aerr != nil {
			return memoTxFail(aerr)
		}
		out = next
		return tx.WriteMemo(next)
	})
	if aerr := memoTxResult(err); aerr != nil {
		return memoDTO{}, aerr
	}
	return memoToDTO(out), nil
}

// applyMemoPatch applies the non-nil fields of a patch and re-checks the kind's requirement.
func applyMemoPatch(cur store.Memo, in memoPatch) (store.Memo, *apiError) {
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
			return cur, aerr
		}
		cur.Attachments = atts
	}
	if in.Position != nil {
		cur.Position = *in.Position
	}
	if cur.Kind == "file" && cur.RefPath == "" {
		return cur, &apiError{http.StatusBadRequest, "bad_ref", "refPath is required for kind=file"}
	}
	if cur.Kind == "text" && cur.Body == "" && cur.Attachments == "" {
		return cur, &apiError{http.StatusBadRequest, "bad_body", "body or attachments is required for kind=text"}
	}
	return cur, nil
}

func (a memoAPI) update(via memoVia) func(http.ResponseWriter, *http.Request, store.Identity, store.MembershipView) {
	return func(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
		var in memoPatch
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
			return
		}
		dto, aerr := memoUpdateFor(r.Context(), a.store, mv.MembershipID, r.PathValue("id"), in, via)
		if aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
		writeJSON(w, http.StatusOK, dto)
	}
}

// buildFlushMessage concatenates the selected memos directly, grouping by category
// (an empty category becomes the uncategorized heading) and preserving the ORDER of the
// resolved memos
// (already sorted by category/position on the way in). File memos surface their ~/repos
// ref path plus any comment, text memos surface their body.
//
// A single memo is sent bare: with nothing to group or count, the heading and the "1. "
// prefix are scaffolding the recipient has to read past, and they turn a multi-line note
// into a list item that lost its indent. The Console's composeMemoMessage
// (SendMemoModal.tsx) does the same so an unedited send matches this byte for byte.
func buildFlushMessage(memos []store.Memo) string {
	var b strings.Builder
	solo := len(memos) == 1
	lastCat := "\x00" // sentinel so the first real category (incl. "") emits a heading
	n := 0
	num := func(n int) string {
		if solo {
			return ""
		}
		return fmt.Sprintf("%d. ", n)
	}
	cont := "   " // continuation lines align under the number
	if solo {
		cont = ""
	}
	var imgPaths []string // absolute in-container image paths, appended once at the end
	for _, m := range memos {
		if !solo && m.Category != lastCat {
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
			b.WriteString(num(n) + "対象ファイル: " + m.RefPath + "\n")
			if m.Body != "" {
				b.WriteString(cont + m.Body + "\n")
			}
		case m.Body != "":
			b.WriteString(num(n) + m.Body + "\n")
		default:
			b.WriteString(num(n) + "（画像）\n")
		}
		if len(atts) > 0 {
			names := make([]string, len(atts))
			for i, a := range atts {
				names[i] = a.Name
				imgPaths = append(imgPaths, a.Path)
			}
			b.WriteString(cont + "添付画像: " + strings.Join(names, ", ") + "\n")
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

// memoOriginLine heads the message of a taken-in memo, so the session sees where the text
// came from. It is the server's line, outside the text a member can edit.
func memoOriginLine(m store.Memo) string {
	who := m.OriginSenderKey
	if who == "" {
		who = "?"
	}
	return "受信箱から取り込んだ内容（送信者: " + who + "、項目: " + m.OriginItemID + "）"
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
func (a memoAPI) flush(via memoVia) func(http.ResponseWriter, *http.Request, *resolved) {
	return func(w http.ResponseWriter, r *http.Request, res *resolved) { a.flushVia(w, r, res, via) }
}

func (a memoAPI) flushVia(w http.ResponseWriter, r *http.Request, res *resolved, via memoVia) {
	var in struct {
		SessionName string   `json:"sessionName"`
		IDs         []string `json:"ids"`
		// Text, when non-empty, is sent verbatim instead of the server-composed
		// message — the send modal lets the user edit the concatenated text before
		// sending (docs/log/21 UI overhaul). The ids still drive which memos get stamped sent.
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
		return
	}
	out, aerr := memoFlushFor(r.Context(), a.store, res.rt, res.mv.MembershipID, in.SessionName, in.IDs, in.Text, via)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a memoAPI) delete(via memoVia) func(http.ResponseWriter, *http.Request, store.Identity, store.MembershipView) {
	return func(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
		if aerr := memoDeleteFor(r.Context(), a.store, mv.MembershipID, r.PathValue("id"), via); aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// memoDeleteFor deletes an owned memo. Shared core. A claimed message belongs to its delivery
// and is deleted by nobody. From inside the workspace and /mcp a message in a queue is not
// deleted either: that stays with the Console. An unknown id is a no-op.
func memoDeleteFor(ctx context.Context, st store.MemoStore, membershipID, id string, via memoVia) *apiError {
	return memoTxResult(st.MemoTx(ctx, membershipID, func(tx store.MemoTxOps) error {
		m, ok, err := tx.GetMemo(id)
		if err != nil || !ok {
			return err
		}
		if m.State == store.MemoStateClaimed {
			return memoTxFail(&apiError{http.StatusConflict, errCodeMemoLocked, "this message has been taken by a delivery"})
		}
		if !via.fromWorkspace() {
			return tx.DeleteMemo(id)
		}
		inQ, err := memoInQueue(tx, m.Repo, m.Category)
		if err != nil {
			return err
		}
		if inQ || m.State != "" {
			return memoTxFail(&apiError{http.StatusConflict, errCodeMemoInQueue, "a message in a queue is deleted from the Console only"})
		}
		return tx.DeleteMemo(id)
	}))
}

// --- Memo categories (docs/log/21 UI overhaul) -------------------------------------------
// First-class categories: created ahead of any memo, reordered by drag-and-drop, kept
// while empty. A category's NAME stays the grouping key (Memo.Category), so a rename
// cascades onto the memos and a rename onto an existing name merges the two.

// memoCategoryDTO is a category on the wire. Queue says whether it has queue settings
// (ADR 0137), on every face, so that a session listing memos can tell a queue from a note.
type memoCategoryDTO struct {
	ID        string `json:"id"`
	Repo      string `json:"repo"`
	Name      string `json:"name"`
	Position  int    `json:"position"`
	CreatedAt string `json:"createdAt"`
	Queue     bool   `json:"queue"`
}

func memoCategoryToDTO(c store.MemoCategory, queue bool) memoCategoryDTO {
	return memoCategoryDTO{ID: c.ID, Repo: c.Repo, Name: c.Name, Position: c.Position, CreatedAt: c.CreatedAt, Queue: queue}
}

// memoQueueIDs is the set of a membership's category ids that are queues.
func (a memoAPI) memoQueueIDs(ctx context.Context, membershipID string) (map[string]bool, error) {
	qs, err := a.store.ListMemoQueues(ctx, membershipID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(qs))
	for _, q := range qs {
		out[q.CategoryID] = true
	}
	return out, nil
}

func (a memoAPI) listCategories(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	rows, err := a.store.ListCategories(r.Context(), mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	queues, err := a.memoQueueIDs(r.Context(), mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := make([]memoCategoryDTO, 0, len(rows))
	for _, c := range rows {
		out = append(out, memoCategoryToDTO(c, queues[c.ID]))
	}
	writeJSON(w, http.StatusOK, out)
}

// createCategory adds a category to a repo bucket, appended after the existing ones. A
// duplicate (membership, repo, name) is a no-op that returns the existing row, so the
// add-category button is idempotent and never 409s.
func (a memoAPI) createCategory(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
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
				queues, err := a.memoQueueIDs(r.Context(), mv.MembershipID)
				if err != nil {
					writeAPIErr(w, internalErr(err))
					return
				}
				writeJSON(w, http.StatusOK, memoCategoryToDTO(c, queues[c.ID]))
				return
			}
			if c.Position > maxPos {
				maxPos = c.Position
			}
		}
	}
	c := store.MemoCategory{ID: store.NewID(), MembershipID: mv.MembershipID, Repo: repo, Name: name, Position: maxPos + 1, CreatedAt: store.NowTS()}
	if err := a.store.CreateCategory(r.Context(), c); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusCreated, memoCategoryToDTO(c, false))
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
//
// Queues (ADR 0137): a rename keeps the category's settings (they are keyed by its id), and a
// merge keeps the survivor's, so the merged memos enter or leave a queue by the survivor's.
// From inside the workspace a queue is not renamed or merged, and nothing is renamed onto one.
func (a memoAPI) updateCategory(via memoVia) func(http.ResponseWriter, *http.Request, store.Identity, store.MembershipView) {
	return func(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
		var in memoCategoryPatch
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
			return
		}
		var out memoCategoryDTO
		err := a.store.MemoTx(r.Context(), mv.MembershipID, func(tx store.MemoTxOps) error {
			cur, found, err := tx.GetCategory(r.PathValue("id"))
			if err != nil {
				return err
			}
			if !found {
				return memoTxFail(&apiError{http.StatusNotFound, "not_found", "category not found"})
			}
			_, curQ, err := tx.GetQueue(cur.ID)
			if err != nil {
				return err
			}
			if in.Position != nil {
				cur.Position = *in.Position
			}
			if in.Name != nil {
				newName := strings.TrimSpace(*in.Name)
				if newName == "" {
					return memoTxFail(&apiError{http.StatusBadRequest, "bad_name", "name is required"})
				}
				if newName != cur.Name {
					dup, hasDup, err := tx.FindCategory(cur.Repo, newName)
					if err != nil {
						return err
					}
					nextQ := curQ
					if hasDup {
						if _, nextQ, err = tx.GetQueue(dup.ID); err != nil {
							return err
						}
					}
					if via.fromWorkspace() && (curQ || nextQ) {
						return memoTxFail(&apiError{http.StatusConflict, errCodeMemoQueueCategory, "a queue is renamed, merged or deleted from the Console only"})
					}
					// Move the memos onto the new name, then either merge into an existing
					// same-name category or rename this row.
					if err := memoMoveBucket(tx, cur.Repo, cur.Name, newName, curQ, nextQ, via); err != nil {
						return err
					}
					if hasDup {
						if err := tx.DeleteQueue(cur.ID); err != nil {
							return err
						}
						if err := tx.DeleteCategory(cur.ID); err != nil {
							return err
						}
						out = memoCategoryToDTO(dup, nextQ)
						return nil
					}
					cur.Name = newName
				}
			}
			out = memoCategoryToDTO(cur, curQ)
			return tx.UpdateCategory(cur)
		})
		if aerr := memoTxResult(err); aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// deleteCategory removes a category and empties its memos (they move to the
// uncategorized bucket rather than being deleted — a category delete must never lose
// notes). The memos of a queue leave it with its settings, and from inside the workspace a
// queue is not deleted at all.
func (a memoAPI) deleteCategory(via memoVia) func(http.ResponseWriter, *http.Request, store.Identity, store.MembershipView) {
	return func(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
		err := a.store.MemoTx(r.Context(), mv.MembershipID, func(tx store.MemoTxOps) error {
			cur, found, err := tx.GetCategory(r.PathValue("id"))
			if err != nil {
				return err
			}
			if !found {
				return memoTxFail(&apiError{http.StatusNotFound, "not_found", "category not found"})
			}
			_, curQ, err := tx.GetQueue(cur.ID)
			if err != nil {
				return err
			}
			if curQ && via.fromWorkspace() {
				return memoTxFail(&apiError{http.StatusConflict, errCodeMemoQueueCategory, "a queue is renamed, merged or deleted from the Console only"})
			}
			if err := memoMoveBucket(tx, cur.Repo, cur.Name, "", curQ, false, via); err != nil {
				return err
			}
			if err := tx.DeleteQueue(cur.ID); err != nil {
				return err
			}
			return tx.DeleteCategory(cur.ID)
		})
		if aerr := memoTxResult(err); aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// memoFlushFor resolves the requested ids to OWNED memos, concatenates them into one
// message (category-grouped), sends it once to the session's input via the workspace
// runtime, and stamps sent_at on those memos. Returns the flush summary. Shared core
// between the session HTTP handler, the internal operator-token bridge, and the CP MCP
// tool — every face funnels through this so the "send once + mark sent" stays atomic.
//
// A flush runs in three steps. It reserves the rows (send_token) in one MemoTx that reads and
// checks them, sends with no lock held, and then settles the rows that still carry its token.
// While a row is reserved, another flush of it is refused and a queue message in it cannot
// change, so what is stamped delivered is what was sent. Nothing says sent before the send
// has succeeded, and a reservation left by a CP that died is taken back by MemoTx after
// store.MemoSendStaleAfter.
func memoFlushFor(ctx context.Context, st store.MemoStore, rt runtime.Runtime, membershipID, sessionName string, ids []string, textOverride string, via memoVia) (map[string]any, *apiError) {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return nil, &apiError{http.StatusBadRequest, "bad_session", "sessionName is required"}
	}
	if len(ids) == 0 {
		return nil, &apiError{http.StatusBadRequest, "no_ids", "ids is required"}
	}
	token := store.NewID()
	var memos []store.Memo
	err := st.MemoTx(ctx, membershipID, func(tx store.MemoTxOps) error {
		memos = make([]store.Memo, 0, len(ids))
		// Resolve the requested ids to owned memos (foreign/unknown ids are dropped).
		for _, id := range ids {
			m, found, err := tx.GetMemo(id)
			if err != nil {
				return err
			}
			if found {
				memos = append(memos, m)
			}
		}
		if len(memos) == 0 {
			return memoTxFail(&apiError{http.StatusNotFound, "no_memos", "no owned memos for the given ids"})
		}
		// A memo taken in from the inbox goes on its own, with its origin (ADR 0111 decision 3):
		// concatenated with the member's own memos, a colleague's text would read as theirs. The
		// rule holds for an edited text too, which is why it is decided here and not by the
		// Console that composed it.
		for _, m := range memos {
			if m.OriginItemID != "" && len(memos) > 1 {
				return memoTxFail(&apiError{http.StatusConflict, errCodeMemoOriginAlone, "a memo taken in from the inbox is sent on its own"})
			}
		}
		for _, m := range memos {
			if m.SendToken != "" {
				return memoTxFail(&apiError{http.StatusConflict, errCodeMemoSending, "this memo is being sent"})
			}
			// A claimed message belongs to its delivery: nobody sends it by hand.
			if m.State == store.MemoStateClaimed {
				return memoTxFail(&apiError{http.StatusConflict, errCodeMemoLocked, "this message has been taken by a delivery"})
			}
			// A message in a queue is sent from the Console only (ADR 0137 section 3): a send
			// from inside the workspace or /mcp is the unattended path the release guards.
			if via.fromWorkspace() {
				inQ, err := memoInQueue(tx, m.Repo, m.Category)
				if err != nil {
					return err
				}
				if inQ || m.State != "" {
					return memoTxFail(&apiError{http.StatusConflict, errCodeMemoInQueue, "a message in a queue is sent from the Console only"})
				}
			}
		}
		reserved := map[string]bool{}
		for _, m := range memos {
			if reserved[m.ID] {
				continue
			}
			reserved[m.ID] = true
			m.SendToken, m.SendStartedAt = token, store.NowTS()
			if err := tx.WriteMemo(m); err != nil {
				return err
			}
		}
		return nil
	})
	if aerr := memoTxResult(err); aerr != nil {
		return nil, aerr
	}
	// Group by category (stable within-category order preserved from the request).
	sort.SliceStable(memos, func(i, j int) bool { return memos[i].Category < memos[j].Category })

	// Use the caller's edited text when provided; otherwise compose from the memos.
	message := strings.TrimSpace(textOverride)
	if message == "" {
		message = buildFlushMessage(memos)
	}
	if o := memos[0]; o.OriginItemID != "" {
		message = memoOriginLine(o) + "\n\n" + message
	}
	sendErr := sendMemoPrompt(ctx, rt, sessionName, message)
	sentAt := store.NowTS()
	if err := memoSettleSend(context.WithoutCancel(ctx), st, membershipID, token, memos, sendErr, sentAt); err != nil {
		// The outcome is known but not recorded. The rows keep the reservation until
		// MemoSendStaleAfter takes it back: a queue message then becomes unconfirmed and is
		// never fired again by itself. After a successful send, answering 500 would make the
		// client send the same text twice, so the send's own outcome is reported.
		log.Printf("memo flush: settling the send failed session=%s sent=%v: %v", sessionName, sendErr == nil, err)
	}
	if sendErr != nil {
		return nil, sendErr
	}
	sentIDs := make([]string, len(memos))
	for i, m := range memos {
		sentIDs[i] = m.ID
	}
	return map[string]any{"sent": len(sentIDs), "sentAt": sentAt, "ids": sentIDs}, nil
}

// memoSameSentContent says whether a row still holds the text a flush composed from sent: the
// kind, body, ref path and attachments. Repo and category are where it is filed, not what was
// sent.
func memoSameSentContent(cur, sent store.Memo) bool {
	return cur.Kind == sent.Kind && cur.Body == sent.Body && cur.RefPath == sent.RefPath && cur.Attachments == sent.Attachments
}

// memoSendUnknown is a send whose outcome is unknown: the Agent was not reached or did not
// answer with one of its own errors. An Agent error (question_pending, not_running, ...) is a
// refusal, and nothing was sent.
func memoSendUnknown(aerr *apiError) bool { return aerr != nil && aerr.code == "flush_failed" }

// memoSettleSend records a send's outcome on the rows that still carry its reservation. Sent:
// sent_at, and a queue message delivered. Refused: as before the flush. Unknown: a held or
// pending message becomes unconfirmed, because firing it again could do its work twice; a
// plain note stays unsent, as a failed flush always left it.
func memoSettleSend(ctx context.Context, st store.MemoStore, membershipID, token string, memos []store.Memo, sendErr *apiError, sentAt string) error {
	return st.MemoTx(ctx, membershipID, func(tx store.MemoTxOps) error {
		done := map[string]bool{}
		for _, m := range memos {
			if done[m.ID] {
				continue
			}
			done[m.ID] = true
			cur, ok, err := tx.GetMemo(m.ID)
			if err != nil {
				return err
			}
			if !ok || cur.SendToken != token {
				continue
			}
			next := cur
			next.SendToken, next.SendStartedAt = "", ""
			switch {
			case sendErr == nil && cur.State != "" && !memoSameSentContent(cur, m):
				// A queue message whose text is not what was sent is not delivered. The
				// reservation keeps this from happening; this keeps it from ever counting.
			case sendErr == nil:
				next.SentAt = sentAt
				if next.State != "" {
					next.State, next.ReleaseHash, next.ReleasedAt = store.MemoStateDelivered, "", ""
				}
			case memoSendUnknown(sendErr) && (cur.State == store.MemoStateHeld || cur.State == store.MemoStatePending):
				next.State, next.ReleaseHash, next.ReleasedAt = store.MemoStateUnconfirmed, "", ""
			}
			if err := tx.WriteMemo(next); err != nil {
				return err
			}
		}
		return nil
	})
}

// sendMemoPrompt uses the same semantic turn endpoint as Console chat. /input is
// deliberately TUI-only now; managed sessions have no tmux process, so sending a
// memo there incorrectly reports not_running. Old workspace images can briefly lag
// the Control Plane during a rollout, hence the narrow fallback when /turn itself is
// absent (a plain 404/405 rather than a structured Agent error).
func sendMemoPrompt(ctx context.Context, rt runtime.Runtime, sessionName, prompt string) *apiError {
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
