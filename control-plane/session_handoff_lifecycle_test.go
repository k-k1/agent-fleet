package main

// The member handover lifecycle (ADR 0057) between two memberships of one tenant, through
// the CP handlers, with the owner's Agent replaced by an httptest server. Each test names
// the steps of the two-account run sheet on #1960 it stands in for (H-n).

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

type handoffPerson struct {
	ident store.Identity
	mv    store.MembershipView
}

type handoffLife struct {
	t         *testing.T
	st        *store.SQL
	api       sessionHandoffAPI
	ownerRes  *resolved
	owner     handoffPerson
	recipient handoffPerson
	outsider  handoffPerson // same tenant, nothing shared
	foreigner handoffPerson // another tenant
	shareID   string

	mu       sync.Mutex
	agentCtx map[string]any
	hits     map[string]int
	// push answers POST /sessions/session-1/handoff-push, called with f.mu held; nil
	// answers a plain 404 like an Agent whose mux lacks the route.
	push func(r *http.Request, body []byte) (int, map[string]any)
}

func newHandoffLife(t *testing.T) *handoffLife {
	t.Helper()
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, _ := st.EnsureDefaultTenant(ctx)
	elsewhere, err := st.CreateTenant(ctx, "elsewhere", "Elsewhere")
	if err != nil {
		t.Fatal(err)
	}
	person := func(email, key, tenantID string) handoffPerson {
		id, err := st.UpsertIdentity(ctx, email, key, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.EnsureMembership(ctx, id.ID, tenantID, "member"); err != nil {
			t.Fatal(err)
		}
		views, _ := st.ListMemberships(ctx, id.ID)
		for _, v := range views {
			if v.TenantID == tenantID {
				return handoffPerson{ident: id, mv: v}
			}
		}
		t.Fatalf("no membership of %s in %s", email, tenantID)
		return handoffPerson{}
	}
	f := &handoffLife{t: t, st: st, hits: map[string]int{},
		owner:     person("owner@example.com", "owner", tenant.ID),
		recipient: person("recipient@example.com", "recipient", tenant.ID),
		outsider:  person("outsider@example.com", "outsider", tenant.ID),
		foreigner: person("foreigner@example.com", "foreigner", elsewhere.ID),
		agentCtx: map[string]any{"repo": "private", "vcs": "git", "branch": "feature/x",
			"remote": "https://example.com/team/private.git", "headSha": "abcdef1234567890", "ahead": 0, "dirty": false},
	}
	ws := store.Workspace{ID: "ws-owner", TenantID: tenant.ID, MembershipID: f.owner.mv.MembershipID, ContainerName: "owner",
		Network: "test", DataDir: "/data/owner", AgentPort: "1", AgentToken: "tok", State: "running", CreatedAt: store.NowTS()}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.hits[r.URL.Path]++
		hctx := map[string]any{}
		for k, v := range f.agentCtx {
			hctx[k] = v
		}
		f.mu.Unlock()
		switch r.URL.Path {
		case "/sessions/catalog":
			_ = json.NewEncoder(w).Encode(map[string]any{"sessions": oneSession})
		case "/repos":
			_ = json.NewEncoder(w).Encode(map[string]any{"repos": []any{map[string]any{"workingCopyId": "wc-1"}}})
		case "/sessions/session-1/handoff-context":
			_ = json.NewEncoder(w).Encode(hctx)
		case "/sessions/session-1/handoff-push":
			f.mu.Lock()
			push := f.push
			if push == nil || r.Method != http.MethodPost {
				f.mu.Unlock()
				http.NotFound(w, r) // what an Agent's mux without the route answers
				return
			}
			raw, _ := io.ReadAll(r.Body)
			code, body := push(r, raw)
			f.mu.Unlock()
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(body)
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "not_found", "message": "no such session"}})
		}
	}))
	t.Cleanup(agent.Close)
	rt := stubRuntime{endpoint: agent.URL, token: "tok"}
	mgr := &manager{store: st, rts: map[string]cachedRT{f.owner.mv.MembershipID: {rt: rt, ws: ws}}}
	f.api = newSessionHandoffAPI(mgr, newSessionShareAPI(mgr), false)
	f.ownerRes = &resolved{ident: f.owner.ident, mv: f.owner.mv, ws: ws, rt: rt}
	f.shareID = "share-1"
	if err := st.PutSessionShare(ctx, store.SessionShare{ID: f.shareID, TenantID: tenant.ID,
		OwnerMembershipID: f.owner.mv.MembershipID, RecipientMembershipID: f.recipient.mv.MembershipID,
		ScopeType: "session", ScopeKey: "session-1", Permission: "ro", CreatedAt: store.NowTS(), UpdatedAt: store.NowTS()}); err != nil {
		t.Fatal(err)
	}
	return f
}

// setAgent changes what the owner Agent answers for the working copy's handoff context.
func (f *handoffLife) setAgent(kv map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, v := range kv {
		if v == nil {
			delete(f.agentCtx, k)
		} else {
			f.agentCtx[k] = v
		}
	}
}

func jsonBody(v any) *bytes.Reader {
	b, _ := json.Marshal(v)
	return bytes.NewReader(b)
}

// offer posts an offer as the owner.
func (f *handoffLife) offer(extra map[string]any) *httptest.ResponseRecorder {
	f.t.Helper()
	body := map[string]any{"sessionName": "session-1", "recipientUserKey": f.recipient.ident.UserKey,
		"title": "Carry on", "prompt": "Continue the work on feature/x."}
	for k, v := range extra {
		body[k] = v
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/session-handoff-offers", jsonBody(body))
	req.Header.Set("Content-Type", "application/json")
	f.api.create(rec, req, f.ownerRes)
	return rec
}

func (f *handoffLife) mustOffer(extra map[string]any) handoffOfferWire {
	f.t.Helper()
	rec := f.offer(extra)
	if rec.Code != http.StatusCreated {
		f.t.Fatalf("offer = %d %s, want 201", rec.Code, rec.Body.String())
	}
	var o handoffOfferWire
	if err := json.Unmarshal(rec.Body.Bytes(), &o); err != nil {
		f.t.Fatal(err)
	}
	return o
}

type membershipHandler func(http.ResponseWriter, *http.Request, store.Identity, store.MembershipView)

// as calls a member-scoped handoff handler on offer id as p.
func (f *handoffLife) as(p handoffPerson, h membershipHandler, method, id string, body any) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(method, "/api/session-handoff-offers/"+id, jsonBody(body))
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	h(rec, req, p.ident, p.mv)
	return rec
}

func errorOf(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var env struct {
		Error map[string]any `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return env.Error
}

func (f *handoffLife) requireConflict(rec *httptest.ResponseRecorder, code, status string) {
	f.t.Helper()
	if rec.Code != http.StatusConflict {
		f.t.Fatalf("answer = %d %s, want 409 %s", rec.Code, rec.Body.String(), code)
	}
	e := errorOf(f.t, rec)
	if e["code"] != code || (status != "" && (e["status"] != status || e["offerStatus"] != status)) {
		f.t.Fatalf("409 = %v, want code %s status %q", e, code, status)
	}
}

func (f *handoffLife) row(id string) store.SessionHandoffOffer {
	f.t.Helper()
	o, ok, err := f.st.GetSessionHandoffOffer(context.Background(), id)
	if err != nil || !ok {
		f.t.Fatalf("offer %s: ok=%v err=%v", id, ok, err)
	}
	return o
}

func (f *handoffLife) notifications(p handoffPerson) []store.Notification {
	f.t.Helper()
	ns, err := f.st.ListNotifications(context.Background(), p.mv.MembershipID, "", 100)
	if err != nil {
		f.t.Fatal(err)
	}
	return ns
}

func (f *handoffLife) inbox(p handoffPerson) []handoffOfferInboxWire {
	f.t.Helper()
	rec := httptest.NewRecorder()
	f.api.listReceived(rec, httptest.NewRequest(http.MethodGet, "/api/session-handoff-offers/received", nil), p.ident, p.mv)
	var out struct {
		Offers []handoffOfferInboxWire `json:"offers"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		f.t.Fatalf("inbox = %d %s", rec.Code, rec.Body.String())
	}
	return out.Offers
}

func (f *handoffLife) take(id string) string {
	f.t.Helper()
	rec := f.as(f.recipient, f.api.take, http.MethodPost, id, map[string]any{})
	var out struct {
		Token string `json:"token"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Token == "" {
		f.t.Fatalf("take = %d %s, want 200 with a token", rec.Code, rec.Body.String())
	}
	return out.Token
}

// TestHandoffLifecycleOfferTakeAccept walks the main path: the offer carries the Agent's
// coordinates without credentials (H-5), the recipient is notified with no workspace on
// either side being asked (H-6), reads it in the inbox (H-7), takes it, launches and
// accepts (H-10b), and the owner is notified of the acceptance under a different event id
// (H-11).
func TestHandoffLifecycleOfferTakeAccept(t *testing.T) {
	f := newHandoffLife(t)
	f.setAgent(map[string]any{"remote": "https://ci-bot:s3cret@example.com/team/private.git"})
	o := f.mustOffer(nil)
	if o.Status != "pending" || o.Branch != "feature/x" || o.HeadSha != "abcdef1234567890" {
		t.Fatalf("offer = %+v, want pending feature/x@abcdef1234567890 from the Agent", o)
	}
	if o.RepoRemote != "https://example.com/team/private.git" || strings.Contains(f.row(o.ID).RepoRemote, "s3cret") {
		t.Fatalf("repo remote = %q (row %q), want it without userinfo", o.RepoRemote, f.row(o.ID).RepoRemote)
	}

	// From here on nothing may need the owner's Agent: both workspaces may be stopped.
	agentCalls := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		n := 0
		for _, c := range f.hits {
			n += c
		}
		return n
	}
	before := agentCalls()

	offerEvent := "handoff-offer-" + o.ID + "-" + f.recipient.mv.MembershipID
	ns := f.notifications(f.recipient)
	if len(ns) != 1 || ns[0].EventID != offerEvent || ns[0].Kind != "handoff-offer" {
		t.Fatalf("recipient notifications = %+v, want one %s", ns, offerEvent)
	}
	in := f.inbox(f.recipient)
	if len(in) != 1 || in[0].ID != o.ID || in[0].Prompt != "Continue the work on feature/x." || in[0].OwnerUserKey != "owner" {
		t.Fatalf("recipient inbox = %+v", in)
	}
	if got := f.inbox(f.outsider); len(got) != 0 {
		t.Fatalf("an outsider's inbox holds %+v", got)
	}

	token := f.take(o.ID)
	rec := f.as(f.recipient, f.api.accept, http.MethodPost, o.ID, map[string]any{"sessionName": "taken-over", "token": token})
	if rec.Code != http.StatusOK {
		t.Fatalf("accept = %d %s", rec.Code, rec.Body.String())
	}
	row := f.row(o.ID)
	if row.Status != "accepted" || row.AcceptedSessionName != "taken-over" || row.Ciphertext == "" {
		t.Fatalf("after accept: %+v", row)
	}
	acceptedEvent := "handoff-accepted-" + o.ID + "-" + f.owner.mv.MembershipID
	ns = f.notifications(f.owner)
	if len(ns) != 1 || ns[0].EventID != acceptedEvent || !strings.Contains(ns[0].Payload, `"acceptedSessionName":"taken-over"`) {
		t.Fatalf("owner notifications = %+v, want one %s naming taken-over", ns, acceptedEvent)
	}
	if acceptedEvent == offerEvent {
		t.Fatal("the two notifications share an event id; one would silently replace the other")
	}
	if got := f.notifications(f.recipient); len(got) != 1 {
		t.Fatalf("the acceptance notification reached the recipient too: %+v", got)
	}
	if got := f.inbox(f.recipient); len(got) != 0 {
		t.Fatalf("the accepted offer stayed in the inbox: %+v", got)
	}
	if got := agentCalls(); got != before {
		t.Fatalf("notifying, the inbox, take and accept asked the owner Agent %d more time(s)", got-before)
	}
}

// TestHandoffTakeBeforeLaunch is issue #2020 through the handlers: the take happens before
// the session is created, the owner cannot withdraw while it holds, a failed launch gives
// it back, and a take on an offer the owner already withdrew is refused with the reason
// (H-13, H-14).
func TestHandoffTakeBeforeLaunch(t *testing.T) {
	f := newHandoffLife(t)
	o := f.mustOffer(nil)

	token := f.take(o.ID)
	f.requireConflict(f.as(f.owner, f.api.withdraw, http.MethodDelete, o.ID, nil), "handoff_being_taken", "pending")
	f.requireConflict(f.as(f.recipient, f.api.take, http.MethodPost, o.ID, map[string]any{}), "handoff_being_taken", "pending")
	f.requireConflict(f.as(f.recipient, f.api.decline, http.MethodPost, o.ID, map[string]any{}), "handoff_being_taken", "pending")
	f.requireConflict(f.as(f.recipient, f.api.release, http.MethodPost, o.ID, map[string]any{"token": "not-it"}), "handoff_not_taken", "pending")

	// The launch failed: the Console gives it back and the owner may withdraw again.
	if rec := f.as(f.recipient, f.api.release, http.MethodPost, o.ID, map[string]any{"token": token}); rec.Code != http.StatusOK {
		t.Fatalf("release = %d %s", rec.Code, rec.Body.String())
	}
	if row := f.row(o.ID); row.Status != "pending" || row.TakenAt != "" {
		t.Fatalf("after release: %+v", row)
	}
	f.requireConflict(f.as(f.recipient, f.api.accept, http.MethodPost, o.ID, map[string]any{"sessionName": "s", "token": token}), "handoff_not_taken", "pending")
	if rec := f.as(f.owner, f.api.withdraw, http.MethodDelete, o.ID, nil); rec.Code != http.StatusOK {
		t.Fatalf("withdraw after the release = %d %s", rec.Code, rec.Body.String())
	}

	// The recipient's dialog was still open: the take is refused and says why, so no
	// session is created.
	f.requireConflict(f.as(f.recipient, f.api.take, http.MethodPost, o.ID, map[string]any{}), "handoff_already_decided", "withdrawn")
	if got := f.notifications(f.owner); len(got) != 0 {
		t.Fatalf("owner notified about a withdrawn offer: %+v", got)
	}
}

// TestHandoffTakeOfExpiredOffer: an offer past its deadline answers "expired" to a take,
// even before anything else ran the expiry.
func TestHandoffTakeOfExpiredOffer(t *testing.T) {
	f := newHandoffLife(t)
	o := f.mustOffer(nil)
	if _, err := f.st.DB().Exec(`UPDATE session_handoff_offer SET expires_at='2000-01-01T00:00:00Z' WHERE id=?`, o.ID); err != nil {
		t.Fatal(err)
	}
	f.requireConflict(f.as(f.recipient, f.api.take, http.MethodPost, o.ID, map[string]any{}), "handoff_already_decided", "expired")
}

// TestHandoffAcceptWithoutTakeStillLands keeps a Console tab loaded before the take
// working: its token-less report after a launch still accepts an untaken pending offer.
func TestHandoffAcceptWithoutTakeStillLands(t *testing.T) {
	f := newHandoffLife(t)
	o := f.mustOffer(nil)
	if rec := f.as(f.recipient, f.api.accept, http.MethodPost, o.ID, map[string]any{"sessionName": "old-tab"}); rec.Code != http.StatusOK {
		t.Fatalf("token-less accept = %d %s", rec.Code, rec.Body.String())
	}
	if row := f.row(o.ID); row.Status != "accepted" || row.AcceptedSessionName != "old-tab" {
		t.Fatalf("after accept: %+v", row)
	}
}

// TestHandoffPushGateIsTheAgentsVerdict covers H-3 and H-4: what blocks an offer is the
// Agent's verdict alone. Each blocking token refuses with that reason and creates nothing;
// a dirty copy needs the acknowledgement; raw fields the Agent did not turn into a verdict
// (ahead, dirty) are not re-judged by the CP.
func TestHandoffPushGateIsTheAgentsVerdict(t *testing.T) {
	f := newHandoffLife(t)
	for _, reason := range []string{"no_upstream", "unpushed_commits", "detached_head"} {
		f.setAgent(map[string]any{"blocked": reason})
		rec := f.offer(map[string]any{"ackWarning": true})
		f.requireConflict(rec, "handoff_blocked", "")
		if e := errorOf(t, rec); e["reason"] != reason {
			t.Fatalf("blocked %s answered reason %v", reason, e["reason"])
		}
	}
	f.setAgent(map[string]any{"blocked": nil, "warning": "uncommitted_changes"})
	rec := f.offer(nil)
	f.requireConflict(rec, "handoff_warning", "")
	if e := errorOf(t, rec); e["reason"] != "uncommitted_changes" {
		t.Fatalf("warning answered reason %v", e["reason"])
	}
	if rows, _ := f.st.ListSessionHandoffOffersByOwner(context.Background(), f.owner.mv.MembershipID); len(rows) != 0 {
		t.Fatalf("a refused offer was stored: %+v", rows)
	}
	f.mustOffer(map[string]any{"ackWarning": true})

	// The raw state says "ahead and dirty", but the Agent's verdict is clear: the CP sends.
	g := newHandoffLife(t)
	g.setAgent(map[string]any{"ahead": 3, "dirty": true})
	g.mustOffer(nil)
}

// TestHandoffDeclineWithdrawSecondOfferUnshare covers H-12, H-13, H-15 and H-16: a decline
// tells the owner nothing and erases the body, a withdraw erases it, a second pending offer
// on the session is refused, and unsharing expires the pending one with its body.
func TestHandoffDeclineWithdrawSecondOfferUnshare(t *testing.T) {
	ctx := context.Background()
	f := newHandoffLife(t)

	declined := f.mustOffer(nil)
	if rec := f.as(f.recipient, f.api.decline, http.MethodPost, declined.ID, map[string]any{}); rec.Code != http.StatusOK {
		t.Fatalf("decline = %d %s", rec.Code, rec.Body.String())
	}
	if row := f.row(declined.ID); row.Status != "declined" || row.Ciphertext != "" {
		t.Fatalf("after decline: %+v", row)
	}
	if got := f.notifications(f.owner); len(got) != 0 {
		t.Fatalf("the owner was told about a decline: %+v", got)
	}

	withdrawn := f.mustOffer(nil)
	if rec := f.as(f.owner, f.api.withdraw, http.MethodDelete, withdrawn.ID, nil); rec.Code != http.StatusOK {
		t.Fatalf("withdraw = %d %s", rec.Code, rec.Body.String())
	}
	if row := f.row(withdrawn.ID); row.Status != "withdrawn" || row.Ciphertext != "" {
		t.Fatalf("after withdraw: %+v", row)
	}
	f.requireConflict(f.as(f.owner, f.api.withdraw, http.MethodDelete, withdrawn.ID, nil), "handoff_already_decided", "withdrawn")

	pending := f.mustOffer(nil)
	f.requireConflict(f.offer(nil), "handoff_already_pending", "")

	if err := f.st.DeleteSessionShare(ctx, f.shareID, f.owner.mv.MembershipID); err != nil {
		t.Fatal(err)
	}
	if row := f.row(pending.ID); row.Status != "expired" || row.Ciphertext != "" {
		t.Fatalf("after unshare: %+v", row)
	}
	if got := f.inbox(f.recipient); len(got) != 0 {
		t.Fatalf("the inbox kept an offer after the unshare: %+v", got)
	}
	f.requireConflict(f.as(f.recipient, f.api.take, http.MethodPost, pending.ID, map[string]any{}), "handoff_already_decided", "expired")
}

// TestHandoffOutsidersGet404 — a member of the tenant the offer is not addressed to, a
// member of another tenant, and the owner acting as recipient learn nothing, not even that
// the offer exists; the recipient cannot withdraw the owner's offer either.
func TestHandoffOutsidersGet404(t *testing.T) {
	f := newHandoffLife(t)
	o := f.mustOffer(nil)
	recipientOnly := map[string]membershipHandler{
		"take": f.api.take, "release": f.api.release, "accept": f.api.accept, "decline": f.api.decline,
	}
	for who, p := range map[string]handoffPerson{"outsider": f.outsider, "foreigner": f.foreigner, "owner": f.owner} {
		for name, h := range recipientOnly {
			if rec := f.as(p, h, http.MethodPost, o.ID, map[string]any{"token": "x", "sessionName": "s"}); rec.Code != http.StatusNotFound {
				t.Errorf("%s %s = %d %s, want 404", who, name, rec.Code, rec.Body.String())
			}
		}
	}
	for who, p := range map[string]handoffPerson{"outsider": f.outsider, "foreigner": f.foreigner, "recipient": f.recipient} {
		if rec := f.as(p, f.api.withdraw, http.MethodDelete, o.ID, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s withdraw = %d %s, want 404", who, rec.Code, rec.Body.String())
		}
	}
	if row := f.row(o.ID); row.Status != "pending" || row.TakenAt != "" {
		t.Fatalf("an outsider changed the offer: %+v", row)
	}
}

// recipientCreate is the recipient's POST /api/sessions through the CP, to an Agent stub of
// their own that records what reached it.
type recipientCreate struct {
	api    workspaceAPI
	res    *resolved
	bodies []map[string]any
}

func (f *handoffLife) recipientWorkspace() *recipientCreate {
	f.t.Helper()
	rc := &recipientCreate{}
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/reserve") {
			w.Header().Set(createKeyHeader, strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/sessions-idempotency/"), "/reserve"))
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/sessions" {
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			f.mu.Lock()
			rc.bodies = append(rc.bodies, b)
			f.mu.Unlock()
			if key, _ := b["idempotency_key"].(string); key != "" {
				w.Header().Set(createKeyHeader, key)
				w.Header().Set(createOutcomeHeader, "created")
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "taken-over"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	f.t.Cleanup(agent.Close)
	ws := store.Workspace{ID: "ws-recipient", TenantID: f.recipient.mv.TenantID, MembershipID: f.recipient.mv.MembershipID,
		ContainerName: "recipient", Network: "test", DataDir: "/data/recipient", AgentPort: "1", AgentToken: "tok",
		State: "running", CreatedAt: store.NowTS()}
	if err := f.st.CreateWorkspace(context.Background(), ws); err != nil {
		f.t.Fatal(err)
	}
	rt := stubRuntime{endpoint: agent.URL, token: "tok"}
	rc.api = newWorkspaceAPI(f.api.mgr, false)
	rc.res = &resolved{ident: f.recipient.ident, mv: f.recipient.mv, ws: ws, rt: rt}
	return rc
}

func (rc *recipientCreate) create(t *testing.T, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	rc.api.sessionCreate(rec, httptest.NewRequest(http.MethodPost, "/api/sessions", jsonBody(body)), rc.res)
	return rec
}

// TestHandoffCreateIsFencedByTheTake is the commit point of #2020's take: the create that
// carries it confirms it immediately before the Agent is asked. A Console that took the
// offer and went quiet past the take window has lost it to the sender's withdraw, and when
// it resumes its create is refused without reaching the Agent. A confirmed take, however
// slow the create, is never withdrawn under it.
func TestHandoffCreateIsFencedByTheTake(t *testing.T) {
	f := newHandoffLife(t)
	rc := f.recipientWorkspace()
	backdate := func(id string) {
		if _, err := f.st.DB().Exec(`UPDATE session_handoff_offer SET taken_at='2000-01-01T00:00:00Z' WHERE id=?`, id); err != nil {
			t.Fatal(err)
		}
	}

	slept := f.mustOffer(nil)
	token := f.take(slept.ID)
	backdate(slept.ID) // the machine slept past the take window
	if rec := f.as(f.owner, f.api.withdraw, http.MethodDelete, slept.ID, nil); rec.Code != http.StatusOK {
		t.Fatalf("withdraw past an unconfirmed take's window = %d %s", rec.Code, rec.Body.String())
	}
	rec := rc.create(t, map[string]any{"kind": "claude", "dir": "/r/private", "handoff_take": map[string]any{"offerId": slept.ID, "token": token}})
	f.requireConflict(rec, "handoff_take_lost", "")
	if len(rc.bodies) != 0 {
		t.Fatalf("a fenced create reached the Agent: %v", rc.bodies)
	}

	o := f.mustOffer(nil)
	token = f.take(o.ID)
	rec = rc.create(t, map[string]any{"kind": "claude", "dir": "/r/private", "handoff_take": map[string]any{"offerId": o.ID, "token": token}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create with a live take = %d %s", rec.Code, rec.Body.String())
	}
	if len(rc.bodies) != 1 || rc.bodies[0]["handoff_take"] != nil || rc.bodies[0]["kind"] != "claude" {
		t.Fatalf("the Agent received %v, want the create without handoff_take", rc.bodies)
	}
	// The CP saw the session created, so it settled the offer itself (#2040); the Console's
	// own accept finds it done.
	if row := f.row(o.ID); row.Status != "accepted" || row.AcceptedSessionName != "taken-over" {
		t.Fatalf("the create did not settle the take: %+v", row)
	}
	if rec := f.as(f.recipient, f.api.accept, http.MethodPost, o.ID, map[string]any{"sessionName": "taken-over", "token": token}); rec.Code != http.StatusOK {
		t.Fatalf("accept after the create = %d %s", rec.Code, rec.Body.String())
	}

	// A confirmed take whose create is slow, past the window: the session may exist, so
	// the sender cannot withdraw under it.
	slow := f.mustOffer(nil)
	token = f.take(slow.ID)
	if ok, err := f.st.ConfirmSessionHandoffTake(context.Background(), slow.ID, f.recipient.mv.MembershipID, token, store.NowTS()); err != nil || !ok {
		t.Fatalf("confirm: ok=%v err=%v", ok, err)
	}
	backdate(slow.ID)
	f.requireConflict(f.as(f.owner, f.api.withdraw, http.MethodDelete, slow.ID, nil), "handoff_being_taken", "pending")
	if rec := f.as(f.recipient, f.api.accept, http.MethodPost, slow.ID, map[string]any{"sessionName": "slow", "token": token}); rec.Code != http.StatusOK {
		t.Fatalf("accept after a slow create = %d %s", rec.Code, rec.Body.String())
	}

	// Unsharing under an unconfirmed take expires the offer at once; the late release
	// answers with that status.
	o3 := f.mustOffer(nil)
	token = f.take(o3.ID)
	if err := f.st.DeleteSessionShare(context.Background(), f.shareID, f.owner.mv.MembershipID); err != nil {
		t.Fatal(err)
	}
	f.requireConflict(f.as(f.recipient, f.api.release, http.MethodPost, o3.ID, map[string]any{"token": token}), "handoff_not_taken", "expired")

	// Unsharing under a confirmed take keeps it, and the Console cannot give it back (its
	// session may exist, #2040); it expires when the CP gives it back on the Agent's word.
	if err := f.st.PutSessionShare(context.Background(), store.SessionShare{ID: f.shareID, TenantID: f.owner.mv.TenantID,
		OwnerMembershipID: f.owner.mv.MembershipID, RecipientMembershipID: f.recipient.mv.MembershipID,
		ScopeType: "session", ScopeKey: "session-1", Permission: "ro", CreatedAt: store.NowTS(), UpdatedAt: store.NowTS()}); err != nil {
		t.Fatal(err)
	}
	o2 := f.mustOffer(nil)
	token = f.take(o2.ID)
	if ok, err := f.st.ConfirmSessionHandoffTake(context.Background(), o2.ID, f.recipient.mv.MembershipID, token, store.NowTS()); err != nil || !ok {
		t.Fatalf("confirm: ok=%v err=%v", ok, err)
	}
	if err := f.st.DeleteSessionShare(context.Background(), f.shareID, f.owner.mv.MembershipID); err != nil {
		t.Fatal(err)
	}
	f.requireConflict(f.as(f.owner, f.api.withdraw, http.MethodDelete, o2.ID, nil), "handoff_being_taken", "pending")
	f.requireConflict(f.as(f.recipient, f.api.release, http.MethodPost, o2.ID, map[string]any{"token": token}), "handoff_launch_unknown", "pending")
	if ok, err := f.st.ReleaseSessionHandoffOffer(context.Background(), o2.ID, f.recipient.mv.MembershipID, token); err != nil || !ok {
		t.Fatalf("release on the Agent's word: ok=%v err=%v", ok, err)
	}
	if row := f.row(o2.ID); row.Status != "expired" {
		t.Fatalf("after the unshare and the release: %+v, want expired", row)
	}

	// A create without a take is passed through untouched.
	if rec := rc.create(t, map[string]any{"kind": "shell"}); rec.Code != http.StatusCreated || len(rc.bodies) != 2 || rc.bodies[1]["idempotency_key"] != nil {
		t.Fatalf("plain create = %d (%d bodies)", rec.Code, len(rc.bodies))
	}
}

// The sender's card and ledger and the recipient's band name the other participant by the
// e-mail the share picker showed, not by the internal user key (#2075). Each side gets only the
// other participant's address: the recipient's inbox must not echo the reader's own.
func TestHandoffWireNamesTheOtherParticipantByEmail(t *testing.T) {
	f := newHandoffLife(t)
	o := f.mustOffer(nil)
	if o.RecipientUserKey != "recipient" || o.RecipientEmail != "recipient@example.com" {
		t.Fatalf("create = %q / %q, want recipient / recipient@example.com", o.RecipientUserKey, o.RecipientEmail)
	}

	rec := httptest.NewRecorder()
	f.api.listOwned(rec, httptest.NewRequest(http.MethodGet, "/api/session-handoff-offers", nil), f.owner.ident, f.owner.mv)
	var owned struct {
		Offers []map[string]any `json:"offers"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &owned) != nil || len(owned.Offers) != 1 {
		t.Fatalf("ledger = %d %s", rec.Code, rec.Body.String())
	}
	if got := owned.Offers[0]["recipientEmail"]; got != "recipient@example.com" {
		t.Fatalf("ledger recipientEmail = %v, want recipient@example.com", got)
	}
	if _, ok := owned.Offers[0]["ownerEmail"]; ok {
		t.Fatalf("ledger carries ownerEmail: %v", owned.Offers[0])
	}

	rec = httptest.NewRecorder()
	f.api.listReceived(rec, httptest.NewRequest(http.MethodGet, "/api/session-handoff-offers/received", nil), f.recipient.ident, f.recipient.mv)
	var inbox struct {
		Offers []map[string]any `json:"offers"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &inbox) != nil || len(inbox.Offers) != 1 {
		t.Fatalf("inbox = %d %s", rec.Code, rec.Body.String())
	}
	if got := inbox.Offers[0]["ownerEmail"]; got != "owner@example.com" {
		t.Fatalf("inbox ownerEmail = %v, want owner@example.com", got)
	}
	if v, ok := inbox.Offers[0]["recipientEmail"]; ok {
		t.Fatalf("inbox echoes the reader's own address: recipientEmail = %v", v)
	}
}
