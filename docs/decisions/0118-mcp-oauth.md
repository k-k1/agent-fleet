# 0118. OAuth for the `/mcp` endpoint: the Control Plane becomes the authorization server for its own MCP, so claude.ai, ChatGPT and the CLIs connect by signing in to the Console instead of pasting a PAT

English | [日本語](0118-mcp-oauth.ja.md)

- Status: **proposed** (2026-10-20). Documentation only: nothing below is built. The code facts
  were read from the tree on this date; the client behaviour was read from the published
  documents named under Notes, **not measured** against a live client.
- Follow-ups: #2433, #2434, #2435
- Related: [0006](0006-mcp-unified.md) decision 2 (PAT authentication; it named "native OAuth2.1"
  as the later stage, this record is that stage) / [0032](0032-mcp-2026-07-28.md) (left OAuth out
  of scope) / [0116](0116-dev-oidc-idp.md) (the development IdP whose parts this reuses, and why it
  is not reused as is) / [0047](0047-tenant-network-restriction.md) decision 3 (why `/mcp` is not
  IP-gated) / [0043](0043-login-idp.md) (the login the authorization step relies on)

## Context

`/mcp` accepts a Personal Access Token and nothing else (`internal/mcpsrv/mcp.go`, `authMCP`).
A member copies an `af_pat_…` string from the Console into each client. That works for a CLI
and not at all for the hosted clients the member actually wants to use: claude.ai (web, Desktop,
mobile) and ChatGPT connect to a remote MCP server only through OAuth, and a 401 from `/mcp`
today carries a bare `WWW-Authenticate: Bearer` that tells them nothing.

0006 deferred OAuth because the on-prem oauth2-proxy could not take part in an MCP client's
OAuth flow. That premise is gone: under `AUTH=oauth` the CP is itself the login (0043), and
0116 added the server-side pieces an authorization server needs — single-use hashed codes,
S256 PKCE, canonical redirect matching, opaque revocable tokens, a consent page without
`form-action` (`dev_idp_page.go`) and the audit-before-issue pattern (`dev_idp.go`,
`dev_idp_client.go`).

What the protocol now asks (MCP 2026-07-28, *Authorization*):

- the MCP server **MUST** publish Protected Resource Metadata (RFC 9728) and clients **MUST**
  discover the authorization server from it;
- the authorization server **MUST** offer RFC 8414 or OIDC discovery and **MUST** implement
  OAuth 2.1 for public clients (PKCE);
- client registration is, in order, **Client ID Metadata Documents** (CIMD, SHOULD),
  pre-registration, or Dynamic Client Registration (RFC 7591), which is **deprecated** and kept
  for backward compatibility;
- the client **MUST** send `resource` (RFC 8707) on both requests, and the server **MUST** accept
  only tokens issued for itself;
- `iss` in the authorization response (RFC 9207) is SHOULD, expected to become MUST.

What the two hosted clients add (their published documents, see Notes):

- **ChatGPT** requires S256 in `code_challenge_methods_supported`, prefers CIMD
  (`client_id` = `https://chatgpt.com/oauth/client.json`), and uses the **stable** redirect
  `https://chatgpt.com/connector_platform_oauth_redirect` only when the server advertises
  `authorization_response_iss_parameter_supported: true` and returns a byte-exact `iss` in every
  response; otherwise the redirect carries a per-connection id.
- **claude.ai** uses `https://claude.ai/api/mcp/auth_callback` (and says it may move to
  `claude.com`); it picks CIMD only when `none` is among the token endpoint auth methods.
  **Claude Code** uses a loopback redirect on a changing port.
- Both call `/mcp` and the token endpoint **from the vendor's servers**, not from the member's
  machine.

## Decision

### 1. A separate authorization server for `/mcp`; `/dev-idp` stays a development tool

The issuer is `{PUBLIC_BASE_URL}/mcp-oauth`, on the Console origin, alongside `/dev-idp` and not
merged with it. 0116 is built on the opposite premises — a confidential client per workspace,
preview-host redirects, no refresh token, `af_dev: true` and "never trust this in production" —
and serving production MCP tokens from that issuer would void every one of those defences.
The code is shared, the issuer, keys, tables and audit actions are not.

Endpoints:

| Path | Auth | Purpose |
|---|---|---|
| `/.well-known/oauth-protected-resource/mcp` | none | RFC 9728 metadata for the resource `{PUBLIC_BASE_URL}/mcp` |
| `/.well-known/oauth-authorization-server/mcp-oauth` | none | RFC 8414 metadata (path insertion) |
| `/mcp-oauth/authorize` | Console session (`authGate`) | the signed-in member authorizes; consent page |
| `/mcp-oauth/consent` | Console session, same-origin POST | the consent form's target, as in 0116's persona page |
| `/mcp-oauth/token` | client auth `none` + PKCE | `authorization_code` and `refresh_token` grants |
| `/mcp-oauth/register` | none, rate-limited | RFC 7591, for clients without CIMD |
| `/mcp-oauth/revoke` | the token itself | RFC 7009 |

`/mcp` answers 401 with
`WWW-Authenticate: Bearer resource_metadata="…/.well-known/oauth-protected-resource/mcp", scope="read"`.
Metadata advertises `code_challenge_methods_supported: ["S256"]`,
`authorization_response_iss_parameter_supported: true`,
`client_id_metadata_document_supported: true`, `token_endpoint_auth_methods_supported: ["none"]`
and `scopes_supported: ["read", "write"]`. No OIDC scopes and no userinfo in the first stage:
nothing needs the member's identity outside af, and ChatGPT's workspace-domain restriction,
which would, is not a goal.

### 2. Public clients only, PKCE S256 always, `resource` always, `iss` always

- Every client is public. A secret would have to live in a vendor's cloud or on a laptop and
  adds nothing PKCE does not already give.
- PKCE S256 is mandatory (unlike 0116 decision 8, which kept it optional for Spring): there is no
  confidential client here to fall back on.
- `resource` must equal `{PUBLIC_BASE_URL}/mcp` exactly, on both requests; anything else is
  `invalid_target`. The token is bound to it, and `/mcp` refuses a token bound elsewhere. This is
  the line that keeps the issuer from minting tokens for anything but `/mcp`.
- `iss` is returned on every authorization response, success or error, byte-equal to the
  metadata `issuer` (ChatGPT rejects a mismatch, and gets the stable redirect only with it).
- Implicit, password, client-credentials and device grants are not offered.

### 3. Who may be a client: CIMD and loopback freely, any other redirect only to an operator-approved host

Registration identifies an *application*, never a person: no token is issued without a member
signing in to the Console and consenting (decision 4). What registration can do is choose where
the authorization code is sent, and that is the threat: an attacker who registers a client whose
redirect is their own server, and gets a member to click an authorization link, receives a code
for that member's account after a perfectly genuine login. PKCE does not help, because the
attacker started the flow and holds the verifier.

So the rule is on the redirect, the same for CIMD and DCR:

- **Loopback** (`http://127.0.0.1/…`, `http://localhost/…`, `http://[::1]/…`) is accepted with
  **any port** (RFC 8252 §7.3), path matched exactly. A code sent there reaches the member's own
  machine, never a third party. This is what Claude Code and Codex use.
- **Any other redirect must be `https` on a host in the operator's trusted-client list**,
  matched on the canonical spelling with 0116 decision 7's refusals (case, trailing dot, explicit
  default port, userinfo, fragment, backslash). The default list is `claude.ai`, `claude.com`,
  `chatgpt.com`; the operator may remove any of them or add one.
- **A CIMD `client_id` is fetched only from a host on the same list**, so the authorization
  server never makes an outbound request to an address a stranger chose (the SSRF that CIMD
  otherwise invites). The document is fetched with a short timeout and a size cap, cached for
  its `Cache-Control` lifetime within bounds, and its `redirect_uris` are checked against the
  rules above.
- **DCR** registrations are subject to the same redirect rule, rate-limited per source, capped in
  number, and deleted after 30 days without a grant. A deleted client gets `invalid_client` at
  `token`, which claude.ai documents as its signal to register again.
- Pre-registration (an operator typing in a client id) is not built; the two hosted clients
  cover their own registration and a CLI has loopback.

### 4. The consent page decides tenant and scope, and shows where the code goes

`authorize` vets client and redirect first and answers a 400 page, never a redirect, when either
is untrusted (0116 decision 7). Then the signed-in member sees one page that shows:

- the **redirect host** in large type, and the client's self-declared name marked as such —
  the name is free text in both CIMD and DCR and proves nothing;
- the **tenant**: one choice per active membership, preselected when there is one. The token is
  fixed to that membership, as a PAT is (0006 decision 2), so a client cannot switch tenant;
- the **scope**: `read` and `write`, preselected from the request, and the member can narrow
  it. A narrower grant is returned in the token response's `scope`. `admin:dangerous` is **never
  grantable through OAuth**, whatever the role: a token held in a vendor's cloud does not get the
  fleet-wide tier. An administrator's `write` token reaches the admin write tools exactly as an
  administrator's `write` PAT does.

`prompt=none` is honoured only for a client that already holds a live grant for the same member,
tenant and an equal or wider scope; otherwise it is `consent_required`. The page keeps 0116's
headers and its Cancel (`access_denied`): `frame-ancestors 'none'`, no `form-action` (Chromium
applies it to the redirect after the POST and would block the return to claude.ai or ChatGPT),
`no-store`, and `Referrer-Policy: same-origin` (`no-referrer` makes the form POST carry
`Origin: null`, which the same-origin check refuses; 0116, note on #2243).

`authorize` resolves the member through the normal path, so the tenant's network restriction
(0047) applies to the person in the browser. Calls to `/mcp` and `/token` come from the vendor's
servers and are not IP-gated, the same as PAT calls today (0047 decision 3).

### 5. Tokens: a grant per connection, short opaque access tokens, rotating refresh tokens

- A **grant** row records client, identity, membership, scope and its creation; it is what the
  member sees and revokes. Access and refresh tokens hang off it.
- **Access token**: opaque, 1 hour, stored as a hash, bound to the resource. Opaque rather than
  a JWT for 0116's reason: revocation takes effect on the next call, and `/mcp` needs no key.
- **Refresh token**: opaque, hashed, **rotated on every use**; presenting a used one revokes the
  whole grant (OAuth 2.1 §4.3.1 for public clients). A grant idle for 30 days expires; a grant
  lives at most 90 days, then the member signs in again.
- Codes live 60 seconds and are spent by one conditional update, as in 0116.
- Grants and tokens are **not PAT rows**. A PAT is a credential the member names and holds,
  capped at 20 per identity (`maxActivePATs`); OAuth tokens rotate hourly and belong to a client.
  They meet at the principal: `authMCP` resolves either kind to the same `mcpPrincipal`
  (identity, membership, scope, and a grant id where the PAT id was), so role resolution per call,
  the tenant pin, the tool filter and the audit row below it do not change.
- Audit: `mcp.oauth.grant` (created, narrowed, revoked, reuse-revoked) and
  `mcp.oauth.token` (each issuance, before the response leaves, as 0116 decision 9). No token
  value is ever logged or audited.

### 6. Enablement: off by default, by deployment, then by tenant

- The deployment serves it when `AF_MCP_ENABLED=true`, `AF_MCP_OAUTH=1` and an `https`
  `PUBLIC_BASE_URL` are all set. Otherwise every `/mcp-oauth` and metadata path answers 404 and
  `/mcp` keeps its bare 401, so a client falls back to asking for a token.
- A tenant administrator can turn it off for the tenant (a tenant setting beside the network
  restriction). Turning it off revokes the tenant's grants; existing PATs are unaffected.
- Under `AUTH=proxy`, `/mcp-oauth/authorize` and `/consent` must pass through the gateway and
  every other path in decision 1 must bypass it, as `/mcp` already must; the operator guide says
  so. Under `AUTH=dev` it works and authorizes the fixed user.
- The hosted clients need the CP reachable from the internet. A deployment behind a VPN or a
  narrow `AlbIngressCidr` gets loopback clients only, by construction; the guide says that
  rather than letting it look broken.

### 7. PATs stay

For CI and other headless callers, and for clients without OAuth. The Console's token page lists
OAuth grants beside PATs ("connected apps") with client, redirect host, tenant, scope, last use,
and a revoke button.

## Rejected

- **Issuing MCP tokens from `/dev-idp`.** Decision 1.
- **Open DCR with any redirect.** Decision 3: registration is unauthenticated by design, so the
  redirect is the only thing that can be constrained.
- **Fetching any CIMD URL.** An outbound request to an attacker-chosen address from the CP.
- **Confidential clients / `private_key_jwt`.** No client needs them; ChatGPT and claude.ai both
  accept `none`. `private_key_jwt` can be added when a client requires it.
- **JWT access tokens.** Revocation would wait for expiry.
- **Storing OAuth tokens as PATs.** Decision 5.
- **`admin:dangerous` over OAuth.** Decision 4.
- **OIDC userinfo for ChatGPT's workspace-domain restriction.** Not needed to connect; it would
  put the member's address in another vendor's hands for no af feature.

## Consequences

- Control Plane: a new `mcp_oauth*.go` set (metadata, authorize/consent, token, register, CIMD
  fetch, redirect rules), `authMCP` taught the second token kind, `/mcp`'s 401 header, the
  exempt-path list, a tenant setting, and the Console's connected-apps list.
- Store: grant, token, code and client tables, with migrations for **both sqlite and Postgres**,
  forward compatible. No sealed column is needed (every token is a hash), so `rewrap-keys` is
  untouched; the signing keyring is not needed either, because no token is a JWT.
- Guide: member (connecting claude.ai, ChatGPT, Claude Code), admin (the tenant switch),
  operate (`AF_MCP_OAUTH`, the trusted-client list, ingress paths under `AUTH=proxy`).
- 0006 decision 2's "native OAuth2.1 support is secondary" is superseded by this record; PAT
  remains as described there.

## Notes

- Sources read on 2026-10-20: MCP specification 2026-07-28, *Authorization*
  (modelcontextprotocol.io); OpenAI, *Authentication* for ChatGPT apps and MCP servers
  (developers.openai.com/plugins/build/auth); Anthropic, *Authentication for connectors*
  (claude.com/docs/connectors/building/authentication).
- Not measured: any of the three hosted flows against a real deployment. That needs a deployment
  reachable from the internet with `PUBLIC_BASE_URL` on `https`.
- Open: whether ChatGPT and claude.ai always send the `scope` from the 401 challenge or request
  every `scopes_supported` entry. Decision 4 handles both (the member narrows), but the default
  shown on the consent page follows whichever they do.
- 2026-10-21, implementation of decisions 1-5 and the deployment flag (#2433). No decision changed;
  the points the text left open were settled as follows. The trusted-client list is
  `AF_MCP_OAUTH_TRUSTED_HOSTS` (exact host names, `none` for loopback only). `resource` is required
  on `authorize` and on the `authorization_code` request and checked when present on a refresh. A
  grant's access token carries its own scope, so a refresh may narrow it below the grant's. A
  second use of a spent refresh token revokes the grant even when it is a concurrent duplicate
  (one request wins, the rest revoke): there is no grace window. A CIMD host is checked again at
  `token` but the document is not refetched there. Not built here, as the Consequences already
  say: the tenant switch and the connected-apps list (#2434); live-client runs (#2435).

- 2026-10-21, #2434: implemented decisions 6 and 7: connected apps beside PATs and a
  tenant-admin MCP OAuth switch beside source networks. Disabling atomically audits and revokes
  tenant grants, marks outstanding codes, and serializes with grant creation on the tenant row.
  Token issuance checks the tenant and revocation mark after insertion; re-enabling cannot
  resurrect a connection interrupted by disabling. Live-client runs remain tracked by #2435.

- 2026-10-21, #2457: chose option A for ChatGPT's CIMD document. Its preferred
  `token_endpoint_auth_method` is `private_key_jwt`, but its
  `token_endpoint_auth_methods_supported` includes `none`. Accept a document when the
  preferred method is empty or `none`, or the supported list contains `none`; continue
  refusing a client secret. Our authorization server advertises only `none`, so ChatGPT
  must use that common method. The client remains public with mandatory S256 PKCE;
  `/token` still rejects `client_assertion`, Basic authentication and `client_secret`
  rather than silently ignoring authentication. This clarifies decision 2 without
  implementing private-key authentication or changing DCR. Option B, verifying
  `private_key_jwt` against the document's `jwks_uri`, remains possible if a client
  requires it. The published ChatGPT document is covered by a fixed test fixture;
  a live ChatGPT connection remains unverified and tracked by #2435.

- 2026-10-21, #2455: chose option 1: omit `scope` from the `/mcp` 401 challenge,
  keeping `resource_metadata` and the invalid-token error when applicable. This settles
  the open question above: the #2435 live run found that claude.ai sends the challenged
  scope, while ChatGPT requests every `scopes_supported` entry. Advertising `scope="read"`
  therefore prevented claude.ai from ever asking for write tools. Without a challenged
  scope, clients can request the advertised scopes; the consent page always starts at
  `read` and offers `write` only when requested, requiring an explicit member choice.
  The token response reports the granted scope, never a wider scope than requested;
  `admin:dangerous` remains unavailable. This refines decisions 1 and 4 without changing
  insufficient-scope handling. `prompt=none` still requires an existing grant covering
  the entire requested scope: a read grant cannot silently approve a read+write request.
  Live verification of the changed hosted-client flow remains tracked by #2435.

- 2026-10-22, #2531: `/mcp` calls are now observable. The #2435 live run showed ChatGPT
  completing the OAuth flow and one call, then listing no tools, while nothing recorded what it
  had sent or been answered, so the cause (suspected: a 400 from the stateless-era validation,
  or the 405 on `GET`) could not be seen. Every `/mcp` request now writes one `mcp-call` log
  line (method, status, JSON-RPC methods per batch item, era and protocol version, which
  mirrored headers were present, error code and message, tool count of `tools/list`, latency,
  principal or the refusal reason), and an authenticated one also enters a per-PAT / per-grant
  in-memory ring (last 50, per replica, not persisted) that only its owner reads at
  `GET /api/mcp-oauth/grants/{id}/calls` and `GET /api/pat/{id}/calls`. Tokens, `Authorization`
  values, arguments and results are never recorded. No decision above changes; this is
  diagnostics only and does not fix ChatGPT.

- 2026-10-22, #2542: input sent over `/mcp` now carries its origin. `send_to_session` used to post
  the same `/input` the Console uses, so a hosted assistant or PAT client was indistinguishable
  from the member typing. It now posts to a separate Agent route the Console cannot reach, with
  the connection (grant or PAT id and its name) taken from the authenticated principal; the Agent
  prepends `[agent-fleet:mcp via=… connection=… reply=none]`, the Console badges it "Via MCP:
  <name>", and `[agent-fleet:mcp …]` carries a peer message's authority and its four prohibitions
  in the Workspace policy. A prompt starting with `[agent-fleet:` is refused, and shell / ssm
  targets are refused as for peer messages (ADR 0041 decision 5). The envelope names the
  connection, not a client conversation (MCP 2026-07-28 has no session id). No decision above
  changes. Guide: `guide/member/10-integrations.md`.

- 2026-10-23, #2567: the trusted-client list is editable at runtime. A super admin edits it in the
  Admin modal (`GET`/`PUT`/`DELETE /api/admin/mcp-oauth/trusted-hosts`, CP setting
  `mcp_oauth_trusted_hosts`, deployment-wide); precedence is the saved value, else
  `AF_MCP_OAUTH_TRUSTED_HOSTS`, else the built-in default, and "reset" deletes the saved value. The
  list is read per request (no cache, so replicas agree) at authorize, consent, DCR and the token
  endpoint; the CIMD cache keeps the document's own redirect URIs and the list is applied on each
  read, so a change bites immediately in both directions. The saved value is a JSON array, so a
  saved empty list (`none`) differs from nothing saved. Saving validates strictly (exact host,
  no scheme / port / path / wildcard / IP literal) and an unreadable saved value trusts nothing
  rather than falling back to the wider default. Each change is audited with the old and new lists.
  No decision above changes. Guide: `guide/admin/05-access.md`.
