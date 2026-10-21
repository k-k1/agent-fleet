# 0119. OAuth for registered MCP servers: af is the OAuth client, the member signs in from the Console, and sessions reach the server through a local relay that holds the token

English | [日本語](0119-mcp-client-oauth.ja.md)

- Status: **proposed** (2026-10-22). Documentation only: nothing below is built. The code facts
  were read from the tree on this date; the protocol requirements from the MCP 2026-07-28
  specification. Nothing was measured against a real authorization server.
- Tracking: #2479
- Related: [0031](0031-mcp-registry.md) (the registry; its "Left over" says OAuth MCPs are not
  driven from af, and its rejected options include "af acts as an MCP proxy", which decision 6
  revisits) / [0118](0118-mcp-oauth.md) (the other side: af as an authorization server for its own
  `/mcp`) / [0040](0040-project-mcp.md) (project MCP, out of scope here) / `docs/log/80` and
  `control-plane/oauth_jira.go` (the CP-callback, tokens-in-the-workspace split this reuses)

## Context

A registered remote MCP server can carry static headers and nothing else
(`secrets.MCPServer.Headers`, `workspace/agent/internal/secrets/secrets.go`). The connection test
(`workspace/agent/internal/mcpreg/probe.go`, `probeHTTP`) reports a 401 as a plain failure. Hosted
MCP servers increasingly accept only OAuth, so a member cannot register them at all.

Three facts about the tree shape the answer:

- **Every agent kind reads its MCP configuration at start-up.** af writes the registry into seven
  native config files (`materialize*.go`, ADR 0031 decision 3) and a running CLI does not re-read
  them, with the one measured exception of codex's app-server per `thread/start`.
- **af already runs OAuth for connections** with a fixed split: the CP owns the public callback and
  the CSRF state; the tokens live in the member's workspace store; the refresh bridge in the CP
  exists only because those apps have a tenant client secret.
- **Outbound calls from a workspace already go through the workspace's own egress**, which is what
  reaches the MCP servers the member registered. The CP may not reach them at all.

What the protocol asks of a client (MCP 2026-07-28, *Authorization*, *Authorization Server
Discovery*, *Client Registration*):

- discover the Protected Resource Metadata from `WWW-Authenticate: resource_metadata`, else from
  `/.well-known/oauth-protected-resource/<path>` then the root (RFC 9728), and treat the
  challenge's `scope` as authoritative for the request;
- discover the AS metadata in a fixed order (RFC 8414 path insertion, OIDC path insertion, OIDC
  appending) and **not use** a document whose `issuer` differs from the issuer the URL was built
  from;
- register in the order pre-registered → CIMD (when `client_id_metadata_document_supported`) →
  DCR (deprecated, `application_type` required) → ask the user; keep credentials per AS `issuer`
  and re-register when the PRM names another AS;
- PKCE S256, `resource` (RFC 8707) on authorization and token requests, `iss` validation (RFC 9207)
  per the table in the specification, refresh tokens kept confidential, step-up on 403
  `insufficient_scope`.

## Decision

### 1. af is the OAuth client; each member signs in from the Console

A remote definition gains `auth: oauth` beside the existing headers. The member signs in once, in
their own browser, from **Settings > MCP servers**; every agent kind in every session then uses the
grant. Each CLI's own OAuth flow is not used (see Rejected).

Scope: user-scope and tenant-distributed remote definitions. Built-ins, stdio and project MCP
(0040) are out of scope.

### 2. Discovery and validation run in the Agent, to the letter of the specification

The Agent runs discovery through the workspace's egress, as the probe already does, and refuses
rather than guesses:

- an AS whose metadata `issuer` differs from the issuer the well-known URL was built from;
- an AS without `S256` in `code_challenge_methods_supported`;
- a PRM whose `resource` is not the registered server URL in canonical form.

When `authorization_servers` lists several, the first one whose metadata validates is used, and the
Console shows which.

### 3. Client registration in the specification's order, with af's own CIMD document

1. **Pre-registered** client id (and secret, if the AS requires one) entered by the member for that
   server, kept per AS issuer.
2. **CIMD** when the AS advertises it. af hosts one document per deployment at
   `{PUBLIC_BASE_URL}/oauth/mcp-client.json`: `client_id` equal to that URL, `client_name` from the
   deployment's brand, `redirect_uris` = `[{PUBLIC_BASE_URL}/oauth2/mcp/callback]`,
   `token_endpoint_auth_method: none`, `grant_types: [authorization_code, refresh_token]`. It needs an
   `https` `PUBLIC_BASE_URL` that the AS can fetch; whether it can is only known when the AS tries.
3. **DCR** when the AS has a `registration_endpoint`: `application_type: "web"`, the same redirect,
   `none`. The returned credentials are stored per workspace per AS issuer and reused.
4. Otherwise the Console asks the member for a client id.

When a CIMD authorization fails at the AS and the AS also offers DCR, the Console offers to retry
with DCR. The Console always says which path was used.

### 4. The flow: the Agent prepares, the CP owns the callback, the Agent exchanges

1. The Console asks to sign in. The request reaches a **CP handler** (not the plain Agent proxy),
   which mints `state`, records it with the member, membership and workspace and a 10-minute
   expiry, and calls the Agent.
2. The Agent creates the PKCE verifier, chooses scopes (the challenge's `scope`, else the PRM's
   `scopes_supported`, plus `offline_access` when the AS lists it), and returns the authorization
   URL with `response_type=code`, `client_id`, `redirect_uri`, `code_challenge` (S256), `state`,
   `resource` and `scope`. The Console opens it.
3. The AS redirects to `{PUBLIC_BASE_URL}/oauth2/mcp/callback` (already under the auth-exempt
   `/oauth2/` prefix). The CP consumes the `state` once, requires the browser's Console session to
   be the member who started it, and forwards `code`, `iss` and `state` to that workspace's Agent.
4. The Agent validates `iss` per the specification's table, exchanges the code with the verifier and
   `resource`, and stores the tokens. The CP redirects the browser back to the Console with the
   outcome only.

The verifier, the code exchange and the tokens never touch the CP: the token endpoint is reached
through the workspace's egress like the server itself, and a CP that cannot reach it does not
matter. This is the connections split without the refresh bridge, which those apps need only for a
tenant client secret (see decision 9 for the case where one exists).

### 5. Tokens live in the workspace store; refresh is single-flight and rotation-safe

- Access and refresh tokens, and DCR credentials, are stored in `secrets.enc`, keyed by server id
  and AS issuer. Recreate and clean-home keep them, as they keep connections.
- Refresh runs in the Agent, at most one at a time per grant across all sessions; the new refresh
  token is persisted before the new access token is used, so a rotation is never lost to a crash.
- `invalid_grant` on refresh marks the grant "sign in again"; nothing retries it in a loop.
- Disconnect revokes at the AS (RFC 7009) when it offers revocation, then deletes locally either
  way.

### 6. Sessions reach the server through a local relay

The Agent listens on `127.0.0.1` only and exposes one path per OAuth server. For those servers the
materialized config of every kind names the relay URL and a **relay key** header instead of the
real URL and a token. The relay:

- accepts a request only with the relay key (random per workspace, written to the `0600` config
  files only) and a loopback `Host` (the DNS-rebinding guard);
- drops any `Authorization` the CLI sent and sets a fresh access token;
- forwards to exactly the registered URL, for POST, GET (SSE) and DELETE, flushing streamed
  responses as they arrive and passing the MCP headers through (`MCP-Protocol-Version`,
  `Mcp-Method`, `Mcp-Name`, the legacy `Mcp-Session-Id`); it does not follow upstream redirects;
- on an upstream 401, refreshes once and retries (the server processed nothing);
- **never passes the upstream's `WWW-Authenticate` to the CLI**, so no CLI starts its own OAuth
  flow against the server; a grant that cannot be refreshed becomes an error that names the Console
  as the place to sign in again;
- passes a 403 `insufficient_scope` through and records it, so the Console can offer to sign in
  again with the scopes the challenge named (step-up needs the member's browser).

Static-header servers do not use the relay; nothing about them changes. Assistant chat uses the
relay URL in its per-launch config.

ADR 0031 rejected "af acts as an MCP proxy and relays all traffic" because af would take on
authentication, streaming and OAuth. The relay is narrower: OAuth servers only, transport-level
pass-through with no MCP parsing, inside the workspace, and its outbound calls take the same egress
path as the probe. The alternative (decision 1's rejected options) is a token that dies an hour into
a session that lives for days. The side effect is wanted: a session can use the grant but cannot
read the token, where today's static headers sit in plaintext in seven config files.

### 7. Materialization

An OAuth definition is written into a kind's config only while the member holds a live grant for it,
the same rule as a `user_secret` header with no value (ADR 0031 decision 4). The drift tests gain
one case per kind: an `http://127.0.0.1:<port>/…` URL with a custom header, because the relay
depends on every kind accepting a plain-http loopback URL.

### 8. Console

- The server form offers authentication **None / Headers / OAuth**.
- Test connection, on a 401 with OAuth metadata, reports what was found (AS issuer, registration
  path available, S256, `iss` support, scopes) and offers **Sign in**.
- Each OAuth row shows: not signed in / connected (expiry, scopes) / sign in again / needs more
  permission, and a **Disconnect** button.
- The egress note checks the AS hosts the Agent will call (PRM, metadata, token, registration),
  not just the server's host.

### 9. Tenant distribution

A distributed definition may say `auth: oauth`. It carries no credential; each member signs in for
themselves, and it is not materialized for a member until they have. A tenant pre-registered
**confidential** client is a later phase: its secret would stay in the CP, which would then have to
run the code exchange and the refresh (the existing `/internal/git-oauth/*/refresh` bridge shape),
reversing decision 4's "the CP never sees the token" for that case only. It is built when a tenant
needs it.

### 10. Audit

The CP writes `mcp.client_oauth` rows for sign-in started, completed, failed and disconnected,
naming the server and the AS issuer. No token, code or verifier is logged or audited.

## Rejected

- **Each CLI's own OAuth.** Their callbacks are loopback listeners inside the container, which the
  member's browser cannot reach; seven kinds keep tokens in seven places the Console cannot see;
  tenant distribution would ask each member to sign in seven times.
- **The access token as a static header, re-materialized on refresh.** Configs are read at
  start-up; the token dies inside a long session, and managed sessions are not restarted. It also
  puts a bearer token in plaintext in every kind's config.
- **Tokens and refresh in the CP.** The CP may have no route to the server's AS, and it would gather
  every member's grants in one place, where the workspace store keeps each with its owner.
- **The relay in the CP.** Every MCP byte would cross the CP, and the egress that the member's
  registration is checked against is the workspace's, not the CP's.
- **A full MCP proxy that parses messages.** Nothing here needs to understand MCP; parsing would
  tie the relay to protocol revisions (0032).

## Consequences

- Agent: discovery and registration (`mcpreg`), the flow and token store (`secrets`), the relay
  listener, materialization for OAuth rows, probe reporting.
- CP: the sign-in handler and `state` table, `/oauth2/mcp/callback`, the CIMD document, audit, the
  tenant field `auth: oauth` (CP table and the agent's `acceptTenant` re-validation, which ADR 0031
  requires in both places).
- Console: the form, the probe report, the row states, sign-in and disconnect.
- Guide: member (registering an OAuth MCP server), admin (distributing one), operate (CIMD needs a
  reachable `https` `PUBLIC_BASE_URL`).
- Phases are tracked in #2479: report "OAuth required" in the probe → sign-in and storage → relay
  and materialization → tenant distribution → tenant confidential clients if needed → a live run,
  first against another deployment's `/mcp` (ADR 0118).

## Notes

- Source read on 2026-10-22: MCP specification 2026-07-28, *Authorization*, *Authorization Server
  Discovery*, *Client Registration* (modelcontextprotocol.io).
- Not measured: which hosted MCP servers' authorization servers support CIMD, and whether every
  agent kind accepts a plain-http loopback URL with a header (decision 7 adds the test that will
  say).
