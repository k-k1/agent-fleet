package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

// Admin API for tenant-defined login providers (docs/61 §61.11.6 + ADR0043 決定 30).
//
// The split of powers IS the feature, so it is worth stating in one place:
//
//	tenant_admin   creates, edits, suspends and deletes their own tenant's rows.
//	               Everything about the row except whether it works.
//	super_admin    approves (pending -> active). Registering an IdP is the power to
//	               declare who somebody is, and both user_key and the deployment role
//	               are keyed by email across the WHOLE deployment — so an admin who
//	               could activate their own issuer could assert the operator's address
//	               and take the deployment (docs/61 §61.11.2).
//
// Suspending is open to the tenant_admin as well: stopping is always allowed to be
// faster than starting.
type tenantIdPAPI struct {
	memberAuth
}

func newTenantIdPAPI(m *manager) tenantIdPAPI { return tenantIdPAPI{memberAuth{m}} }

// tenantIdPBody is the wire shape. Secret is write-only: it is never returned, and
// an update that leaves it at the mask (or empty) keeps the stored value — the same
// contract mcp_server.go uses for header values.
type tenantIdPBody struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name"`
	LabelJA string `json:"label_ja,omitempty"`
	LabelEN string `json:"label_en,omitempty"`
	// Kind is "oidc" (default) or "github". It decides which of the fields below the
	// form even shows, and which ones this API requires (docs/61 §61.15).
	Kind           string `json:"kind,omitempty"`
	Issuer         string `json:"issuer"`
	ClientID       string `json:"client_id"`
	ClientSecret   string `json:"client_secret,omitempty"`
	Trust          string `json:"trust"`
	AllowedTIDs    string `json:"allowed_tids,omitempty"`
	AllowedDomains string `json:"allowed_domains"`
	AllowedOrgs    string `json:"allowed_orgs,omitempty"`
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

func (a tenantIdPAPI) rowToBody(row TenantIdP, slug string, usable bool) tenantIdPBody {
	kind := row.Kind
	if kind == "" {
		kind = tenantIdPKindOIDC // rows written before 0041
	}
	return tenantIdPBody{
		ID: row.ID, Name: row.Name, LabelJA: row.LabelJA, LabelEN: row.LabelEN,
		Kind: kind, Issuer: row.Issuer, ClientID: row.ClientID, Trust: row.Trust,
		AllowedTIDs: row.AllowedTIDs, AllowedDomains: row.AllowedDomains,
		AllowedOrgs: row.AllowedOrgs,
		ProviderID:  tenantProviderID(slug, row.Name), TenantSlug: slug,
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
		id := tenantProviderID(t.Slug, row.Name)
		out = append(out, a.rowToBody(row, t.Slug, a.mgr.tenantIdP.providerFor(r.Context(), id) != nil))
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out, "tenant": t.Slug})
}

// queue (GET /api/admin/idp) — the super_admin approval queue across every tenant
// (docs/61 §61.11.6). Pending rows come first: that is the list somebody is waiting on.
func (a tenantIdPAPI) queue(w http.ResponseWriter, r *http.Request, _ Identity) {
	rows, slugs, err := a.mgr.store.ListAllTenantIdPs(r.Context())
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := make([]tenantIdPBody, 0, len(rows))
	for _, row := range rows {
		slug := slugs[row.TenantID]
		id := tenantProviderID(slug, row.Name)
		out = append(out, a.rowToBody(row, slug, a.mgr.tenantIdP.providerFor(r.Context(), id) != nil))
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
	if b.Kind == "" {
		b.Kind = tenantIdPKindOIDC
	}
	tids := splitCSVLower(b.AllowedTIDs)
	domains := splitDomainCSV(b.AllowedDomains)
	orgs := splitCSVLower(b.AllowedOrgs)
	// ★ github rows do not carry an issuer or a trust rule from the form: there is
	// exactly one GitHub and its email rule is fixed (trust=api, the verified flag on
	// /user/emails — docs/61 §61.4). Writing them here rather than leaving them blank
	// keeps every row readable in the register and in the audit line, where "which
	// identity source" is the question being asked (§61.15).
	if b.Kind == tenantIdPKindGitHub {
		b.Issuer, b.Trust, tids = githubWebBase, trustAPI, nil
	}

	id := r.PathValue("id")
	rows, err := a.mgr.store.ListTenantIdPs(r.Context(), t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	var stored *TenantIdP
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

	// The secret: a blank (or still-masked) value on an edit keeps what is stored, so
	// editing a label does not require re-typing the client_secret — and, more to the
	// point, does not tempt anyone to paste it into a form again (§61.11.4).
	enc, keyRef := "", ""
	switch s := strings.TrimSpace(b.ClientSecret); {
	case s != "" && s != maskedValue:
		if enc, keyRef, err = a.mgr.sealTenantSecret(r.Context(), t.ID, s); err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
	case stored != nil:
		// ★ Verify the stored value is still readable before carrying it forward. A row
		// whose secret cannot be decrypted (the master key changed) would otherwise be
		// saved back looking healthy and fail at the token endpoint, where the cause is
		// invisible (§61.11.4 の「復号不能は空にせず明示エラー」).
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

	now := nowTS()
	row := TenantIdP{
		ID: id, TenantID: t.ID, Name: b.Name, LabelJA: strings.TrimSpace(b.LabelJA),
		LabelEN: strings.TrimSpace(b.LabelEN), Kind: b.Kind, Issuer: b.Issuer, ClientID: b.ClientID,
		SecretEnc: enc, KeyRef: keyRef, Trust: b.Trust,
		AllowedTIDs: joinCSV(tids), AllowedDomains: joinCSV(domains), AllowedOrgs: joinCSV(orgs),
		Status: "pending", CreatedBy: ident.ID, CreatedAt: now, UpdatedAt: now,
	}
	action := "tenant_idp.create"
	if stored != nil {
		action = "tenant_idp.update"
		row.CreatedBy, row.CreatedAt = stored.CreatedBy, stored.CreatedAt
		row.Status, row.ApprovedBy, row.ApprovedAt = stored.Status, stored.ApprovedBy, stored.ApprovedAt
		if repend(*stored, row) {
			// ★ The approval was given to THIS issuer, for THESE addresses. Change what
			// was approved and the approval no longer applies to what the row now says
			// (決定 30) — so it goes back to the queue and the button disappears until a
			// super_admin looks again.
			row.Status, row.ApprovedBy, row.ApprovedAt = "pending", "", ""
		}
	}
	if stored == nil {
		row.ID = newID()
		err = a.mgr.store.CreateTenantIdP(r.Context(), row)
	} else {
		err = a.mgr.store.UpdateTenantIdP(r.Context(), row)
	}
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	a.mgr.tenantIdP.invalidate()
	a.audit(r, ident, t.ID, action, tenantProviderID(t.Slug, row.Name),
		"kind="+row.Kind+" issuer="+row.Issuer+" trust="+row.Trust+" orgs="+row.AllowedOrgs+
			" domains="+row.AllowedDomains+" status="+row.Status)
	writeJSON(w, http.StatusOK, a.rowToBody(row, t.Slug, false))
}

// repend reports whether an edit invalidates an existing approval. The three fields
// §61.11.6 names are here, plus two the approval equally rests on: a WIDENED domain
// or tenant list lets the issuer assert addresses the approver never saw. Narrowing
// either one does not — you cannot become more dangerous by admitting fewer people.
// A new client_secret does not re-pend: it is the same issuer and the same app
// registration, and forcing re-approval on a routine credential rotation would teach
// people to avoid rotating.
// ★ For a github row the approval rests on a different pair — (allowed_orgs,
// allowed_domains) instead of (issuer, allowed_domains) — because github.com is one
// issuer shared by every tenant (docs/61 §61.15 + 決定 34). So ADDING an org repends:
// the approver said "the members of these organizations", and another organization
// is another set of people they never saw. Removing one does not, for the same
// reason narrowing the domains does not.
func repend(old, next TenantIdP) bool {
	if old.Status != "active" {
		return false
	}
	if old.Issuer != next.Issuer || old.ClientID != next.ClientID || old.Trust != next.Trust {
		return true
	}
	if old.Kind != next.Kind {
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
		// ★ Activation is the operator's, and ONLY the operator's (決定 30).
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
	// ★ Approving a row that cannot be built is refused, not stored. Otherwise the
	// approval is recorded against a definition nobody can use, and the tenant is left
	// looking at an "approved" row with no button and no reason given.
	if status == "active" {
		secret, err := a.mgr.openTenantSecret(r.Context(), row.SecretEnc, row.KeyRef)
		if err != nil {
			writeAPIErr(w, &apiError{http.StatusConflict, "tenant_idp_secret_unreadable",
				"the stored client secret cannot be decrypted — the tenant has to enter it again"})
			return
		}
		if _, err := buildTenantProvider(row, t.Slug, secret); err != nil {
			writeAPIErr(w, &apiError{http.StatusBadRequest, "tenant_idp_invalid", err.Error()})
			return
		}
	}
	approvedBy, approvedAt := "", ""
	if status == "active" {
		approvedBy, approvedAt = ident.ID, nowTS()
	}
	if err := a.mgr.store.SetTenantIdPStatus(r.Context(), t.ID, row.ID, status, approvedBy, approvedAt, nowTS()); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	a.mgr.tenantIdP.invalidate()
	a.audit(r, ident, t.ID, "tenant_idp."+status, tenantProviderID(t.Slug, row.Name), "issuer="+row.Issuer)
	writeJSON(w, http.StatusOK, map[string]any{"id": row.ID, "status": status})
}

// remove (DELETE /api/admin/tenants/{slug}/idp/{id}).
//
// ★ Deleting the row does NOT undo what people signed in with it already did: the
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
	a.mgr.tenantIdP.invalidate()
	a.audit(r, ident, t.ID, "tenant_idp.delete", tenantProviderID(t.Slug, row.Name), "issuer="+row.Issuer)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": row.ID})
}

func (a tenantIdPAPI) audit(r *http.Request, ident Identity, tenantID, action, target, detail string) {
	_ = a.mgr.store.InsertAudit(r.Context(), AuditLog{
		ID: newID(), TenantID: tenantID, ActorKind: "admin", ActorID: ident.ID,
		Action: action, Target: target, Detail: detail, At: nowTS(),
	})
}

// checkDomainsUnclaimed refuses a domain another tenant's provider already claims.
//
// ★ This is the load-bearing check of the whole feature, and it is not obvious.
// allowed_domains bounds which addresses an issuer may assert. If two tenants could
// both claim acme.co.jp, the subsidiary's administrator could assert the parent
// company's addresses — and since identity is keyed by email deployment-wide, that is
// the takeover 決定 30 describes, merely one step further along. One domain, one
// tenant, exactly as auto_join_domains works (§61.9.8) — and refusing on save is the
// only moment a human is present to read why.
func (a tenantIdPAPI) checkDomainsUnclaimed(r *http.Request, tenantID, rowID string, domains []string) *apiError {
	rows, slugs, err := a.mgr.store.ListAllTenantIdPs(r.Context())
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
					"domain " + d + " is already claimed by the sign-in method of tenant " + slugs[other.TenantID]}
			}
		}
	}
	return nil
}

// validateTenantIdPBody is the save-time half of the rules the env path enforces at
// startup. It has to be a 400 rather than a fatal: a running CP cannot be brought
// down because somebody typed a bad issuer into a form (§61.11.5).
func validateTenantIdPBody(b tenantIdPBody, domains, tids, orgs []string) *apiError {
	if !validTenantIdPName(b.Name) {
		return &apiError{http.StatusBadRequest, "tenant_idp_name_invalid",
			"name must be 1-32 chars of a-z 0-9 - _ and start with a letter or digit"}
	}
	switch b.Kind {
	case tenantIdPKindOIDC:
	case tenantIdPKindGitHub:
		// ★ The org list carries the whole weight an issuer carries for OIDC. github.com
		// is one issuer for every tenant on earth, so "which organization vouches for
		// this person" is what makes the login mean anything (docs/61 §61.15 + 決定 34),
		// and it is the same rule the env path enforces by disabling GitHub outright
		// when AF_GITHUB_ALLOWED_ORGS is empty (§61.3).
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
			"kind must be " + tenantIdPKindOIDC + " or " + tenantIdPKindGitHub}
	}
	if !validIssuerURL(b.Issuer) {
		return &apiError{http.StatusBadRequest, "tenant_idp_issuer_invalid",
			"issuer must be the IdP's https issuer URL (http is accepted only for loopback)"}
	}
	// 決定 7, on the DB side: a multi-tenant Entra endpoint accepts every Microsoft
	// account in the world, and a personal account can rewrite its own email — so the
	// tenant list is what makes the domain list mean anything at all.
	if multiTenantIssuer(b.Issuer) && len(tids) == 0 {
		return &apiError{http.StatusBadRequest, "tenant_idp_tids_required",
			"this issuer is a multi-tenant endpoint: list the allowed tenant ids, or pin the issuer to one tenant"}
	}
	if b.ClientID == "" {
		return &apiError{http.StatusBadRequest, "bad_request", "client_id is required"}
	}
	switch b.Trust {
	case trustEmailVerified, trustIssuer:
	default:
		return &apiError{http.StatusBadRequest, "tenant_idp_trust_invalid",
			"trust must be " + trustEmailVerified + " (the IdP asserts email_verified) or " + trustIssuer + " (the issuer is pinned to one tenant)"}
	}
	// ★ allowed_domains is REQUIRED — the answer to docs/61 §61.14's open question.
	// A tenant-defined provider does not fall back to the deployment allowlist (決定
	// 32-3), so an empty list is not "everyone" but "nobody", and an approval that
	// admits nobody is worse than a refusal: it looks finished. Requiring it also
	// bounds which addresses this issuer may assert, which is what makes the
	// one-domain-one-tenant check above meaningful.
	if len(domains) == 0 {
		return &apiError{http.StatusBadRequest, "tenant_idp_domains_required",
			"list the email domains this sign-in method may admit (an empty list would admit nobody)"}
	}
	return nil
}
