package main

// The member handoff API (docs/log/77 / ADR 0057).
//
// An owner A offers "please carry this on" to a B who is already sharing that session; B
// then starts a session in B's own Workspace from B's own Console.
//
// Nothing here executes anything on A's behalf. CP touches the owner Agent exactly once,
// when the offer is created, to read the coordinates (GET /sessions/{name}/handoff-context),
// and that read has no side effects. That is why the owner lease, idempotency ledger and
// double-execution guards that the shared RW proposal (session_share.go) needed do not exist
// on this surface: only prose and coordinates cross the boundary (ADR 0057 decisions 1/3).

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const (
	// handoffPromptMaxBytes caps the handoff prompt. It matches the Agent-side proposal
	// limit (handoffProposalMaxBytes) at 64 KiB: a prompt that was acceptable before being
	// offered but rejected on the way out would be an accident.
	handoffPromptMaxBytes = 64 << 10
	handoffTitleMaxBytes  = 512
	// handoffOfferTTL is how long an offer waits to be taken. Longer than the 24 hours of
	// the shared RW proposal because the addressee is a person, who is routinely away from
	// the desk or on leave.
	handoffOfferTTL = 7 * 24 * time.Hour
)

type sessionHandoffAPI struct {
	memberAuth
	// share must be the same instance as the sharing API, which owns the catalog sync
	// throttle (syncedAt) and the read-rate buckets. A separate instance would let this
	// surface add its own round trips to the owner Workspace on top.
	share sessionShareAPI
	// ws reaches the recipient's own workspace when their take press has to ask their Agent
	// what became of an earlier create (reconcileTake).
	ws workspaceAPI
}

func newSessionHandoffAPI(m *manager, share sessionShareAPI, autostart bool) sessionHandoffAPI {
	return sessionHandoffAPI{memberAuth: memberAuth{m}, share: share, ws: newWorkspaceAPI(m, autostart)}
}

// writeHandoffErr writes an error carrying detail — the gate's reason, the current status.
// writeAPIErr returns only code and message, but here the Console has to tell the user why
// it was stopped.
func writeHandoffErr(w http.ResponseWriter, status int, code, message string, detail map[string]any) {
	e := map[string]any{"code": code, "message": message}
	for k, v := range detail {
		e[k] = v
	}
	writeJSON(w, status, map[string]any{"error": e})
}

// handoffOfferBody is the input of an offer. The addressee is a person (userKey), not a
// session, and no coordinates are accepted: CP asks the owner Agent for repo, branch and
// HEAD itself (ADR 0057 decision 5).
type handoffOfferBody struct {
	SessionName      string `json:"sessionName"`
	RecipientUserKey string `json:"recipientUserKey"`
	Title            string `json:"title"`
	Prompt           string `json:"prompt"`
	// AckWarning means "there are uncommitted changes and I am sending anyway". A blocked
	// state cannot be overridden this way.
	AckWarning bool `json:"ackWarning"`
	// PushFirst is the owner's "push, then send" (#2072): when the gate blocks for a reason a
	// push clears, the owner Agent pushes the current branch and the gate is asked again.
	// It never overrides the gate; the offer goes only if the recomputed verdict passes.
	PushFirst bool `json:"pushFirst"`
	// PushExpect is what the owner was shown and confirmed: the push happens only while the
	// working copy still matches it, and the offer only if the state after the push does too.
	PushExpect *handoffPushExpect `json:"pushExpect,omitempty"`
}

// handoffPushExpect pins a push to the snapshot the owner confirmed. It is also the body of
// the Agent's POST /sessions/{name}/handoff-push, which pushes exactly HeadSha to Branch on
// PushRemote or refuses.
type handoffPushExpect struct {
	Branch     string `json:"branch"`
	HeadSha    string `json:"headSha"`
	PushRemote string `json:"pushRemote"`
}

func (a sessionHandoffAPI) seal(ctx context.Context, tenant string, body []byte) (string, string, error) {
	if a.mgr.custodian == nil {
		return base64.StdEncoding.EncodeToString(body), "", nil
	}
	ct, err := a.mgr.custodian.Wrap(ctx, tenant, body)
	return ct, tenant, err
}

func (a sessionHandoffAPI) open(ctx context.Context, o store.SessionHandoffOffer) string {
	if o.Ciphertext == "" {
		return ""
	}
	if o.KeyRef == "" {
		b, err := base64.StdEncoding.DecodeString(o.Ciphertext)
		if err != nil {
			return ""
		}
		return string(b)
	}
	b, err := a.mgr.custodian.Unwrap(ctx, o.KeyRef, o.Ciphertext)
	if err != nil {
		return ""
	}
	return string(b)
}

// handoffOfferWire is the base form of one handoff offer (the create response and the
// owner's list). It carries no prompt: A still has the original proposal, so the ledger only
// has to make the status readable.
//
// was: map[string]any{"id":…, "sessionId":…, …}, 13 keys (handoffOfferDTO's return value).
//
// All 13 keys are unconditional, so none of them takes omitempty — branch, headSha and
// decidedAt can legitimately be empty strings, and omitempty would drop the key entirely.
// The inbox adds three more keys, which is why it is a separate embedding type
// (handoffOfferInboxWire) rather than optional fields here.
type handoffOfferWire struct {
	ID                  string `json:"id"`
	SessionID           string `json:"sessionId"`
	SessionName         string `json:"sessionName"`
	RecipientUserKey    string `json:"recipientUserKey"`
	Title               string `json:"title"`
	Status              string `json:"status"`
	Branch              string `json:"branch"`
	RepoRemote          string `json:"repoRemote"`
	HeadSha             string `json:"headSha"`
	CreatedAt           string `json:"createdAt"`
	ExpiresAt           string `json:"expiresAt"`
	DecidedAt           string `json:"decidedAt"`
	AcceptedSessionName string `json:"acceptedSessionName"`
	// TakeState is store.HandoffTakeState: "" when no take holds the offer, which is also
	// every decided offer, so it may be absent.
	TakeState string `json:"takeState,omitempty"`
	// RecipientEmail is what the sender's picker showed for the recipient, so the ledger and
	// the source session's card name them the same way. Only the sender's routes set it: on the
	// recipient's inbox it would be the reader's own address. Absent when the member has no
	// e-mail or has left the tenant; the Console then falls back to the key.
	RecipientEmail string `json:"recipientEmail,omitempty"`
}

// handoffOfferInboxWire is one entry of the recipient's inbox: the base form plus three
// keys, 21 in all with every optional key. The three cannot be optional fields on the base type —
// ownerUserKey is "" when the member is unknown and prompt is "" when the ciphertext cannot be
// opened, and both still have to appear — so they are added by embedding, which JSON flattens.
//
// was: map[string]any (handoffOfferDTO's return value) with listReceived adding three keys.
//
// The effective JSON names of the base and the three additions must not collide:
// encoding/json emits neither side of a collision, so a key would vanish silently. The
// equivalence test catches that, and the counts 15 / 21 (optional keys set) are pinned
// separately.
type handoffOfferInboxWire struct {
	handoffOfferWire
	OwnerUserKey      string `json:"ownerUserKey"`
	Prompt            string `json:"prompt"`
	SourceSessionKind string `json:"sourceSessionKind"`
	// TakeRev names the recipient's confirmed take (handoffTakeRev), and the give-back must send
	// it back, so a confirmation made against one take never releases a later one. Only the
	// recipient's inbox carries it.
	TakeRev string `json:"takeRev,omitempty"`
	// RecoveryOnly marks an offer the inbox lists only because the recipient's confirmed take
	// still holds it after an unshare or archive (ListReceivedSessionHandoffOffers): it
	// carries no body or coordinates, and nothing launches from it.
	RecoveryOnly bool `json:"recoveryOnly,omitempty"`
	// OwnerEmail names the sender the way the recipient's shared-sessions list does (ownerEmail
	// there too), absent when unknown; the Console falls back to ownerUserKey.
	OwnerEmail string `json:"ownerEmail,omitempty"`
}

// handoffTakeRev is an opaque name for a take, safe to hand to the recipient's Console: a one-way
// digest of the take's token, which itself never leaves the CP (it is the Agent key's suffix).
func handoffTakeRev(token string) string {
	sum := sha256.Sum256([]byte("af-handoff-take-rev\x00" + token))
	return hex.EncodeToString(sum[:12])
}

func handoffOfferDTO(o store.SessionHandoffOffer, recipientKey string) handoffOfferWire {
	return handoffOfferWire{
		ID: o.ID, SessionID: o.CatalogID, SessionName: o.SourceSessionName,
		RecipientUserKey: recipientKey, Title: o.Title, Status: o.Status,
		Branch: o.Branch, RepoRemote: o.RepoRemote, HeadSha: o.HeadSha,
		CreatedAt: o.CreatedAt, ExpiresAt: o.ExpiresAt, DecidedAt: o.DecidedAt,
		AcceptedSessionName: o.AcceptedSessionName,
		TakeState:           store.HandoffTakeState(o, time.Now()),
	}
}

// catalogForOwnedSession looks up the owner's own catalog row by name. ok=false means the
// row is absent, which is not an error — the caller decides what it means (a read takes it
// as "not shared with anyone yet", a write answers 404).
//
// How often it syncs decides how this behaves. A sync is a round trip to the owner Agent's
// /sessions/catalog, plus /repos when its cache is stale, and /repos runs git per working
// copy, so it gets heavier with every worktree. That is why shared reads are thinned through
// freshCatalog (see the note in session_share.go): running it on every read, such as
// populating the recipient list, leaves the modal apparently stuck on "loading". So reads go
// through the same throttle.
//
// Skipping the sync altogether is not an option either. Repo- and worktree-scoped shares
// apply dynamically to sessions created later (docs/log/59 §1), so a missing catalog row is
// no proof that a session is unshared — skipping it would tell the user that an
// already-shared new session is "not shared with anyone yet".
func (a sessionHandoffAPI) catalogForOwnedSession(r *http.Request, res *resolved, name string, exact bool) (store.SharedSessionCatalog, bool, *apiError) {
	if exact || !a.share.freshCatalog(res.mv.MembershipID, shareCatalogTTL) {
		if _, err := a.mgr.syncOwnerCatalog(r.Context(), res, catalogSyncNeed{exact: exact}); err != nil {
			a.share.invalidateCatalog(res.mv.MembershipID) // a failed sync must not count as fresh
			return store.SharedSessionCatalog{}, false, catalogSyncAPIError(err)
		}
	}
	rows, err := a.mgr.store.ListSharedSessionCatalogByOwner(r.Context(), res.mv.MembershipID)
	if err != nil {
		return store.SharedSessionCatalog{}, false, internalErr(err)
	}
	for _, c := range rows {
		if c.Name == name && !c.Archived {
			return c, true, nil
		}
	}
	return store.SharedSessionCatalog{}, false, nil
}

// recipientsFor lists who can see this session, i.e. the candidate addressees. It is a
// reverse lookup over the sharing ACL, not the tenant roster (ADR 0057 decision 2: the
// roster never enters anyone's context).
func (a sessionHandoffAPI) recipientsFor(ctx context.Context, mv store.MembershipView, c store.SharedSessionCatalog) ([]map[string]string, error) {
	shares, err := a.mgr.store.ListSessionSharesByOwner(ctx, mv.MembershipID)
	if err != nil {
		return nil, err
	}
	members, err := a.mgr.store.ListMembersByTenant(ctx, mv.TenantID)
	if err != nil {
		return nil, err
	}
	byID := map[string]store.MemberInfo{}
	for _, m := range members {
		byID[m.MembershipID] = m
	}
	byRecipient := map[string][]store.SessionShare{}
	for _, s := range shares {
		byRecipient[s.RecipientMembershipID] = append(byRecipient[s.RecipientMembershipID], s)
	}
	out := []map[string]string{}
	for id, rules := range byRecipient {
		// A read-only share is enough: a handoff needs the conversation to be readable,
		// not the right to propose operations.
		if effectivePermission(rules, c) == "" {
			continue
		}
		m, ok := byID[id]
		if !ok {
			continue // removed from the tenant; ListMembersByTenant returns active members only
		}
		out = append(out, map[string]string{"userKey": m.UserKey, "email": m.Email})
	}
	return out, nil
}

// recipients (GET /api/sessions/{name}/handoff-recipients) returns both the list A picks an
// addressee from and the push gate's verdict (docs/log/77 §77.5). Putting the gate only on
// submission would reject A after a long prompt had already been written.
//
// The verdict costs a git run on the owner's Agent, which the list does not need, so the
// Console asks for the list with ?context=skip and reads the verdict from handoffContextGET
// next. Without the parameter both come back together, as before.
func (a sessionHandoffAPI) recipients(w http.ResponseWriter, r *http.Request, res *resolved) {
	name := r.PathValue("name")
	if res.rt.State(r.Context()) != "running" {
		writeAPIErr(w, &apiError{http.StatusConflict, "workspace_not_running", "owner workspace must be running to hand off"})
		return
	}
	c, found, e := a.catalogForOwnedSession(r, res, name, false)
	if e != nil {
		writeAPIErr(w, e)
		return
	}
	// "not shared with anyone yet" is a normal state, so it is not an error: as an error
	// the screen can only say "fetch failed" and the user has no idea what to do next
	// (the first thing real use hit). Answer with an empty candidate list and let the
	// Console offer the path to share it first.
	members := []map[string]string{}
	if found {
		var err error
		if members, err = a.recipientsFor(r.Context(), res.mv, c); err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
	}
	var ctx map[string]any
	if r.URL.Query().Get("context") != "skip" {
		var e *apiError
		if ctx, _, e = a.handoffContext(r.Context(), res, name); e != nil {
			writeAPIErr(w, e)
			return
		}
	}
	pending := ""
	if found {
		if rows, err := a.mgr.store.ListSessionHandoffOffersByOwner(r.Context(), res.mv.MembershipID); err == nil {
			for _, o := range rows {
				if o.CatalogID == c.ID && o.Status == "pending" {
					pending = o.ID
					break
				}
			}
		}
	}
	w.Header().Set("Cache-Control", "private, no-store")
	out := map[string]any{"members": members, "pendingOfferId": pending}
	if ctx != nil {
		out["context"] = ctx
	}
	writeJSON(w, http.StatusOK, out)
}

// handoffContextGET (GET /api/sessions/{name}/handoff-context) is the push gate's verdict
// alone, relayed from the owner's Agent unchanged. The Console shows the recipients first and
// enables the send button only once this has answered and passes.
func (a sessionHandoffAPI) handoffContextGET(w http.ResponseWriter, r *http.Request, res *resolved) {
	if res.rt.State(r.Context()) != "running" {
		writeAPIErr(w, &apiError{http.StatusConflict, "workspace_not_running", "owner workspace must be running to hand off"})
		return
	}
	ctx, _, e := a.handoffContext(r.Context(), res, r.PathValue("name"))
	if e != nil {
		writeAPIErr(w, e)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"context": ctx})
}

// handoffContext asks the owner Agent for the state of the working copy. The raw map is
// passed to the Console unchanged; the blocked and warning tokens are built by the Agent,
// because conditions split across two places always drift apart.
func (a sessionHandoffAPI) handoffContext(ctx context.Context, res *resolved, name string) (map[string]any, string, *apiError) {
	payload, status, e := a.share.ownerGET(ctx, res, "/sessions/"+url.PathEscape(name)+"/handoff-context")
	if e != nil {
		return nil, "", e
	}
	// Never merge the upstream 404 and 409. "no such session" and "no working copy to hand
	// off" lead the user to different next steps, and merging them makes a deleted session
	// report "there is no working copy", which hides the actual cause.
	switch {
	case status == http.StatusNotFound:
		return nil, "", &apiError{http.StatusNotFound, "not_found", "no such session"}
	case status >= 400:
		return nil, "", &apiError{http.StatusConflict, "handoff_no_working_copy", "session has no working copy to hand off"}
	}
	blocked, _ := payload["blocked"].(string)
	return payload, blocked, nil
}

// stripURLUserinfo drops credentials from a remote URL before it is stored and shown to the
// recipient. The Agent already sanitises it (sanitizeRemoteURL); this is the CP's own guard,
// because the row crosses to another member and an older Agent, or a token embedded in an
// https remote, must not ride along. A URL that does not parse (scp-like git@host:path) has
// no secret to strip and is kept as is.
func stripURLUserinfo(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = nil
	return u.String()
}

// handoffPushClears mirrors the Agent's set of gate reasons a push can clear. It only
// decides whether asking the Agent to push is worth a round trip; the Agent refuses any
// other state itself and the verdict after the push is the Agent's again.
func handoffPushClears(blocked string) bool {
	return blocked == "unpushed_commits" || blocked == "no_upstream"
}

// pushThenRecheck is "push, then send": the owner Agent pushes the session's current branch
// (POST /sessions/{name}/handoff-push) and the push gate is asked again, so the offer is
// stored only on a verdict the Agent built after the push.
//
// The push is pinned to what the owner confirmed (exp): the Agent's state must still match it
// before the push, the Agent pushes exactly that commit to that branch on that remote, and the
// state after the push must match it again. Anything that would refuse the offer anyway is
// checked BEFORE the push, so a click that cannot produce an offer publishes nothing. The
// Agent call carries a member-origin mark (ADR 0113): AGENT_TOKEN is in every session's shell,
// and the Agent accepts this route only on the CP's word that the owner asked. ok=false means
// it has written the refusal.
func (a sessionHandoffAPI) pushThenRecheck(w http.ResponseWriter, r *http.Request, res *resolved, c store.SharedSessionCatalog, hctx map[string]any, exp *handoffPushExpect, ack bool) (map[string]any, string, bool) {
	fail := func(e *apiError) (map[string]any, string, bool) {
		writeAPIErr(w, e)
		return nil, "", false
	}
	changed := func(ctx map[string]any) (map[string]any, string, bool) {
		writeHandoffErr(w, http.StatusConflict, "handoff_changed", "the working copy changed after it was shown; check it again", map[string]any{"context": ctx})
		return nil, "", false
	}
	str := func(m map[string]any, k string) string { s, _ := m[k].(string); return s }
	if exp == nil || exp.Branch == "" || exp.HeadSha == "" || exp.PushRemote == "" {
		return fail(&apiError{http.StatusBadRequest, "bad_handoff", "pushFirst needs the branch, commit and remote that were confirmed"})
	}
	if str(hctx, "branch") != exp.Branch || str(hctx, "headSha") != exp.HeadSha || str(hctx, "pushRemote") != exp.PushRemote {
		return changed(hctx)
	}
	if warn := str(hctx, "warning"); warn != "" && !ack {
		writeHandoffErr(w, http.StatusConflict, "handoff_warning", "the working copy has uncommitted changes", map[string]any{"reason": warn, "context": hctx})
		return nil, "", false
	}
	rows, err := a.mgr.store.ListSessionHandoffOffersByOwner(r.Context(), res.mv.MembershipID)
	if err != nil {
		return fail(internalErr(err))
	}
	for _, o := range rows {
		if o.CatalogID == c.ID && o.Status == "pending" {
			return fail(&apiError{http.StatusConflict, "handoff_already_pending", "this session already has a pending handoff"})
		}
	}
	// The mark speaks for the workspace's own member; withResolved resolves the caller's own
	// workspace, and this says so rather than relying on it.
	if res.ws.MembershipID == "" || res.ws.MembershipID != res.mv.MembershipID {
		return fail(&apiError{http.StatusForbidden, "forbidden", "only the workspace's own member can push from it"})
	}
	path := "/sessions/" + url.PathEscape(c.Name) + "/handoff-push"
	body, _ := json.Marshal(exp)
	mark, e := a.mgr.memberOriginSign(res, http.MethodPost, path, "", body, time.Now())
	if e != nil {
		return fail(e)
	}
	hdr := http.Header{}
	if mark != "" {
		hdr.Set(memberOriginHeader, mark)
	}
	payload, status, e := a.share.ownerSendWith(r.Context(), res, http.MethodPost, path, body, hdr)
	if e != nil {
		return fail(e)
	}
	audit := func(code int, detail string) {
		_ = a.mgr.store.InsertAudit(context.Background(), store.AuditLog{ID: store.NewID(), TenantID: res.mv.TenantID,
			ActorKind: "user", ActorID: res.ident.ID, Action: "session.handoff.push", Target: c.Name,
			Detail: detail, HTTPStatus: code, At: store.NowTS()})
	}
	if status != http.StatusOK {
		// The Agent's code and message pass through: the message is git's own report (a
		// non-fast-forward, a protected branch, a pre-push hook), already scrubbed of
		// credentials by the Agent, and it is what tells the owner what to do next.
		pe, _ := payload["error"].(map[string]any)
		code, msg := str(pe, "code"), str(pe, "message")
		if status == http.StatusNotFound && code == "not_found" {
			// Only the Agent's own "no such session"; an Agent without the route also answers
			// 404, and that is a refused push, reported below with its code.
			return fail(&apiError{http.StatusNotFound, "not_found", "no such session"})
		}
		if code == "" {
			code = "push_failed"
		}
		if msg == "" {
			msg = "the push failed"
		}
		audit(http.StatusConflict, "push refused: "+code)
		writeHandoffErr(w, http.StatusConflict, "handoff_push_failed", msg, map[string]any{"reason": code, "context": hctx})
		return nil, "", false
	}
	if pushed, _ := payload["pushed"].(bool); pushed {
		audit(http.StatusOK, "pushed "+exp.HeadSha+" to "+exp.Branch+" on "+stripURLUserinfo(exp.PushRemote))
	}
	// The verdict the offer is judged on is asked for afresh, not taken from the push answer:
	// the create path keeps exactly one source for it. It must still describe the commit that
	// was pushed, on the branch and remote the offer will name.
	fresh, blocked, e := a.handoffContext(r.Context(), res, c.Name)
	if e != nil {
		return fail(e)
	}
	if str(fresh, "branch") != exp.Branch || str(fresh, "headSha") != exp.HeadSha || str(fresh, "remote") != exp.PushRemote {
		return changed(fresh)
	}
	return fresh, blocked, true
}

// create handles POST /api/session-handoff-offers. It can sign a member-origin mark (pushFirst),
// so it runs memberOriginRequestCheck before reading anything, like the other signers; the
// guarantee must not depend on the global write gate or its exemptions.
func (a sessionHandoffAPI) create(w http.ResponseWriter, r *http.Request, res *resolved) {
	if aerr := memberOriginRequestCheck(r); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, handoffPromptMaxBytes+handoffTitleMaxBytes+4096))
	if err != nil {
		writeAPIErr(w, &apiError{http.StatusRequestEntityTooLarge, "handoff_too_large", "handoff is too large"})
		return
	}
	var in handoffOfferBody
	if json.Unmarshal(raw, &in) != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_handoff", "invalid handoff request"})
		return
	}
	in.Title, in.Prompt = strings.TrimSpace(in.Title), strings.TrimSpace(in.Prompt)
	switch {
	case in.Title == "" || in.Prompt == "":
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_handoff", "title and prompt are required"})
		return
	case len(in.Title) > handoffTitleMaxBytes || len(in.Prompt) > handoffPromptMaxBytes:
		writeAPIErr(w, &apiError{http.StatusRequestEntityTooLarge, "handoff_too_large", "handoff is too large"})
		return
	}
	if res.rt.State(r.Context()) != "running" {
		writeAPIErr(w, &apiError{http.StatusConflict, "workspace_not_running", "owner workspace must be running to hand off"})
		return
	}
	c, found, e := a.catalogForOwnedSession(r, res, strings.TrimSpace(in.SessionName), true)
	if e != nil {
		writeAPIErr(w, e)
		return
	}
	if !found {
		writeAPIErr(w, &apiError{http.StatusNotFound, "handoff_session_not_shared", "session is not shared with anyone"})
		return
	}
	// Only people the session is shared with may be addressed, decided by membership in the
	// reverse ACL lookup rather than by consulting the roster.
	members, err := a.recipientsFor(r.Context(), res.mv, c)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	recipientKey := strings.TrimSpace(in.RecipientUserKey)
	allowed := false
	for _, m := range members {
		if m["userKey"] == recipientKey {
			allowed = true
			break
		}
	}
	if !allowed {
		writeAPIErr(w, &apiError{http.StatusNotFound, "handoff_recipient_not_shared", "recipient does not have this session shared"})
		return
	}
	recipient, ok, err := memberByUserKey(r.Context(), a.mgr.store, res.mv.TenantID, recipientKey)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok || recipient.MembershipID == res.mv.MembershipID {
		writeAPIErr(w, &apiError{http.StatusNotFound, "handoff_recipient_not_shared", "recipient does not have this session shared"})
		return
	}
	// The push gate judges only on what CP asked the Agent; coordinates in the request body
	// are never accepted.
	hctx, blocked, e := a.handoffContext(r.Context(), res, c.Name)
	if e != nil {
		writeAPIErr(w, e)
		return
	}
	if in.PushFirst && in.PushExpect != nil && blocked == "" {
		// The gate opened by itself since the modal was shown (someone else pushed). The owner
		// still confirmed one branch and commit: an offer naming another is not what they sent.
		str := func(k string) string { s, _ := hctx[k].(string); return s }
		if str("branch") != in.PushExpect.Branch || str("headSha") != in.PushExpect.HeadSha || str("remote") != in.PushExpect.PushRemote {
			writeHandoffErr(w, http.StatusConflict, "handoff_changed", "the working copy changed after it was shown; check it again", map[string]any{"context": hctx})
			return
		}
	}
	if blocked != "" && in.PushFirst && handoffPushClears(blocked) {
		var ok bool
		if hctx, blocked, ok = a.pushThenRecheck(w, r, res, c, hctx, in.PushExpect, in.AckWarning); !ok {
			return
		}
	}
	if blocked != "" {
		writeHandoffErr(w, http.StatusConflict, "handoff_blocked", "the working copy is not in a handoff-able state", map[string]any{"reason": blocked, "context": hctx})
		return
	}
	if warn, _ := hctx["warning"].(string); warn != "" && !in.AckWarning {
		writeHandoffErr(w, http.StatusConflict, "handoff_warning", "the working copy has uncommitted changes", map[string]any{"reason": warn, "context": hctx})
		return
	}
	ct, kr, err := a.seal(r.Context(), res.mv.TenantID, []byte(in.Prompt))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	now := time.Now().UTC()
	str := func(k string) string { s, _ := hctx[k].(string); return s }
	o := store.SessionHandoffOffer{
		ID: store.NewID(), TenantID: res.mv.TenantID, CatalogID: c.ID,
		OwnerMembershipID: res.mv.MembershipID, RecipientMembershipID: recipient.MembershipID,
		Title: in.Title, Ciphertext: ct, KeyRef: kr,
		RepoRemote: stripURLUserinfo(str("remote")), Branch: str("branch"), HeadSha: str("headSha"),
		SourceSessionName: c.Name, SourceSessionKind: c.Kind, Status: "pending",
		CreatedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(handoffOfferTTL).Format(time.RFC3339),
	}
	created, err := a.mgr.store.CreateSessionHandoffOffer(r.Context(), o)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !created {
		writeAPIErr(w, &apiError{http.StatusConflict, "handoff_already_pending", "this session already has a pending handoff"})
		return
	}
	a.notifyOffered(o, res.mv.TenantSlug, c)
	_ = a.mgr.store.InsertAudit(context.Background(), store.AuditLog{ID: store.NewID(), TenantID: res.mv.TenantID,
		ActorKind: "user", ActorID: res.ident.ID, Action: "session.handoff.offer", Target: c.Name,
		Detail: "recipient=" + recipient.UserKey, HTTPStatus: http.StatusCreated, At: store.NowTS()})
	dto := handoffOfferDTO(o, recipient.UserKey)
	dto.RecipientEmail = recipient.Email
	writeJSON(w, http.StatusCreated, dto)
}

// listOwned (GET /api/session-handoff-offers) is A's ledger. Notifications scroll away, so
// this is the only place an offer can be traced afterwards (docs/log/77 §77.10).
func (a sessionHandoffAPI) listOwned(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	a.expireDue(r.Context())
	rows, err := a.mgr.store.ListSessionHandoffOffersByOwner(r.Context(), mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	members := a.members(r.Context(), mv.TenantID)
	out := []any{}
	for _, o := range rows {
		dto := handoffOfferDTO(o, members[o.RecipientMembershipID].UserKey)
		dto.RecipientEmail = members[o.RecipientMembershipID].Email
		out = append(out, dto)
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"offers": out})
}

// listReceived (GET /api/session-handoff-offers/received) is B's inbox. It returns the
// prompt because deciding whether to accept requires reading it, and this is the only place
// the prompt leaves CP.
func (a sessionHandoffAPI) listReceived(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	a.expireDue(r.Context())
	// One snapshot: each row's takeRev comes from the take read with that row, so a row never
	// pairs one take's state with a later take's revision.
	rows, err := a.mgr.store.ListReceivedSessionHandoffOffers(r.Context(), mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	members := a.members(r.Context(), mv.TenantID)
	out := []any{}
	for _, row := range rows {
		o := row.SessionHandoffOffer
		rev := ""
		if row.ConfirmedTakeToken != "" {
			rev = handoffTakeRev(row.ConfirmedTakeToken)
		}
		wire := handoffOfferInboxWire{
			handoffOfferWire:  handoffOfferDTO(o, members[o.RecipientMembershipID].UserKey),
			OwnerUserKey:      members[o.OwnerMembershipID].UserKey,
			SourceSessionKind: o.SourceSessionKind,
			TakeRev:           rev,
			RecoveryOnly:      row.RecoveryOnly,
			OwnerEmail:        members[o.OwnerMembershipID].Email,
		}
		if row.RecoveryOnly {
			// A take whose offer the inbox no longer lists (unshared or archived) would otherwise
			// pin it with no way back for anyone. It is shown for the give-back only: no body, no
			// coordinates to launch from.
			wire.Branch, wire.RepoRemote, wire.HeadSha = "", "", ""
		} else {
			wire.Prompt = a.open(r.Context(), o)
		}
		out = append(out, wire)
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"offers": out})
}

// members maps the tenant's active members by membership ID. A member who left reads as the
// zero value, so their key and e-mail go out as "" rather than failing the list.
func (a sessionHandoffAPI) members(ctx context.Context, tenantID string) map[string]store.MemberInfo {
	out := map[string]store.MemberInfo{}
	rows, err := a.mgr.store.ListMembersByTenant(ctx, tenantID)
	if err != nil {
		return out
	}
	for _, m := range rows {
		out[m.MembershipID] = m
	}
	return out
}

// withdraw (DELETE /api/session-handoff-offers/{id}) is the owner's retraction.
//
// The Console also calls it when A gives up waiting and starts the work personally. The race
// between that start and an acceptance — the same work running twice — is closed by the
// conditional pending-to-withdrawn update: the loser gets a 409 and abandons its start
// (ADR 0057 decision 6).
func (a sessionHandoffAPI) withdraw(w http.ResponseWriter, r *http.Request, ident store.Identity, mv store.MembershipView) {
	o, ok, err := a.mgr.store.GetSessionHandoffOffer(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok || o.OwnerMembershipID != mv.MembershipID {
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "handoff offer not found"})
		return
	}
	changed, err := a.mgr.store.TransitionSessionHandoffOffer(r.Context(), o.ID, "pending", "withdrawn", store.NowTS(), "")
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !changed {
		a.writeUndecidable(w, r.Context(), o.ID)
		return
	}
	_ = a.mgr.store.InsertAudit(context.Background(), store.AuditLog{ID: store.NewID(), TenantID: mv.TenantID,
		ActorKind: "user", ActorID: ident.ID, Action: "session.handoff.withdraw", Target: o.SourceSessionName,
		HTTPStatus: http.StatusOK, At: store.NowTS()})
	writeJSON(w, http.StatusOK, map[string]any{"status": "withdrawn"})
}

func (a sessionHandoffAPI) statusOf(ctx context.Context, id string) string {
	o, ok, err := a.mgr.store.GetSessionHandoffOffer(ctx, id)
	if err != nil || !ok {
		return ""
	}
	return o.Status
}

// writeUndecidable answers a conditional update on a pending offer that changed nothing. An
// offer still pending was refused because the recipient holds it for a launch in progress,
// which the Console words differently from "already decided": the launch may yet fail and
// hand it back. takeState tells the sender whether the recipient is still starting it or the
// launch was sent and its result is being confirmed; it carries no token or key.
func (a sessionHandoffAPI) writeUndecidable(w http.ResponseWriter, ctx context.Context, id string) {
	o, ok, err := a.mgr.store.GetSessionHandoffOffer(ctx, id)
	status := ""
	if err == nil && ok {
		status = o.Status
	}
	if status == "pending" {
		take := store.HandoffTakeState(o, time.Now())
		if take == "" && !a.launchable(ctx, o) {
			// Unshared or archived with no take left: still pending for the sender, but nothing
			// may launch from it any more (TakeSessionHandoffOffer).
			writeHandoffErr(w, http.StatusConflict, "handoff_unavailable", "the handoff is no longer shared with you or its session was archived", offerStatusDetail(status))
			return
		}
		d := offerStatusDetail(status)
		d["takeState"] = take
		writeHandoffErr(w, http.StatusConflict, "handoff_being_taken", "the recipient is starting a session from this handoff", d)
		return
	}
	writeHandoffErr(w, http.StatusConflict, "handoff_already_decided", "the handoff was already decided", offerStatusDetail(status))
}

// launchable mirrors the store's handoffLaunchable for an answer's wording: the body is there
// and the source session is listed and not archived.
func (a sessionHandoffAPI) launchable(ctx context.Context, o store.SessionHandoffOffer) bool {
	if o.Ciphertext == "" {
		return false
	}
	c, ok, err := a.mgr.store.GetSharedSessionCatalog(ctx, o.CatalogID)
	return err == nil && ok && !c.Archived
}

// offerStatusDetail names the offer's status in an error. The Console reads offerStatus:
// its API client stamps every error's `status` with the HTTP status, so the string under
// `status` never reaches it. `status` stays for other callers that read it.
func offerStatusDetail(status string) map[string]any {
	return map[string]any{"status": status, "offerStatus": status}
}

// handoffTakeRef is the take a session create carries when it launches from an offer
// (handoff_take in the POST /api/sessions body).
type handoffTakeRef struct {
	OfferID string `json:"offerId"`
	Token   string `json:"token"`
}

// handoffCreateKey is the Agent idempotency key of the create one take makes. Bound to the
// take, so the CP can later ask the recipient's Agent what became of exactly that create
// (reconcileTake), and a new take after a release never collides with it.
func handoffCreateKey(offerID, token string) string { return "handoff-" + offerID + "-" + token }

// takeHandoffFence reads the handoff_take of a session-create body and replaces it with the
// take's idempotency key, so the Agent receives the body it always did plus a key the CP can
// look the create up by. nil means the create is not from an offer.
func takeHandoffFence(r *http.Request) (*handoffTakeRef, *apiError) {
	body, err := readAllBody(r)
	if err != nil {
		return nil, &apiError{http.StatusBadRequest, "bad_request", "cannot read the request body"}
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields["handoff_take"] == nil {
		restoreBody(r, body)
		return nil, nil
	}
	var take handoffTakeRef
	if json.Unmarshal(fields["handoff_take"], &take) != nil || strings.TrimSpace(take.OfferID) == "" || strings.TrimSpace(take.Token) == "" {
		restoreBody(r, body)
		return nil, &apiError{http.StatusBadRequest, "bad_handoff_take", "handoff_take needs an offer id and a token"}
	}
	delete(fields, "handoff_take")
	fields["idempotency_key"], _ = json.Marshal(handoffCreateKey(take.OfferID, take.Token))
	fields["idempotency_durable"] = json.RawMessage("true")
	stripped, err := json.Marshal(fields)
	if err != nil {
		restoreBody(r, body)
		return nil, internalErr(err)
	}
	restoreBody(r, stripped)
	return &take, nil
}

// reserveHandoffCreate records the take's key on the recipient's Agent after the take is
// confirmed and before the create is sent (issue #2040). The reservation is what lets the Agent later
// answer "not created" for the key (reconcileTake): a key it has no record of may have been
// created under by an Agent that kept none, so its absence proves nothing. An Agent without
// the route (no key header on its 404) gets the create anyway, and an unknown outcome there
// stays unknown. Any other failure refuses the create, and the caller gives the take back.
func reserveHandoffCreate(ctx context.Context, res *resolved, take handoffTakeRef) *apiError {
	key := handoffCreateKey(take.OfferID, take.Token)
	failed := &apiError{http.StatusBadGateway, "handoff_reserve_failed", "your workspace could not record the launch; nothing was started"}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		res.rt.Endpoint()+"/sessions-idempotency/"+url.PathEscape(key)+"/reserve", nil)
	if err != nil {
		return failed
	}
	if res.rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+res.rt.Token())
	}
	resp, err := agentRelayClient.Do(req)
	if err != nil {
		return failed
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode == http.StatusOK && resp.Header.Get(createKeyHeader) == key:
		return nil
	case resp.StatusCode == http.StatusNotFound && resp.Header.Get(createKeyHeader) == "":
		return nil
	default:
		return failed
	}
}

// confirmHandoffTake is the commit point of a launch from an offer, run by sessionCreateThen
// immediately before the Agent is asked (issue #2020). Until here the sender may still have
// won: a take that went quiet past the take window loses the offer to their withdraw, and a
// Console that resumes after that must not start the work. After here the take holds until
// it is accepted or released (store.ConfirmSessionHandoffTake), whatever the time.
func confirmHandoffTake(ctx context.Context, st store.Store, mv store.MembershipView, take handoffTakeRef) *apiError {
	ok, err := st.ConfirmSessionHandoffTake(ctx, take.OfferID, mv.MembershipID, take.Token, store.NowTS())
	if err != nil {
		return internalErr(err)
	}
	if !ok {
		if token, held, _ := st.ConfirmedSessionHandoffTake(ctx, take.OfferID, mv.MembershipID); held && token == take.Token {
			return &apiError{http.StatusConflict, "handoff_take_used", "this launch of the handoff already reached your workspace"}
		}
		return &apiError{http.StatusConflict, "handoff_take_lost", "the handoff was withdrawn or taken elsewhere before the session was created"}
	}
	return nil
}

// settleHandoffCreate settles a confirmed take from the Agent's answer to its create, so the
// offer no longer depends on the Console hearing that answer (issue #2040). Only the Agent's
// own word moves it, and that word is the answer's create headers naming this take's key
// (createKeyHeader / createOutcomeHeader, which a proxy in between does not write):
//   - created with a session name: the offer is accepted with that session;
//   - refused: no session came of it, nor ever will under this key (the Agent keeps a durable
//     key refused for good, and only the create that owns the key reports an outcome), so the
//     take is given back. The Console's driver fallback starts over under a new take.
//
// Anything else — no answer, a gateway's or proxy's own error, JSON or not, an Agent too old
// to write the headers, a create still in progress — is an unknown outcome: the take stays
// confirmed and the recipient's next take press asks their Agent (reconcileTake). Releasing
// there would hand the offer back while the session may exist (ADR 0057 decision 6).
//
// snap is the offer as it stood when the take was confirmed, so a session reported after the
// offer is gone (the sender deleted the source session) still reaches the recipient.
func settleHandoffCreate(ctx context.Context, mgr *manager, res *resolved, take handoffTakeRef, snap store.SessionHandoffOffer, status int, h http.Header, body []byte) {
	ctx = context.WithoutCancel(ctx)
	key := handoffCreateKey(take.OfferID, take.Token)
	outcome := ""
	if h.Get(createKeyHeader) == key {
		outcome = h.Get(createOutcomeHeader)
	}
	switch {
	case outcome == "created" && status >= 200 && status < 300:
		var s struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(body, &s) != nil || s.Name == "" {
			log.Printf("handoff: offer %s left taken: the Agent answered %d without a session name", take.OfferID, status)
			return
		}
		api := sessionHandoffAPI{memberAuth: memberAuth{mgr}}
		accepted, err := api.acceptTake(ctx, res.ident, res.mv, take.OfferID, take.Token, s.Name)
		if err != nil {
			log.Printf("handoff: offer %s left taken: accepting %s failed: %v", take.OfferID, s.Name, err)
		} else if !accepted {
			api.reportLateSession(ctx, res.ident, res.mv, snap, s.Name)
		}
	case outcome == "refused" && (status < 200 || status >= 300):
		if _, err := mgr.store.ReleaseSessionHandoffOffer(ctx, take.OfferID, res.mv.MembershipID, take.Token); err != nil {
			log.Printf("handoff: offer %s: releasing the take after a refused create failed: %v", take.OfferID, err)
		}
	default:
		log.Printf("handoff: offer %s left taken after create status %d (outcome %q)", take.OfferID, status, outcome)
	}
}

// createKeyHeader and createOutcomeHeader are the Agent's create headers
// (workspace/agent sessionx createOutcomeWriter): the key the answer is about, and created or
// refused.
const (
	createKeyHeader     = "X-AF-Create-Key"
	createOutcomeHeader = "X-AF-Create-Outcome"
)

// acceptTake settles a take as accepted by the session it created and records the decision.
// accepted=false means the offer is no longer held by this take.
func (a sessionHandoffAPI) acceptTake(ctx context.Context, ident store.Identity, mv store.MembershipView, offerID, token, sessionName string) (bool, error) {
	o, ok, err := a.mgr.store.GetSessionHandoffOffer(ctx, offerID)
	if err != nil || !ok || o.RecipientMembershipID != mv.MembershipID {
		return false, err
	}
	accepted, err := a.mgr.store.AcceptSessionHandoffOffer(ctx, offerID, mv.MembershipID, token, store.NowTS(), sessionName)
	if err != nil || !accepted {
		return false, err
	}
	a.recordDecision(ident, mv, o, "accepted", sessionName)
	return true, nil
}

// recipientOffer loads an offer for its recipient, answering 404 for anyone else: as with
// sharing, not even existence is disclosed.
func (a sessionHandoffAPI) recipientOffer(w http.ResponseWriter, r *http.Request, mv store.MembershipView) (store.SessionHandoffOffer, bool) {
	o, ok, err := a.mgr.store.GetSessionHandoffOffer(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return o, false
	}
	if !ok || o.RecipientMembershipID != mv.MembershipID {
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "handoff offer not found"})
		return o, false
	}
	return o, true
}

type handoffTakeBody struct {
	Token       string `json:"token"`
	SessionName string `json:"sessionName"`
}

func readHandoffTakeBody(r *http.Request) handoffTakeBody {
	var in handoffTakeBody
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in)
	in.Token, in.SessionName = strings.TrimSpace(in.Token), strings.TrimSpace(in.SessionName)
	return in
}

// take (POST /api/session-handoff-offers/{id}/take) reserves a pending offer for the
// recipient's launch, BEFORE the session is created (issue #2020). The answer carries the
// token that later accepts or releases it. An offer that is no longer pending — withdrawn
// by the sender, expired, revoked with the share — is refused with its status, so the
// Console makes no session and says why.
//
// The take decides between the two sides (ADR 0057 decision 6): the sender's withdraw,
// which their own launch performs first, is a conditional update that a holding take
// refuses, and a take is refused once the withdraw landed. The commit point is the create
// that carries the take (confirmHandoffTake): only from there on does the take hold without
// a time limit. Nothing here touches anyone's
// Workspace (decision 3); the recipient's Console still creates the session itself.
func (a sessionHandoffAPI) take(w http.ResponseWriter, r *http.Request, ident store.Identity, mv store.MembershipView) {
	a.expireDue(r.Context()) // an overdue offer answers "expired", not "pending"
	o, ok := a.recipientOffer(w, r, mv)
	if !ok {
		return
	}
	token := store.NewID()
	taken, err := a.mgr.store.TakeSessionHandoffOffer(r.Context(), o.ID, mv.MembershipID, token, store.NowTS())
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !taken {
		a.reconcileTake(w, r, ident, mv, o, readHandoffTakeBody(r).Token)
		return
	}
	a.writeTaken(w, ident, mv, o, token)
}

func (a sessionHandoffAPI) writeTaken(w http.ResponseWriter, ident store.Identity, mv store.MembershipView, o store.SessionHandoffOffer, token string) {
	_ = a.mgr.store.InsertAudit(context.Background(), store.AuditLog{ID: store.NewID(), TenantID: mv.TenantID,
		ActorKind: "user", ActorID: ident.ID, Action: "session.handoff.take", Target: o.SourceSessionName,
		HTTPStatus: http.StatusOK, At: store.NowTS()})
	writeJSON(w, http.StatusOK, map[string]any{"status": "pending", "token": token})
}

// reconcileTake answers a take press that found the offer held. A Console that lost its
// previous launch's answer sends that take's token (prev): when the take is still
// unconfirmed, nothing reached the Agent under it, so it is released and this press takes
// the offer afresh. When the hold is this recipient's own confirmed take — an earlier launch
// whose create reached their Agent and whose outcome nobody learnt (settleHandoffCreate) — it asks their Agent to abandon that
// create's idempotency key, which reports a create that began and otherwise records the key
// so that a create arriving late is refused. It never creates anything itself:
//   - the session exists: the offer is accepted with it, and the answer names it;
//   - still being created: busy, as for any held offer;
//   - the Agent answers not_created (the key is now recorded as abandoned): the stale take
//     is released and this press takes the offer afresh;
//   - anything else (the Agent is unreachable, has no record of the key — it was never
//     reserved there, as with an Agent from before reserveHandoffCreate — its record is
//     unreadable, or it is too old to abandon; every answer must name the key in
//     createKeyHeader):
//     handoff_launch_unknown, and the take stays.
//
// The Agent is asked only on the recipient's own press, in their own workspace (ADR 0057
// decision 3). A confirmed take therefore holds only until the recipient next presses
// launch on the offer, and only when the CP never saw the Agent's answer.
func (a sessionHandoffAPI) reconcileTake(w http.ResponseWriter, r *http.Request, ident store.Identity, mv store.MembershipView, o store.SessionHandoffOffer, prev string) {
	ctx := r.Context()
	released, err := a.mgr.store.ReleaseUnconfirmedSessionHandoffTake(ctx, o.ID, mv.MembershipID, prev)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if released {
		a.retake(w, ctx, ident, mv, o)
		return
	}
	token, held, err := a.mgr.store.ConfirmedSessionHandoffTake(ctx, o.ID, mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !held {
		a.writeUndecidable(w, ctx, o.ID)
		return
	}
	unknown := func() {
		writeHandoffErr(w, http.StatusConflict, "handoff_launch_unknown",
			"a session may already have been started from this handoff; check your sessions", offerStatusDetail("pending"))
	}
	id := a.mgr.resolveIdentity(r)
	res, aerr := a.mgr.resolveFull(ctx, id.key, id.email, tenantSel(r))
	if aerr != nil || res.mv.MembershipID != mv.MembershipID {
		unknown()
		return
	}
	if a.ws.autostart {
		if aerr := a.ws.ensureWorkspaceReady(ctx, res); aerr != nil {
			unknown()
			return
		}
	} else if res.rt.State(ctx) != "running" {
		unknown()
		return
	}
	key := handoffCreateKey(o.ID, token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		res.rt.Endpoint()+"/sessions-idempotency/"+url.PathEscape(key)+"/abandon", nil)
	if err != nil {
		unknown()
		return
	}
	if res.rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+res.rt.Token())
	}
	resp, err := agentRelayClient.Do(req)
	if err != nil {
		unknown()
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.Header.Get(createKeyHeader) != key {
		// Not the Agent's answer about this key (an Agent from before abandon, or something
		// in between): it proves nothing either way.
		unknown()
		return
	}
	switch {
	case resp.StatusCode == http.StatusOK:
		var s struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(b, &s) != nil || s.Name == "" {
			unknown()
			return
		}
		accepted, err := a.acceptTake(ctx, ident, mv, o.ID, token, s.Name)
		if err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
		if !accepted {
			if a.reportLateSession(ctx, ident, mv, o, s.Name) {
				writeHandoffErr(w, http.StatusConflict, "handoff_session_after_give_back",
					"a session was started from this handoff after it was given back", map[string]any{
						"offerStatus": a.statusOf(ctx, o.ID), "sessionName": s.Name})
				return
			}
			a.writeUndecidable(w, ctx, o.ID)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "accepted", "sessionName": s.Name})
	case resp.StatusCode == http.StatusAccepted:
		writeHandoffErr(w, http.StatusConflict, "handoff_being_taken", "the recipient is starting a session from this handoff", offerStatusDetail("pending"))
	case resp.StatusCode == http.StatusNotFound && agentErrCode(b) == "not_created":
		// The Agent recorded the key as abandoned: the confirmed create never ran and never
		// will (a late one is refused), so the take can go.
		if _, err := a.mgr.store.ReleaseSessionHandoffOffer(ctx, o.ID, mv.MembershipID, token); err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
		a.retake(w, ctx, ident, mv, o)
	default:
		unknown()
	}
}

// agentErrCode is the error code of an Agent's JSON error answer, "" for anything else.
func agentErrCode(body []byte) string {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	return e.Error.Code
}

// retake takes the offer under a new token after reconcileTake gave a stale take back.
func (a sessionHandoffAPI) retake(w http.ResponseWriter, ctx context.Context, ident store.Identity, mv store.MembershipView, o store.SessionHandoffOffer) {
	fresh := store.NewID()
	taken, err := a.mgr.store.TakeSessionHandoffOffer(ctx, o.ID, mv.MembershipID, fresh, store.NowTS())
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !taken {
		a.writeUndecidable(w, ctx, o.ID)
		return
	}
	a.writeTaken(w, ident, mv, o, fresh)
}

// reportLateSession handles the recipient's Agent reporting a session under a take that no
// longer holds the offer: the create's answer, or a reconcile's, arrived after the recipient
// gave the take back (giveBack). The offer is left as it is — it may be pending again,
// retaken, withdrawn or accepted by another launch, and the sender may already be acting on
// it — and the recipient is told instead, through a notification that reaches them whether or
// not a Console is open, because only they can decide what to do with the session. Answers
// false, telling nothing, when the offer was accepted by this very session (a concurrent press
// settled it first).
//
// snap is the recipient's offer as read before the Agent was asked. When the offer has since
// been deleted with its source session, the report still goes to that recipient from snap,
// with offerStatus "": the offer is not restored.
func (a sessionHandoffAPI) reportLateSession(ctx context.Context, ident store.Identity, mv store.MembershipView, snap store.SessionHandoffOffer, sessionName string) bool {
	o, status := snap, ""
	if cur, ok, err := a.mgr.store.GetSessionHandoffOffer(ctx, snap.ID); err == nil && ok {
		o, status = cur, cur.Status
	}
	if o.RecipientMembershipID != mv.MembershipID {
		return false
	}
	if status == "accepted" && o.AcceptedSessionName == sessionName {
		return false
	}
	offerID := o.ID
	log.Printf("handoff: offer %s: the recipient's Agent reported a session after the take was given back", offerID)
	// One notice per reported session: two launches given back could each turn up.
	a.notify(mv.MembershipID, "handoff-late-session", "handoff-late-session-"+offerID+"-"+sessionName+"-"+mv.MembershipID,
		"session", sessionName, o.SourceSessionKind, o.Title,
		map[string]any{"offerId": offerID, "sessionName": sessionName, "offerStatus": status})
	_ = a.mgr.store.InsertAudit(context.Background(), store.AuditLog{ID: store.NewID(), TenantID: mv.TenantID,
		ActorKind: "user", ActorID: ident.ID, Action: "session.handoff.late_session", Target: o.SourceSessionName,
		HTTPStatus: http.StatusOK, At: store.NowTS()})
	return true
}

// giveBack (POST /api/session-handoff-offers/{id}/give-back) is the recipient's way out of a
// launch whose outcome stays unknown (issue #2058): "this session is not in my workspace —
// give the offer back". It releases only a take in store.HandoffTakeState "unknown" — confirmed,
// and older than the take window, so its create has had that long to answer — in one
// conditional update; anything else is refused with the take's state, and nobody but the
// recipient learns the offer exists. The body must carry confirmed=true and the take's
// takeRev from the inbox: the Console sends them only after the recipient acknowledged that
// the same work may run twice if the session exists, or if the earlier launch is still being
// processed and starts later — giving back cancels nothing on the Agent, and the window is a
// least wait, not a point by which a create has finished. The statement is the recipient's, so the CP still drives nobody's workspace
// (ADR 0057 decision 3); if their Agent reports the session later, reportLateSession tells
// them.
func (a sessionHandoffAPI) giveBack(w http.ResponseWriter, r *http.Request, ident store.Identity, mv store.MembershipView) {
	var in struct {
		Confirmed bool   `json:"confirmed"`
		TakeRev   string `json:"takeRev"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in)
	o, ok := a.recipientOffer(w, r, mv)
	if !ok {
		return
	}
	if !in.Confirmed {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "handoff_give_back_unconfirmed", "giving the handoff back needs confirmed=true"})
		return
	}
	// The confirmation names the take it was made against (a missing takeRev names none). A
	// later take — the one confirmed was given back and the offer taken again, while this
	// screen or request was stale — is refused, and the update below is bound to the token
	// checked here. With no confirmed take there is nothing to bind; the update then changes
	// nothing and the answer says why.
	token, held, err := a.mgr.store.ConfirmedSessionHandoffTake(r.Context(), o.ID, mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if held && handoffTakeRev(token) != in.TakeRev {
		cur, _, _ := a.mgr.store.GetSessionHandoffOffer(r.Context(), o.ID)
		d := offerStatusDetail(cur.Status)
		d["takeState"] = store.HandoffTakeState(cur, time.Now())
		writeHandoffErr(w, http.StatusConflict, "handoff_give_back_stale", "the handoff is held by another launch than the one confirmed", d)
		return
	}
	released, err := a.mgr.store.GiveBackUnknownSessionHandoffTake(r.Context(), o.ID, mv.MembershipID, token, time.Now())
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !released {
		cur, ok, err := a.mgr.store.GetSessionHandoffOffer(r.Context(), o.ID)
		if err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
		if !ok || cur.Status != "pending" {
			writeHandoffErr(w, http.StatusConflict, "handoff_already_decided", "the handoff was already decided", offerStatusDetail(cur.Status))
			return
		}
		d := offerStatusDetail(cur.Status)
		d["takeState"] = store.HandoffTakeState(cur, time.Now())
		writeHandoffErr(w, http.StatusConflict, "handoff_not_unknown", "the handoff is not held by a launch whose outcome is unknown", d)
		return
	}
	_ = a.mgr.store.InsertAudit(context.Background(), store.AuditLog{ID: store.NewID(), TenantID: mv.TenantID,
		ActorKind: "user", ActorID: ident.ID, Action: "session.handoff.give_back", Target: o.SourceSessionName,
		HTTPStatus: http.StatusOK, At: store.NowTS()})
	// Usually pending again; expired when the share was revoked during the launch.
	writeJSON(w, http.StatusOK, map[string]any{"status": a.statusOf(r.Context(), o.ID)})
}

// release (POST /api/session-handoff-offers/{id}/release) gives an unconfirmed take back after
// the launch failed or was abandoned, so the offer is pending again and the sender can
// withdraw it or the recipient retry. A release that no longer matches the take answers 409; the
// Console ignores that, since the offer has moved on either way.
func (a sessionHandoffAPI) release(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	in := readHandoffTakeBody(r)
	o, ok := a.recipientOffer(w, r, mv)
	if !ok {
		return
	}
	// Only an unconfirmed take is given back on the Console's word. A confirmed one may have
	// made its session: a Console, current or older, that read a gateway's error as a failure
	// must not hand the offer to the sender. reconcileTake settles it instead.
	released, err := a.mgr.store.ReleaseUnconfirmedSessionHandoffTake(r.Context(), o.ID, mv.MembershipID, in.Token)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !released {
		if token, held, _ := a.mgr.store.ConfirmedSessionHandoffTake(r.Context(), o.ID, mv.MembershipID); held && token == in.Token {
			writeHandoffErr(w, http.StatusConflict, "handoff_launch_unknown",
				"the launch reached your workspace and its outcome is not known; launch again to check", offerStatusDetail("pending"))
			return
		}
		writeHandoffErr(w, http.StatusConflict, "handoff_not_taken", "this launch does not hold the handoff", offerStatusDetail(a.statusOf(r.Context(), o.ID)))
		return
	}
	// Usually pending again; expired when the share was revoked during a confirmed launch.
	writeJSON(w, http.StatusOK, map[string]any{"status": a.statusOf(r.Context(), o.ID)})
}

// accept (POST /api/session-handoff-offers/{id}/accept) settles the recipient's take once
// the session exists, naming the session that took the work.
//
// The start itself is done by B's Console through the existing POST /sessions (ADR 0057
// decision 3). Starting it here instead would have CP operating someone else's Workspace —
// exactly the structure this feature was built to avoid.
//
// Without a token it is the report a Console from before the take sends after its launch:
// it still lands on an untaken pending offer, so such a tab is not left with a session the
// sender never hears about.
func (a sessionHandoffAPI) accept(w http.ResponseWriter, r *http.Request, ident store.Identity, mv store.MembershipView) {
	in := readHandoffTakeBody(r)
	if in.Token == "" {
		a.decide(w, r, ident, mv, "accepted", in.SessionName)
		return
	}
	o, ok := a.recipientOffer(w, r, mv)
	if !ok {
		return
	}
	accepted, err := a.acceptTake(r.Context(), ident, mv, o.ID, in.Token, in.SessionName)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !accepted {
		// The CP settles the take itself when it sees the create succeed
		// (settleHandoffCreate), so the Console's own accept usually finds it done.
		if cur, ok, _ := a.mgr.store.GetSessionHandoffOffer(r.Context(), o.ID); ok && cur.Status == "accepted" &&
			in.SessionName != "" && cur.AcceptedSessionName == in.SessionName {
			writeJSON(w, http.StatusOK, map[string]any{"status": "accepted"})
			return
		}
		writeHandoffErr(w, http.StatusConflict, "handoff_not_taken", "this launch does not hold the handoff", offerStatusDetail(a.statusOf(r.Context(), o.ID)))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "accepted"})
}

// decline handles POST /api/session-handoff-offers/{id}/decline. No reason is asked for
// (ADR 0057 decision 8).
func (a sessionHandoffAPI) decline(w http.ResponseWriter, r *http.Request, ident store.Identity, mv store.MembershipView) {
	a.decide(w, r, ident, mv, "declined", "")
}

func (a sessionHandoffAPI) decide(w http.ResponseWriter, r *http.Request, ident store.Identity, mv store.MembershipView, to, sessionName string) {
	o, ok := a.recipientOffer(w, r, mv)
	if !ok {
		return
	}
	changed, err := a.mgr.store.TransitionSessionHandoffOffer(r.Context(), o.ID, "pending", to, store.NowTS(), sessionName)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !changed {
		a.writeUndecidable(w, r.Context(), o.ID)
		return
	}
	a.recordDecision(ident, mv, o, to, sessionName)
	writeJSON(w, http.StatusOK, map[string]any{"status": to})
}

// recordDecision is what follows a recipient's decision that landed: the owner hears about
// an acceptance (decline has no notification, ADR 0057 decision 8) and the audit log records
// it.
func (a sessionHandoffAPI) recordDecision(ident store.Identity, mv store.MembershipView, o store.SessionHandoffOffer, to, sessionName string) {
	if to == "accepted" {
		a.notifyAccepted(o, mv.TenantID, sessionName)
	}
	_ = a.mgr.store.InsertAudit(context.Background(), store.AuditLog{ID: store.NewID(), TenantID: mv.TenantID,
		ActorKind: "user", ActorID: ident.ID, Action: "session.handoff." + to, Target: o.SourceSessionName,
		HTTPStatus: http.StatusOK, At: store.NowTS()})
}

// expireDue expires overdue offers and tells the owner exactly once. It rides along with a
// list read so this feature needs no worker of its own; expiring a few minutes late does no
// harm.
func (a sessionHandoffAPI) expireDue(ctx context.Context) {
	expired, err := a.mgr.store.ExpireSessionHandoffOffers(ctx, store.NowTS())
	if err != nil {
		return
	}
	for _, o := range expired {
		a.notify(o.OwnerMembershipID, "handoff-expired", "handoff-expired-"+o.ID+"-"+o.OwnerMembershipID,
			"session", o.SourceSessionName, o.SourceSessionKind, o.Title,
			map[string]any{"offerId": o.ID, "sessionName": o.SourceSessionName})
	}
}

// notifyOffered and notifyAccepted insert notifications from CP directly (docs/log/77
// §77.9). Ordinary session notifications travel by draining the Agent's outbox, but a
// handoff typically happens while the Workspace on either side is stopped, and nothing would
// arrive that way.
func (a sessionHandoffAPI) notifyOffered(o store.SessionHandoffOffer, _ string, c store.SharedSessionCatalog) {
	title := o.Title
	if title == "" {
		title = c.Name
	}
	a.notify(o.RecipientMembershipID, "handoff-offer", "handoff-offer-"+o.ID+"-"+o.RecipientMembershipID,
		"shared-session", o.CatalogID, o.SourceSessionKind, title,
		map[string]any{"offerId": o.ID, "catalogId": o.CatalogID, "sessionName": o.SourceSessionName})
}

func (a sessionHandoffAPI) notifyAccepted(o store.SessionHandoffOffer, _ string, sessionName string) {
	a.notify(o.OwnerMembershipID, "handoff-accepted", "handoff-accepted-"+o.ID+"-"+o.OwnerMembershipID,
		"session", o.SourceSessionName, o.SourceSessionKind, o.Title,
		map[string]any{"offerId": o.ID, "sessionName": o.SourceSessionName, "acceptedSessionName": sessionName})
}

// notify inserts one notification. InsertNotification is idempotent on ON CONFLICT(event_id)
// alone, which does not include the membership, so eventID must always mix in the recipient:
// reusing one id for two notifications silently drops one of them.
func (a sessionHandoffAPI) notify(membershipID, kind, eventID, targetType, targetID, targetKind, displayName string, payload map[string]any) {
	b, _ := json.Marshal(payload)
	_ = a.mgr.store.InsertNotification(context.Background(), store.Notification{
		EventID: eventID, MembershipID: membershipID, Kind: kind,
		TargetType: targetType, TargetID: targetID, TargetKind: targetKind,
		DisplayName: displayName, Payload: string(b), CreatedAt: store.NowTS(),
	})
}
