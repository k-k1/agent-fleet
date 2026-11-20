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
	"github.com/k-k1/agent-fleet/control-plane/internal/memoafter"
	"github.com/k-k1/agent-fleet/control-plane/internal/memoref"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
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
	// A delivery open longer than max_task_hours is overdue (ADR 0137 section 6).
	memoQueueMaxTaskHoursDefault = 12
	memoQueueMaxTaskHoursMax     = 72
	// Fires by themselves: the daily bound, the longest window and the largest threshold.
	memoQueueMaxFiresDefault = 20
	memoQueueMaxFiresMax     = 200
	memoQueueWindowMax       = 24 * 60
	memoQueueOpenAfterMax    = 100
	memoQueueInstructionMax  = 16 << 10
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
	// continuation and queueDelivery are the claim of a create that follows the message a queue
	// run was given (memo_continuation.go): the model's boolean, and the delivery id the Agent
	// stamped from the session's meta. Read on the token face only, and only as a claim: the
	// Control Plane decides from its own rows.
	continuation  bool
	queueDelivery string
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
// attachments, the reference, the prerequisites, the repo and the category. The category is its name, so a rename changes the
// hash and a pending message's release is re-recorded in the same transaction.
func memoContentHash(m store.Memo) string {
	parts := append([]string{"memo-content/v1", m.Kind, m.Body, m.RefPath, m.Attachments, m.Repo, m.Category}, memoRefHashParts(m)...)
	b, _ := json.Marshal(append(parts, memoafter.HashParts(m)...))
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

// memoAcceptedBySetting is the one rule that skips the release press for a message from inside
// the workspace: the queue's switch is on, the message came by the token face, and the Agent
// named the session's origin as user. The token face is required because /mcp carries no
// session, and the recorded provenance and origin because an update carries no caller identity.
// Anything else, an empty or unknown origin included, answers false.
func memoAcceptedBySetting(m store.Memo, via memoVia, accept bool) bool {
	return accept && via.face == memoFaceToken &&
		memoEffectiveProvenance(m) == store.MemoProvenanceWorkspace && m.ProducerOrigin == store.MemoOriginUser
}

// memoEnter puts a message that enters a queue into its first state: pending for the member's
// own note arriving by the Console and, with the queue's switch on, for a message from a session
// the member started; held for everything else (ADR 0137 section 3). accept is the switch of the
// queue it enters. A memo the member already sent by hand enters as delivered: pending would
// fire it again.
func memoEnter(m store.Memo, via memoVia, accept bool) store.Memo {
	m.ReleasedBy = ""
	m.ChainID, m.ChainSeq, m.ChainDelivery = "", 0, ""
	if m.SentAt != "" {
		m.State, m.ReleaseHash, m.ReleasedAt = store.MemoStateDelivered, "", ""
		return m
	}
	if via.face == memoFaceConsole && memoEffectiveProvenance(m) == store.MemoProvenanceMember {
		m.State, m.ReleaseHash, m.ReleasedAt = store.MemoStatePending, memoContentHash(m), memoNow()
		return m
	}
	if memoAcceptedBySetting(m, via, accept) {
		m.State, m.ReleaseHash, m.ReleasedAt, m.ReleasedBy = store.MemoStatePending, memoContentHash(m), memoNow(), store.MemoReleasedBySetting
		return m
	}
	m.State, m.ReleaseHash, m.ReleasedAt = store.MemoStateHeld, "", ""
	return m
}

// memoQueueMove applies the queue rules to one row that goes from prev to next, where inPrev
// and inNext say whether each lies in a queue. Every write path that can change a hashed field
// or a row's queue goes through it, so a pending message's release hash is its content hash
// after every write. A plain note that stays out of every queue comes back unchanged. accept is
// the switch of the queue next is in (false when it is in none).
func memoQueueMove(prev, next store.Memo, inPrev, inNext, accept bool, via memoVia) (store.Memo, *apiError) {
	if inNext && memoIsInbox(next) {
		return next, &apiError{http.StatusConflict, errCodeMemoInboxInQueue, "a memo taken in from the inbox cannot be in a queue: it is sent alone, by you"}
	}
	moved := inPrev != inNext || memoContentHash(prev) != memoContentHash(next)
	if prev.State == store.MemoStateClaimed && moved {
		return next, &apiError{http.StatusConflict, errCodeMemoLocked, "this message has been taken by a delivery and cannot be changed"}
	}
	// A row in a flush in flight keeps what is being sent until the flush settles when it is,
	// or would become, a queue message: otherwise the settle would mark delivered a content
	// that entered the queue after the send began.
	if prev.SendToken != "" && (prev.State != "" || inNext) && moved {
		return next, &apiError{http.StatusConflict, errCodeMemoSending, "this message is being sent"}
	}
	if !inNext {
		// A plain note waits for nothing: a list left behind would block its every later move,
		// and come back if the category became a queue again.
		next.State, next.ReleaseHash, next.ReleasedAt, next.ReleasedBy = "", "", "", ""
		next.AfterIDs, next.HeldReason = "", ""
		return next, nil
	}
	// A superseded message was replaced by a tidy run and comes back only by undoing it: whatever
	// else moves it, it never enters a queue again as held or pending.
	if prev.State == store.MemoStateSuperseded {
		return next, nil
	}
	if !inPrev || prev.State == "" {
		return memoEnter(next, via, accept), nil
	}
	if memoStateLocked(prev.State) || !moved {
		return next, nil
	}
	if prev.State == store.MemoStatePending {
		switch {
		case memoAcceptedBySetting(next, via, accept):
			// The switch already accepts that such a session could have added this text as a
			// new message, so a change of it keeps the state and records the release again.
			next.ReleaseHash, next.ReleasedAt, next.ReleasedBy = memoContentHash(next), memoNow(), store.MemoReleasedBySetting
		case via.fromWorkspace():
			next.State, next.ReleaseHash, next.ReleasedAt, next.ReleasedBy = store.MemoStateHeld, "", "", ""
		default:
			// The member's own edit keeps what admitted the message: a continuation stays one, so an
			// edit or a rename of its queue does not make it the root of a fresh chain.
			by := ""
			if prev.ReleasedBy == store.MemoReleasedByContinuation {
				by = prev.ReleasedBy
			}
			next.ReleaseHash, next.ReleasedAt, next.ReleasedBy = memoContentHash(next), memoNow(), by
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

// memoQueueSwitch is memoInQueue plus the queue's accept-from-own-sessions switch.
func memoQueueSwitch(tx store.MemoTxOps, repo, category string) (inQ, accept bool, err error) {
	q, ok, err := tx.QueueOf(repo, category)
	return ok, ok && q.AcceptOwnSessions, err
}

// memoMoveBucket moves every row of (repo, from) to category to, applying the queue rules to
// each. inPrev and inNext are the queue membership of the bucket before and after. It is the
// rename, the merge, the category delete and the settings create or delete. Those run through the
// Console or touch no queue from the token face, so accept is always false here: moving a bucket
// never makes a message pending by the switch.
func memoMoveBucket(tx store.MemoTxOps, repo, from, to string, inPrev, inNext, sameQueue bool, via memoVia) error {
	rows, err := tx.ListMemosIn(repo, from)
	if err != nil {
		return err
	}
	// dest is what the destination holds: for a merge the survivor's own rows, for a bucket that
	// becomes a queue (from == to) it starts empty and fills with the rows as they enter.
	var dest []store.Memo
	if inNext && to != from {
		if dest, err = tx.ListMemosIn(repo, to); err != nil {
			return err
		}
	}
	for _, prev := range rows {
		next := prev
		next.Category = to
		next, aerr := memoQueueMove(prev, next, inPrev, inNext, false, via)
		if aerr != nil {
			return memoTxFail(aerr)
		}
		if !via.fromWorkspace() && inPrev && inNext && prev.State == store.MemoStatePending && next.State == store.MemoStatePending {
			// A rename or a merge changes the release copy and hash and nothing that says who approved:
			// a switch-accepted, tidy-made or continuation message must not become the member's own.
			// The destination's own rules still decide what it may start (memoContinue).
			next.ReleasedBy = prev.ReleasedBy
		}
		if !sameQueue && prev.ChainID != "" {
			// A merge into another category ends the chain: the destination is another queue.
			next.ChainID, next.ChainSeq, next.ChainDelivery = "", 0, ""
		}
		// The rows that stay or become open in a queue hold each reference once. A bucket that
		// cannot be made so is refused whole: which of two notes to drop is the member's choice.
		if inNext && memoOpenState(next.State) {
			if dup, ok := memoOpenRefIn(dest, next); ok {
				return memoTxFail(memoRefDuplicate(dup))
			}
			dest = append(dest, next)
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
	// AcceptOwnSessions is the switch "accept from sessions I started": a message from a session
	// the member started enters without a release. Off by default.
	AcceptOwnSessions bool `json:"acceptOwnSessions"`
	// ContinuationMax is how many continuations of one released message a run may add without a
	// press (0 turns it off, at most memoContinuationCeiling). The default for every queue is 4.
	ContinuationMax int `json:"continuationMax"`
	MaxActive       int `json:"maxActive"`
	Batch           int `json:"batch"`
	// MaxTaskHours is how long one of its deliveries may stay open before it is overdue.
	MaxTaskHours int `json:"maxTaskHours"`
	// When the queue fires by itself (ADR 0137 section 6): OpenCron, OpenMinutes and TZ are a
	// window, OpenAfter a threshold, none of them always. MaxFiresPerDay bounds 24 hours.
	OpenCron       string `json:"openCron"`
	OpenMinutes    int    `json:"openMinutes"`
	TZ             string `json:"tz"`
	OpenAfter      int    `json:"openAfter"`
	MaxFiresPerDay int    `json:"maxFiresPerDay"`
	// State, set by the Control Plane: Paused is the circuit breaker (PausedAt, PausedReason),
	// Draining a threshold queue that is emptying, and NoReportAt is set while its fires by
	// themselves are held back because the workspace's Agent does not send the queue report.
	Paused       bool   `json:"paused"`
	PausedAt     string `json:"pausedAt"`
	PausedReason string `json:"pausedReason"`
	Draining     bool   `json:"draining"`
	NoReportAt   string `json:"noReportAt"`
	// NoRefsAt is set while the Agent cannot show a reference: the messages that carry one stay
	// where they are, and the rest of the queue fires as usual.
	NoRefsAt string `json:"noRefsAt"`
	// Tidy turns the tidy run before a fire on (ADR 0137 section 8), TidyDestinations are the
	// queues a run may move a message to as pending, and NoTidyAt is set while the workspace's
	// Agent cannot tidy. A guard rail, not a wall: see memo_tidy.go.
	Tidy             bool     `json:"tidy"`
	TidyDestinations []string `json:"tidyDestinations"`
	NoTidyAt         string   `json:"noTidyAt"`
	Enabled          bool     `json:"enabled"`
	Generation       int      `json:"generation"`
	CreatedAt        string   `json:"createdAt"`
	UpdatedAt        string   `json:"updatedAt"`
	// Waiting is set by the list only: why a queue with notes waiting starts nothing now (see
	// memoQueueWaiting). Absent when nothing waits or a run can start.
	Waiting *memoWaiting `json:"waiting,omitempty"`
}

func memoQueueToDTO(q store.MemoQueue, c store.MemoCategory) memoQueueDTO {
	return memoQueueDTO{CategoryID: q.CategoryID, Repo: c.Repo, Category: c.Name, Instruction: q.Instruction,
		AgentKind: q.AgentKind, Model: q.Model, Effort: q.Effort, Route: q.Route, SpendCapUSD: q.SpendCapUSD,
		Worktree: q.Worktree, AcceptOwnSessions: q.AcceptOwnSessions, ContinuationMax: memoContinuationMax(q), MaxActive: q.MaxActive, Batch: q.Batch, MaxTaskHours: memoQueueTaskHours(q),
		OpenCron: q.OpenCron, OpenMinutes: q.OpenMinutes, TZ: q.TZ, OpenAfter: q.OpenAfter, MaxFiresPerDay: memoQueueFiresPerDay(q),
		Paused: q.Paused, PausedAt: q.PausedAt, PausedReason: q.PausedReason, Draining: q.Draining, NoReportAt: q.NoReportAt, NoRefsAt: q.NoRefsAt,
		Tidy: q.Tidy, TidyDestinations: nonNilIDs(q.TidyDestinations), NoTidyAt: q.NoTidyAt,
		Enabled: q.Enabled, Generation: q.Generation, CreatedAt: q.CreatedAt, UpdatedAt: q.UpdatedAt}
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
	// AcceptOwnSessions omitted means off.
	AcceptOwnSessions bool `json:"acceptOwnSessions"`
	// ContinuationMax is omitted for the default of 4 (a PUT replaces the row), 0 turns it off.
	ContinuationMax *int `json:"continuationMax"`
	MaxActive       *int `json:"maxActive"`
	Batch           *int `json:"batch"`
	// MaxTaskHours is omitted for the default, like the numbers above.
	MaxTaskHours   *int   `json:"maxTaskHours"`
	OpenCron       string `json:"openCron"`
	OpenMinutes    int    `json:"openMinutes"`
	TZ             string `json:"tz"`
	OpenAfter      int    `json:"openAfter"`
	MaxFiresPerDay *int   `json:"maxFiresPerDay"`
	Enabled        bool   `json:"enabled"`
	// Tidy omitted means off, like AcceptOwnSessions. TidyDestinations are category ids.
	Tidy             bool     `json:"tidy"`
	TidyDestinations []string `json:"tidyDestinations"`
}

func nonNilIDs(ids []string) []string {
	if ids == nil {
		return []string{}
	}
	return ids
}

// memoQueueFiresPerDay is the queue's daily bound, the default for a row that never had one.
func memoQueueFiresPerDay(q store.MemoQueue) int {
	if q.MaxFiresPerDay < 1 {
		return memoQueueMaxFiresDefault
	}
	return q.MaxFiresPerDay
}

// memoQueueTaskHours is the queue's overdue bound, the default for a row that never had one.
func memoQueueTaskHours(q store.MemoQueue) int {
	if q.MaxTaskHours < 1 {
		return memoQueueMaxTaskHoursDefault
	}
	return q.MaxTaskHours
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

		AcceptOwnSessions: in.AcceptOwnSessions,
		ContinuationMax:   memoContinuationDefault,
		Tidy:              in.Tidy,
		MaxTaskHours:      memoQueueMaxTaskHoursDefault,
		MaxFiresPerDay:    memoQueueMaxFiresDefault,
		OpenCron:          strings.TrimSpace(in.OpenCron),
		OpenMinutes:       in.OpenMinutes,
		TZ:                strings.TrimSpace(in.TZ),
		OpenAfter:         in.OpenAfter,
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
	if in.ContinuationMax != nil {
		q.ContinuationMax = *in.ContinuationMax
	}
	if q.ContinuationMax < 0 || q.ContinuationMax > memoContinuationCeiling {
		return q, badQueueSettings(fmt.Sprintf("continuationMax must be between 0 and %d", memoContinuationCeiling))
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
	if in.MaxTaskHours != nil {
		q.MaxTaskHours = *in.MaxTaskHours
	}
	if q.MaxTaskHours < 1 || q.MaxTaskHours > memoQueueMaxTaskHoursMax {
		return q, badQueueSettings(fmt.Sprintf("maxTaskHours must be between 1 and %d", memoQueueMaxTaskHoursMax))
	}
	if in.MaxFiresPerDay != nil {
		q.MaxFiresPerDay = *in.MaxFiresPerDay
	}
	if q.MaxFiresPerDay < 1 || q.MaxFiresPerDay > memoQueueMaxFiresMax {
		return q, badQueueSettings(fmt.Sprintf("maxFiresPerDay must be between 1 and %d", memoQueueMaxFiresMax))
	}
	if q.OpenAfter < 0 || q.OpenAfter > memoQueueOpenAfterMax {
		return q, badQueueSettings(fmt.Sprintf("openAfter must be between 0 (off) and %d", memoQueueOpenAfterMax))
	}
	if q.OpenCron != "" {
		if q.OpenAfter != 0 {
			return q, badQueueSettings("a queue opens in a window or after a number of messages, not both")
		}
		if _, err := parseCron(q.OpenCron); err != nil {
			return q, badQueueSettings("openCron: " + err.Error())
		}
		if q.OpenMinutes < 1 || q.OpenMinutes > memoQueueWindowMax {
			return q, badQueueSettings(fmt.Sprintf("openMinutes must be between 1 and %d", memoQueueWindowMax))
		}
		if q.TZ != "" {
			if _, err := time.LoadLocation(q.TZ); err != nil {
				return q, badQueueSettings("tz is not an IANA time zone")
			}
		}
	} else {
		q.OpenMinutes, q.TZ = 0, ""
	}
	if len(q.Instruction) > memoQueueInstructionMax {
		return q, badQueueSettings(fmt.Sprintf("instruction is at most %d bytes", memoQueueInstructionMax))
	}
	seen := map[string]bool{}
	for _, id := range in.TidyDestinations {
		id = strings.TrimSpace(id)
		if id == "" {
			return q, badQueueSettings("tidyDestinations holds an empty id")
		}
		if !seen[id] {
			seen[id] = true
			q.TidyDestinations = append(q.TidyDestinations, id)
		}
	}
	if len(q.TidyDestinations) > store.MaxTidyDestinations {
		return q, badQueueSettings(fmt.Sprintf("tidyDestinations holds at most %d queues", store.MaxTidyDestinations))
	}
	if q.Enabled && q.Instruction == "" {
		return q, &apiError{http.StatusBadRequest, errCodeMemoQueueNoInstruction, "a queue without an instruction cannot be enabled"}
	}
	return q, nil
}

// memoQueuesWire is GET /api/memo-queues: the member's queues and whether their tenant lets
// them create one (tenant.memo_queues). Existing queues are listed whatever the switch says.
type memoQueuesWire struct {
	TenantEnabled bool           `json:"tenantEnabled"`
	Queues        []memoQueueDTO `json:"queues"`
}

func (a memoAPI) listQueues(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	on, err := a.mgr.store.GetTenantMemoQueues(r.Context(), mv.TenantID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
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
	stats, err := a.store.ListMemberQueuePendingStats(r.Context(), mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := make([]memoQueueDTO, 0, len(qs))
	queueSet := make(map[string]struct{}, len(qs))
	for _, q := range qs {
		queueSet[q.CategoryID] = struct{}{}
	}
	now := time.Now()
	err = a.store.MemoRead(r.Context(), mv.MembershipID, func(rd store.MemoWaitOps) error {
		for _, q := range qs {
			c, ok := byID[q.CategoryID]
			if !ok {
				continue
			}
			dto := memoQueueToDTO(q, c)
			// A destination whose queue settings are gone is not shown: apply ignores it the same way.
			dto.TidyDestinations = []string{}
			for _, id := range q.TidyDestinations {
				if _, isQ := queueSet[id]; isQ {
					dto.TidyDestinations = append(dto.TidyDestinations, id)
				}
			}
			var err error
			if dto.Waiting, err = memoQueueWaiting(rd, q, on, stats[q.CategoryID], now); err != nil {
				return err
			}
			out = append(out, dto)
		}
		return nil
	})
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, memoQueuesWire{TenantEnabled: on, Queues: out})
}

// putQueue creates or replaces a category's queue settings. A category that becomes a queue
// takes its memos in under the rules of a Console write: the member's own become pending,
// everything else held. A category holding a memo taken in from the inbox cannot become one.
// Refused while the member's tenant has memo queues off (tenant_memo_queues.go): that switch
// is what keeps queue rows away from a Control Plane binary without the queue rules.
func (a memoAPI) putQueue(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	if on, err := a.mgr.store.GetTenantMemoQueues(r.Context(), mv.TenantID); err != nil || !on {
		if err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
		writeAPIErr(w, &apiError{http.StatusConflict, errCodeMemoQueuesDisabled, "your tenant has not turned memo queues on"})
		return
	}
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
		// A destination is another queue of the member's: a run could not move a message out of a
		// queue into itself, and an id that is no queue of theirs names nothing.
		for _, id := range q.TidyDestinations {
			if _, isQ, err := tx.GetQueue(id); err != nil {
				return err
			} else if !isQ || id == c.ID {
				return memoTxFail(badQueueSettings("tidyDestinations must name other queues of yours"))
			}
		}
		now := store.NowTS()
		q.CategoryID, q.MembershipID, q.UpdatedAt = c.ID, mv.MembershipID, now
		if existed {
			q.CreatedAt, q.Generation, q.NoTidyAt = cur.CreatedAt, cur.Generation, cur.NoTidyAt
			// A client that does not know the field (an older Console tab) must not turn the member's
			// 0 back into the default: an omitted value keeps what is stored.
			if in.ContinuationMax == nil {
				q.ContinuationMax = cur.ContinuationMax
			}
		} else {
			q.CreatedAt = now
			if err := memoMoveBucket(tx, c.Repo, c.Name, c.Name, false, true, false, memoViaConsole); err != nil {
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
		if err := memoMoveBucket(tx, c.Repo, c.Name, c.Name, true, false, false, memoViaConsole); err != nil {
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

// resumeQueue (POST /api/memo-queues/{categoryId}/resume) lifts the circuit breaker's pause and
// restarts its count from now. Console only. A queue that is not paused answers the same.
func (a memoAPI) resumeQueue(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	// Behind the membership lock, like the breaker's pause, so that a pause decided on failures
	// from before this resume cannot land after it.
	found := false
	err := a.store.MemoTx(r.Context(), mv.MembershipID, func(tx store.MemoTxOps) (err error) {
		found, err = tx.ResumeQueue(r.PathValue("categoryId"), store.DeliveryTS(time.Now()))
		return err
	})
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !found {
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "queue not found"})
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
			m.State, m.ReleaseHash, m.ReleasedAt, m.ReleasedBy = store.MemoStatePending, h, at, ""
			m.ChainID, m.ChainSeq, m.ChainDelivery = "", 0, ""
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

// --- Typed references (ADR 0137 section 4) ----------------------------------------------------
// A message may carry a pointer {provider, key, url} to a work item, with the member's own note
// beside it. The item's body is never copied: the session reads it with gh or jira.

// memoRefWire is a reference on the wire. In a patch an object with all three empty clears it.
type memoRefWire struct {
	Provider string `json:"provider"`
	Key      string `json:"key"`
	URL      string `json:"url"`
}

func (r memoRefWire) empty() bool { return r.Provider == "" && r.Key == "" && r.URL == "" }

func memoRefToWire(m store.Memo) *memoRefWire {
	if m.RefKey == "" {
		return nil
	}
	return &memoRefWire{Provider: m.RefProvider, Key: m.RefKey, URL: m.RefURL}
}

func memoRefInvalid(format string, a ...any) *apiError {
	return &apiError{http.StatusBadRequest, errCodeMemoRefInvalid, fmt.Sprintf(format, a...)}
}

// memoRefNormalize is memoref.Normalize against the providers the work items know.
func memoRefNormalize(in memoRefWire) (memoRefWire, *apiError) {
	r, err := memoref.Normalize(memoref.Ref{Provider: in.Provider, Key: in.Key, URL: in.URL}, func(p string) bool { return workItemProviders[p] })
	if err != nil {
		return memoRefWire{}, memoRefInvalid("%s", err.Error())
	}
	return memoRefWire{Provider: r.Provider, Key: r.Key, URL: r.URL}, nil
}
func memoSetRef(m store.Memo, r memoRefWire) store.Memo {
	m.RefProvider, m.RefKey, m.RefURL = r.Provider, r.Key, r.URL
	return m
}

// memoOpenState is a state a message can still be fired from or is being fired in: held and
// pending wait to be fired, claimed is taken by a fire. unconfirmed (no session could be found
// and nothing fires it by itself), delivered and superseded are closed, so the same item can
// be added again.
func memoOpenState(s string) bool {
	switch s {
	case store.MemoStateHeld, store.MemoStatePending, store.MemoStateClaimed:
		return true
	}
	return false
}

// memoOpenRefIn finds the open message of rows that carries the same reference as m, other than
// m itself.
func memoOpenRefIn(rows []store.Memo, m store.Memo) (store.Memo, bool) {
	id := memoRefIdent(m)
	if id == "" {
		return store.Memo{}, false
	}
	for _, r := range rows {
		if r.ID != m.ID && memoOpenState(r.State) && memoRefIdent(r) == id {
			return r, true
		}
	}
	return store.Memo{}, false
}

// memoRefIdent is what two messages of one queue are compared by (memoref.Ident).
func memoRefIdent(m store.Memo) string { return memoref.Ident(m.RefProvider, m.RefKey) }

func memoRefDuplicate(existing store.Memo) *apiError {
	return &apiError{http.StatusConflict, errCodeMemoRefDuplicate,
		fmt.Sprintf("%s is already waiting in this queue as message %s", existing.RefKey, existing.ID)}
}

// memoRefHashParts is appended to the content hash of a message that carries a reference. A
// message without one hashes as it did before references existed, so no release already
// recorded changes.
func memoRefHashParts(m store.Memo) []string {
	if m.RefKey == "" {
		return nil
	}
	return []string{"ref", m.RefProvider, m.RefKey, m.RefURL}
}

// memoAfterResolve is memoafter.Resolve with its refusal as the API's answer (400 memo_after_invalid).
func memoAfterResolve(self store.Memo, rows []store.Memo, in []string, inQueue bool) (string, *apiError) {
	out, inv := memoafter.Resolve(self, rows, in, inQueue)
	if inv != nil {
		return "", &apiError{http.StatusBadRequest, errCodeMemoAfterInvalid, inv.Msg}
	}
	return out, nil
}

func derefRef(r *memoRefWire) memoRefWire {
	if r == nil {
		return memoRefWire{}
	}
	return *r
}

// memoRefLine is a reference as a line of a hand-sent message.
func memoRefLine(m store.Memo) string {
	return "作業項目: " + m.RefKey + " (" + m.RefURL + ")"
}
