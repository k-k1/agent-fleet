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

	// slot is this create's claim on one of the parent's child slots. Empty for every route
	// that is not a spawn, and then every method is a plain meta write.
	slot *spawnSlot
	// title is the cleaned request title, and driver the validated driver ("" = tui).
	title  string
	driver string
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

// refuseStart is stage 3: the refusals that answer from state that changes, so they run after
// the claim and before any side effect.
func (c *createRun) refuseStart() bool {
	c.slot = &spawnSlot{}
	// A kind whose CLI failed to install at start and is still absent cannot launch: say why
	// (typically a full home) instead of the generic launch failure further down. After the
	// idempotency claim, like the spawn refusals below and for the same reason: the answer
	// depends on state that changes, so a retry of a create that already succeeded must replay
	// its session (the ledger answered in claim), and a durable key must end as refused through
	// the outcome writer and settleClaim.
	if ref := startRefusal(c.req.Kind); ref != nil {
		httpx.WriteErr(c.w, ref.Status, ref.Code, ref.Message)
		return false
	}
	// The spawn refusals (ADR 0073) run AFTER the idempotency claim and before any side effect
	// (clone / worktree), so a refused spawn leaves nothing behind.
	//
	// After the claim, because they must not answer a RETRY. A client whose create timed out
	// re-sends the same request, and by then the first child exists: run the budget first and
	// the retry is refused as "you already have three" instead of being replayed the session it
	// actually created. The same for a create still in flight — the ledger answers that with
	// create_in_progress, which the calling tool reconciles into the eventual session.
	//
	// The working-copy check is not here: it needs the RESOLVED dir (stage 6), not the raw one.
	if c.spawnParent == "" {
		return true
	}
	if ref := SpawnCreateRefusal(c.spawnParent, c.req.Kind); ref != nil {
		httpx.WriteErr(c.w, ref.Status, ref.Code, ref.Message)
		return false
	}
	if err := reserveSpawnSlot(c.spawnParent); err != nil {
		httpx.WriteErr(c.w, http.StatusConflict, "spawn_budget", err.Error())
		return false
	}
	// The slot is handed over to the child's meta as that meta is written (spawnSlot.publish
	// — the two are one atomic step). The caller's deferred release is the failure net.
	c.slot = &spawnSlot{parent: c.spawnParent}
	return true
}

// validateOptions is stage 4's first half: the request fields that need no state at all.
func (c *createRun) validateOptions() bool {
	title, ok := CleanTitle(c.req.Title)
	if !ok {
		httpx.WriteErr(c.w, http.StatusBadRequest, "bad_title", "title is too long (max 80) or contains control characters")
		return false
	}
	c.title = title
	if c.req.Mode != "" && c.req.Mode != "normal" && c.req.Mode != "plan" {
		httpx.WriteErr(c.w, http.StatusBadRequest, "bad_mode", `mode must be "plan" or "normal"`)
		return false
	}
	if c.req.SpendCapUSD != nil {
		if _, ok := session.NormalizeSpendCap(*c.req.SpendCapUSD); !ok {
			httpx.WriteErr(c.w, http.StatusBadRequest, "bad_spend_cap", spendCapRangeMsg)
			return false
		}
	}
	// "Ask for tool approval" is only accepted for kinds whose pending approvals can be
	// answered from the Console (docs/log/76). Ignoring it silently would leave the caller
	// believing the session runs with approvals on, so refusing is the honest answer.
	// Skipping (true) is the historical default and stays allowed for every kind.
	if c.req.SkipPermissions != nil && !*c.req.SkipPermissions && !AgentOf(NormalizeKind(c.req.Kind)).Caps().PermissionChoice {
		httpx.WriteErr(c.w, http.StatusBadRequest, "permission_choice_unsupported",
			"this agent kind cannot surface tool approvals to the Console; skip_permissions must stay true")
		return false
	}
	return true
}

// resolveDriver is stage 4's second half: driver validation up-front, before any side effect
// (clone / worktree). The default tui is normalized to "" when persisted, keeping metas
// byte-identical to existing ones — except for a kind with no Terminal(CLI) route at all
// (ADR 0093 decision 2), where an unspecified driver has to default to managed instead: leaving
// it "" would silently try to launch a tui session with no pane program.
func (c *createRun) resolveDriver() bool {
	driver := strings.TrimSpace(c.req.Driver)
	if driver == "" && AgentOf(NormalizeKind(c.req.Kind)).Caps().ManagedOnly {
		driver = session.DriverManaged
	}
	switch driver {
	case "", session.DriverTUI:
		driver = ""
	case session.DriverManaged:
		// docs/log/27 P2/P3: only kinds registered in driverOf (opencode / codex); claude
		// is out of scope (ADR 0015). A raw req.Kind can still turn into claude through
		// NormalizeKind further down, so never decide on it — normalize here and keep
		// using the normalized value.
		if _, ok := managedDrivers[NormalizeKind(c.req.Kind)]; !ok {
			httpx.WriteErr(c.w, http.StatusBadRequest, "driver_unsupported",
				"managed ドライバはこの kind では利用できません")
			return false
		}
	default:
		httpx.WriteErr(c.w, http.StatusBadRequest, "bad_driver", "unknown driver: "+c.req.Driver)
		return false
	}
	c.driver = driver
	return true
}
