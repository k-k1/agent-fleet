package main

// Message queues, step 1a (ADR 0137 sections 1, 3 and 10, issue #4156): the queue settings,
// a message's state and provenance, the release bound to a content hash, and what the token
// face and /mcp may not do to a queue. Nothing here starts a session.
//
// Which face a request arrived by is fixed by the route it was registered on (routes.go,
// mcp_wiring.go) and passed into the shared memo cores as a memoVia. No request field can set
// a state, a provenance, a release hash or a release time.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/control-plane/internal/agentkinds"
	"github.com/k-k1/agent-fleet/control-plane/internal/agentroutes"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Error codes of the queue rules. They stay out of errcodes.go until the Console slice adds
// their err.<code> catalog entries: TestCPEmittedErrCodesHaveConsoleCatalogEntry requires one
// for every code declared there.
const (
	errCodeMemoLocked             = "memo_locked"
	errCodeMemoInQueue            = "memo_in_queue"
	errCodeMemoQueueCategory      = "memo_queue_category"
	errCodeMemoInboxInQueue       = "memo_inbox_in_queue"
	errCodeMemoReleaseStale       = "memo_release_stale"
	errCodeMemoTooLarge           = "memo_too_large"
	errCodeMemoEnvelope           = "memo_envelope"
	errCodeMemoRateLimited        = "memo_rate_limited"
	errCodeMemoQueueNoInstruction = "memo_queue_no_instruction"
	errCodeMemoQueueBadSettings   = "memo_queue_bad_settings"
)

// Bounds on a create into a queue from the token face or /mcp (ADR 0137 section 3). The
// values are provisional (ADR 0137 open question 1).
const (
	memoQueueBodyMax       = 16 << 10
	memoQueueRatePerMinute = 30
	memoEnvelopePrefix     = "[agent-fleet:"
	memoProducerMax        = 200
)

// Settings bounds (ADR 0137 section 6).
const (
	memoQueueMaxActiveDefault = 2
	memoQueueMaxActiveMax     = 6
	memoQueueBatchDefault     = 1
	memoQueueBatchMax         = 20
	memoQueueInstructionMax   = 16 << 10
)

// memoFace is the route a memo request arrived by.
type memoFace int

const (
	// memoFaceConsole is /api/memos*, with the member's Console session.
	memoFaceConsole memoFace = iota
	// memoFaceToken is /internal/memos*, with AF_MEMO_TOKEN from inside the workspace.
	memoFaceToken
	// memoFaceMCP is the CP's /mcp memo tools, with a PAT or an OAuth grant.
	memoFaceMCP
)

// memoVia is the face plus the attribution it carries: on the token face the session name and
// origin the Agent relay supplies, on /mcp the connection id. Attribution only: no rule reads it.
type memoVia struct {
	face                     memoFace
	producer, producerOrigin string
}

var memoViaConsole = memoVia{face: memoFaceConsole}

func (v memoVia) provenance() string {
	switch v.face {
	case memoFaceConsole:
		return store.MemoProvenanceMember
	case memoFaceMCP:
		return store.MemoProvenanceMCP
	default:
		return store.MemoProvenanceWorkspace
	}
}

// fromWorkspace is a face that the member's Console session does not stand behind.
func (v memoVia) fromWorkspace() bool { return v.face != memoFaceConsole }

// memoContentHash is what a release is bound to: the body, the kind, the ref path, the
// attachments, the repo and the category. The category is its name, so a rename changes the
// hash and a pending message's release is re-recorded in the same transaction.
func memoContentHash(m store.Memo) string {
	b, _ := json.Marshal([]string{"memo-content/v1", m.Kind, m.Body, m.RefPath, m.Attachments, m.Repo, m.Category})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// memoEffectiveProvenance reads an empty provenance as workspace, except on a row with an
// inbox origin: a binary that predates the column takes memos in without writing it.
func memoEffectiveProvenance(m store.Memo) string {
	if m.Provenance == "" && m.OriginItemID != "" {
		return store.MemoProvenanceInbox
	}
	if m.Provenance == "" {
		return store.MemoProvenanceWorkspace
	}
	return m.Provenance
}

func memoIsInbox(m store.Memo) bool {
	return m.OriginItemID != "" || m.Provenance == store.MemoProvenanceInbox
}

// memoStateLocked is a state from claimed on: such a message cannot be changed.
func memoStateLocked(s string) bool {
	switch s {
	case store.MemoStateClaimed, store.MemoStateDelivered, store.MemoStateUnconfirmed, store.MemoStateSuperseded:
		return true
	}
	return false
}

func memoNow() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// memoEnter puts a message that enters a queue into its first state: pending only for the
// member's own note arriving by the Console, held for everything else (ADR 0137 section 3).
// A memo the member already sent by hand enters as delivered: pending would fire it again.
func memoEnter(m store.Memo, via memoVia) store.Memo {
	if m.SentAt != "" {
		m.State, m.ReleaseHash, m.ReleasedAt = store.MemoStateDelivered, "", ""
		return m
	}
	if via.face == memoFaceConsole && memoEffectiveProvenance(m) == store.MemoProvenanceMember {
		m.State, m.ReleaseHash, m.ReleasedAt = store.MemoStatePending, memoContentHash(m), memoNow()
		return m
	}
	m.State, m.ReleaseHash, m.ReleasedAt = store.MemoStateHeld, "", ""
	return m
}

// memoQueueMove applies the queue rules to one row that goes from prev to next, where inPrev
// and inNext say whether each lies in a queue. Every write path that can change a hashed field
// or a row's queue goes through it, so a pending message's release hash is its content hash
// after every write. A plain note that stays out of every queue comes back unchanged.
func memoQueueMove(prev, next store.Memo, inPrev, inNext bool, via memoVia) (store.Memo, *apiError) {
	if inNext && memoIsInbox(next) {
		return next, &apiError{http.StatusConflict, errCodeMemoInboxInQueue, "a memo taken in from the inbox cannot be in a queue: it is sent alone, by you"}
	}
	moved := inPrev != inNext || memoContentHash(prev) != memoContentHash(next)
	if prev.State == store.MemoStateClaimed && moved {
		return next, &apiError{http.StatusConflict, errCodeMemoLocked, "this message has been taken by a delivery and cannot be changed"}
	}
	if !inNext {
		next.State, next.ReleaseHash, next.ReleasedAt = "", "", ""
		return next, nil
	}
	if !inPrev || prev.State == "" {
		return memoEnter(next, via), nil
	}
	if memoStateLocked(prev.State) || !moved {
		return next, nil
	}
	if prev.State == store.MemoStatePending {
		if via.fromWorkspace() {
			next.State, next.ReleaseHash, next.ReleasedAt = store.MemoStateHeld, "", ""
		} else {
			next.ReleaseHash, next.ReleasedAt = memoContentHash(next), memoNow()
		}
	}
	return next, nil
}

// memoTxErr carries an apiError out of a store.MemoTx callback, which rolls back on it.
type memoTxErr struct{ *apiError }

func (e memoTxErr) Error() string { return e.message }

func memoTxFail(aerr *apiError) error { return memoTxErr{aerr} }

// memoTxResult turns a MemoTx error back into the apiError to answer with.
func memoTxResult(err error) *apiError {
	if err == nil {
		return nil
	}
	var te memoTxErr
	if errors.As(err, &te) {
		return te.apiError
	}
	return internalErr(err)
}

// inQueue reports whether (repo, category) is a queue.
func memoInQueue(tx store.MemoTxOps, repo, category string) (bool, error) {
	_, ok, err := tx.QueueOf(repo, category)
	return ok, err
}

// memoMoveBucket moves every row of (repo, from) to category to, applying the queue rules to
// each. inPrev and inNext are the queue membership of the bucket before and after. It is the
// rename, the merge, the category delete and the settings create or delete.
func memoMoveBucket(tx store.MemoTxOps, repo, from, to string, inPrev, inNext bool, via memoVia) error {
	rows, err := tx.ListMemosIn(repo, from)
	if err != nil {
		return err
	}
	for _, prev := range rows {
		next := prev
		next.Category = to
		next, aerr := memoQueueMove(prev, next, inPrev, inNext, via)
		if aerr != nil {
			return memoTxFail(aerr)
		}
		if next != prev {
			if err := tx.WriteMemo(next); err != nil {
				return err
			}
		}
	}
	return nil
}

// memoCreateBounds are the checks on a create into a queue from inside the workspace or /mcp.
// It answers the message to return instead when an identical one is already waiting.
func memoCreateBounds(tx store.MemoTxOps, m store.Memo) (*store.Memo, *apiError, error) {
	if len(m.Body) > memoQueueBodyMax {
		return nil, &apiError{http.StatusRequestEntityTooLarge, errCodeMemoTooLarge, fmt.Sprintf("a message in a queue is at most %d bytes", memoQueueBodyMax)}, nil
	}
	if strings.HasPrefix(m.Body, memoEnvelopePrefix) {
		return nil, &apiError{http.StatusBadRequest, errCodeMemoEnvelope, "a message that starts with \"" + memoEnvelopePrefix + "\" is refused"}, nil
	}
	rows, err := tx.ListMemosIn(m.Repo, m.Category)
	if err != nil {
		return nil, nil, err
	}
	h := memoContentHash(m)
	for i := range rows {
		if (rows[i].State == store.MemoStateHeld || rows[i].State == store.MemoStatePending) && memoContentHash(rows[i]) == h {
			return &rows[i], nil, nil
		}
	}
	n, err := tx.CountQueueIntakeSince(time.Now().UTC().Add(-time.Minute).Format(time.RFC3339))
	if err != nil {
		return nil, nil, err
	}
	if n >= memoQueueRatePerMinute {
		return nil, &apiError{http.StatusTooManyRequests, errCodeMemoRateLimited, fmt.Sprintf("at most %d messages a minute go into queues from a session", memoQueueRatePerMinute)}, nil
	}
	return nil, nil, nil
}

// clipProducer bounds the attribution the token face carries.
func clipProducer(s string) string {
	s = strings.TrimSpace(s)
	for len(s) > memoProducerMax {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}

// --- Queue settings: Console only ----------------------------------------------------------

// memoQueueDTO is a queue's settings on the wire. Repo and category name the category row it
// belongs to, for display.
type memoQueueDTO struct {
	CategoryID  string  `json:"categoryId"`
	Repo        string  `json:"repo"`
	Category    string  `json:"category"`
	Instruction string  `json:"instruction"`
	AgentKind   string  `json:"agentKind"`
	Model       string  `json:"model"`
	Effort      string  `json:"effort"`
	Route       string  `json:"route"`
	SpendCapUSD float64 `json:"spendCapUsd"`
	Worktree    bool    `json:"worktree"`
	MaxActive   int     `json:"maxActive"`
	Batch       int     `json:"batch"`
	Enabled     bool    `json:"enabled"`
	Generation  int     `json:"generation"`
	CreatedAt   string  `json:"createdAt"`
	UpdatedAt   string  `json:"updatedAt"`
}

func memoQueueToDTO(q store.MemoQueue, c store.MemoCategory) memoQueueDTO {
	return memoQueueDTO{CategoryID: q.CategoryID, Repo: c.Repo, Category: c.Name, Instruction: q.Instruction,
		AgentKind: q.AgentKind, Model: q.Model, Effort: q.Effort, Route: q.Route, SpendCapUSD: q.SpendCapUSD,
		Worktree: q.Worktree, MaxActive: q.MaxActive, Batch: q.Batch, Enabled: q.Enabled, Generation: q.Generation,
		CreatedAt: q.CreatedAt, UpdatedAt: q.UpdatedAt}
}

// memoQueueIn is a PUT body. Omitted numbers and worktree take their defaults: a PUT replaces
// the whole settings row.
type memoQueueIn struct {
	Instruction string  `json:"instruction"`
	AgentKind   string  `json:"agentKind"`
	Model       string  `json:"model"`
	Effort      string  `json:"effort"`
	Route       string  `json:"route"`
	SpendCapUSD float64 `json:"spendCapUsd"`
	Worktree    *bool   `json:"worktree"`
	MaxActive   *int    `json:"maxActive"`
	Batch       *int    `json:"batch"`
	Enabled     bool    `json:"enabled"`
}

func badQueueSettings(msg string) *apiError {
	return &apiError{http.StatusBadRequest, errCodeMemoQueueBadSettings, msg}
}

// validate normalizes a PUT body into the settings row (category, membership and timestamps
// unset).
func (in memoQueueIn) validate() (store.MemoQueue, *apiError) {
	q := store.MemoQueue{
		Instruction: strings.TrimSpace(in.Instruction),
		AgentKind:   strings.TrimSpace(in.AgentKind),
		Model:       strings.TrimSpace(in.Model),
		Effort:      strings.TrimSpace(in.Effort),
		Route:       strings.TrimSpace(in.Route),
		SpendCapUSD: in.SpendCapUSD,
		Worktree:    true,
		MaxActive:   memoQueueMaxActiveDefault,
		Batch:       memoQueueBatchDefault,
		Enabled:     in.Enabled,
	}
	if q.AgentKind == "" {
		q.AgentKind = "claude"
	}
	if q.AgentKind == "shell" || q.AgentKind == "ssm" {
		return q, badQueueSettings("a queue's consumer must be an agent: " + q.AgentKind + " is refused")
	}
	if err := agentkinds.CheckCreateKind(q.AgentKind); err != nil {
		return q, badQueueSettings(err.Error())
	}
	if q.Model != "" && !launchModelRe.MatchString(q.Model) {
		return q, badQueueSettings("model is not a model id")
	}
	if !validRecipeToken(q.Effort) {
		return q, badQueueSettings("effort is not an effort level")
	}
	if q.Route != "" {
		if _, err := agentroutes.CheckCreateRoute(q.AgentKind, &agentroutes.Selection{Route: q.Route}); err != nil {
			return q, badQueueSettings(err.Error())
		}
	}
	if q.SpendCapUSD < 0 || q.SpendCapUSD > maxLaunchBudgetUS || q.SpendCapUSD != q.SpendCapUSD {
		return q, badQueueSettings(fmt.Sprintf("spendCapUsd must be between 0 and %d (0 = no cap)", maxLaunchBudgetUS))
	}
	if in.Worktree != nil {
		q.Worktree = *in.Worktree
	}
	if in.MaxActive != nil {
		q.MaxActive = *in.MaxActive
	}
	if q.MaxActive < 1 || q.MaxActive > memoQueueMaxActiveMax {
		return q, badQueueSettings(fmt.Sprintf("maxActive must be between 1 and %d", memoQueueMaxActiveMax))
	}
	if in.Batch != nil {
		q.Batch = *in.Batch
	}
	if q.Batch < 1 || q.Batch > memoQueueBatchMax {
		return q, badQueueSettings(fmt.Sprintf("batch must be between 1 and %d", memoQueueBatchMax))
	}
	if len(q.Instruction) > memoQueueInstructionMax {
		return q, badQueueSettings(fmt.Sprintf("instruction is at most %d bytes", memoQueueInstructionMax))
	}
	if q.Enabled && q.Instruction == "" {
		return q, &apiError{http.StatusBadRequest, errCodeMemoQueueNoInstruction, "a queue without an instruction cannot be enabled"}
	}
	return q, nil
}

func (a memoAPI) listQueues(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	qs, err := a.store.ListMemoQueues(r.Context(), mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	cats, err := a.store.ListCategories(r.Context(), mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	byID := map[string]store.MemoCategory{}
	for _, c := range cats {
		byID[c.ID] = c
	}
	out := make([]memoQueueDTO, 0, len(qs))
	for _, q := range qs {
		if c, ok := byID[q.CategoryID]; ok {
			out = append(out, memoQueueToDTO(q, c))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// putQueue creates or replaces a category's queue settings. A category that becomes a queue
// takes its memos in under the rules of a Console write: the member's own become pending,
// everything else held. A category holding a memo taken in from the inbox cannot become one.
func (a memoAPI) putQueue(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	var in memoQueueIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
		return
	}
	q, aerr := in.validate()
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	var out memoQueueDTO
	err := a.store.MemoTx(r.Context(), mv.MembershipID, func(tx store.MemoTxOps) error {
		c, ok, err := tx.GetCategory(r.PathValue("categoryId"))
		if err != nil {
			return err
		}
		if !ok {
			return memoTxFail(&apiError{http.StatusNotFound, "not_found", "category not found"})
		}
		cur, existed, err := tx.GetQueue(c.ID)
		if err != nil {
			return err
		}
		now := store.NowTS()
		q.CategoryID, q.MembershipID, q.UpdatedAt = c.ID, mv.MembershipID, now
		if existed {
			q.CreatedAt, q.Generation = cur.CreatedAt, cur.Generation
		} else {
			q.CreatedAt = now
			if err := memoMoveBucket(tx, c.Repo, c.Name, c.Name, false, true, memoViaConsole); err != nil {
				return err
			}
		}
		if err := tx.PutQueue(q); err != nil {
			return err
		}
		out = memoQueueToDTO(q, c)
		return nil
	})
	if aerr := memoTxResult(err); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// deleteQueue removes a category's settings: its messages leave the queue and are plain notes
// again. Deleting settings that do not exist is a no-op.
func (a memoAPI) deleteQueue(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	err := a.store.MemoTx(r.Context(), mv.MembershipID, func(tx store.MemoTxOps) error {
		c, ok, err := tx.GetCategory(r.PathValue("categoryId"))
		if err != nil {
			return err
		}
		if !ok {
			return memoTxFail(&apiError{http.StatusNotFound, "not_found", "category not found"})
		}
		if _, isQ, err := tx.GetQueue(c.ID); err != nil || !isQ {
			return err
		}
		if err := memoMoveBucket(tx, c.Repo, c.Name, c.Name, true, false, memoViaConsole); err != nil {
			return err
		}
		return tx.DeleteQueue(c.ID)
	})
	if aerr := memoTxResult(err); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Release: Console only -----------------------------------------------------------------

// memoReleaseIn names the messages a release card showed, each with the content hash it showed.
type memoReleaseIn struct {
	Items []memoReleaseItem `json:"items"`
}

type memoReleaseItem struct {
	ID          string `json:"id"`
	ContentHash string `json:"contentHash"`
}

// memoReleaseOut is a release's answer: the messages now pending, or, with 409, the ones whose
// content no longer matches what the card showed. Nothing is released when any is stale.
type memoReleaseOut struct {
	Released []memoDTO `json:"released"`
	Stale    []string  `json:"stale"`
}

// release moves held messages to pending, all or none, each only if its current content hash
// is the one the member saw. Each release is a conditional write on the message's own row.
func (a memoAPI) release(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	var in memoReleaseIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
		return
	}
	if len(in.Items) == 0 {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "no_ids", "items is required"})
		return
	}
	out := memoReleaseOut{Released: []memoDTO{}, Stale: []string{}}
	err := a.store.MemoTx(r.Context(), mv.MembershipID, func(tx store.MemoTxOps) error {
		at := memoNow()
		for _, it := range in.Items {
			m, ok, err := tx.GetMemo(it.ID)
			if err != nil {
				return err
			}
			if !ok {
				return memoTxFail(&apiError{http.StatusNotFound, "not_found", "memo not found"})
			}
			h := memoContentHash(m)
			if m.State == store.MemoStatePending && m.ReleaseHash == h && h == it.ContentHash {
				out.Released = append(out.Released, memoToDTO(m))
				continue
			}
			if m.State != store.MemoStateHeld && m.State != store.MemoStatePending {
				return memoTxFail(&apiError{http.StatusConflict, errCodeMemoLocked, "only a held message can be released"})
			}
			inQ, err := memoInQueue(tx, m.Repo, m.Category)
			if err != nil {
				return err
			}
			if !inQ {
				return memoTxFail(&apiError{http.StatusConflict, errCodeMemoInQueue, "this memo is not in a queue"})
			}
			if h != it.ContentHash {
				out.Stale = append(out.Stale, m.ID)
				continue
			}
			won, err := tx.ReleaseMemo(m, h, at)
			if err != nil {
				return err
			}
			if !won {
				out.Stale = append(out.Stale, m.ID)
				continue
			}
			m.State, m.ReleaseHash, m.ReleasedAt = store.MemoStatePending, h, at
			out.Released = append(out.Released, memoToDTO(m))
		}
		if len(out.Stale) > 0 {
			return memoTxFail(&apiError{http.StatusConflict, errCodeMemoReleaseStale, "a message changed since it was shown"})
		}
		return nil
	})
	if aerr := memoTxResult(err); aerr != nil {
		if aerr.code == errCodeMemoReleaseStale {
			writeJSON(w, http.StatusConflict, memoReleaseConflict{Error: memoReleaseConflictErr{Code: aerr.code, Message: aerr.message, Stale: out.Stale}})
			return
		}
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// memoReleaseConflict is the 409 of a stale release: the usual error envelope plus the ids
// whose content changed, so the card can be refreshed.
type memoReleaseConflict struct {
	Error memoReleaseConflictErr `json:"error"`
}

type memoReleaseConflictErr struct {
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Stale   []string `json:"stale"`
}
