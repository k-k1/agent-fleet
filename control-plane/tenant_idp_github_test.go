package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/auth"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// docs/log/61 §61.15 / ADR0043 decisions 34 / 35 — a tenant using GitHub as a sign-in
// method. The only difference from P4 (OIDC) is what the trust rests on; what is pinned
// here is that the difference has not disappeared from the implementation:
//
//   - a github row needs both an org and accepted domains (the issuer is shared, so the
//     org is the tenant boundary itself)
//   - the one-domain-one-tenant ledger applies regardless of kind
//   - adding an org sends the row back for approval (approval was given for "members of
//     this org")
//   - rule 1.5: the same GitHub account is the same person whether pressed from the
//     deployment's button or the tenant's. It is not an email match, so it opens no way
//     for a tenant to claim someone else

// --- row -> adapter -----------------------------------------------------------

func TestTenantGitHubRowBuildsTheGitHubAdapter(t *testing.T) {
	base := store.TenantIdP{
		Name: "github", Kind: auth.TenantIdPKindGitHub, ClientID: "c",
		AllowedOrgs: "Acme-Sub, other", AllowedDomains: "@sub.co.jp",
	}
	p, err := auth.BuildTenantProvider(base, store.TenantRef{Slug: "sub"}, "s")
	if err != nil {
		t.Fatalf("the valid row must build: %v", err)
	}
	gh, ok := p.(*auth.GitHubProvider)
	if !ok {
		t.Fatalf("kind=github must build the GitHub adapter, got %T", p)
	}
	if gh.ID() != "t:sub:github" {
		t.Fatalf("id = %q", gh.ID())
	}
	// Orgs are matched lowercased (GitHub org names are case-insensitive).
	if strings.Join(gh.AllowedOrgs, ",") != "acme-sub,other" {
		t.Fatalf("orgs = %v", gh.AllowedOrgs)
	}
	if !gh.AllowDomains["sub.co.jp"] {
		t.Fatalf("domains = %v", gh.AllowDomains)
	}
	// A row must not be able to replace github.com. If these could be moved, a tenant could
	// stand up its own server and claim any subject — forging rule 1.5's key.
	if gh.Web() != "https://github.com" || gh.API() != "https://api.github.com" {
		t.Fatalf("row must not be able to move the endpoints: %s / %s", gh.Web(), gh.API())
	}
	// The realm is "where the identity was proven". Rule 1.5 rests on it matching the
	// env-configured GitHub; without a match the account becomes a different person.
	if auth.ProviderRealm(gh) != auth.GithubWebBase {
		t.Fatalf("realm = %q", auth.ProviderRealm(gh))
	}
	// No fallback to the deployment-wide allow list or roster (decision 32-3).
	if gh.DeployAllowed != nil || gh.DBAllowed != nil || gh.DeployHasList {
		t.Fatal("a tenant row must not inherit the deployment-wide entry gate")
	}

	bad := map[string]func(*store.TenantIdP){
		"no orgs":    func(r *store.TenantIdP) { r.AllowedOrgs = "" },
		"no domains": func(r *store.TenantIdP) { r.AllowedDomains = "" },
		"no client":  func(r *store.TenantIdP) { r.ClientID = "" },
		"bad name":   func(r *store.TenantIdP) { r.Name = "Git Hub" },
		"other kind": func(r *store.TenantIdP) { r.Kind = "saml" },
	}
	for label, mutate := range bad {
		row := base
		mutate(&row)
		if _, err := auth.BuildTenantProvider(row, store.TenantRef{Slug: "sub"}, "s"); err == nil {
			t.Fatalf("%s: must be refused", label)
		}
	}
	// An empty secret (decryption failed, say) must not build either.
	if _, err := auth.BuildTenantProvider(base, store.TenantRef{Slug: "sub"}, ""); err == nil {
		t.Fatal("an empty client_secret must be refused")
	}
}

// --- save-time validation (the same rules on the API side and at runtime) ------

func TestTenantGitHubSaveTimeValidation(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p4Manager(t, st)
	tn, _ := st.CreateTenant(ctx, "sub", "子会社")
	other, _ := st.CreateTenant(ctx, "sibling", "別会社")
	seedTenantIdP(t, st, other.ID, "entra", "sibling.co.jp", "active")
	if _, err := st.UpsertIdentity(ctx, "boss@acme.co.jp", "boss-acme-co-jp", "super_admin"); err != nil {
		t.Fatalf("super admin: %v", err)
	}
	api := newTenantIdPAPI(mgr, nil)
	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/admin/tenants/sub/idp", strings.NewReader(body))
		r.SetPathValue("slug", "sub")
		r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
		w := httptest.NewRecorder()
		api.upsert(w, r)
		return w
	}

	if w := post(`{"name":"github","kind":"github","client_id":"c","client_secret":"s","allowed_domains":"@sub.co.jp"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("no orgs must be refused — membership is what authorizes the login: %d %s", w.Code, w.Body.String())
	}
	if w := post(`{"name":"github","kind":"github","client_id":"c","client_secret":"s","allowed_orgs":"acme-sub"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("no domains must be refused: %d %s", w.Code, w.Body.String())
	}
	// One domain, one tenant, regardless of kind. GitHub cannot forge a verified email on
	// another company's domain, but taking the ledger slot first blocks that company from
	// registering at all.
	if w := post(`{"name":"github","kind":"github","client_id":"c","client_secret":"s","allowed_orgs":"acme-sub","allowed_domains":"sibling.co.jp"}`); w.Code != http.StatusConflict {
		t.Fatalf("a domain another tenant claims must be refused: %d %s", w.Code, w.Body.String())
	}
	if w := post(`{"name":"github","kind":"saml","client_id":"c","client_secret":"s","allowed_domains":"@sub.co.jp"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("an unknown kind must be refused: %d %s", w.Code, w.Body.String())
	}
	if w := post(`{"name":"github","kind":"github","client_id":"c","client_secret":"s","allowed_orgs":"Acme-Sub","allowed_domains":"@SUB.co.jp","issuer":"https://evil.example/","trust":"issuer"}`); w.Code != http.StatusOK {
		t.Fatalf("the valid row must be accepted: %d %s", w.Code, w.Body.String())
	}
	rows, _ := st.ListTenantIdPs(ctx, tn.ID)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	// issuer and trust never come from the form. A github row has exactly one source of
	// identity, and letting the row declare it makes the registry and the audit log lie.
	switch row := rows[0]; {
	case row.Issuer != auth.GithubWebBase:
		t.Fatalf("issuer = %q, want the constant %q", row.Issuer, auth.GithubWebBase)
	case row.Trust != auth.TrustAPI:
		t.Fatalf("trust = %q, want %q", row.Trust, auth.TrustAPI)
	case row.AllowedOrgs != "acme-sub":
		t.Fatalf("orgs = %q (must be normalized on save)", row.AllowedOrgs)
	case row.AllowedDomains != "sub.co.jp":
		t.Fatalf("domains = %q (must be normalized on save)", row.AllowedDomains)
	case row.Status != "pending":
		t.Fatalf("status = %q — a new row is never born active (決定 30)", row.Status)
	}
}

// Approval was given for "members of this org, within this domain range". Adding an org
// changes the set of people it covers, so approval starts over; removing one does not.
func TestGitHubRowRepends(t *testing.T) {
	active := store.TenantIdP{
		Kind: auth.TenantIdPKindGitHub, Status: "active", ClientID: "c", Issuer: auth.GithubWebBase,
		Trust: auth.TrustAPI, AllowedOrgs: "acme-sub", AllowedDomains: "sub.co.jp",
	}
	widened := active
	widened.AllowedOrgs = "acme-sub,acme-parent"
	if !repend(active, widened) {
		t.Fatal("adding an organization must send the row back for approval")
	}
	narrowed := active
	narrowed.AllowedOrgs = ""
	if repend(active, narrowed) {
		t.Fatal("removing an organization admits fewer people and must not repend")
	}
	rotated := active
	rotated.SecretEnc = "new"
	if repend(active, rotated) {
		t.Fatal("a client_secret rotation must not repend (or nobody rotates)")
	}
	switched := active
	switched.Kind = auth.TenantIdPKindOIDC
	if !repend(active, switched) {
		t.Fatal("changing the kind changes what was approved")
	}
}

// --- rule 1.5 (decision 35) ---------------------------------------------------

// The same GitHub account pressed from the deployment's button and from the tenant's. With
// P4 alone the second is refused as email_taken and someone holding both roles is locked
// out. The grounds are realm and subject matching, i.e. GitHub itself saying it is the
// same account.
func TestRule15JoinsTheSameIdPAccountAcrossButtons(t *testing.T) {
	st, ctx := newLinkStore(t), t.Context()
	const email = "yamada@acme.co.jp"

	first, _, err := st.LinkIdentity(ctx, store.IdentityLink{
		Provider: auth.GithubProviderID, Subject: "42", Realm: auth.GithubWebBase, Email: email,
		FallbackKey: sanitizeUser(email), EmailJoin: true,
	})
	if err != nil {
		t.Fatalf("deployment github: %v", err)
	}
	// A tenant-defined row, so EmailJoin=false — rule 2 is not available here.
	second, isNew, err := st.LinkIdentity(ctx, store.IdentityLink{
		Provider: "t:sub:github", Subject: "42", Realm: auth.GithubWebBase, Email: email,
		FallbackKey: sanitizeUser(email), EmailJoin: false,
	})
	if err != nil {
		t.Fatalf("tenant github must not be refused: %v", err)
	}
	if isNew || second.ID != first.ID || second.UserKey != first.UserKey {
		t.Fatalf("同じ GitHub アカウントが別人になった: %+v want %+v (isNew=%v)", second, first, isNew)
	}
	if n := countRows(t, st, "identity"); n != 1 {
		t.Fatalf("identity rows = %d, want 1", n)
	}

	// A different realm means a different account even when the subject happens to match.
	// Numeric subjects can collide across IdPs, so loosening this lands one person in
	// another person's workspace.
	if _, _, err := st.LinkIdentity(ctx, store.IdentityLink{
		Provider: "t:sub:keycloak", Subject: "42", Realm: "https://idp.sub.co.jp/realms/x",
		Email: email, FallbackKey: sanitizeUser(email), EmailJoin: false,
	}); err == nil {
		t.Fatal("別 realm・同 subject は結合してはいけない（email はすでに他人のもの）")
	}

	// A row carrying no realm (pre-0041, through the proxy) stays refused.
	if _, _, err := st.LinkIdentity(ctx, store.IdentityLink{
		Provider: "t:sub:entra", Subject: "99", Email: email,
		FallbackKey: sanitizeUser(email), EmailJoin: false,
	}); err == nil {
		t.Fatal("realm 無しで email だけ一致する行は拒否のまま（決定 32）")
	}
}

// Backfill at startup. Rows written before 0041 carry an empty realm, and left that way
// someone who signed in through the deployment's GitHub is refused at the tenant's button.
func TestFillProviderRealmMakesOldRowsJoinable(t *testing.T) {
	st, ctx := newLinkStore(t), t.Context()
	const email = "yamada@acme.co.jp"

	// A login recorded when the realm was not known.
	first, _, err := st.LinkIdentity(ctx, linkOf(auth.GithubProviderID, "42", email, true))
	if err != nil {
		t.Fatalf("legacy login: %v", err)
	}
	if _, _, err := st.LinkIdentity(ctx, store.IdentityLink{
		Provider: "t:sub:github", Subject: "42", Realm: auth.GithubWebBase, Email: email,
		FallbackKey: sanitizeUser(email), EmailJoin: false,
	}); err == nil {
		t.Fatal("埋め戻す前は拒否される（この状態を直すのが FillProviderRealm）")
	}

	if err := st.FillProviderRealm(ctx, auth.GithubProviderID, auth.GithubWebBase); err != nil {
		t.Fatalf("fill: %v", err)
	}
	joined, _, err := st.LinkIdentity(ctx, store.IdentityLink{
		Provider: "t:sub:github", Subject: "42", Realm: auth.GithubWebBase, Email: email,
		FallbackKey: sanitizeUser(email), EmailJoin: false,
	})
	if err != nil {
		t.Fatalf("after fill: %v", err)
	}
	if joined.ID != first.ID {
		t.Fatalf("埋め戻し後も別人のまま: %s want %s", joined.ID, first.ID)
	}

	// An already recorded realm is never overwritten, so that a deployment repointed at
	// another provider id cannot rewrite what past logins actually were.
	if err := st.FillProviderRealm(ctx, "t:sub:github", "https://idp.example/"); err != nil {
		t.Fatalf("re-fill: %v", err)
	}
	var realm string
	if err := st.DB().QueryRowContext(ctx,
		`SELECT realm FROM identity_provider WHERE provider='t:sub:github'`).Scan(&realm); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if realm != auth.GithubWebBase {
		t.Fatalf("realm was overwritten: %q", realm)
	}
}

// --- accepted but not offered (§61.15.9) --------------------------------------

// The shape: a subsidiary wants to run on "our own GitHub only", but a person seconded
// from the parent company can sign in only through the parent's method. Dropping the
// parent's method from what is accepted locks that person out, so it must be possible to
// drop the button alone.
//
// What is pinned here is "hidden still gets in" — display is display, not a gate
// (decision 14).
func TestHiddenProvidersHideTheButtonButNotTheDoor(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p4Manager(t, st)
	tn, _ := st.CreateTenant(ctx, "sub", "子会社")
	seedTenantIdP(t, st, tn.ID, "entra", "sub.co.jp", "active")
	// Accepted: the subsidiary's own method plus the parent's google. Shown as a button:
	// the subsidiary's own method only.
	if err := st.SetTenantLogin(ctx, tn.ID, "t:sub:entra,google", "", "", "google"); err != nil {
		t.Fatalf("set login rules: %v", err)
	}

	cfg := config{
		publicBaseURL: "https://af.example.com",
		cookieSecret:  []byte("0123456789abcdef0123456789abcdef"),
		mgr:           mgr,
	}
	cfg.setProviders([]auth.LoginProvider{&auth.OIDCProvider{ProviderID: "google", LabelJA: "Google でサインイン"}})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login/{slug}", cfg.handleLogin)
	body := func(path string) string {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w.Body.String()
	}

	page := body("/login/sub")
	if strings.Contains(page, "provider=google") {
		t.Fatalf("隠した方式のボタンが出ている:\n%s", page)
	}
	if !strings.Contains(page, "t%3Asub%3Aentra") {
		t.Fatalf("自社の方式のボタンは出ていないといけない:\n%s", page)
	}
	// The point of the test: hidden or not, someone who signed in with that method can use
	// this tenant.
	ok, _ := mgr.tenantLogin.providerAllowed(ctx, tn.ID, "google")
	if !ok {
		t.Fatal("隠した方式が門でも閉じている — 兼務の人が締め出される（表示と強制を混ぜている）")
	}

	// When everything is hidden, the hide list is ignored instead: a login page with no
	// button is a dead end, and a tenant's misconfiguration must not be able to create one.
	if err := st.SetTenantLogin(ctx, tn.ID, "", "", "", "google,t:sub:entra"); err != nil {
		t.Fatalf("set login rules: %v", err)
	}
	mgr.tenantLogin.invalidate()
	if page := body("/login/sub"); !strings.Contains(page, "provider=google") {
		t.Fatalf("全部隠したときはボタンを出す（行き止まりを作らない）:\n%s", page)
	}
}

// --- telling the buttons apart (docs/log/61 §61.15.10) ---------------------------

// The env GitHub and a tenant's GitHub sit on the same page (/login/<slug>). Their ids do
// not collide, but the id is not on the button — while both keep the same default "sign in
// with GitHub" label, two indistinguishable buttons send people to different OAuth apps.
//
// Three things are pinned:
//   - a tenant row's default label carries the tenant name, so it differs from the env
//     button's string
//   - a row's own label_ja / label_en wins (the precedence is unchanged)
//   - both languages are covered (fixing Japanese alone leaves the collision in English)
func TestTenantGitHubButtonSaysWhichCompanyItIs(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p4Manager(t, st)
	tn, _ := st.CreateTenant(ctx, "sub", "子会社")
	row := store.TenantIdP{
		ID: store.NewID(), TenantID: tn.ID, Name: "github", Kind: auth.TenantIdPKindGitHub,
		Issuer: auth.GithubWebBase, ClientID: "c", SecretEnc: "s", Trust: auth.TrustAPI,
		AllowedOrgs: "acme-sub", AllowedDomains: "@sub.co.jp",
		Status: "active", CreatedAt: store.NowTS(), UpdatedAt: store.NowTS(),
	}
	if err := st.CreateTenantIdP(ctx, row); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cfg := config{
		publicBaseURL: "https://af.example.com",
		cookieSecret:  []byte("0123456789abcdef0123456789abcdef"),
		mgr:           mgr,
	}
	// The deployment's own GitHub button (from env), carrying the default wording.
	cfg.setProviders([]auth.LoginProvider{&auth.GitHubProvider{
		ProviderID: auth.GithubProviderID, LabelJA: "GitHub でサインイン", LabelEN: "Sign in with GitHub",
	}})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login/{slug}", cfg.handleLogin)
	body := func(accept string) string {
		r := httptest.NewRequest(http.MethodGet, "/login/sub", nil)
		r.Header.Set("Accept-Language", accept)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Body.String()
	}

	for _, tc := range []struct{ lang, accept, env, tenant string }{
		{"ja", "ja,en;q=0.8", "GitHub でサインイン", "GitHub でサインイン（子会社）"},
		{"en", "en-US,en;q=0.9", "Sign in with GitHub", "Sign in with GitHub (子会社)"},
	} {
		page := body(tc.accept)
		if !strings.Contains(page, tc.tenant) {
			t.Fatalf("%s: テナント行のボタンに会社名が入っていない（%q が無い）:\n%s", tc.lang, tc.tenant, page)
		}
		// The env button keeps its original wording, and the tenant row's wording contains
		// it, so "two buttons with the same string" is checked by the occurrence count.
		if n := strings.Count(page, tc.env); n != 2 {
			t.Fatalf("%s: %q の出現が %d（env の 1 つ＋テナント行の接頭辞 1 つ = 2 のはず）:\n%s",
				tc.lang, tc.env, n, page)
		}
		if !strings.Contains(page, "provider="+auth.GithubProviderID) || !strings.Contains(page, "t%3Asub%3Agithub") {
			t.Fatalf("%s: 両方のボタンが出ていない:\n%s", tc.lang, page)
		}
	}

	// A row that writes its own label wins: the point is to fill in a default, not to
	// overwrite wording an administrator chose.
	row.LabelJA, row.LabelEN = "子会社の GitHub", "Subsidiary GitHub"
	p, err := auth.BuildTenantProvider(row, store.TenantRef{Slug: "sub", Name: "子会社"}, "s")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if p.Label("ja") != "子会社の GitHub" || p.Label("en") != "Subsidiary GitHub" {
		t.Fatalf("行が書いたラベルが勝っていない: %q / %q", p.Label("ja"), p.Label("en"))
	}
}

// A tenant with no display name (empty name) falls back to the slug: knowing which button
// is which matters more than being able to read the company name.
func TestTenantLabelFallsBackToTheSlug(t *testing.T) {
	if got := auth.TenantLabelSuffix("GitHub でサインイン", store.TenantRef{Slug: "sub"}, "ja"); got != "GitHub でサインイン（sub）" {
		t.Fatalf("ja = %q", got)
	}
	if got := auth.TenantLabelSuffix("Sign in with GitHub", store.TenantRef{Slug: "sub"}, "en"); got != "Sign in with GitHub (sub)" {
		t.Fatalf("en = %q", got)
	}
	// Neither slug nor name does not happen in practice; when it does, no suffix is added.
	if got := auth.TenantLabelSuffix("x", store.TenantRef{}, "ja"); got != "x" {
		t.Fatalf("empty tenant = %q", got)
	}
}
