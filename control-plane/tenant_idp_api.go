package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/auth"
	"github.com/k-k1/agent-fleet/control-plane/internal/mcpsrv"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Admin API for tenant-defined login providers (docs/log/61 §61.11.6 + ADR0043
// decision 30).
//
// The split of powers IS the feature, so it is worth stating in one place:
//
//	tenant_admin   creates, edits, suspends and deletes their own tenant's rows.
//	               Everything about the row except whether it works.
//	super_admin    approves (pending -> active). Registering an IdP is the power to
//	               declare who somebody is, and both user_key and the deployment role
//	               are keyed by email across the WHOLE deployment — so an admin who
//	               could activate their own issuer could assert the operator's address
//	               and take the deployment (docs/log/61 §61.11.2).
//
// Suspending is open to the tenant_admin as well: stopping is always allowed to be
// faster than starting.
type tenantIdPAPI struct {
	memberAuth
	// provs is the env-defined set, for one question only: does the issuer being
	// registered already have a door on this deployment (docs/log/61 §61.17.4 (b))?
	// A tenant registering a second app registration of the SAME directory the
	// deployment itself uses is the commonest form of that, and the DB rows alone
	// would not see it.
	provs []auth.LoginProvider
}

func newTenantIdPAPI(m *manager, provs []auth.LoginProvider) tenantIdPAPI {
	return tenantIdPAPI{memberAuth{m}, provs}
}

// tenantIdPBody is the wire shape. Secret is write-only: it is never returned, and
// an update that leaves it at the mask (or empty) keeps the stored value — the same
// contract mcp_server.go uses for header values.
type tenantIdPBody struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name"`
	LabelJA string `json:"label_ja,omitempty"`
	LabelEN string `json:"label_en,omitempty"`
	// Kind is "oidc" (default) or "github". It decides which of the fields below the
	// form even shows, and which ones this API requires (docs/log/61 §61.15).
	Kind           string `json:"kind,omitempty"`
	Issuer         string `json:"issuer"`
	ClientID       string `json:"client_id"`
	ClientSecret   string `json:"client_secret,omitempty"`
	Trust          string `json:"trust"`
	AllowedTIDs    string `json:"allowed_tids,omitempty"`
	AllowedDomains string `json:"allowed_domains"`
	AllowedOrgs    string `json:"allowed_orgs,omitempty"`
	// LinkClaim names the stable claim rule 1.5 matches on when the issuer's `sub` is
	// pairwise (docs/log/61 §61.15.10). Only the names in tenantLinkClaims are accepted.
	LinkClaim string `json:"link_claim,omitempty"`
	// Read-only fields.
	ProviderID string `json:"provider_id,omitempty"`
	TenantSlug string `json:"tenant_slug,omitempty"`
	Status     string `json:"status,omitempty"`
	HasSecret  bool   `json:"has_secret,omitempty"`
	ApprovedBy string `json:"approved_by,omitempty"`
	ApprovedAt string `json:"approved_at,omitempty"`
	CreatedBy  string `json:"created_by,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
	// Usable reports that the row is active AND could actually be built into a
	// provider. An approved row whose issuer stopped resolving shows no button, and
	// without this the tenant cannot tell that from "not approved yet".
	Usable bool `json:"usable,omitempty"`
}

func (a tenantIdPAPI) rowToBody(row store.TenantIdP, slug string, usable bool) tenantIdPBody {
	kind := row.Kind
	if kind == "" {
		kind = auth.TenantIdPKindOIDC // rows written before 0041
	}
	return tenantIdPBody{
		ID: row.ID, Name: row.Name, LabelJA: row.LabelJA, LabelEN: row.LabelEN,
		Kind: kind, Issuer: row.Issuer, ClientID: row.ClientID, Trust: row.Trust,
		AllowedTIDs: row.AllowedTIDs, AllowedDomains: row.AllowedDomains,
		AllowedOrgs: row.AllowedOrgs, LinkClaim: row.LinkClaim,
		ProviderID: auth.TenantProviderID(slug, row.Name), TenantSlug: slug,
		Status: row.Status, HasSecret: row.SecretEnc != "",
		ApprovedBy: row.ApprovedBy, ApprovedAt: row.ApprovedAt,
		CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		Usable: usable,
	}
}

// list (GET /api/admin/tenants/{slug}/idp) — the tenant's own providers.
func (a tenantIdPAPI) list(w http.ResponseWriter, r *http.Request) {
	_, t, ok := a.tenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	rows, err := a.mgr.store.ListTenantIdPs(r.Context(), t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := make([]tenantIdPBody, 0, len(rows))
	for _, row := range rows {
		id := auth.TenantProviderID(t.Slug, row.Name)
		out = append(out, a.rowToBody(row, t.Slug, a.mgr.tenantIdP.ProviderFor(r.Context(), id) != nil))
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out, "tenant": t.Slug})
}

// queue (GET /api/admin/idp) — the super_admin approval queue across every tenant
// (docs/log/61 §61.11.6). Pending rows come first: that is the list somebody is waiting on.
func (a tenantIdPAPI) queue(w http.ResponseWriter, r *http.Request, _ store.Identity) {
	rows, tenants, err := a.mgr.store.ListAllTenantIdPs(r.Context())
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := make([]tenantIdPBody, 0, len(rows))
	for _, row := range rows {
		slug := tenants[row.TenantID].Slug
		id := auth.TenantProviderID(slug, row.Name)
		out = append(out, a.rowToBody(row, slug, a.mgr.tenantIdP.ProviderFor(r.Context(), id) != nil))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return statusRank(out[i].Status) < statusRank(out[j].Status)
	})
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}

func statusRank(s string) int {
	switch s {
	case "pending":
		return 0
	case "active":
		return 1
	default:
		return 2
	}
}

// upsert handles POST (create) and PUT /{id} (edit). tenant_admin.
func (a tenantIdPAPI) upsert(w http.ResponseWriter, r *http.Request) {
	var b tenantIdPBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<17)).Decode(&b); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	ident, t, ok := a.tenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	b.Name = strings.ToLower(strings.TrimSpace(b.Name))
	b.Issuer = strings.TrimSpace(b.Issuer)
	b.ClientID = strings.TrimSpace(b.ClientID)
	b.Trust = strings.ToLower(strings.TrimSpace(b.Trust))
	b.Kind = strings.ToLower(strings.TrimSpace(b.Kind))
	b.LinkClaim = strings.ToLower(strings.TrimSpace(b.LinkClaim))
	if b.Kind == "" {
		b.Kind = auth.TenantIdPKindOIDC
	}
	tids := splitCSVLower(b.AllowedTIDs)
	domains := splitDomainCSV(b.AllowedDomains)
	orgs := splitCSVLower(b.AllowedOrgs)
	// github rows do not carry an issuer or a trust rule from the form: there is
	// exactly one GitHub and its email rule is fixed (trust=api, the verified flag on
	// /user/emails — docs/log/61 §61.4). Writing them here rather than leaving them blank
	// keeps every row readable in the register and in the audit line, where "which
	// identity source" is the question being asked (§61.15).
	// link_claim goes the same way for a github row: the GitHub adapter's subject is
	// the account's numeric id, which is already the same for every OAuth App, so rule
	// 1.5 needs no second key there and a stored value would only be a lie in the
	// register.
	if b.Kind == auth.TenantIdPKindGitHub {
		b.Issuer, b.Trust, tids, b.LinkClaim = auth.GithubWebBase, auth.TrustAPI, nil, ""
	}

	id := r.PathValue("id")
	rows, err := a.mgr.store.ListTenantIdPs(r.Context(), t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	var stored *store.TenantIdP
	for i := range rows {
		if id != "" && rows[i].ID == id {
			stored = &rows[i]
			continue
		}
		if strings.EqualFold(rows[i].Name, b.Name) {
			writeAPIErr(w, &apiError{http.StatusConflict, "tenant_idp_name_taken",
				"this tenant already has a sign-in method named " + b.Name})
			return
		}
	}
	if id != "" && stored == nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, "tenant_idp_not_found", "unknown sign-in method"})
		return
	}
	if aerr := validateTenantIdPBody(b, domains, tids, orgs); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	if aerr := a.checkDomainsUnclaimed(r, t.ID, id, domains); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	if aerr := a.checkPairwiseNeedsLinkClaim(r, b, id); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}

	// The secret: a blank (or still-masked) value on an edit keeps what is stored, so
	// editing a label does not require re-typing the client_secret — and, more to the
	// point, does not tempt anyone to paste it into a form again (§61.11.4).
	enc, keyRef := "", ""
	switch s := strings.TrimSpace(b.ClientSecret); {
	case s != "" && s != mcpsrv.MaskedValue:
		if enc, keyRef, err = a.mgr.sealTenantSecret(r.Context(), t.ID, s); err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
	case stored != nil:
		// Verify the stored value is still readable before carrying it forward. A row
		// whose secret cannot be decrypted (the master key changed) would otherwise be
		// saved back looking healthy and fail at the token endpoint, where the cause is
		// invisible (§61.11.4: an undecryptable secret is an explicit error, never an
		// empty one).
		if _, err := a.mgr.openTenantSecret(r.Context(), stored.SecretEnc, stored.KeyRef); err != nil {
			writeAPIErr(w, &apiError{http.StatusConflict, "tenant_idp_secret_unreadable",
				"the stored client secret cannot be decrypted — enter it again"})
			return
		}
		enc, keyRef = stored.SecretEnc, stored.KeyRef
	default:
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "client_secret is required"})
		return
	}

	now := store.NowTS()
	row := store.TenantIdP{
		ID: id, TenantID: t.ID, Name: b.Name, LabelJA: strings.TrimSpace(b.LabelJA),
		LabelEN: strings.TrimSpace(b.LabelEN), Kind: b.Kind, Issuer: b.Issuer, ClientID: b.ClientID,
		SecretEnc: enc, KeyRef: keyRef, Trust: b.Trust,
		AllowedTIDs: joinCSV(tids), AllowedDomains: joinCSV(domains), AllowedOrgs: joinCSV(orgs),
		LinkClaim: b.LinkClaim,
		Status:    "pending", CreatedBy: ident.ID, CreatedAt: now, UpdatedAt: now,
	}
	action := "tenant_idp.create"
	if stored != nil {
		action = "tenant_idp.update"
		row.CreatedBy, row.CreatedAt = stored.CreatedBy, stored.CreatedAt
		row.Status, row.ApprovedBy, row.ApprovedAt = stored.Status, stored.ApprovedBy, stored.ApprovedAt
		if repend(*stored, row) {
			// The approval was given to THIS issuer, for THESE addresses. Change what
			// was approved and the approval no longer applies to what the row now says
			// (decision 30) — so it goes back to the queue and the button disappears until a
			// super_admin looks again.
			row.Status, row.ApprovedBy, row.ApprovedAt = "pending", "", ""
		}
	}
	if stored == nil {
		row.ID = store.NewID()
		err = a.mgr.store.CreateTenantIdP(r.Context(), row)
	} else {
		err = a.mgr.store.UpdateTenantIdP(r.Context(), row)
	}
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	a.mgr.tenantIdP.Invalidate()
	a.audit(r, ident, t.ID, action, auth.TenantProviderID(t.Slug, row.Name),
		"kind="+row.Kind+" issuer="+row.Issuer+" trust="+row.Trust+" orgs="+row.AllowedOrgs+
			" domains="+row.AllowedDomains+" link_claim="+row.LinkClaim+" status="+row.Status)
	writeJSON(w, http.StatusOK, a.rowToBody(row, t.Slug, false))
}

// repend reports whether an edit invalidates an existing approval. The three fields
// §61.11.6 names are here, plus two the approval equally rests on: a WIDENED domain
// or tenant list lets the issuer assert addresses the approver never saw. Narrowing
// either one does not — you cannot become more dangerous by admitting fewer people.
// A new client_secret does not re-pend: it is the same issuer and the same app
// registration, and forcing re-approval on a routine credential rotation would teach
// people to avoid rotating.
// For a github row the approval rests on a different pair — (allowed_orgs,
// allowed_domains) instead of (issuer, allowed_domains) — because github.com is one
// issuer shared by every tenant (docs/log/61 §61.15 + decision 34). So ADDING an org
// repends: the approver said "the members of these organizations", and another
// organization is another set of people they never saw. Removing one does not, for the
// same reason narrowing the domains does not.
func repend(old, next store.TenantIdP) bool {
	if old.Status != "active" {
		return false
	}
	if old.Issuer != next.Issuer || old.ClientID != next.ClientID || old.Trust != next.Trust {
		return true
	}
	if old.Kind != next.Kind {
		return true
	}
	// link_claim too (docs/log/61 §61.15.10). It does not change WHO may sign in, so it
	// is easy to read as cosmetic — but it changes WHERE a login LANDS: rule 1.5 joins
	// on it, so an existing account can be reached through a button that could not
	// reach it before. That is the approver's business, exactly like the issuer.
	if old.LinkClaim != next.LinkClaim {
		return true
	}
	return widened(old.AllowedDomains, next.AllowedDomains) ||
		widened(old.AllowedTIDs, next.AllowedTIDs) ||
		widened(old.AllowedOrgs, next.AllowedOrgs)
}

// widened reports whether next contains an entry old did not.
func widened(old, next string) bool {
	have := map[string]bool{}
	for _, v := range strings.Split(old, ",") {
		have[strings.TrimSpace(v)] = true
	}
	for _, v := range strings.Split(next, ",") {
		if v = strings.TrimSpace(v); v != "" && !have[v] {
			return true
		}
	}
	return false
}

// setStatus (POST /api/admin/tenants/{slug}/idp/{id}/status) is the approval path.
func (a tenantIdPAPI) setStatus(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	ident, t, ok := a.tenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	status := strings.ToLower(strings.TrimSpace(body.Status))
	switch status {
	case "suspended", "pending":
		// Stopping (and withdrawing an approval) is open to the tenant_admin too.
	case "active":
		// Activation is the operator's, and ONLY the operator's (decision 30).
		if ident.Role != "super_admin" {
			writeAPIErr(w, &apiError{http.StatusForbidden, "forbidden",
				"activating a sign-in method requires a deployment administrator's approval"})
			return
		}
	default:
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request",
			"status must be pending, active or suspended"})
		return
	}
	row, found, err := a.mgr.store.GetTenantIdP(r.Context(), t.ID, r.PathValue("id"))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !found {
		writeAPIErr(w, &apiError{http.StatusNotFound, "tenant_idp_not_found", "unknown sign-in method"})
		return
	}
	// Approving a row that cannot be built is refused, not stored. Otherwise the
	// approval is recorded against a definition nobody can use, and the tenant is left
	// looking at an "approved" row with no button and no reason given.
	if status == "active" {
		secret, err := a.mgr.openTenantSecret(r.Context(), row.SecretEnc, row.KeyRef)
		if err != nil {
			writeAPIErr(w, &apiError{http.StatusConflict, "tenant_idp_secret_unreadable",
				"the stored client secret cannot be decrypted — the tenant has to enter it again"})
			return
		}
		if _, err := auth.BuildTenantProvider(row, store.TenantRef{Slug: t.Slug, Name: t.Name}, secret); err != nil {
			writeAPIErr(w, &apiError{http.StatusBadRequest, "tenant_idp_invalid", err.Error()})
			return
		}
	}
	// Stopping a method that is somebody's ONLY door locks them out (the ordering in
	// docs/log/61 §61.17.4). The commonest way to get here is the migration this whole
	// section is about: a second app registration goes live, and the old row is
	// suspended before everyone has linked the new one — and after that they cannot
	// self-serve, because linking needs a session and their session needs this row.
	//
	// It is a 409 the caller can override, NOT a refusal. Suspending is also how a
	// compromised IdP is stopped, and "stopping is always allowed to be faster than
	// starting" (see the type comment). So this buys one question, not a veto.
	if status == "suspended" && row.Status == "active" && r.URL.Query().Get("confirm") != "1" {
		n, err := a.mgr.store.CountMembersOnlyOnProvider(r.Context(), t.ID, auth.TenantProviderID(t.Slug, row.Name))
		if err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
		if n > 0 {
			// Written by hand rather than through writeAPIErr: the COUNT has to reach
			// the Console, and only the server can know it. The shared error envelope is
			// {code, message} — widening it for one response would touch every handler,
			// so the number rides alongside as its own field. `error` keeps its usual
			// shape, so a caller that only reads that still gets a sentence.
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": map[string]string{
					"code": "tenant_idp_last_method_for_members",
					"message": "this is the only sign-in method " + strconv.Itoa(n) + " active member(s) have ever used; " +
						"suspending it locks them out, and they cannot add another method without signing in first",
				},
				"members": n,
			})
			return
		}
	}
	approvedBy, approvedAt := "", ""
	if status == "active" {
		approvedBy, approvedAt = ident.ID, store.NowTS()
	}
	if err := a.mgr.store.SetTenantIdPStatus(r.Context(), t.ID, row.ID, status, approvedBy, approvedAt, store.NowTS()); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	a.mgr.tenantIdP.Invalidate()
	a.audit(r, ident, t.ID, "tenant_idp."+status, auth.TenantProviderID(t.Slug, row.Name), "issuer="+row.Issuer)
	writeJSON(w, http.StatusOK, map[string]any{"id": row.ID, "status": status})
}

// remove (DELETE /api/admin/tenants/{slug}/idp/{id}).
//
// Deleting the row does NOT undo what people signed in with it already did: the
// identities it created keep their workspaces, and their (provider, subject) rows
// stay behind so re-creating a provider of the same name reconnects them to the same
// people rather than to new accounts. What it does end is the ability to sign in,
// within the registry's TTL.
func (a tenantIdPAPI) remove(w http.ResponseWriter, r *http.Request) {
	ident, t, ok := a.tenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	row, found, err := a.mgr.store.GetTenantIdP(r.Context(), t.ID, r.PathValue("id"))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !found {
		writeAPIErr(w, &apiError{http.StatusNotFound, "tenant_idp_not_found", "unknown sign-in method"})
		return
	}
	if err := a.mgr.store.DeleteTenantIdP(r.Context(), t.ID, row.ID); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	a.mgr.tenantIdP.Invalidate()
	a.audit(r, ident, t.ID, "tenant_idp.delete", auth.TenantProviderID(t.Slug, row.Name), "issuer="+row.Issuer)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": row.ID})
}

func (a tenantIdPAPI) audit(r *http.Request, ident store.Identity, tenantID, action, target, detail string) {
	_ = a.mgr.store.InsertAudit(r.Context(), store.AuditLog{
		ID: store.NewID(), TenantID: tenantID, ActorKind: "admin", ActorID: ident.ID,
		Action: action, Target: target, Detail: detail, At: store.NowTS(),
	})
}

// checkDomainsUnclaimed refuses a domain another tenant's provider already claims.
//
// This is the load-bearing check of the whole feature, and it is not obvious.
// allowed_domains bounds which addresses an issuer may assert. If two tenants could
// both claim acme.co.jp, the subsidiary's administrator could assert the parent
// company's addresses — and since identity is keyed by email deployment-wide, that is
// the takeover decision 30 describes, merely one step further along. One domain, one
// tenant, exactly as auto_join_domains works (§61.9.8) — and refusing on save is the
// only moment a human is present to read why.
func (a tenantIdPAPI) checkDomainsUnclaimed(r *http.Request, tenantID, rowID string, domains []string) *apiError {
	rows, tenants, err := a.mgr.store.ListAllTenantIdPs(r.Context())
	if err != nil {
		return internalErr(err)
	}
	for _, other := range rows {
		if other.TenantID == tenantID && other.ID == rowID {
			continue
		}
		if other.TenantID == tenantID {
			continue // a tenant may split its own domains across its own providers
		}
		claimed := map[string]bool{}
		for _, d := range splitDomainCSV(other.AllowedDomains) {
			claimed[d] = true
		}
		for _, d := range domains {
			if claimed[d] {
				return &apiError{http.StatusConflict, "tenant_idp_domain_conflict",
					"domain " + d + " is already claimed by the sign-in method of tenant " + tenants[other.TenantID].Slug}
			}
		}
	}
	return nil
}

// checkPairwiseNeedsLinkClaim refuses a SECOND app registration of a directory whose
// `sub` is pairwise, unless the row says which stable claim identifies the person
// (docs/log/61 §61.17.4 (b) + decision 41).
//
// The situation it catches: a tenant registers its own Entra app for a directory this
// deployment already has a door to. Entra mints a per-client `sub`, so the same person
// arrives with a DIFFERENT subject, rule 1.5 does not join them, and rule 2' refuses
// the login outright — `email_taken`, with no session. The failure lands on the
// person, at login, weeks later, and reads like a bug. Refusing at save is the only
// moment anybody who can fix it is present.
//
// Three deliberate narrowings:
//
//   - pairwise is read from DISCOVERY (`subject_types_supported`), never guessed
//     from the issuer's hostname. Measured 2026-08-20: Google public, Entra pairwise.
//   - It only fires when the issuer is ALREADY in use here. One registration of a
//     pairwise IdP splits nobody, and demanding a claim from every Entra tenant would
//     be noise on the common case.
//   - Discovery failing is NOT a refusal. The issuer may be unreachable from the CP
//     at this moment (it is fetched lazily everywhere else for the same reason), and a
//     network blip must not stop an administrator from saving a form.
//
// Why it does not also check that the claim is EMITTED: it cannot. `claims_supported`
// under-reports — Entra's document does not list `oid` at all, though every v2 token
// carries it (measured 2026-08-20). And naming a claim the IdP never sends is inert:
// realm_subject stays empty, and identityIDForRealmClaim refuses an empty subject, so
// nothing is joined by accident. Requiring the field costs nothing when it cannot help.
func (a tenantIdPAPI) checkPairwiseNeedsLinkClaim(r *http.Request, b tenantIdPBody, rowID string) *apiError {
	if b.Kind == auth.TenantIdPKindGitHub || b.LinkClaim != "" || b.Issuer == "" {
		return nil
	}
	inUse, err := a.mgr.store.TenantIdPIssuerInUse(r.Context(), b.Issuer, rowID)
	if err != nil {
		return internalErr(err)
	}
	if !inUse {
		// The deployment's own providers are doors too, and the commonest second
		// registration is "the tenant's app for the directory the deployment uses".
		for _, p := range a.provs {
			if auth.ProviderRealm(p) == b.Issuer {
				inUse = true
				break
			}
		}
	}
	if !inUse {
		return nil
	}
	// Bounded: this runs inside a form submit, and the answer is advisory enough that
	// waiting on a slow IdP would be worse than not asking.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	pairwise, err := auth.IssuerUsesPairwiseSubjects(ctx, b.Issuer)
	if err != nil {
		log.Printf("tenant idp: %s: subject_types probe failed, saving without the pairwise check: %v", b.Issuer, err)
		return nil
	}
	if !pairwise {
		return nil
	}
	return &apiError{http.StatusBadRequest, "tenant_idp_link_claim_required",
		"this deployment already has a sign-in method for " + b.Issuer + ", and that issuer gives each app " +
			"registration a different subject for the same person — so without a stable claim to match on, " +
			"everybody who already signs in here would be refused with \"this email address is already used by " +
			"another sign-in method\". Set how the same account is recognised (" + strings.Join(auth.TenantLinkClaimList(), ", ") + ")"}
}

// validateTenantIdPBody is the save-time half of the rules the env path enforces at
// startup. It has to be a 400 rather than a fatal: a running CP cannot be brought
// down because somebody typed a bad issuer into a form (§61.11.5).
func validateTenantIdPBody(b tenantIdPBody, domains, tids, orgs []string) *apiError {
	if !auth.ValidTenantIdPName(b.Name) {
		return &apiError{http.StatusBadRequest, "tenant_idp_name_invalid",
			"name must be 1-32 chars of a-z 0-9 - _ and start with a letter or digit"}
	}
	switch b.Kind {
	case auth.TenantIdPKindOIDC:
	case auth.TenantIdPKindGitHub:
		// The org list carries the whole weight an issuer carries for OIDC. github.com
		// is one issuer for every tenant on earth, so "which organization vouches for
		// this person" is what makes the login mean anything (docs/log/61 §61.15 +
		// decision 34), and it is the same rule the env path enforces by disabling
		// GitHub outright when AF_GITHUB_ALLOWED_ORGS is empty (§61.3).
		if len(orgs) == 0 {
			return &apiError{http.StatusBadRequest, "tenant_idp_orgs_required",
				"list the GitHub organizations whose members may sign in (membership in one of them is what authorizes a GitHub sign-in)"}
		}
		if b.ClientID == "" {
			return &apiError{http.StatusBadRequest, "bad_request", "client_id is required"}
		}
		// allowed_domains stays required for github too — see the note below. It is not
		// what stops a forged address here (GitHub verifies the mailbox itself), it is
		// this row's claim in the deployment-wide one-domain-one-tenant ledger.
		if len(domains) == 0 {
			return &apiError{http.StatusBadRequest, "tenant_idp_domains_required",
				"list the email domains this sign-in method may admit (an empty list would admit every address the organization's members carry)"}
		}
		return nil
	default:
		return &apiError{http.StatusBadRequest, "tenant_idp_kind_invalid",
			"kind must be " + auth.TenantIdPKindOIDC + " or " + auth.TenantIdPKindGitHub}
	}
	if !auth.ValidIssuerURL(b.Issuer) {
		return &apiError{http.StatusBadRequest, "tenant_idp_issuer_invalid",
			"issuer must be the IdP's https issuer URL (http is accepted only for loopback)"}
	}
	// Decision 7, on the DB side: a multi-tenant Entra endpoint accepts every Microsoft
	// account in the world, and a personal account can rewrite its own email — so the
	// tenant list is what makes the domain list mean anything at all.
	if auth.MultiTenantIssuer(b.Issuer) && len(tids) == 0 {
		return &apiError{http.StatusBadRequest, "tenant_idp_tids_required",
			"this issuer is a multi-tenant endpoint: list the allowed tenant ids, or pin the issuer to one tenant"}
	}
	if b.ClientID == "" {
		return &apiError{http.StatusBadRequest, "bad_request", "client_id is required"}
	}
	// The whitelist, and the save-time half of the pair buildTenantProvider enforces
	// at runtime (docs/log/61 §61.15.10). `oid` is a per-directory object id nobody can
	// choose; `email` / `upn` / `preferred_username` are ASSERTED, and a tenant that
	// could name one would have an email join inside a shared realm — the takeover
	// rule 2' refuses, arriving through another door.
	if b.LinkClaim != "" && !auth.TenantLinkClaimAllowed(b.LinkClaim) {
		return &apiError{http.StatusBadRequest, "tenant_idp_link_claim_invalid",
			"link_claim must be one of " + strings.Join(auth.TenantLinkClaimList(), ", ") +
				" — a claim the IdP assigns and nobody can choose. An asserted claim (email, upn, …) would let this sign-in method reach accounts created by a different authority"}
	}
	switch b.Trust {
	case auth.TrustEmailVerified, auth.TrustIssuer:
	default:
		return &apiError{http.StatusBadRequest, "tenant_idp_trust_invalid",
			"trust must be " + auth.TrustEmailVerified + " (the IdP asserts email_verified) or " + auth.TrustIssuer + " (the issuer is pinned to one tenant)"}
	}
	// allowed_domains is REQUIRED — the answer to docs/log/61 §61.14's open question.
	// A tenant-defined provider does not fall back to the deployment allowlist
	// (decision 32-3), so an empty list is not "everyone" but "nobody", and an approval
	// that admits nobody is worse than a refusal: it looks finished. Requiring it also
	// bounds which addresses this issuer may assert, which is what makes the
	// one-domain-one-tenant check above meaningful.
	if len(domains) == 0 {
		return &apiError{http.StatusBadRequest, "tenant_idp_domains_required",
			"list the email domains this sign-in method may admit (an empty list would admit nobody)"}
	}
	return nil
}
