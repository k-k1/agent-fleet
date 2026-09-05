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
	"encoding/base64"
	"encoding/json"
	"io"
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
}

func newSessionHandoffAPI(m *manager, share sessionShareAPI) sessionHandoffAPI {
	return sessionHandoffAPI{memberAuth: memberAuth{m}, share: share}
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
}

// handoffOfferInboxWire is one entry of the recipient's inbox: the base form plus three
// keys, 16 in all. The three cannot be optional fields on the base type — ownerUserKey is ""
// when the member is unknown and prompt is "" when the ciphertext cannot be opened, and both
// still have to appear — so they are added by embedding, which JSON flattens.
//
// was: map[string]any (handoffOfferDTO's return value) with listReceived adding three keys.
//
// The effective JSON names of the base and the three additions must not collide:
// encoding/json emits neither side of a collision, so a key would vanish silently. The
// equivalence test catches that, and the counts 13 / 16 are pinned separately.
type handoffOfferInboxWire struct {
	handoffOfferWire
	OwnerUserKey      string `json:"ownerUserKey"`
	Prompt            string `json:"prompt"`
	SourceSessionKind string `json:"sourceSessionKind"`
}

func handoffOfferDTO(o store.SessionHandoffOffer, recipientKey string) handoffOfferWire {
	return handoffOfferWire{
		ID: o.ID, SessionID: o.CatalogID, SessionName: o.SourceSessionName,
		RecipientUserKey: recipientKey, Title: o.Title, Status: o.Status,
		Branch: o.Branch, RepoRemote: o.RepoRemote, HeadSha: o.HeadSha,
		CreatedAt: o.CreatedAt, ExpiresAt: o.ExpiresAt, DecidedAt: o.DecidedAt,
		AcceptedSessionName: o.AcceptedSessionName,
	}
}

// catalogForOwnedSession looks up the owner's own catalog row by name. ok=false means the
// row is absent, which is not an error — the caller decides what it means (a read takes it
// as "not shared with anyone yet", a write answers 404).
//
// How often it syncs decides how this behaves. syncCatalogLocked is two round trips to the
// owner Agent (/sessions/catalog and /repos), and /repos runs git per working copy, so it
// gets heavier with every worktree. That is why shared reads are thinned through
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
		lock := a.mgr.shareLockFor(res.mv.MembershipID)
		lock.Lock()
		_, err := a.share.syncCatalogLocked(r.Context(), res)
		lock.Unlock()
		if err != nil {
			a.share.invalidateCatalog(res.mv.MembershipID) // a failed sync must not count as fresh
			return store.SharedSessionCatalog{}, false, &apiError{http.StatusConflict, "workspace_not_running", "owner workspace must be running to hand off"}
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
	ctx, _, e := a.handoffContext(r.Context(), res, name)
	if e != nil {
		writeAPIErr(w, e)
		return
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
	writeJSON(w, http.StatusOK, map[string]any{"members": members, "context": ctx, "pendingOfferId": pending})
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

// create handles POST /api/session-handoff-offers.
func (a sessionHandoffAPI) create(w http.ResponseWriter, r *http.Request, res *resolved) {
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
		RepoRemote: str("remote"), Branch: str("branch"), HeadSha: str("headSha"),
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
	writeJSON(w, http.StatusCreated, handoffOfferDTO(o, recipient.UserKey))
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
	keys := a.userKeys(r.Context(), mv.TenantID)
	out := []any{}
	for _, o := range rows {
		out = append(out, handoffOfferDTO(o, keys[o.RecipientMembershipID]))
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"offers": out})
}

// listReceived (GET /api/session-handoff-offers/received) is B's inbox. It returns the
// prompt because deciding whether to accept requires reading it, and this is the only place
// the prompt leaves CP.
func (a sessionHandoffAPI) listReceived(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	a.expireDue(r.Context())
	rows, err := a.mgr.store.ListSessionHandoffOffersByRecipient(r.Context(), mv.MembershipID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	keys := a.userKeys(r.Context(), mv.TenantID)
	out := []any{}
	for _, o := range rows {
		out = append(out, handoffOfferInboxWire{
			handoffOfferWire:  handoffOfferDTO(o, keys[o.RecipientMembershipID]),
			OwnerUserKey:      keys[o.OwnerMembershipID],
			Prompt:            a.open(r.Context(), o),
			SourceSessionKind: o.SourceSessionKind,
		})
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"offers": out})
}

func (a sessionHandoffAPI) userKeys(ctx context.Context, tenantID string) map[string]string {
	out := map[string]string{}
	rows, err := a.mgr.store.ListMembersByTenant(ctx, tenantID)
	if err != nil {
		return out
	}
	for _, m := range rows {
		out[m.MembershipID] = m.UserKey
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
		writeHandoffErr(w, http.StatusConflict, "handoff_already_decided", "the handoff was already decided", map[string]any{"status": a.statusOf(r.Context(), o.ID)})
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

// accept (POST /api/session-handoff-offers/{id}/accept) is the recipient's report after the
// session has already been started.
//
// The start itself is done by B's Console through the existing POST /sessions (ADR 0057
// decision 3). Starting it here instead would have CP operating someone else's Workspace —
// exactly the structure this feature was built to avoid.
func (a sessionHandoffAPI) accept(w http.ResponseWriter, r *http.Request, ident store.Identity, mv store.MembershipView) {
	var in struct {
		SessionName string `json:"sessionName"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in)
	a.decide(w, r, ident, mv, "accepted", strings.TrimSpace(in.SessionName))
}

// decline handles POST /api/session-handoff-offers/{id}/decline. No reason is asked for
// (ADR 0057 decision 8).
func (a sessionHandoffAPI) decline(w http.ResponseWriter, r *http.Request, ident store.Identity, mv store.MembershipView) {
	a.decide(w, r, ident, mv, "declined", "")
}

func (a sessionHandoffAPI) decide(w http.ResponseWriter, r *http.Request, ident store.Identity, mv store.MembershipView, to, sessionName string) {
	o, ok, err := a.mgr.store.GetSessionHandoffOffer(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// 404 for anyone without the right to it: as with sharing, not even existence is
	// disclosed.
	if !ok || o.RecipientMembershipID != mv.MembershipID {
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "handoff offer not found"})
		return
	}
	changed, err := a.mgr.store.TransitionSessionHandoffOffer(r.Context(), o.ID, "pending", to, store.NowTS(), sessionName)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !changed {
		writeHandoffErr(w, http.StatusConflict, "handoff_already_decided", "the handoff was already decided", map[string]any{"status": a.statusOf(r.Context(), o.ID)})
		return
	}
	if to == "accepted" {
		a.notifyAccepted(o, mv.TenantID, sessionName)
	}
	_ = a.mgr.store.InsertAudit(context.Background(), store.AuditLog{ID: store.NewID(), TenantID: mv.TenantID,
		ActorKind: "user", ActorID: ident.ID, Action: "session.handoff." + to, Target: o.SourceSessionName,
		HTTPStatus: http.StatusOK, At: store.NowTS()})
	writeJSON(w, http.StatusOK, map[string]any{"status": to})
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
