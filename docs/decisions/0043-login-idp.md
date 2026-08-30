# 0043. For login IdPs, do not add per-provider implementations — one generic OIDC client plus a dedicated one for GitHub alone — and put the same-person guarantee in before GitHub

English | [日本語](0043-login-idp.ja.md)

- Status: adopted; **P0 / P1 / P2 / P3 implemented** (P4 remains. 2026-08-14. The design is docs/61. On
  the same day, following a requirement to split one deployment into tenants by department,
  **decisions 13–28 (per-tenant login, the division of responsibility, delegation and departure, P3)
  were added**. Of those, decisions 15/16 came from a review prompted by the observation that "it is
  enough to manage, per tenant, which IDs can log in", and measuring that the invitation API already
  exists **dropped the tenant-side email list from the design**. Decision 16 was revised when P3 was
  implemented — the union stays within the email axis. See below.)
- See also: [61-login-idp.md](../log/61-login-idp.md) /
  [build/07-security.md](../build/07-security.md) §7.3 (the three AUTH modes = the current contract) /
  [build/06-data.md](../build/06-data.md) (`identity` / `user_key`) /
  [0001-self-host-vs-saas.md](0001-self-host-vs-saas.md) (each company self-hosts, so the IdP is theirs)

## Context

The L1 login IdP is fixed to Google (`control-plane/oauth_google.go`). Self-hosting presumes "each
group company hosts its own employees" (roadmap §12.2), so a company not using Google Workspace cannot
even install it. The majority of prospective customers are on M365, which means the distribution
premise is missing.

Three things measured in the code decided the design:

- **The OAuth wiring is light.** The Google implementation does not verify the id_token; it just goes
  authorisation code → token → userinfo to get the email (`oauth_google.go:238-256`). There is no JWT
  library (`go.mod`) and it is all stdlib. A generic OIDC client differs only by reading a discovery
  JSON once.
- **The heavy part is the identity side.** `identity.user_key = sanitizeUser(email)`
  (`resolver.go:281`) is **the workspace's home directory name and what the encrypted secrets belong
  to** (build/07 §7.2). In other words the email *is* the identity. `disambiguateUserKey`
  (`store_sqlite.go:337`) already prevents identities merging through a sanitisation collision, so this
  uniqueness is deliberately protected.
- **What makes an email trustworthy differs per IdP.** Today `email_verified` is checked (Google emits
  it), but **Entra ID does not emit that claim**. Requiring `email_verified == true` uniformly locks
  Entra out; dropping it lets "register someone else's company email on GitHub and pass the allowlist"
  succeed.

## Decisions

1. **Do not add per-provider implementations; build one generic OIDC client.** Entra ID / Okta /
   Keycloak / Auth0 / Cognito / GitLab all ride on it. Google moves internally to being one instance of
   that implementation (**the env names stay** — existing deployments work without changing a single
   line of configuration).
2. **The only dedicated adapter written is GitHub's.** GitHub does not support OIDC (its OIDC is a
   token for Actions, not for user login), so it is unavoidable. It goes in **only together with org
   membership checking** — a personal GitHub account is outside the company's control, so it must not
   be opened up by an email allowlist alone.
3. ★ **Put the same-person guarantee (P1) in before GitHub (P2).** A GitHub account's registered email
   is normally different from the company email, and since `user_key` derives from the email, shipping
   without a linking mechanism gives **two identities, two homes and separate secrets**. To the user it
   looks like "my repositories are gone". The order is **P0 generic OIDC → P1 linking → P2 GitHub**.
   P0 alone satisfies "I want to log in with Microsoft".
   (An addendum following the withdrawal of decision 5: P1 became **fixing identity by
   `(provider, subject)`** rather than "linking". The premise for shipping GitHub changed to defining
   it as **for people who have registered their company email with GitHub**, and a different email may
   simply mean a different workspace. The order P0 → P1 → P2 itself stands.)
4. **`user_key` is immutable; `(provider, subject)` is added alongside.** A new table
   `identity_provider` (`0038`) is created and `identity` itself is untouched. Rebuilding `user_key` on
   `sub` would require **migrating the data of everyone on every existing deployment**, since it is the
   home directory name, and it would make `af-ws-<user>` unreadable to humans. Migrating an existing
   deployment is just writing a `(google, sub)` row at first login (zero migration).
5. ~~**Joining a different email is not done from the login screen.** It joins only when a second IdP
   is passed through "add an account" in the Console while already signed in (= proof of being able to
   log in to both).~~
   ★ **Withdrawn (2026-08-14, during P1) — do not build a mechanism for joining a different email at
   all.** Being able to log in to both proves only "this person can operate those two accounts", not
   **that they are the same person**. If even one weakly verifying IdP is enabled, it becomes a route
   for merging an account obtained there into the company account's home (the side holding the
   company's secrets), and **there is no route to undo a join: once merged, there is no way back**.
   Moreover, if the entrance allowlist is limited to the company domain, **the only emails that can log
   in are company emails**, and a different email only appears where the operation chose to loosen the
   domain — in which case separate workspaces are the configured outcome. GitHub (P2) fits the same
   line once defined as "for people who have registered their company email with GitHub".
   As a result P1 became just rules 1–3 of §61.5, with **zero work on the Console side**. The idea of an
   admin joining them via a mapping table remains rejected as in the first edition (the table does not
   get maintained).
6. **Make the grounds for trusting an email a per-provider declaration (`trust`).** Three kinds:
   `email_verified` / `issuer` (guaranteed by pinning the tenant — Entra) / `api` (a verification flag
   from another API — GitHub). **A provider with no declaration is refused at startup**, so fail-closed
   is not dropped.
7. **Do not let Entra be accepted on the `common` endpoint.** Either pin the issuer to a tenant or
   require a `tid` allowlist. `common` with `ALLOWED_TIDS` unset is **fatal**. Allowing it puts "every
   human being with a Microsoft account" at the door, and a personal MSA can change its email, so the
   email allowlist loses meaning.
8. **redirect_uri stays a single `/oauth2/callback`**, with the provider carried in the signed state
   cookie. Splitting the URI per provider multiplies the work an installing company must do at the IdP
   by the number of IdPs. On the callback, the state's provider id is **checked against the configured
   set** before branching.
9. **The id_token's signature is still not verified.** It is the authorisation code flow, with a
   client_secret, received over TLS directly from the token endpoint (the same rationale as the note in
   OIDC Core §3.1.3.7). Claims that do not appear in userinfo, such as `tid`, are read from **the
   id_token payload in the same response, without signature verification**. This premise is pinned at
   the top of `oauth_oidc.go`, stating that **adding any path that receives an id_token on the front
   channel makes JWKS verification mandatory**. Go's dependencies stay at zero additions.
10. **SAML is not implemented in the CP.** Japanese companies' IdPs (HENNGE One, TrustLogin, CloudGate
    and so on) often presume SAML, but an SP implementation is several times the surface of OIDC and
    does not fit in stdlib. **The existing `AUTH=proxy` plus oauth2-proxy / Keycloak bridge** is
    documented as the official answer.
11. **One IdP's misconfiguration must not lock everybody out.** An individual provider that is
    under-configured is disabled with a warning; **only zero valid providers is fatal** (preserving
    current behaviour). If every allowlist is empty, it refuses everything with a warning, as today.
12. **Do not lose the property that offboarding re-evaluates on every request.** Only GitHub's org
    check cannot be evaluated locally, so it gets a TTL cache keyed by `(provider, subject)` (10
    minutes by default), plus a grace on API failure that keeps the last positive result alive (1 hour
    by default) and then refuses.
    (An addendum from implementing P2 — the user's call: re-evaluation needs **the person's own access
    token**. Putting it in a cookie would leak it to XSS, so it is held only in process memory, and
    therefore **it is lost along with the cache when the CP restarts**. At that moment the person is
    still an org member, so "not permitted" (`forbidden`) is untrue. An error code **`reauth`** was
    added to **demand a re-login** — preserving fail-closed while letting them return without any
    interaction if their GitHub session is alive. The API returns **401** rather than 403, so it rides
    the SPA's existing unauthenticated path. Not holding the material for a judgement is a different
    thing from not being permitted.)

### Per-tenant login (P3, department split, docs/61 §61.9)

13. ★ **Put "the entrance gate" and "the tenant gate" in different layers.** The entrance (may this
    person sign in to this deployment?) is judged per request by `authGate`, and the tenant (may they
    use this tenant right now?) is judged in `resolveFull` / `selectMembership`. **Do not bring tenant
    rules into `authGate`** — `authGate` does not know about tenants (`X-AF-Tenant` is read at
    `httpapi.go:34`), so "which tenant are we judging for?" is undecidable and it is guaranteed to be
    either a hole or over-refusal.
14. ★ **A tenant named in a URL is not grounds for authorisation.** `/login/<slug>` is only a hint for
    which screen to show, and the user can rewrite it. Which tenant they can actually enter is decided
    solely by the server-side membership plus the tenant rules. For the same reason, a per-tenant
    `allowed_providers` **is not enough as a filter on the screen's buttons**: the session's `prov` is
    checked and enforced at tenant resolution (otherwise one can log in with GitHub at the generic
    `/login` and slip through by swapping `X-AF-Tenant`). Half the value of adding the `prov` claim in
    decision 1 is here.
15. ★ **The roster of "which IDs can log in per tenant" is held by membership; no `allowed_emails` is
    added to the tenant.** Measurement showed the vessel already exists —
    `POST /api/admin/memberships` (`tenants.go:254`) **creates the identity of someone who has not yet
    logged in** from an email and attaches a membership, i.e. it is an invitation, and there is UI for
    it in the Console's admin tab (`AdminTab.tsx:1593`). Adding an email list on the tenant side would
    manage the same "who may enter" in **two ledgers**, which will inevitably diverge. This also makes
    **a department split work even at a company with one shared domain** (a roster does not depend on a
    domain). What the tenant holds is only two things: `auto_join_domains` (automatic joining, for
    convenience) and `allowed_domains` (**a guard on invitations only**, preventing a tenant_admin from
    adding domains outside their department). `allowed_domains` is not a per-request constraint —
    properly invited contractors (a different domain) would be locked out, an exception list would be
    needed, and we would be back to two ledgers. **Continuing eligibility is held by membership.**
16. **Make the entrance check a union, and include "having a membership" in it**
    (the deployment-wide allowlist ∪ each tenant's `auto_join_domains` ∪ membership).
    ★ The last term is the connection missing today: even an invited person is refused at the entrance
    by `authGate` unless they are in `AF_OAUTH_ALLOWED_*` (env). Connecting it makes **the env allowlist
    unnecessary on an invitation-run deployment**, gathering the roster into membership alone. An
    intersection would mean adding to the env on every invitation — two ledgers again. A union adds no
    danger, because passing the entrance does not mean being able to enter anywhere (decision 13).
    **If everything is empty it refuses everything, as today.**
    ★ **Revised (2026-08-14, during P3) — the union is *within the email axis only*, and it splits into
    two terms.** Writing it naively as `provider.Allowed(...) || hasMembership(...)` breaks in two ways.
    (a) **GitHub's org check can be bypassed** — P2's GitHub is an AND of two gates, "org ∧ email
    allowlist" (decision 2), so merely holding a membership would let someone outside the org in.
    (b) **A provider-specific list's narrowing silently widens** — P0's "a provider-specific list
    replaces the common list" is a specification usable as per-provider narrowing ("Google for the whole
    company, Entra for the subsidiary's domain only"), and unioning it with the common list is a
    regression.
    So the adopted form is `( provider-specific | deployment-common ) ∪ ( auto_join | membership )` —
    **the replacement rule stays exactly as in P0, and only the DB-derived terms are always ORed**.
    Checks of a different kind (GitHub's org) stay ANDed, and DB-derived terms are only added to the
    email-side gate. This achieves "removing the two ledgers" (an invited person passes the entrance in
    either form) and made revising P0's documented description unnecessary. The details and the
    regression tests are in the revised section of docs/61 §61.9.6.
17. **The login screen is split by path (`/login/<slug>`).** A subdomain approach needs wildcard DNS
    and certificates, Funnel can only expose one hostname, and it multiplies redirect_uris, breaking
    decision 8. **An unknown slug returns the generic screen rather than a 404** (do not leak whether a
    tenant slug exists to an unauthenticated visitor).
18. **A session holds exactly one provider.** When moving between tenants requires signing in again, it
    returns `provider_required` and **guides to a re-login rather than ending at a 403** ("this tenant
    requires signing in with Microsoft"). Holding several providers at once would make the cookie "a set
    of authorisation states" and blur what revocation and offboarding mean.
19. **Tenant rules live in the database (`tenant` columns, `0039`), not in env.** Tenants are created at
    runtime, so `AF_TENANT_<SLUG>_…` is bound to drift. The admin API already exists
    (`routes.go:129-137`). Per-request lookups use a short TTL (30-second) cache, invalidated by admin
    API writes. Note that the current `emailAllowed` does **an `os.ReadFile` on every request** when an
    allowlist file is specified (`oauth_google.go:130`); DB plus cache is lighter than that.
    ★ **A decision made during implementation (P3) — only `super_admin` may edit these three columns**
    (`PUT /api/admin/tenants/{slug}/login` is `withSuperAdmin`). It looks like the opposite direction
    from decision 26, which opened workspace / home to tenant_admin, but two of the three **take effect
    outside the tenant**: `auto_join_domains` opens **the deployment's entrance** by domain (the union
    term in decision 16), and `allowed_providers` is the choice of which IdP may declare "who someone
    is". What a tenant_admin holds is their own tenant's roster and those people's workspaces and homes.
    That also matches the operating picture in docs/61 §61.10.3 (creating a tenant and configuring it
    are super_admin).

    Two validations were added at save time while we were there: **two tenants cannot hold the same
    `auto_join_domains`** (do not allow decision 15's conflict to be created; the "ascending slug" rule
    remains for existing rows), and **a provider id that does not exist on the deployment cannot be
    written into `allowed_providers`** (preventing a login page that silently has no buttons).
20. **P3 (per-tenant login) is independent of P1 / P2** and may be started right after P0 (the only
    dependency is `prov`). Even a company that does not use GitHub gets value from "split by department
    into tenants and restrict to Entra".
21. **IdP group (Entra `groups` / GitHub team) → tenant synchronisation is not added.** Decision 15
    moved the roster to membership, so it is no longer required, and the remaining benefit is only
    automatically following transfers. Adding it would break the single source of truth ("membership is
    canonical") and mean handling synchronisation conflicts. If it is ever added, it must not overwrite
    membership — it should **show the differences in the admin screen for a human to approve**.
    (Entra's `groups` degenerates into a Graph lookup on overage, so the implementation is heavier than
    it looks.)
22. ★ **The membership deletion/disabling API must be in P3's scope.** A hole found by writing the
    operations down: today `MembershipStore` (`store.go:383-402`) has only `EnsureMembership`
    (insert-only) and `SetMembershipRole`, and `routes.go` has no `DELETE /api/admin/memberships`. The
    reason that has sufficed is that offboarding worked by **removing someone from the env/file
    allowlist** (the per-request re-evaluation at `oauth_google.go:299-309`). Moving the roster to
    membership in decisions 15/16 **removes that route** — disabling someone at the IdP still leaves a
    signed session cookie valid for up to `AF_SESSION_TTL` (168h = 7 days by default), so without a way
    to remove them they stay for up to 7 days. **Neither a transfer (remove from the old department) nor
    a departure (remove from everything) can be carried out.**
    `Membership` has a `Status` and `GetMembershipByID` already anticipates missing/inactive, so
    **a logical delete (`status='inactive'`) is enough** (they can be locked out while the workspace and
    home remain). What happens to the workspace and home afterwards follows
    [0028-deletion-lock](0028-deletion-lock.md) and the staged cleanup; nothing is deleted immediately.
    ★ **An addendum from implementation (P3) — `EnsureMembership` must not carry "resurrection".**
    The same function is used by `auto_join_domains` and by `AF_PROVISION=auto`'s automatic
    provisioning, so setting `status='active'` there would **automatically return a removed person to
    the roster on their next login**. Resurrection is done explicitly by the invitation API only. For
    the same reason **auto_join "does not join if a row exists (even inactive)"** — decision 15's
    "membership is canonical if it exists" includes "was removed".
    We also made it so **you cannot remove yourself** (there is no undo inside the product).
    → **On 2026-08-21 this was narrowed to "refuse only your *last* active membership"** (docs/61
    §61.10.6). What is being protected is the way back, not whether the row is yours; refusing all of
    them means you cannot tidy up a throwaway tenant you created yourself from within the product
    (docs/64 §64.28).
23. **`super_admin` can enter the admin screen without a membership (P3).**
    Following the current code, starting with `AF_PROVISION=invite` means the first person cannot get in
    — with zero memberships, `GET /api/tenants` is 403 `not_provisioned` (`resolver.go:69`), the Console
    does not set `superAdmin` on the `data.error` branch (`tenant.ts:93-100`, defaulting to `false`),
    and the admin menu's display condition `superAdmin || tenant_admin` (`TopBar.tsx:319`) is false. The
    admin API itself passes on `identityFor` alone, so hitting the API directly works, but that is not a
    workable procedure.
    ~~Until it is fixed, the practice is "start with `auto` → create the tenant and invite → switch to
    `invite`"~~
    → ✅ **Resolved in P3** (`tenantAPI.list`). **You may start with `AF_PROVISION=invite` as it is.**
    → ✅ **P7-2 made that the default** (in the templates only; see "options rejected" above). Alongside
    it, **a landing surface before invitation** was added: anyone other than `super_admin` still gets a
    403 `not_provisioned` as before, but the Console treats it **as a state rather than an error**
    (`notProvisioned` in `tenant.ts`) and draws a screen saying "you have not been invited yet / you are
    signed in with this address".
    ★ **Point ③, letting them read their own address, is the substance.** An admin adds people to the
    roster by address, and if they cannot read it, a round trip is always added — and the more sign-in
    methods a person has, the less they know which one they used. Previously the ordinary Console opened
    in that state and every subsequent request was rejected with a 403, producing one toast at a time.
    ★ Taking "right after deployment, *only* super_admin can sign in" literally is not adopted (it would
    mean requiring a membership in `authGate`, conflicting with decision 15's "do not put the tenant gate
    in authGate"). Since passing sign-in with no membership gives access to not one surface, **all that
    changed is how the refusal looks; not one gate moved.**

### Division of responsibility (confirmed 2026-08-14)

24. **Designating `super_admin` stays in the host's env (`SUPER_ADMIN_EMAILS`, `main.go:85`), with no
    promotion from the Console.** The authority to operate the whole deployment belongs only to whoever
    installed it, i.e. whoever can touch the host's files. If it could be granted inside the app, someone
    other than the installer could create that authority. Unlike the allowlists, it is **read once at
    startup rather than live**, so changing it requires a CP restart.
    ★ **But it cannot be revoked (a current hole, closed in P3).** `UpsertIdentity` is
    "upgrade (never downgrade)" (`store_sqlite.go:314-317`), so removing someone from
    `SUPER_ADMIN_EMAILS` and restarting leaves `identity.role` as `super_admin`, and there is no
    demotion API (`setMembershipRole` covers tenant roles only) — leaving no means other than editing the
    database directly. env is made the single source of truth and **synchronised in bulk when the CP
    starts** (dropping any `super_admin` not in `SUPER_ADMIN_EMAILS` to `user`). ★ The reason it is at
    **startup** rather than at login is the delegation and departure cases — someone who has left never
    logs in again, so login-time synchronisation leaves `super_admin` in the database forever (docs/61
    §61.10.7). At startup it coincides with the delegation procedure of editing env and restarting.
    An implementation note — **do not put the demotion in `UpsertIdentity`'s `roleHint`**.
    `addMembership` (`tenants.go:285`), `cleanHome` (`:195`) and `stopWorkspace` (`:149`) call it with
    `roleHint=""`, so merely adding someone to a tenant would drop a super_admin. A bulk UPDATE at
    startup does not go through `roleHint` and structurally avoids the trap.
    ★ **An addendum from implementation (P3) — do not demote an identity with an empty `email`.**
    `SUPER_ADMIN_EMAILS` is a list of emails, so dropping rows that have no email (the fixed user of
    `AUTH=dev`, for instance) would make **the documented recovery procedure (write the env and restart)
    unable to restore them**. Anyone demoted is recorded in the CP's log.
25. **Creating tenants and appointing `tenant_admin` are `super_admin`'s.** The implementation is already
    this and needs no change (`POST /api/admin/tenants` is `withSuperAdmin` = `routes.go:133`; only
    super_admin can grant `tenant_admin` = `tenants.go:280`; `PUT /api/admin/membership-role` is also
    `withSuperAdmin`). **These two are the only places** super_admin appears in day-to-day operation.
26. ★ **Deleting a workspace or home is `tenant_admin`'s responsibility.** The department knows its own
    headcount; do not make them ask IT every time. **Currently `clean-home` is super_admin only**
    (`adm.withSuperAdmin(adm.cleanHome)` at `routes.go:136`; the handler does not check tenant membership
    either), so **P3 switches it to a `tenantAdminFor` gate inside the handler**. The precedent is
    `stopWorkspace` in the same file (`tenants.go:145`), which lets a tenant_admin delete only the homes
    of their own department's members. The order is "disable the membership → stop the workspace → delete
    the home", and since stopping is already available to tenant_admin, the only thing to align is
    `clean-home`. Not deleting immediately follows [0028-deletion-lock](0028-deletion-lock.md) and the
    staged cleanup. Decision 22's membership-disabling API is likewise opened to tenant_admin (for their
    own tenant).
    → ✅ **Implemented in P3**: `withSuperAdmin(adm.cleanHome)` was removed from `routes.go` and
    `tenantAdminFor` is taken inside the handler. **Because the privilege widened, it is always audited**
    (`workspace.clean_home`).
27. ★ **The only way to revoke a session immediately is rotating `AF_COOKIE_SECRET`. Write it in the
    runbook.** The session cookie is stateless (an HMAC over `{email, exp}`, `oauth_google.go:85-93`);
    there is no server-side session store, no individual revocation and no "log out of all devices". The
    reason that has sufficed is that **removing someone from the allowlist made `authGate` refuse them on
    every request** (`:299-309`) — the de facto revocation mechanism. Once decisions 15/16 move the
    roster to membership, that role is taken over by disabling the membership, but
    ★ **it does not close on a deployment that also uses `AF_OAUTH_ALLOWED_DOMAINS`** — a predecessor
    stripped of every membership still passes the entrance on the domain match alone, the admin API passes
    on `identityFor` alone without requiring a membership, and with decision 24 unfixed their `role` is
    still `super_admin`, so they pass `withSuperAdmin` and **can reinstate themselves** (their cookie is
    valid for up to `AF_SESSION_TTL` = 168h by default). The countermeasure is two-stage: decision 24's
    startup synchronisation plus this rotation procedure. **No server-side session store is built** — the
    lightness of a stateless cookie is a design advantage of the CP, and holding state for the sake of
    revocation costs too much.
28. **No invitation notification feature is built.** The invitation URL (`/login/<slug>`) is conveyed by
    the tenant_admin **outside the CP** (verbally, in the company chat, and so on). It is policy not to
    give the CP email sending (the same reason magic links were rejected in decision 10), and adding a
    notification route still would not guarantee that it reached the person.

### Tenant-defined authentication methods (P4, when each subsidiary has a different Entra, docs/61 §61.11)

29. **Let a tenant hold provider definitions themselves (P4).** In group companies and spin-offs the
    Entra tenant differs per tenant (a different issuer and different client_id/secret). P0's env can do
    it by listing `AF_OIDC_PROVIDERS=entra_a,entra_b` (measured), but that means **editing a file on the
    host and restarting the CP every time a tenant is added**, contradicting "creating a tenant needs no
    restart" (decision 25, docs/61 §61.10.3). The definitions move to the database, editable by a
    tenant_admin from the Console.
30. ★ **Activation requires `super_admin`'s approval. Editing and saving are the tenant_admin's;
    activation is someone else's.** Project MCP ([0031](0031-mcp-registry.md)) can be registered by a
    tenant_admin alone, but that is "where that tenant's agents go **outward**", whereas **an IdP is the
    subject that declares who someone is**, so it cannot be treated the same. The measured hijack route:
    `user_key = sanitizeUser(email)` (`resolver.go:281`) is **one namespace across the whole deployment**,
    and the deployment role is decided by **an email match** too (`roleHintFor`, `resolver.go:28`). So a
    tenant_admin who can register an IdP under their own control can issue themselves a token asserting
    `email=<super_admin>` and sign in; `UpsertIdentity` raises the role, and **never downgrade
    (`store_sqlite.go:314-317`) means it persists even after the rogue provider is deleted.**
    `trust: "issuer"` is no breakwater (the issuer is the attacker). ★ **It can happen without malice** —
    registering an Auth0 tenant with self-signup enabled in good faith opens the whole deployment the
    moment it is done. Approval is once per subsidiary, so the operational cost is overwhelmingly smaller
    than what is at stake. Disabling (`suspended`) can be done by a tenant_admin too — **stopping should
    always be quick for anyone.**
    ★ **Changing the issuer, client_id or trust returns it to `pending`.** Approval was given to "this
    issuer may be trusted", and if the issuer changes, the thing approved has changed.
    ★ **An addendum from implementation (P4) — "widening the allowed domains or tids" was added to the
    conditions for returning it.** Approval means "this issuer may be trusted **within this range**", and
    if the range widens, the subject has changed. Narrowing does not return it. Updating the
    `client_secret` does not return it either (with the same issuer and the same app registration,
    demanding re-approval on every key rotation costs more, because **people stop rotating**).
    ★ **Refusal before approval takes effect at the callback.** Not showing the button is a display
    matter; hitting `/oauth2/login?provider=t:…` directly would otherwise get through (the same hole as
    decision 14).
31. ★ **A tenant-defined provider cannot obtain `super_admin`.** `roleHintFor` applies **only to logins
    through env-derived providers**. Even when approved, that IdP's administrators are that company's IT
    department, not the person who installed this deployment (decision 24's "deployment-wide authority
    belongs only to whoever can touch the host").
32. ★ **P4 presupposes P1 (`0038`).** Tenant-defined providers create identities by
    `(provider, subject)` and **disable decision 4 / docs/61 §61.5's "join an existing identity on an
    email match"**. Without that, merely claiming an email would hijack an existing identity = home =
    secrets. Alongside that, a tenant-defined provider **can only enter its own tenant** (`prov` is
    checked in `resolveFull`; the entrance allowlist is that tenant's and does not fall back to the
    deployment-wide list), and **it is not shown on the bare `/login`, only on `/login/<slug>`** (a row
    of buttons for every subsidiary would leak the organisation's structure to an unauthenticated
    visitor). The dependency is effectively **P1 → P3 → P4**.
    ★ **Revised (during P4) — "disable it" could not be implemented and became "refuse".** Cutting rule 2
    and falling through to creating a new identity still returns to the same row via
    `ON CONFLICT(user_key)`, because `user_key = sanitizeUser(email)`, and `identity.email` is UNIQUE so
    a separate row cannot be created either. Hence **rule 2'**: only claim an identity that has never
    been signed into (an invitation placeholder), and refuse an address with a login history with
    `email_taken`. The main line of invitation → first login works, and the hijack is closed (docs/61
    §61.11.8).
    ★ **Revised — the entrance gate now requires the row's `allowed_domains`.** Merely not falling back
    to the deployment-wide list (as written above) leaves a row with an empty allowlist meaning either
    "nobody can enter" or "anybody can". Making it mandatory always makes **the range of addresses that
    issuer may claim** explicit, and with one domain per tenant (the same rule as §61.9.8) it cannot
    claim another company's addresses.
33. **The `client_secret` is sealed in the database with the tenant key. The precedent is
    `mcp_server`.** `headers_enc` plus `key_ref` is sealed with `custodian.Wrap(tenantID, …)`
    (`mcp_server.go:146`, AES-256-GCM, AAD=keyRef), masked as `***` in the UI, updated by a merge that
    keeps unedited values, and an undecryptable value is an explicit error rather than empty
    (`mcp_headers_unreadable`) — those four points are followed exactly. Two limits, stated honestly:
    `localCustodian`'s KEK derives from the master and so is **not cryptographic separation between
    tenants** ([0005](0005-envelope-custodian.md)), and moving the secret from env to the database puts
    it **inside `DATA_DIR`, i.e. inside backups** (the existing rule of keeping `AF_MASTER_KEY` outside
    the data area is the premise).
    ★ **Separate the provider id namespaces**: env-derived is `entra`, database-derived is
    `t:<tenant-slug>:<name>`. Mixing them would let a tenant create a row called `google` and override
    env's Google. The ban on `common` / `organizations` with empty TIDs (decision 7) is enforced on the
    database side **as a 400 at save time** (a running CP cannot be killed).
    ★ **An addendum from implementation (P4) — do not loosen `validProviderID`.** The `t:` form is
    validated by a separate function. Loosening it opens the reverse hole of letting an env-side provider
    id be put into the tenant namespace.

### Tenant-defined GitHub (P5, when the grounds for trust are no longer the issuer, docs/61 §61.15)

34. ★ **A tenant may use GitHub as a sign-in method too, but what is approved changes.**
    Decision 30's approval was "this issuer may be trusted within this domain range". Its grounds were
    that **the issuer is pinned to that subsidiary** (decision 7), which GitHub does not have —
    `github.com` is one issuer shared by every tenant. So approval of a GitHub row is reread as
    **"members of this org may be trusted within this domain range"**.
    ★ **Approval is still not dropped.** The hijack decision 30 closed (asserting `email=<IT dept>` via
    your own IdP) does not work on GitHub (GitHub verifies the email and a tenant admin cannot forge it),
    but two things approval still carries: (1) `allowed_domains` is **taking a slot in the one-domain-one-
    tenant ledger**, and even without impersonation it can block another company's registration; and (2)
    who controls entry to and exit from an org is an org owner, not necessarily that company's IT
    department, and whether to allow that as the company's entrance is the deployment's call.
    ★ **Adding an org returns it to `pending`** (the same frame as decision 30's "widening the range").
    Removing one does not.
    ★ **`allowed_domains` is mandatory for GitHub too.** What stops "company A registering an org that
    claims company B's domain" is not the org but **the domain ledger** (409), and without making it
    mandatory the row escapes that ledger.
    ★ **The OAuth App belongs to the row.** Sharing the deployment's app would make each subsidiary's org
    owner approve the IT department's app (the same one as git integration's device flow), so the IT
    department's key rotation would silently break every subsidiary. As a result, **even a deployment
    with no `AF_GITHUB_ALLOWED_ORGS` can enable tenant-defined GitHub alone** (a consequence of decision
    29).
35. ★ **The same IdP account pressed from a different button is the same person (rule 1.5).**
    Setting `emailJoin=false` in decision 32 means **someone who was signing in with env's GitHub gets
    refused with `email_taken` when they press the tenant's GitHub button** (the same account and the
    same email, but a different `(provider, subject)` key). Rather than loosening decision 32,
    `identity_provider.realm` is added (= where the identity was proven; the issuer for OIDC,
    `https://github.com` for GitHub) and joining happens **only when the realm and the subject both
    match**.
    ★ **The realm is declared by the adapter, not written by the row** (`issuerURL()`). The GitHub
    adapter's endpoints stay constants and cannot be swapped from a row — if they could be moved, any
    subject could be claimed and the key forged.
    ★ **A different IdP with only the email matching stays refused** (the user's call). Opening it would
    let that tenant's admin impersonate **an existing account created by a different authority**, within
    the approved domain range. The operational consequence (do not remove from `allowed_providers` the
    method a person with dual roles uses) is written on the screen and in the guide.

36. ★ **Separate "methods accepted" from "methods shown as buttons" (`hidden_providers`).**
    Since decision 14, `allowed_providers` has done two jobs (per-request enforcement plus login-screen
    display). Tenant-defined GitHub brings that into conflict with the requirement: **a subsidiary wants
    to run on its own GitHub only**, yet it must accept head office's method to let through a seconded
    person who has no subsidiary GitHub account at all — and then an unused button sits on the
    subsidiary's login screen. ★ **Accepting is not the same as increasing who can enter** — who can
    enter is decided by the roster (decision 15), and `allowed_providers` is only "which sources of
    identity are recognised". So what may be dropped is display alone, and `hidden_providers` is
    **display-only** (`providerAllowed` does not consult it, leaving decision 14 intact).
    ★ **If everything would be hidden, ignore the hiding.** A login screen with no buttons is a dead end,
    and a tenant's misconfiguration must not be able to create one. It is not rejected at save time
    because that tenant's methods increase and decrease at runtime (approval, suspension), so it is not
    determined at the moment of saving.

### Linking sign-in methods with the person's own consent (P6, docs/61 §61.16)

37. ★ **"A different IdP with the same email" is allowed only when the account's owner presses it
    themselves.** What decision 32 (and decision 35's proviso) refused was **a tenant's admin
    impersonating an existing account created by a different authority**. What is dangerous is **who
    asserted it**, not that the address is the same. So the condition for opening it is "**a logged-in
    person adds a second method to their own account**" — one leg is proven by the session and the other
    by that IdP's callback, and not a single assertion about anyone else is required.
    The conditions that make it work (drop any one and the property above collapses):
    - **A live session is required**, and on the second leg **the same person is re-confirmed** — a
      signed state only says "the CP wrote it", and in between they could sign in again in another tab.
      ★ `/oauth2/` is an **excluded prefix** for `authGate`, so this gate lives in the handler itself.
    - **It passes that method's own gate (org, allowed domains).** `Allowed()` runs exactly as it does at
      login, so linking is not a bypass.
    - **Only a method claiming the same email address** (the user's call). Joining a different address
      falls under §61.5's "being able to sign in to both is not proof of being the same person", and it
      cannot be undone.
    - **Refuse if the other IdP account belongs to somebody** (the pair itself, a rule 1.5 match, or the
      address's owner). Nothing is re-pointed and nothing is merged.
    - **The deployment role does not move** (decision 31). `AttachProvider` never touches the `identity`
      row, so there is no `roleHint` path at all.
    ★ **Unlinking was added too** (originally deferred). Being able to add but not remove is one-way — the
    row of someone who left the org on a transfer would linger wearing the face of "a method still
    usable". Three guards prevent lockout: **refusing the last remaining one** (counted inside the
    `DELETE` statement; counting in the API before deleting lets two tabs take it to zero), **refusing
    the method of the session currently in use**, and **always putting `identity_id` in the WHERE**.
    ★ **There are two entrances**: the "account" tab in settings, and the wording of `email_taken` on the
    login screen (the only place a person refused there can read what to do next).
    ★ **This does not solve the case of a seconded person with no subsidiary GitHub account** (there is
    nothing to link to). That is already solved by decision 36 (`hidden_providers`) plus operations; do
    not conflate them.

38. ★ **Add a key to rule 1.5 — but keep `subject` as `sub` and match on a separate column.**
    Decision 35's rule 1.5 joins "the same IdP account from a different button" by `(realm, subject)`.
    That is enough for GitHub (the numeric id is the same across every OAuth App), but **Entra's `sub` is
    pairwise per (app registration, person)**, so the same person gets a different subject under a
    different app registration even within the same Entra tenant, and rule 1.5 does not fire.
    ★ **Naively "replacing `subject` with `oid`" causes an accident.** The key of existing rows would
    change and rule 1 would stop matching. env providers are saved by rule 2 (email joining), but
    **tenant-defined rows hit rule 2' and get `email_taken`, locking out people who are using it now**.
    So `identity_provider` gains **`realm_claim` (the claim name read) plus `realm_subject` (its value)**,
    and **only rule 1.5's matching also runs against those** (`WHERE realm=? AND realm_claim=? AND
    realm_subject=?`). `subject` stays `sub`.
    - ★ **Include the claim name in the match.** When one side is `oid` and the other is a different
      claim, a coincidental value match must not be applied — they are not answers to the same question.
      Empty never matches, so rows written before this column, and providers that declare no claim, do not
      take part.
    - ★ **Only the claim *name* is configurable; the value is always read from the token** (the same
      practice as realm). If a row could write the value, a tenant could claim someone else's `oid` and
      forge rule 1.5.
    - ★ **A tenant may only name a known stable claim** (an allowlist starting with `oid`). Being able to
      write `email` / `upn` / `preferred_username` would **create email joining inside a shared realm** =
      the hijack decision 32 refused, entering by another door. env (`AF_OIDC_<ID>_LINK_CLAIM`) allows any
      claim name — the speaker there is the operator, who could do the same thing by editing the
      allowlist. The danger is stated in the operations guide.
    - ★ **Validation happens in two places, the API side (`validateTenantIdPBody`) and the runtime side
      (`buildTenantProvider`).** With only one, a row can be created that saves fine and then fails after
      approval.
    - **A change to `link_claim` returns it to `pending`.** Who can enter does not change, but **where they
      land** does (more buttons reach an existing account) — a change the approver should see.
    - This key is added to the refusal conditions for linking (decision 37) too. Looking only at realm and
      subject makes the pair look free because of the pairwise `sub`, while entering actually lands on
      someone else.

### Moving sign-in methods onto the tenant (P7, docs/61 §61.17)

39. ★ **Unify sign-in methods as "something a tenant holds", and make env's methods belong to the default
    tenant.** Even after P4/P5, "the deployment's methods" and "the tenant's methods" lived in different
    layers, and the seam surfaced in three ways: (1) **Google appears nowhere in the admin screen** (the
    only surface that shows it is `GET /api/admin/providers`, and only while a super_admin is *editing*
    the login rules), so a tenant admin's "sign-in methods" is empty even at a company that logs in with
    Google every day; (2) `hidden_providers` has no effect on the bare `/login` (the scope of decision 36,
    §61.15.13); and (3) the deployment's default methods cannot be abolished.
    ★ **(3) is not "inadvisable" but "doing it makes the deployment unmanageable"**: `upsertIdentity`
    unconditionally empties `roleHint` for tenant-defined providers (decision 31), so if every method
    became tenant-defined, **nobody new could become super_admin** even if listed in
    `SUPER_ADMIN_EMAILS`, and with no approver the first tenant row could not be activated either
    (decision 30) — a cycle. On top of that, `AUTH=oauth` refuses to start with zero providers.
    ★ **A precision improvement from review**: this originally said "nobody can become super_admin", but
    precisely it is that **the promotion path becomes zero**, not that existing rows disappear. There are
    two SQL statements that promote across the whole deployment (`UpsertIdentity` / `touchIdentity`), both
    conditioned on `roleHint == "super_admin"` and hence blocked by the above. There is one that demotes,
    the startup `DemoteSuperAdmins`, and it drops rows **not** in `SUPER_ADMIN_EMAILS`, so an existing
    super_admin survives as long as they stay in env. What is affected is **a fresh installation, and
    adding a new super_admin later**. The conclusion is unchanged — a design in which the first person
    cannot be created is not adoptable.
    So **rather than abolishing them, give them a home**: env's methods are treated as the methods of the
    **default tenant** (`EnsureDefaultTenant`, always present at startup), and other tenants **reference**
    and accept them. No separate category called "the deployment's methods" is put on other tenants'
    surfaces.
40. ★ **What moves is attribution in display and rules only; the provider id's form and the identity layer
    are untouched.** Renaming "the default tenant's methods" to `t:default:google` **must not be done**.
    `checkTenantProvider` pins a tenant-defined session to its own tenant by **the string form** in
    `parseTenantProviderID` (decision 32-3), and `upsertIdentity` empties `roleHint` on the same form
    check (`isTenantProviderID`, decision 31). Renaming would mean **(1) people signing in with the shared
    Google could no longer use any tenant but the default** (wiping out both dual-role users and
    department tenants) and **(2) nobody could newly become super_admin through that entrance**.
    ★ **Counting them in review found ten branch points, not two** (excluding tests; the table in docs/61
    §61.17.3). The id's form is scattered across every layer not as a guard but as **the identifier's
    meaning itself**. Three are especially heavy: **(i) `providerFor` resolves `t:` through the database
    registry rather than env**, so a renamed id cannot be resolved at all and **that button cannot log in**
    (the first thing you hit); **(ii) `setTenantLogin`'s validation only accepts a `t:` id as "a row of
    this tenant"**, so another tenant cannot write `t:default:google` into `allowed_providers` — i.e.
    **decision 41-a's "reference" stops working**; and **(iii) `linkableFor` only offers `t:` to members of
    that tenant**, so Google disappears from decision 37's link candidates. The checks stay on the id's
    form, and only the meaning is reread as "**who holds the issuer (the operator or a tenant admin)**". In
    terms of docs/61 §61.9.2's three layers, P7 touches only **the login screen** and **the attribution of
    rules**, leaving **the entrance gate** and **the tenant gate** unchanged.
    ★ As a by-product, an invariant currently hidden in one line of Go becomes visible on screen —
    **only the deployment's methods (= the default tenant's methods) can carry super_admin.**
41. ★ **"Another configuration of the same provider" is allowed, but an IdP with pairwise subjects needs
    handling.** There are two additional operations for convenience, and their safety differs.
    **(a) Adding the same method as the default tenant = a reference** (the id stays `google` and it is
    merely added to the accepted set; the identity layer is untouched) is the main one, and in substance it
    just turns `allowed_providers` from free text into a toggle. ★ **Review removed the "add" operation
    from (a)**: adding and removing a reference is **the same single bit** as toggling "accept" on and off,
    and giving one thing two names inevitably produces the misunderstandings "I thought I could edit the
    reference row" and "I added it but the list did not grow". → **The default tenant's methods are always
    listed as rows**, drawn OFF when unreferenced. [Add a method] becomes a word for (b) only.
    **(b) Another configuration of the same provider = a new row** (a different app registration) is an
    advanced operation, and the dividing line is whether the same person gets a different subject:
    discovery's `subject_types_supported` is **`public` for Google / `pairwise` for Entra** (measured
    2026-08-20), and GitHub's numeric id is common across every OAuth App. `pairwise` is exactly decision
    38's situation, and left alone it produces rule 2's `email_taken`.
    - **Decide by reading discovery** (do not guess from the issuer's hostname). It only needs reading when
      "a row with the same issuer already exists", i.e. in case (b) itself.
    - For `pairwise`, **`link_claim` becomes mandatory** (a 400 at save time). The current UI's initial
      value is "default (distinguish by `sub`)", so leaving it as is causes an accident.
      ✅ **Implemented in P7-3** (`checkPairwiseNeedsLinkClaim`). ★★ This originally said "and if `oid` is
      available", but **measurement showed the second half cannot be determined**: the standard answer,
      `claims_supported`, is **under-reported by Entra** (it lists no `oid` at all, yet a v2 token always
      carries it — 2026-08-20). As a condition it would fail to fire exactly where it is needed. → The
      condition is only **`pairwise` and "this issuer is already in use on this deployment"**. Specifying a
      claim that is not emitted is harmless (`realm_subject` stays empty and `identityIDForRealmClaim`
      rejects an empty subject), so **there is no cost when it does not apply** = no situation where making
      it mandatory hurts.
      ★ "Already in use" **counts env providers too** — the commonest shape is "a tenant adds its own app
      registration to the directory the deployment already uses".
      ★ When discovery cannot be fetched, **let it through**. The issuer may just be temporarily
      unreachable, and the login side also fetches discovery lazily (for the same reason). A momentary
      network failure making saving impossible costs more.
    - A `pairwise` with no stable claim to name is **not refused**. Decision 37 (linking with the person's
      consent) rescues it afterwards, and the `email_taken` screen points to that route. Instead **a warning
      is shown both at save time and on the approval screen** (where someone lands changes = information the
      approver should see).
    - ★ **"It splits" was wrong (corrected in review). What happens is a refusal, and that is the heavier
      outcome.** When someone who has logged into this deployment before presses (b)'s new button, rule 1.5
      does not match → rule 2's `identityHasProvider` is true → `errIdentityClaimed` →
      `/login?error=email_taken`, and **no session is issued**. The identity does not split into two;
      **only existing users become unable to enter through that button** (new people are created normally).
    - ★ **The rescue presupposes that the old method is still alive.** The `email_taken` screen in both
      languages says "log in with your usual method, then add it under Settings → Account", but the typical
      case for (b) is **swapping an app registration**, and after migrating you want to stop the old row.
      The moment you do, anyone unlinked cannot recover by themselves (they cannot create the session that
      starts the linking, and `AttachProvider` does not accept a `(provider, subject)` already bound). →
      The UI carries the order: **enabling a new row does not stop the old one**, and stopping is allowed
      only after everyone has linked. docs/61 §61.17.4.
      ✅ **Implemented in P7-3** (`CountMembersOnlyOnProvider` plus a 409 on suspension).
      ★ **A confirmation, not a refusal** (`?confirm=1` gets through). Suspending is also the means of
      "stopping a leaked IdP", and **stopping should always be allowed to be faster than starting**. What we
      want to protect is the migration case, not the incident case.
      ★ What is counted is "active members who have **only ever** used that method". "Other methods" is not
      limited to this tenant (a row in `identity_provider` is *a proven login*, so they can re-enter via
      another tenant's method). People with no rows at all — invitations not yet used — are not counted.
      ★ Only the CP knows the number, so it is returned as `members` next to `error`, and the Console
      **takes just the number and puts it into wording in its own language** (emitting the CP's English
      directly would make the display language the CP's). Widening the shared error envelope
      `{code, message}` would ripple to every handler, so this one response is written by hand.
    - (b)'s side effects remain: it is a `t:` id, so that session is pinned to that tenant (decision 32-3),
      and the `client_secret` is copied into the database.
42. **Apply the default tenant's `hidden_providers` to the bare `/login` (rendering only).**
    Decision 36's `hidden_providers` had no effect on the bare `/login` because that screen belongs to *no
    tenant*. Drawing it with the default tenant's rules makes it apply, and §61.15.13's "cannot be changed
    in the implementation" can be withdrawn. ★ **It is not grounds for authorisation** — which tenant one
    can enter is decided by membership and tenant rules (decision 13). The behaviour of falling back to the
    generic page for an unknown slug is unchanged.
    ★★ **This originally proposed "render it as the default tenant's page", i.e. applying the whole rule
    set, but review narrowed it to `hidden_providers` only.** The safety valve exists on one side only:
    hiding has the valve "ignore it if everything would be hidden", but **`allowed_providers`' narrowing
    has no valve, and if everything drops out you get an error page with zero buttons**. The bare `/login`
    is the only entrance for people belonging to no tenant (a newly appointed super_admin, someone not yet
    invited), and `PUT .../login`, which restores the rules, is `withSuperAdmin` — it needs a session. If
    the existing session expires on its TTL, **there is no way back except editing the database** = locking
    out the entire deployment. The hole we wanted to close exists only on the hidden side, so narrowing it
    fully achieves the goal while **structurally** removing the lockout route.
    ★ An implementation note: "where the rules come from" and "the slug put on the button URLs" are **separate
    arguments**. Putting `default` on the bare `/login` would make the Console's initial tenant selection
    after login fall to `default` for everyone via the state, and the default tenant's own `t:default:*`
    rows would line up on the bare `/login` (the shape decision 32-4 avoided). Only the rules are applied.
    ✅ **Implemented in P7-1 (three lines in `handleLogin`).** `loginButtons` already had the slug and the
    rules as separate arguments, so the separation work itself was unnecessary — the bare `/login` keeps
    `tenant=""` and only puts the default tenant's value into `hidden`. Both side effects above disappear
    simply by keeping the slug empty.
    ★ **The page for an unknown slug must be exactly identical to the bare `/login`** (applying the default
    tenant's rules to only one of them would reveal whether a slug exists by comparing the two). They go
    down the same branch, and a test pins the equivalence. It also pins that "even if the default tenant
    declares it accepts a method that is not in env, not one button disappears from the bare `/login`" —
    that being the very reason the decision was narrowed.
    ★ On the screen, the two CSVs `allowed_providers` / `hidden_providers` become **two toggles per row**
    (accept / show as a button). **The database representation is unchanged = no schema change.**
    The implementation traps come from the existing meaning of "empty = all" and from the existing safety
    valves: **the last remaining "accept" cannot be turned OFF** (dropping them all saves as "all ON" =
    wide open while believing you narrowed it), ★ **the same applies to "show"** (hidden has a valve too, so
    turning everything OFF saves but has no effect = the screen lies), ★ **contradictions cannot be
    expressed** (rendering requires allowed even inside the hidden check, so "show" on a row with accept=OFF
    is meaningless → it becomes a dependent toggle and is `disabled`), and ★ **a tenant with nothing
    configured is drawn as "all ON" but is not frozen into an explicit list** ("empty" carries the meaning
    *follow the deployment*, and freezing it would silently refuse, for that tenant alone, any method added
    to env later; the normalisation is "**save empty when everything is ON**").
    And **the UI enforces the order** (narrow first and then invite an admin, and that person cannot get in;
    the default tenant's methods can all be turned OFF only when at least one active row of your own
    exists).
    ✅ **Implemented in P7-0.** ★ One function sufficed for both rules — counting the "cannot turn OFF" check
    over *usable* methods (the deployment's methods plus **your own rows that are active and usable**) makes
    "the last one" the ordering rule itself (a row awaiting approval is not usable, so the default tenant's
    methods cannot be removed until your own row starts working). No branch was written for the ordering.
    ★ Saving is a PUT that replaces all four columns, so **each surface sends back the two columns it does
    not own with the values it read** (dropping either would silently erase the other surface's settings).
    ★ The toggles are only editable **when the deployment's method list could be read** — if it could not,
    the "empty when all ON" normalisation would run over a result that dropped ids it does not know about,
    narrowing a tenant that never meant to narrow. §61.17.9 ②'s three states are needed not just for display
    but for whether saving is permitted.

### Tidying up — the delete operations (2026-08-22, docs/61 §61.18)

- **Decision 43: the reserved tenant (`af-golden`) does not appear in the admin screen's list, and cannot
  be deleted.** It is **a vessel** reused on every re-bake, not a person's tenant (only the workspace, the
  home and the slot are thrown away each time). Neither collapsing it nor a display toggle was adopted — it
  simply does not appear.
  ★ It is dropped in the **API layer** (`listTenants` / `usage`), and **`store.ListTenants` passes it
  through**. That store call carries the audit's `tenant_id → slug` resolution and the cost poller's
  `membership → tenant` resolution, and filtering in the store would make the symptom appear in the audit
  and the billing rather than in the admin screen. The check is in one place, `system_tenant.go` (the same
  check serves the list, deletion and cost). The golden's appearance in the slot pool screen is a different
  thing and stays.
- **Decision 44: a reserved membership's cost is folded into SHARED at ingest, not by tagging.**
  `af-membership` is **both a cost allocation key and a matching key** (the runtime finds the EFS access
  point and the home volume with it), so it cannot be emptied. The tag is written by the product's normal
  path, and it becomes `""` at the stage of ingesting from Cost Explorer.
  ⚠️ **Sum when folding** — `PutCloudCost` replaces `(day, membership_id, service)`, so passing two rows
  without adding them makes the later row erase the earlier one's amount. The counterpart of ADR 0048
  decision 13.
- **Decision 45: a tenant may be deleted only when it is empty.** If any active membership, workspace row
  or internal git repository remains, it is a 409. **A database row is sometimes the only handle on
  something still present in the cloud or on disk**, and deleting it first leaves the resource billing with
  nobody able to point at it (the same line as ADR 0045 decision 13-2). Cascading into removals and
  destruction is not adopted — irreversible destruction must not be a side effect of "delete the tenant".
  ⚠️ **Internal git repositories have an ordering trap**: the delete API is behind a `withMembership`
  gate, so removing the last member leaves nobody able to delete them. The refusal message states that
  order.
- **Decision 46: physically deleting a removed member is "delete the working data, keep the history".**
  The conditions are inactive plus workspace disposed of (aligned with ADR 0045 decision 13-2).
  `SetMembershipStatus`'s "hard deletion is deliberately not offered" is withdrawn, but what that sentence
  was really protecting — **the audit log, cloud cost and running hours** — is kept. An audit must not be
  erasable as part of tidying up a removal, and erasing cost would change a past month's total after the
  fact (`memberCloudCost` queries by membership_id alone — ADR 0048's premise). The `identity` row is kept
  too (they may be in another tenant, and even if not, audit rows point at it).

## Options rejected

- **Just add a dedicated Entra implementation.** The shortest route, but the same work repeats every time
  Okta or Keycloak is requested. The difference from generic OIDC is a few dozen lines that read discovery
  (decision 1).
- **Do it all with `AUTH=proxy` (the CP does nothing).** That is in fact the answer for SAML (decision 10),
  but making it the default breaks the self-hosting premise that "bring up compose and it works", and it
  makes the installing company carry the whole operation of oauth2-proxy.
- **Always join automatically on an email match, and join different emails via an admin's mapping table.**
  Rejected as in decision 5.
- **Magic links / password login.** Effective for small companies with no IdP, but the CP would carry
  credentials and SMTP. Decide in a separate ADR once there is demand.
- **Apple / LINE / Slack / Atlassian / Discord.** They do not give a company a means of controlling joiners
  and leavers, and are not worthy of a B2B entrance.
- **Allow promotion to `super_admin` from the Console.** It makes operations easier, but it lets someone
  other than the installer create deployment-wide authority (decision 24).
- **Send invitation emails / notifications from the CP.** The effort of a person conveying the URL remains,
  but it adds an SMTP dependency and still gives no guarantee that it reached the person (decision 27).
- **Keep workspace/home deletion super_admin-only.** That is the current implementation, but it means asking
  an IT department that does not know the department's headcount every time (decision 26).
- **Hold a server-side session store so sessions can be revoked individually.** It can cut off a leaver
  instantly, but it discards the lightness of a stateless cookie (authentication with zero database
  lookups). An immediate means already exists — key rotation — so the price is not paid (decision 27).
- **Synchronise `super_admin` demotion only at login.** The implementation is straightforward, but
  **a leaver never logs in again**, so `super_admin` remains in the database forever (decision 24).
- **Give the tenant an `allowed_emails` column.** The most straightforward implementation of "manage which
  IDs can log in per tenant", but membership is already that same roster, so it becomes two ledgers
  (decision 15).
- ★ **Let a tenant_admin activate an authentication method alone.** The most faithful to decisions 25/26's
  line of "tenant matters stay within the tenant", but **anyone who can add an IdP can become anyone on that
  deployment** (decision 30). Appointing a tenant_admin would become synonymous with appointing a
  super_admin.
- **Provide an env that skips approval (`AF_ALLOW_TENANT_IDP=1`).** An escape hatch for deployments where
  "our own tenant_admins are trustworthy", but **making fail-closed removable with one line of env makes
  that the default installation procedure**. Approval is once per subsidiary, so there is no value in
  removing it (decision 30).
- **Keep the tenant's `client_secret` in env and hold only the tenant's choice in the database.** It has the
  advantage of keeping the secret out of backups, but editing the host and restarting remains on every tenant
  addition. Having already accepted the same posture for MCP headers, there is no reason to be stricter only
  here (decision 33).
- **Use the same id space for tenant-defined providers as env's.** The implementation is shorter, but a
  tenant could create a row called `google` and override env's Google (decision 33).
- **Join on an email match for tenant-defined providers too (the alternative to decision 35).** It satisfies
  "the same address gets in by any route", but within the approved domain range that tenant's admin can
  impersonate **an existing account created by a different authority** (an identity — workspace, secrets —
  created with head office's Google). Approval means "that domain may be accepted", not "that IT department
  may impersonate an individual". To let every route through, the next step is to build **a route for
  linking with the person's consent** (decision 37, implemented in docs/61 §61.16).
- **Link on the spot from the refused login screen (implementation option ③ for decision 37).** "Log in your
  usual way → link automatically" is the fewest steps, but carrying the pending link around in a signed
  cookie adds expiry, mix-up and CSRF surfaces to verify. The settings tab plus the `email_taken` guidance
  are entrances enough, and only a few people a year hit it (only when a dual role arises), so adding a
  surface has no value.
- **Allow a different email if the target address is unused (the alternative to decision 37).** It would let
  "a@honsha at head office and b@sub at the subsidiary are the same person" through with the person's
  consent, but it effectively admits **joining across addresses** and would mean rewriting §61.5's
  explanation (different emails are not joined). There is no reason to open an irreversible operation before
  anyone asks for it.
- **Share the deployment's OAuth App on a tenant's GitHub row.** It makes installation easier for a
  subsidiary, but it makes each org owner approve the IT department's app (the same one as git integration's
  device flow), so the IT department's key rotation silently breaks every subsidiary (decision 34).
- **Abolish the deployment's default methods and make everything tenant-defined (decision 39's starting
  point).** It looks uniform and clean, but decision 31's empty `roleHint` means **nobody new can become
  super_admin**, and with no approver the first tenant row cannot be activated either (decision 30).
  `AUTH=oauth` will not even start with zero providers. "Substitute a step-up" **does not work either** —
  the one method used for stepping up has to be one the operator holds, or decision 31's hijack returns, so
  a deployment-side method always remains (left as an open question in docs/61 §61.17.8).
- **Rename the default tenant's methods to `t:default:*` for uniformity.** As in decision 40, the checks look
  at the id's **form**, so the moment it is renamed dual-role users are wiped out and super_admins stop
  appearing. ★ Counting in review found ten branch points, and the first one hit is `providerFor` — a `t:`
  id is resolved only in the database registry, so **being unable to log in with that button comes first**.
  Uniformity at the price of an unmanageable deployment does not balance.
- **Merely list env's methods as read-only rows on each tenant's surface (P7-0's original proposal).**
  Google becomes visible, but it only adds one unexplained row and does not answer "what is that, and what
  has it to do with us?". **Putting a toggle on the row** turns the same list into a surface that answers
  "does this tenant accept it?" (decision 41-a).
- **Make adding a reference a picker under [Add a method] (decision 41-a's original proposal).** Having an
  "add" operation looks clearer, but the presence of a reference is **the same single bit** as the "accept"
  toggle, creating two entrances to the same state. In the database it is only an append to
  `allowed_providers`, yet the UI reads as "a tenant method was added", which also leaks the abstraction as
  "I thought I could edit the reference row". → The default tenant's methods are always listed as rows and
  expressed only by a toggle (decision 41).
- **Apply the default tenant's whole rule set to the bare `/login` (decision 42's original proposal).**
  Uniform, but `allowed_providers`' narrowing has no "ignore it if everything disappears" valve, so
  **narrowing the default tenant would lock out the whole deployment** (recoverable only by editing the
  database). The hole to close is only on the `hidden_providers` side, so applying it there is enough
  (decision 42).
- **Derive `AF_PROVISION`'s default at runtime as "invite if there are zero memberships" (P7-2's original
  proposal).** The check itself is possible in the startup sequence, but **it is redone on every start**,
  which is fatal: a deployment started with invite stops meeting the condition once the first person is in,
  and reverts to `auto` on the next restart, **silently opening**. Pinning it needs a persisted setting row,
  which breaks P7's property of "add neither env nor schema". → Make the default in the env that the
  installer/guide generates `invite`.
  ✅ **The latter was implemented in P7-2** (`.env.example` has `AF_PROVISION=invite`, and on ECS the
  `AfProvision` parameter defaults to `invite`). **The CP's default stays `auto`**, so an existing `.env`
  with no `AF_PROVISION` behaves identically to the byte.
  ★ Only ECS is different, because the template *is* the configuration, so redeploying an existing stack
  falls to `invite`. The grounds for judging that acceptable are that **`membershipsFor` reads existing
  memberships first** (`resolver.go:144`) — everyone currently working passes as before, and what stops is
  only **new automatic acceptance**. It closes, and locks out nobody who is working.
- **Invert `allowed_providers`' "empty = all" to "empty = this tenant's own methods only".** It looks natural
  in a world where tenants hold their own methods, but (1) every tenant on every existing deployment
  silently closes, and (2) a freshly created tenant has zero methods = nobody can enter and no admin can even
  be invited (decision 42's ordering trap becomes the default).
- **Split per-tenant login by subdomain / use the tenant named in the URL or cookie as grounds for
  authorisation / put tenant rules in `authGate` / make `allowed_domains` a per-request constraint / make
  `auto_join_domains` the only means of belonging / put tenant rules in env / let a session hold several
  providers.** Each is the inverse of decisions 13–19; the reasons are in docs/61 §61.13.

## Impact

- `oauth_google.go` (461 lines) splits into `oauth.go` / `oauth_oidc.go` / `oauth_github.go`. The Google
  flavour disappears from the filenames (`build/90-code-map.md` needs updating).
- `sessionClaims` gains `prov` / `sub`. It is JSON, so **existing cookies read as missing fields** and no
  forced logout is needed at migration. A provisional rule treating a missing `prov` as `"google"` stays for
  one version.
- `oauthState` gains a provider id (the state cookie is signed, so additions are safe).
- P2 widens the meaning of `GITHUB_OAUTH_CLIENT_ID`. It is an env that **git integration's device flow
  already uses**, injected into each workspace by the CP and present in `.env.example`. One OAuth App can
  serve both flows, so it is shared, and the installation procedure gains only **an additional callback URL**
  (use `AF_GITHUB_LOGIN_*` to separate them). For that reason **the signal that enables GitHub login is
  `AF_GITHUB_ALLOWED_ORGS`**, so existing device-flow-only deployments are not warned on every start.
- P2 changes `sessionAllowed`'s return from `bool` to `(bool, error code)` (adding `reauth`, decision 12).
  The login screen's wording gains one entry, `errReauth` (in both ja and en).
- `authGate`'s per-request re-evaluation gains a provider branch (`oauth_google.go:299-309`).
- Startup validation (`main.go:278-284`) becomes per provider.
- Configuration examples are added in six places in the distribution: `deploy/compose/.env.example`,
  `deploy/local/oauth.env.example`, `deploy/aws/ecs/cfn/30-ingress.yaml`, `deploy/aws/ec2-single/README.md`,
  `deploy/compose/README.md` and `docs/guide/operator/*` (the guide **in both languages**).
- The "three AUTH modes" table in `build/07-security.md` §7.3 is rewritten, since the `oauth` row is no
  longer "Google".
- A company adopting GitHub gains one installation step, **approving the OAuth App for the org** (if the org
  has OAuth App access restrictions enabled, membership is invisible before approval and everyone is
  refused).
- ✅ P3: `authGate`'s entrance check consults membership (decision 16). On an invitation-run deployment
  `AF_OAUTH_ALLOWED_*` can be empty, so the "empty = refuse everything" warning now **checks
  `AnyActiveMembership()` at startup** and, when a roster exists, becomes an informational line rather than a
  warning: "the entrance is governed by membership and auto_join_domains".
- ✅ P3: three columns on `tenant` (`0039` / pg `0022`). The `Tenant` struct and the SELECTs in `getTenant` /
  `ListTenants`, `PUT /api/admin/tenants/{slug}/login`, and the Console's admin UI. Not one new env is added
  (all the rules are in the database).
- ✅ P3: `checkTenantProvider` goes into `resolveFull` / `resolveMembership`, and the error codes gain
  `provider_required`. The Console offers a re-sign-in route via a dedicated modal
  (`ProviderRequiredModal`). The provider id needed for the link comes from `/api/tenants`'
  `allowed_providers` (per membership).
- ✅ P3: `clean-home` widens to tenant_admin (for their own tenant), with `workspace.clean_home` added to the
  audit.
- ✅ P3: the demotion path for `identity.role` (decision 24). `UpsertIdentity`'s "never downgrade" is kept, and
  `DemoteSuperAdmins` is called exactly once immediately after startup in `main.go`.
- ✅ P3: the `AF_COOKIE_SECRET` rotation procedure (= logging everyone out) is added to the operator's runbook
  (decision 27). `docs/operate/04-secure{,.ja}.md`.
- ✅ P3: the departure/delegation checklist (docs/61 §61.10.7) is also published in the operator guide. In
  particular the asymmetry that **scheduled execution stops, because `Schedule.MembershipID` is personally
  owned** (while internal git survives, because `git_repo.tenant_id` is tenant-owned) is something you
  discover after someone has left unless you know it in advance.
- P3 widens the meaning of `AF_PROVISION` (an `auto_join_domains` match takes effect before `auto` /
  `invite`). Behaviour when there is no match is unchanged, so existing deployments look the same.
- ✅ P4: `tenant_idp` (`0040` / pg `0023`) is added, and **the provider list is no longer fixed by env**
  (decision 29). The implementation is "env-derived stays fixed at startup, and only database-derived is
  layered onto a runtime registry" (`tenantIdPRegistry` in `tenant_idp.go`; a 30-second TTL invalidated by
  admin API writes, using the previous snapshot if the database cannot be read — the same practice as
  decision 19). It lives in `manager` because `config` is **copied by value** into handlers, so a set placed
  there could not change without a restart.
- ✅ P4: refusal before approval takes effect **at the callback, not on the login screen**. `providerFor`
  only fetches active rows, so hitting `/oauth2/login?provider=t:…` directly passes neither authorize nor
  session issuance (the decision 14 pattern that hiding a button is a display matter). `sessionAllowed` takes
  the same path, so **suspension expires existing sessions within their TTL**.
- ✅ P4: the suppression in `roleHintFor` (`resolver.go`) is placed **in the one place, `upsertIdentity`,
  rather than at three call sites** (decision 31). Since `identityFor` / `resolveFull` / `resolveMembership`
  all pass the same arguments, writing the rule three times means missing it the fourth. Only the callback's
  `linkAfterLogin` calls the store directly, so the same suppression is written there.
- ★ P4's implementation **revised decision 32**. "Disable email-match joining" **cannot be implemented** —
  cutting rule 2 and falling through to creating a new identity still returns to the same row via
  `ON CONFLICT(user_key)` because `user_key = sanitizeUser(email)`, and `identity.email` is UNIQUE so a
  separate row cannot be created either. So it became **rule 2'** (claim only an identity never signed into
  — an invitation placeholder — and **refuse** an address with a login history with `email_taken`). The main
  line of onboarding a subsidiary, invitation → first login, works, and hijacking an existing account is
  closed. Details in docs/61 §61.11.8.
- ★ P4's implementation **made decision 32-3 concrete**. `deployAllowed` is nil (no fallback to the
  deployment-wide list), **the row's `allowed_domains` is mandatory at save time (400)**, and **one domain per
  tenant** is enforced (409 if another tenant's row already claims it). The motive is less about avoiding
  "approved but nobody can enter" than that **allowed_domains is the only means of bounding the range that
  issuer may claim** — if it could be empty, an approved subsidiary IdP could claim the parent company's
  addresses. Alongside that, **widening the allowed domains or tids** was added to the conditions for
  re-approval (narrowing does not return it; nor does updating the `client_secret` — demanding re-approval on
  every rotation means people stop rotating).
- ✅ P4: `tenant.allowed_providers` can now contain the tenant's own `t:<slug>:<name>`. P3's validation only
  accepted env provider ids, which meant **a subsidiary could not restrict itself to its own IdP**.
- ✅ P4: two surfaces are added to the Console — "this tenant's sign-in methods" on the tenant detail
  (tenant_admin) and super_admin's list. Approval is an operation that creates authority, so **it is recorded
  in `audit.go`** (`tenant_idp.create` / `.update` / `.active` / `.suspended` / `.pending` / `.delete`: who
  approved which issuer for which tenant). ★ The latter is **a register, not a queue that empties** —
  approval is a one-off inspection but the IdP's configuration changes afterwards, so keeping approved rows
  with their approver and timestamp gives periodic reviews somewhere to live (the third answer in docs/61
  §61.14). Where they live was settled by the Console IA overhaul of 2026-08-14 (the revision in docs/61
  §61.11.8): the tenant_admin surface is the new **tenant settings modal**, and super_admin's approvals, the
  register and editing the login rules stay in the admin modal. The login rules also appear in tenant settings
  **read-only** (the PUT stays `withSuperAdmin` — decision 19 unchanged). The implementation puts one file,
  `console/src/features/settings/tenantLogin.tsx`, into both, and **the difference is props only; no
  server-side gate changed**.
  ★ Two more things followed on 2026-08-15 (same §61.11.8): **approval can be issued directly from a row in
  the register** (seeing only a count with no way to approve there was a detour; the target is composed from
  the row's `tenant_slug`), and **the admin modal's entrance was restricted to `superAdmin`**. For the latter,
  the surfaces meaningful to a tenant_admin (members, sessions, usage, audit, MCP distribution) were moved
  into tenant settings. Only the entrance was closed; the CP's gates were already fixed at `withSuperAdmin` —
  neither decision 19 nor decision 30 changed.
  ★ One more on 2026-08-15: **`GET /api/admin/providers` (`withSuperAdmin`) was added**, showing read-only,
  directly under the field, what may be written in "usable sign-in methods". The set had long existed in
  `manager.knownProviderIDs` but **had no handler to expose it**, so the only way to know the writable values
  was to read the deployment's env (get it wrong and you are simply rejected with 400 `unknown_provider`,
  with nothing on screen saying what to type next). It returns only the id, the button wording (ja/en) and
  the issuer; `client_id` / `client_secret` are not included. Tenant-defined `t:<slug>:<name>` are not mixed
  in (decision 32-4). Details in docs/61 §61.11.8.
- ✅ P4's secrets go into the database in `DATA_DIR`, so **how backups are handled (keeping `AF_MASTER_KEY`
  outside the data area) matters more than before**. A line was added to the relevant part of the operator
  guide (in both languages).
- ★ P4 adds not one env. Nothing is added to the env examples of the four distribution targets; only the
  operations guide's wording needs updating (the end of docs/61 §61.8).
- ⏳ P7 (decisions 39–42, docs/61 §61.17) adds **neither env nor schema**. What it touches is the Console's
  tenant surfaces, the rendering in `handleLogin` / `loginButtons`, and **the meaning and the read gate of
  `GET /api/admin/providers`** ("the deployment's env list" → "the default tenant's methods"). ★ Not one line
  of `checkTenantProvider` / `providerAllowed` / the identity layer is touched.
- ★ P7 requires **letting tenant_admin read `GET /api/admin/providers`** too (so another tenant's admin can
  list the default tenant's methods in their own tenant's list). It is currently fixed at `withSuperAdmin`,
  and `tenant_login_test.go` **explicitly pins "403 for tenant_admin"** — P7 rewrites that test and the
  wording to "**editing is super_admin, reading is tenant_admin**" (the gate on decision 19's *editing* does
  not change). The grounds for confidentiality were always thin: the id and the button wording appear on the
  **unauthenticated `/login`**, and `GET /api/me/login-methods` (decision 37) returns the same ids to
  non-admin users.
  ★★ **But this API also returns the `issuer` (found in review).** Entra's tenant GUID and Okta's hostname do
  not appear on `/login`, so "always thin" does not apply to that column.
  → **A tenant_admin is returned only the id and the two-language labels**, and the `issuer` rides only on
  the super_admin response. The test is rewritten to "403 → 200 but with fewer columns".
  ✅ **Implemented in P7-0a.** The gate is a new `anyTenantAdminFor` (super_admin ∪ an active tenant_admin
  anywhere; a plain member gets 403). ★ This gate takes no slug and so **does not check which tenant they
  administer** — it must not be used for anything but a READ that returns the same value for every tenant,
  and writes stay on `tenantAdminFor` / `withSuperAdmin` as before. The `issuer` is **dropped key and all**
  (an empty string would be drawn as an empty cell and look like a missing setting).
- ★ P7's Console side has a place that does not mesh with `api()`'s habit of returning a 403 as `{error}`.
  The current `DeploymentSignInMethods` collapses it to **an empty array** with `res?.providers || []`, so it
  **displays the lie** "this deployment has no sign-in methods configured" to someone without permission.
  Before widening who can read it, that branch must be split into **three cases: "could not read" / "genuinely
  zero" / "loading"** (one i18n key is not enough).
  ✅ **Implemented in P7-0a.** The check is `Array.isArray(res?.providers)` — it looks at **whether the shape
  we wanted arrived**, not at the presence of `res.error` (so that a future change to the error shape does
  not get mixed up with zero items). A dropped connection arrives as a rejection, so `.catch` falls to the
  same state. `admin.providers_unreadable` was added.
- ★ P7's audit has **nothing to add**. The two toggles hit the existing
  `PUT /api/admin/tenants/{slug}/login`, so `tenant.login_rules` stays as it is. But Detail is four columns of
  CSV, so **the screen's vocabulary changed while the audit's did not** ("removed a reference" and "narrowed"
  have the same shape).
- ★ P7-1's ripple into the guide is **three surfaces in two languages, six files**: `docs/admin/README(.ja).md`,
  `docs/operate/02-install(.ja).md` and `docs/operate/05-signin(.ja).md`. §61.15.13's operational workaround
  ("hiding has no effect on the bare `/login`, so distribute `/login/<slug>`") is written on those three.
- ★ P7 has **no data migration** (the schema is unchanged). Narrowing decision 42 to `hidden_providers` alone
  also removes the migration risk of "a deployment whose default tenant's `allowed_providers` was already
  narrowed suddenly closing up at P7-1".
