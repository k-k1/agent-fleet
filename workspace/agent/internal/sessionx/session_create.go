package sessionx

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// createRun is one create as it passes through createSession's stages, in order. Each stage
// either moves the create on or writes its refusal and returns false; the caller then returns
// at once. The order is the contract (session_create_stages_test.go pins it), not the layout.
type createRun struct {
	// w is the response writer. Once claim owns an idempotency key it is the outcome writer, so
	// every answer after the claim carries the outcome headers.
	w   http.ResponseWriter
	r   *http.Request
	req *CreateReq

	// spawnParent names the session that raised this create (ADR 0073), "" for every other route.
	spawnParent string
	// idemKey is the idempotency key this create owns ("" when nothing is de-duplicated), and
	// committed says its answer has been recorded under it.
	idemKey   string
	committed bool
}

// validateKind is stage 1. It runs first of all, ahead of the idempotency claim: a refused kind
// must leave no ledger entry, and every guard below goes through NormalizeKind, which would turn
// it into claude.
func (c *createRun) validateKind() bool {
	if err := ValidateCreateKind(c.req.Kind); err != nil {
		httpx.WriteErr(c.w, http.StatusBadRequest, "bad_kind", err.Error())
		return false
	}
	return true
}

// claim is stage 2, the idempotency guard (session_idempotency.go): collapse a retried or
// concurrent create onto the first one so a client that timed out mid-launch can't spawn a
// duplicate. A duplicate is answered here from the ledger. When claim returns true and owns a
// key, the caller defers settleClaim.
func (c *createRun) claim() bool {
	// A create raised by another SESSION (ADR 0073) carries the parent's name, and the whole
	// spawn regime hangs off it. Resolved here; the refusals run further down.
	if origin, _, parent := CreateOrigin(c.req); origin == session.OriginSession {
		c.spawnParent = parent
		// The child has to be able to tell this instruction from one its user typed
		// (ADR 0073 decision 14). Applied before the idempotency key is derived, so the key
		// covers what is actually delivered, and here rather than in the calling tool so it
		// cannot be omitted or forged.
		c.req.InitialPrompt = SpawnEnvelope(parent, c.req.InitialPrompt)
	}
	idemKey := createIdempotencyKey(c.req)
	if idemKey == "" {
		return true
	}
	durable := c.req.IdempotencyDurable && strings.TrimSpace(c.req.IdempotencyKey) != ""
	if prev, dup := createLedger.begin(idemKey, durable); dup {
		writeLedgerAnswer(c.w, idemKey, prev)
		return false
	}
	c.idemKey = idemKey
	// This request owns the key, so its answer is the outcome under it: say so in headers
	// a proxy in between would not write (createOutcomeWriter).
	c.w = &createOutcomeWriter{ResponseWriter: c.w, key: idemKey}
	return true
}

// writeLedgerAnswer answers a create whose key the ledger already holds.
func writeLedgerAnswer(w http.ResponseWriter, idemKey string, prev createLedgerEntry) {
	switch prev.state {
	case createDone:
		// The identical create already succeeded — replay that session verbatim.
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(createKeyHeader, idemKey)
		w.Header().Set(createOutcomeHeader, "created")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(prev.body)
	case createAbandoned:
		// The caller gave this key up (abandon) and may have started over.
		httpx.WriteErr(w, http.StatusConflict, "create_abandoned", "this create was abandoned by its caller")
	case createRefused:
		// A refused durable key stays refused, so this answer is as true as the first.
		w.Header().Set(createKeyHeader, idemKey)
		w.Header().Set(createOutcomeHeader, "refused")
		httpx.WriteErr(w, http.StatusConflict, "create_refused", "a create under this key was refused; start over under a new key")
	case createUnknown:
		httpx.WriteErr(w, http.StatusServiceUnavailable, "create_outcome_unknown", "the outcome of an earlier create under this key is unknown")
	default:
		// The first create is still launching (the timed-out client's retry, or a
		// genuine double-fire). Refuse with a distinct code the MCP tool reconciles
		// into the eventual session rather than surfacing as a retryable failure.
		httpx.WriteErr(w, http.StatusConflict, "create_in_progress",
			"同じ内容のセッションを作成中です。完了までお待ちください")
	}
}

// settleClaim clears the key this create owns on any exit that did not commit, so a real
// failure doesn't wedge the key (a completed entry is kept for replay by ledger.fail).
func (c *createRun) settleClaim() {
	if c.idemKey != "" && !c.committed {
		createLedger.fail(c.idemKey)
	}
}

// writeCreated finalizes a successful launch: cache the session for idempotent replay, mark the
// key committed (so settleClaim won't clear it), then write the response.
func (c *createRun) writeCreated(m session.Meta) {
	body := wireSession(m, true)
	if c.idemKey != "" {
		if b, err := json.Marshal(body); err == nil {
			createLedger.complete(c.idemKey, b)
			c.committed = true
		}
	}
	httpx.WriteJSON(c.w, http.StatusCreated, body)
}
